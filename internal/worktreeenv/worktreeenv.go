// Package worktreeenv is the shell around internal/core/worktreeenv (WE-01,
// WE-02): it gives every worktree a block of ports and copies the repo's
// bootstrap profile files into a new worktree. The decisions are made by the
// core; this package loads state, probes the machine, touches files and
// writes the audit log. It never deletes anything (invariant 1).
package worktreeenv

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	env "github.com/brendatamaar/yggdrasil/internal/core/worktreeenv"
)

// Errors the store implementations return.
var (
	// ErrConflict means a worktree already has a live block or the base is
	// taken by another live block.
	ErrConflict = errors.New("worktreeenv: conflict")
	// ErrNotFound means the repo or worktree is unknown.
	ErrNotFound = errors.New("worktreeenv: not found")
	// ErrRangeFull means no free aligned block is left in the range.
	ErrRangeFull = errors.New("worktreeenv: port range full")
)

// Error is a failure with a plain-language message that holds no paths.
type Error struct {
	Msg string
	Err error
}

func (e *Error) Error() string       { return e.Msg + ": " + e.Err.Error() }
func (e *Error) Unwrap() error       { return e.Err }
func (e *Error) UserMessage() string { return e.Msg }

// PortStore keeps the port blocks. Releasing sets released_at and never
// deletes a row.
type PortStore interface {
	// LoadBlocks returns the live blocks.
	LoadBlocks(ctx context.Context) (map[core.WorktreeID]core.PortBlock, error)
	// SaveBlock stores a live block and the audit entry in one commit. A
	// worktree with a live block, or a base in use, is ErrConflict.
	SaveBlock(ctx context.Context, w core.WorktreeID, b core.PortBlock, at time.Time, audit core.AuditEntry) error
	// ReleaseBlock releases the worktree's live block and appends the audit
	// entry in the same commit. It reports false, and appends nothing, when
	// the worktree had none.
	ReleaseBlock(ctx context.Context, w core.WorktreeID, at time.Time, audit core.AuditEntry) (bool, error)
}

// RepoEnv is a repo's bootstrap profile settings (WE-01). Agent-task copy is
// off by default.
type RepoEnv struct {
	Profile   []env.RelPath
	AgentCopy bool
}

// SettingsStore keeps the profile settings and the port range.
type SettingsStore interface {
	// RepoEnv returns the settings; a repo without any gets the defaults and an
	// unknown repo is ErrNotFound.
	RepoEnv(ctx context.Context, repo core.RepoID) (RepoEnv, error)
	// PutRepoEnv stores the settings and the audit entry in one commit.
	PutRepoEnv(ctx context.Context, repo core.RepoID, e RepoEnv, at time.Time, audit core.AuditEntry) error
	// PortRange returns the configured range, DefaultRange when none is set.
	PortRange(ctx context.Context) (env.Range, error)
	// SetPortRange stores the range and the audit entry in one commit.
	SetPortRange(ctx context.Context, r env.Range, audit core.AuditEntry) error
}

// AuditSink writes audit entries that have no state change of their own.
type AuditSink interface {
	Append(ctx context.Context, e core.AuditEntry) error
}

// PortProber reports ports that are already listening.
type PortProber interface {
	// Busy returns the ports in lo..hi that something listens on.
	Busy(ctx context.Context, lo, hi int) ([]int, error)
}

// FileSystem is the file access the profile copy needs. Both roots are
// absolute folders; rel is a validated path inside them.
type FileSystem interface {
	// Observe looks at rel in srcRoot and at the destination in dstRoot without
	// following any link. A path that is not valid, escapes the root or enters
	// .git is reported as SourceUnreadable. A link or reparse point at any level
	// of the source is SourceLink; a link at any level of the destination is
	// DestExists, so nothing is ever written through it.
	Observe(ctx context.Context, srcRoot, dstRoot string, rel env.RelPath) (env.Source, env.Dest, error)
	// CopyExclusive creates rel in dstRoot with exclusive-create and copies the
	// bytes unchanged. It never overwrites (ErrDestExists, even in a race),
	// refuses links, refuses files over maxBytes (ErrTooLarge) and deletes
	// nothing: after a failed write the partial file stays.
	CopyExclusive(ctx context.Context, srcRoot, dstRoot string, rel env.RelPath, maxBytes int64) error
}

// Errors CopyExclusive returns.
var (
	ErrDestExists = errors.New("worktreeenv: destination exists")
	ErrTooLarge   = errors.New("worktreeenv: file too large")
	ErrNotRegular = errors.New("worktreeenv: not a regular file")
)

// Config wires a Service. All fields except OnProfileDone are required.
type Config struct {
	Ports    PortStore
	Settings SettingsStore
	Audit    AuditSink
	Prober   PortProber
	FS       FileSystem
	// RepoDir returns the folder of a tracked repo (invariant 6).
	RepoDir func(ctx context.Context, repo core.RepoID) (string, error)
	Now     func() time.Time
	NewID   func(prefix string) string
	// Logger receives warnings; it never gets paths or file names. Default:
	// slog.Default().
	Logger *slog.Logger
	// OnProfileDone is called when a profile copy finished (the core's Publish).
	OnProfileDone func(core.WorktreeID)
}

// Service allocates port blocks and copies profiles.
type Service struct {
	c  Config
	mu sync.Mutex // one allocation at a time in this process; the store's unique indexes guard the rest
	// reported holds the block conflicts already written to the audit log, so
	// a persistent overlap is audited once per process and not on every call.
	reported    map[string]bool
	lastProfile map[core.WorktreeID]LastProfile // guarded by mu
}

// New returns a Service.
func New(c Config) (*Service, error) {
	if c.Ports == nil || c.Settings == nil || c.Audit == nil || c.Prober == nil || c.FS == nil ||
		c.RepoDir == nil || c.Now == nil || c.NewID == nil {
		return nil, errors.New("worktreeenv: ports, settings, audit, prober, file system, repo lookup, clock and id source are required")
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	return &Service{c: c, reported: map[string]bool{}, lastProfile: map[core.WorktreeID]LastProfile{}}, nil
}

// Prepare gets a new worktree ready: it gives it a port block and copies the
// repo's profile files. The two parts are independent; the worktree works if
// either fails, so both run and the errors are joined. The trigger says who
// started the worktree (profile copy is off for agent tasks by default).
func (s *Service) Prepare(ctx context.Context, w core.Worktree, trigger env.Trigger) error {
	var errs []error
	if _, err := s.Allocate(ctx, w.ID); err != nil {
		errs = append(errs, err)
	}
	if _, err := s.CopyProfile(ctx, w, trigger); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func auditEntry(id, actor, action, subject string, at time.Time, detail []byte) core.AuditEntry {
	return core.AuditEntry{ID: core.AuditID(id), At: at, Actor: actor, Action: action, Subject: subject, Detail: detail}
}

func wtSubject(w core.WorktreeID) string { return fmt.Sprintf("worktree:%s", w) }
