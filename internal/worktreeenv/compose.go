package worktreeenv

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	env "github.com/brendatamaar/yggdrasil/internal/core/worktreeenv"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// Errors of the compose service. Each is wrapped in an *Error with a plain
// message; callers test with errors.Is.
var (
	// ErrComposeConfirm means the user must confirm the compose file first.
	ErrComposeConfirm = errors.New("worktreeenv: compose file needs confirmation")
	// ErrComposeBusy means another compose operation of the worktree is running.
	ErrComposeBusy = errors.New("worktreeenv: compose operation in progress")
	// ErrComposeOff means compose is not enabled for the repo.
	ErrComposeOff = errors.New("worktreeenv: compose is off")
	// ErrComposeStale means the file changed after the user looked at it.
	ErrComposeStale = errors.New("worktreeenv: compose file changed")
	// ErrComposeNoop means the action does nothing in the project's current state.
	ErrComposeNoop = errors.New("worktreeenv: compose action not possible in this state")
	// ErrComposeHashRequired means a confirmation did not say which file it confirms.
	ErrComposeHashRequired = errors.New("worktreeenv: confirmation needs the hash of the file shown")
)

// statusFlushTimeout bounds the retry of queued audit entries done by a read.
const statusFlushTimeout = time.Second

// msgIndirect is shown when the compose file reads other files.
const msgIndirect = "The compose file uses include:, extends: or env_file:, which pull in files that cannot be shown to you for confirmation. Put the whole setup in this one file (a .env next to it is allowed)."

// ComposeConfig wires a ComposeService. All fields except Logger, Redact and
// OnChange are required.
type ComposeConfig struct {
	Store  ComposeStore
	Audit  AuditSink
	Runner ComposeRunner
	Files  ComposeFiles
	// Worktree returns a tracked worktree (invariant 6); it is ErrNotFound when
	// the worktree is not tracked any more.
	Worktree func(ctx context.Context, w core.WorktreeID) (core.Worktree, error)
	// Block returns the worktree's port block, false when it has none.
	Block  func(ctx context.Context, w core.WorktreeID) (core.PortBlock, bool, error)
	Now    func() time.Time
	NewID  func(prefix string) string
	Logger *slog.Logger
	Redact safety.Redactor
	// OnChange is called after a worktree's compose status changed (the
	// core's ComposePublish). It is never called with the lock held.
	OnChange func(core.WorktreeID)
	// AuditQueueMax caps the audit entries kept for a retry while the sink is
	// down; the oldest are dropped and counted. Default 1000.
	AuditQueueMax int
	// AuditRememberMax caps how many written end-of-operation IDs are
	// remembered (oldest evicted first). Default 4096.
	AuditRememberMax int
	// WaitTimeout bounds Wait when an operation does not end. Default 10 s.
	WaitTimeout time.Duration
}

// An audit entry is quarantined (dropped and counted) only after maxFlushFailures
// failed flushes AND quarantineAfter since its first failure, so a short outage
// of the sink never costs an entry, while one entry that can never be written
// still cannot block the entries behind it for good. The queue cap is the only
// bound by count.
const (
	maxFlushFailures = 10
	quarantineAfter  = 5 * time.Minute
)

// ComposeStatus is the compose state of one worktree for display.
type ComposeStatus struct {
	State env.Service
	File  env.RelPath
	// Hash is the hash of the compose file as it is now ("" when unreadable).
	Hash string
	// Note is a plain-language hint or failure message; it holds no paths.
	Note      string
	UpdatedAt time.Time
}

// ComposeService runs the optional docker compose project of each worktree
// (WE-03, docs/design/worktree-env.md section 3). The decisions are made by
// env.Compose; this service reads the file and its hash, loads settings,
// persists the confirmed hash with its audit entry, and runs the commands
// through the ComposeRunner. Nothing is run at shutdown: containers outlive
// ygg on purpose (invariant C7), and the next start learns about them with
// `ps` (RecoveredPs). It never deletes anything: argv is fixed per operation
// and has no -v, --volumes, --rmi or --remove-orphans.
type ComposeService struct {
	c   ComposeConfig
	ctx context.Context
	wg  sync.WaitGroup

	// mu guards only the maps and the queue below. It is never held across IO.
	// Each worktree has its own lock (composeEntry.mu) that serializes its
	// operations, including the IO they do, so one slow worktree never blocks
	// another.
	mu     sync.Mutex
	states map[core.WorktreeID]*composeEntry
	// confirmedBy is the worktree where the user last confirmed a file of the
	// repo, kept for the audit entry of the worktrees that are confirmed by
	// the same content (memory only; empty after a restart).
	confirmedBy map[core.RepoID]core.WorktreeID
	// queued holds audit entries that could not be written (invariant 8). They
	// are retried by the next call and at Wait, oldest first.
	queued  []queuedAudit
	dropped int        // entries dropped from the queue (full or quarantined)
	flushMu sync.Mutex // one flusher at a time keeps the order
	// written holds the IDs of end-of-operation entries (deterministic IDs)
	// that were already written, so a repeat of the write, whether by a retry
	// of the transition or by the queue, never writes one twice.
	written      map[core.AuditID]struct{}
	writtenOrder []core.AuditID
	// boot is a random token of this process. It is part of the audit IDs of
	// operation ends, so IDs of different runs (attempt counters restart at 1)
	// never collide in the audit log's primary key.
	boot string
}

// queuedAudit is an audit entry waiting for a retry.
type queuedAudit struct {
	e     core.AuditEntry
	det   bool      // its ID is deterministic: remember it once written
	fails int       // failed flushes so far
	first time.Time // when it first failed to be written
}

type composeEntry struct {
	mu      sync.Mutex // serializes the operations of this worktree
	svc     env.Service
	attempt int
	loaded  bool   // startup recovery (ps) has run
	loading bool   // startup recovery is running, outside the lock
	fed     string // last file hash given to the core as FileChanged, so a poll does not repeat it
	// view of the file at the last sync
	file     env.RelPath
	hash     string
	noPorts  bool
	fileGone bool
	refused  bool // the file uses include/extends/env_file
	at       time.Time
	// auto is set while the entry is confirmed by an earlier confirmation of
	// the same content; autoSource is the worktree that was confirmed.
	auto       bool
	autoSource core.WorktreeID
}

// composeSnap is the compose file, the `.env` next to it and their hash, read
// together.
type composeSnap struct {
	compose []byte
	hash    string // "" when the file cannot be run
	hasEnv  bool
	noPorts bool
	err     error // unreadable
	refused error // indirect (include/extends/env_file)
}

// psJob is a startup recovery to run outside the lock.
type psJob struct {
	wt   core.Worktree
	file env.RelPath
	snap composeSnap
}

// applyResult tells what apply did with an input.
type applyResult struct {
	publish bool // subscribers should be told
	noop    bool // the core did nothing with the input
}

// NewComposeService returns a service whose operations are owned by ctx:
// cancelling ctx stops the running `docker compose` processes of this
// service (the containers they started keep running).
func NewComposeService(ctx context.Context, c ComposeConfig) (*ComposeService, error) {
	if c.Store == nil || c.Audit == nil || c.Runner == nil || c.Files == nil || c.Worktree == nil ||
		c.Block == nil || c.Now == nil || c.NewID == nil {
		return nil, errors.New("worktreeenv: compose needs a store, audit sink, runner, file reader, worktree lookup, block lookup, clock and id source")
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.Redact == nil {
		c.Redact = safety.NewRedactor()
	}
	if c.AuditQueueMax <= 0 {
		c.AuditQueueMax = 1000
	}
	if c.AuditRememberMax <= 0 {
		c.AuditRememberMax = 4096
	}
	if c.WaitTimeout <= 0 {
		c.WaitTimeout = 10 * time.Second
	}
	return &ComposeService{c: c, ctx: ctx, states: map[core.WorktreeID]*composeEntry{}, confirmedBy: map[core.RepoID]core.WorktreeID{}, written: map[core.AuditID]struct{}{}, boot: c.NewID("boot")}, nil
}

// Wait blocks until the running operations have ended, but at most
// ComposeConfig.WaitTimeout: a docker call that does not return must not hold
// up the shutdown. Then it makes a last attempt at queued audit entries.
func (s *ComposeService) Wait() {
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	t := time.NewTimer(s.c.WaitTimeout)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
		s.c.Logger.Warn("compose operations did not end in time; shutting down anyway")
	}
	s.flushAudits(10 * time.Second) // last attempt at queued entries
	s.mu.Lock()
	left, dropped := len(s.queued), s.dropped
	s.mu.Unlock()
	if left+dropped > 0 {
		s.c.Logger.Error("compose audit entries not written at shutdown", "unwritten", left, "dropped", dropped)
	}
}

// entryFor returns the entry of a worktree, creating it. It holds s.mu only
// for the map access.
func (s *ComposeService) entryFor(wid core.WorktreeID) *composeEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.states[wid]
	if e == nil {
		e = &composeEntry{svc: env.Off{}}
		s.states[wid] = e
	}
	return e
}

func (s *ComposeService) lookup(wid core.WorktreeID) *composeEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.states[wid]
}

func (s *ComposeService) confirmedByOf(repo core.RepoID) core.WorktreeID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.confirmedBy[repo]
}

// queueAudit keeps an entry that could not be written for a later retry. An
// entry that is already written or queued (same ID) is not queued again. The
// queue is capped: the oldest entries go first and are counted.
func (s *ComposeService) queueAudit(e core.AuditEntry, det bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, done := s.written[e.ID]; done {
		return
	}
	for _, q := range s.queued {
		if q.e.ID == e.ID {
			return
		}
	}
	s.queued = append(s.queued, queuedAudit{e: e, det: det})
	if over := len(s.queued) - s.c.AuditQueueMax; over > 0 {
		s.dropped += over
		s.queued = s.queued[over:]
		s.c.Logger.Error("compose audit queue full: entries dropped", "dropped", over, "dropped_total", s.dropped)
	}
}

// rememberLocked notes that a deterministic ID was written. The set is capped.
func (s *ComposeService) rememberLocked(id core.AuditID) {
	if _, ok := s.written[id]; ok {
		return
	}
	s.written[id] = struct{}{}
	s.writtenOrder = append(s.writtenOrder, id)
	for len(s.writtenOrder) > s.c.AuditRememberMax {
		delete(s.written, s.writtenOrder[0])
		s.writtenOrder = s.writtenOrder[1:]
	}
}

// appendOnce writes an audit entry once. An ID that was already written (this
// process remembers it, or the log refuses it as a duplicate key) counts as
// written, which also settles two writers racing on the same ID. det says the
// ID is deterministic, so it is remembered.
func (s *ComposeService) appendOnce(ctx context.Context, e core.AuditEntry, det bool) error {
	s.mu.Lock()
	_, done := s.written[e.ID]
	s.mu.Unlock()
	if done {
		return nil
	}
	err := s.c.Audit.Append(ctx, e)
	if err != nil {
		if !errors.Is(err, ErrConflict) {
			return err
		}
		// The key is taken: the entry is already in the log.
		s.c.Logger.Info("compose audit entry was already written", "audit", e.ID)
	}
	if det {
		s.mu.Lock()
		s.rememberLocked(e.ID)
		s.mu.Unlock()
	}
	return nil
}

// auditID is the ID of the idx-th audit entry of an input. The end of an
// operation gets <worktree>:<boot>:<attempt>:<idx> (det is true): the same
// wherever it is built (the transition or the retry queue), so it is written
// once, and different from the IDs of earlier runs of ygg. Other inputs get a
// fresh ID.
func (s *ComposeService) auditID(wt core.Worktree, in env.ComposeInput, idx int) (id string, det bool) {
	if v, ok := in.(env.OpFinished); ok {
		return fmt.Sprintf("%s:%s:%d:%d", wt.ID, s.boot, v.Attempt, idx), true
	}
	return s.c.NewID("aud"), false
}

// flushAudits writes the queued entries, oldest first, within total, and stops
// at the first failure (they stay queued). It never holds a worktree lock or
// s.mu across a write, and runs on its own context: a request's context or
// another retry's deadline must not decide whether an entry is written.
func (s *ComposeService) flushAudits(total time.Duration) {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), total)
	defer cancel()
	for {
		s.mu.Lock()
		if len(s.queued) == 0 {
			s.mu.Unlock()
			return
		}
		q := s.queued[0]
		s.mu.Unlock()
		err := s.appendOnce(ctx, q.e, q.det)
		s.mu.Lock()
		if len(s.queued) == 0 || s.queued[0].e.ID != q.e.ID {
			s.mu.Unlock() // the queue changed under us (cap): look again
			if err != nil {
				s.c.Logger.Warn("compose audit entry not written while the queue changed", "audit", q.e.ID, "err", err)
				return
			}
			continue
		}
		if err == nil {
			s.queued = s.queued[1:]
			s.mu.Unlock()
			continue
		}
		now := s.c.Now()
		s.queued[0].fails++
		if s.queued[0].first.IsZero() {
			s.queued[0].first = now
		}
		if s.queued[0].fails >= maxFlushFailures && now.Sub(s.queued[0].first) >= quarantineAfter {
			// Quarantine: it must not block the entries behind it.
			s.queued = s.queued[1:]
			s.dropped++
			total := s.dropped // read under the lock
			s.mu.Unlock()
			s.c.Logger.Error("compose audit entry could not be written and was dropped", "audit", q.e.ID, "subject", q.e.Subject, "dropped_total", total, "err", err)
			continue
		}
		s.mu.Unlock()
		s.c.Logger.Error("compose audit entry still not written; it stays queued", "audit", q.e.ID, "subject", q.e.Subject, "err", err)
		return
	}
}

// Settings returns a repo's compose settings.
func (s *ComposeService) Settings(ctx context.Context, repo core.RepoID) (ComposeSettings, error) {
	return s.c.Store.ComposeSettings(ctx, repo)
}

// Configure stores the compose opt-in of a repo (user action). The file is
// parsed here (ParseComposeFile). Changing the file clears the confirmation.
func (s *ComposeService) Configure(ctx context.Context, repo core.RepoID, enabled bool, file string) error {
	var rel env.RelPath
	if file != "" {
		var err error
		if rel, err = env.ParseComposeFile(file); err != nil {
			return &Error{Msg: "The compose file must be a .yml or .yaml file inside the repo: " + err.Error() + ".", Err: err}
		}
	}
	if enabled && rel == "" {
		return &Error{Msg: "Choose the compose file before turning compose on.", Err: ErrComposeOff}
	}
	detail, err := json.Marshal(map[string]any{"enabled": enabled, "file": string(rel)})
	if err != nil {
		return fmt.Errorf("encode audit detail: %w", err)
	}
	now := s.c.Now().UTC()
	e := auditEntry(s.c.NewID("aud"), "user", "worktreeenv.compose_settings", "repo:"+string(repo), now, detail)
	// The read and the write are one transaction in the store, so the
	// confirmed hash is dropped exactly when the file changes and a concurrent
	// confirmation of the old file cannot survive it.
	err = s.c.Store.UpdateComposeSettings(ctx, repo, now, e, func(old ComposeSettings) (ComposeSettings, bool, error) {
		next := ComposeSettings{Enabled: enabled, File: rel, ConfirmedHash: old.ConfirmedHash}
		if rel != old.File {
			next.ConfirmedHash = ""
		}
		return next, next != old, nil
	})
	if err != nil {
		return fmt.Errorf("save compose settings of repo %s: %w", repo, err)
	}
	return nil
}

// Status returns the compose state of a worktree, bringing it in line with the
// settings and the file first.
func (s *ComposeService) Status(ctx context.Context, wid core.WorktreeID) (ComposeStatus, error) {
	s.flushAudits(statusFlushTimeout)                 // a read must not wait long for audit IO
	if _, err := s.c.Worktree(ctx, wid); err != nil { // untracked: no entry is created
		return ComposeStatus{}, err
	}
	e := s.entryFor(wid)
	e.mu.Lock()
	st, notify, job, err := s.statusLocked(ctx, wid, e)
	e.mu.Unlock()
	s.notify(wid, notify)
	if err != nil || job == nil {
		return st, err
	}
	// Startup recovery asks docker, which can be slow: never with the lock held.
	res := s.runPs(job)
	e.mu.Lock()
	e.loading = false
	notify = false
	if _, off := e.svc.(env.Off); !off {
		r, aerr := s.apply(ctx, job.wt, e, env.RecoveredPs{Result: res, Now: s.c.Now().UTC()}, "system")
		if aerr == nil {
			e.loaded, notify = true, r.publish
		} else {
			s.c.Logger.Warn("record the recovered compose state", "worktree", wid, "err", aerr)
		}
	}
	st = s.viewLocked(e)
	e.mu.Unlock()
	s.notify(wid, notify)
	return st, nil
}

// Confirm records the user's confirmation of the compose file. hash is the
// hash the user was shown and is required; when the file has changed since,
// nothing is confirmed (ErrComposeStale).
func (s *ComposeService) Confirm(ctx context.Context, wid core.WorktreeID, hash string) (ComposeStatus, error) {
	if hash == "" {
		return ComposeStatus{}, &Error{Msg: "Say which version of the compose file you are confirming: reload the worktree and confirm what is shown.", Err: ErrComposeHashRequired}
	}
	return s.act(ctx, wid, func(e *composeEntry, now time.Time) (env.ComposeInput, error) {
		if hash != e.hash {
			return nil, &Error{Msg: "The compose file changed since you looked at it. Review it and confirm again.", Err: ErrComposeStale}
		}
		return env.Confirmed{Hash: e.hash}, nil
	})
}

// Up starts the project (`up -d`). The file must have been confirmed.
func (s *ComposeService) Up(ctx context.Context, wid core.WorktreeID) (ComposeStatus, error) {
	return s.act(ctx, wid, func(e *composeEntry, now time.Time) (env.ComposeInput, error) {
		e.attempt++
		return env.UpRequested{Hash: e.hash, Attempt: e.attempt, Now: now}, nil
	})
}

// Stop stops the containers (`stop`); they and their data stay.
func (s *ComposeService) Stop(ctx context.Context, wid core.WorktreeID) (ComposeStatus, error) {
	return s.act(ctx, wid, func(e *composeEntry, now time.Time) (env.ComposeInput, error) {
		e.attempt++
		return env.StopRequested{Attempt: e.attempt, Now: now}, nil
	})
}

// Down removes the containers (`down`, never with volumes).
func (s *ComposeService) Down(ctx context.Context, wid core.WorktreeID) (ComposeStatus, error) {
	return s.act(ctx, wid, func(e *composeEntry, now time.Time) (env.ComposeInput, error) {
		e.attempt++
		return env.DownRequested{Attempt: e.attempt, Now: now}, nil
	})
}

func (s *ComposeService) notify(w core.WorktreeID, do bool) {
	if do && s.c.OnChange != nil {
		s.c.OnChange(w)
	}
}

func (s *ComposeService) act(ctx context.Context, wid core.WorktreeID, mk func(*composeEntry, time.Time) (env.ComposeInput, error)) (ComposeStatus, error) {
	// Recovery first, outside the lock.
	if _, err := s.Status(ctx, wid); err != nil {
		return ComposeStatus{}, err
	}
	e := s.entryFor(wid) // tracked: Status just found it
	e.mu.Lock()
	st, notify, job, err := s.statusLocked(ctx, wid, e)
	if job != nil {
		e.loading = false // recovery is retried by the next status read
	}
	if err == nil {
		wt, _ := s.c.Worktree(ctx, wid) // tracked: checked by statusLocked
		switch {
		case e.refused:
			err = &Error{Msg: msgIndirect, Err: env.ErrComposeIndirect}
		case isOff(e.svc):
			err = &Error{Msg: "Docker compose is not turned on for this repo. Turn it on in the repo settings first.", Err: ErrComposeOff}
		case e.hash == "":
			err = &Error{Msg: "The compose file could not be read inside this worktree. Check the file name in the repo settings.", Err: ErrComposeFile}
		default:
			var in env.ComposeInput
			if in, err = mk(e, s.c.Now().UTC()); err == nil {
				var r applyResult
				r, err = s.apply(ctx, wt, e, in, "user")
				notify = notify || r.publish
				if err == nil && r.noop {
					err = s.noopError(e, in)
				}
			}
		}
		if err == nil {
			st = s.viewLocked(e)
		}
	}
	e.mu.Unlock()
	s.notify(wid, notify)
	return st, err
}

func isOff(s env.Service) bool { _, off := s.(env.Off); return off }

// noopError explains an action the core did nothing with. A confirmation of a
// file that is already the confirmed one is fine.
func (s *ComposeService) noopError(e *composeEntry, in env.ComposeInput) error {
	if c, ok := in.(env.Confirmed); ok {
		if cf, has := confirmedOf(e.svc); has && cf == c.Hash {
			return nil
		}
		return &Error{Msg: "This file cannot be confirmed while the project is running or changing. Stop the project first, then confirm the new file.", Err: ErrComposeNoop}
	}
	return &Error{Msg: "That is not possible in the project's current state, so nothing was done.", Err: ErrComposeNoop}
}

// confirmedOf is the hash the user confirmed, for the states that hold one.
func confirmedOf(s env.Service) (string, bool) {
	switch v := s.(type) {
	case env.Down:
		return v.Confirmed, true
	case env.Up:
		return v.Confirmed, true
	case env.ComposeStarting:
		return v.Confirmed, true
	case env.ComposeStopping:
		return v.Confirmed, true
	case env.ComposeFailed:
		return v.Confirmed, true
	}
	return "", false
}

// apply feeds one input to the core and interprets the commands. The audit
// entries are written first; when that fails the new state is dropped and no
// command runs. It reports whether subscribers should be told.
func (s *ComposeService) apply(ctx context.Context, wt core.Worktree, e *composeEntry, in env.ComposeInput, actor string) (applyResult, error) {
	next, cmds := env.Compose(e.svc, in)
	var run []env.RunCompose
	var rejected error
	res := applyResult{noop: len(cmds) == 0}
	for _, c := range cmds {
		switch v := c.(type) {
		case env.RunCompose:
			run = append(run, v)
		case env.ComposePublish:
			res.publish = true
		case env.ComposeReject:
			switch v.Reason {
			case env.RejectConfirmFirst:
				rejected = &Error{Msg: "Review and confirm the compose file first. It lives in the worktree, so any change to it needs a new confirmation.", Err: ErrComposeConfirm}
			default:
				rejected = &Error{Msg: "Another compose operation of this worktree is still running. Wait for it to finish.", Err: ErrComposeBusy}
			}
		}
	}
	project, perr := s.project(wt.ID)
	if perr != nil {
		return applyResult{}, perr
	}
	now := s.c.Now().UTC()
	confirmedNow := false
	idx := -1
	for _, c := range cmds {
		a, ok := c.(env.ComposeAudit)
		if !ok {
			continue
		}
		idx++
		id, det := s.auditID(wt, in, idx)
		entry, err := s.buildAudit(id, wt, e, next, a, project, actor, now)
		if err != nil {
			return applyResult{}, err
		}
		if a.Action == env.ActionComposeConfirmed {
			if err := s.persistConfirmed(ctx, wt.RepoID, e, entry, now); err != nil {
				return applyResult{}, err
			}
			confirmedNow = !e.auto
			continue
		}
		if err := s.appendOnce(ctx, entry, det); err != nil {
			return applyResult{}, fmt.Errorf("write audit entry: %w", err)
		}
	}
	if confirmedNow {
		s.mu.Lock()
		s.confirmedBy[wt.RepoID] = wt.ID
		s.mu.Unlock()
	}
	e.svc, e.at = next, now
	for _, rc := range run {
		s.start(wt, e, rc)
	}
	return res, rejected
}

// buildAudit is the audit entry of one core audit command.
func (s *ComposeService) buildAudit(id string, wt core.Worktree, e *composeEntry, next env.Service, a env.ComposeAudit, project env.ProjectName, actor string, now time.Time) (core.AuditEntry, error) {
	fail, summary := "", ""
	switch v := next.(type) {
	case env.ComposeFailed:
		fail, summary = string(v.Kind), v.Summary
	}
	d := map[string]any{
		"project": string(project), "file": string(e.file), "hash": e.hash, "attempt": e.attempt,
		"failure": fail, "summary": s.c.Redact.Redact(summary),
	}
	if e.auto && (a.Action == env.ActionComposeConfirmed || a.Action == env.ActionComposeEnabled) {
		// The content was confirmed earlier for this repo (maybe in another worktree).
		d["autoConfirmed"] = true
		d["sourceWorktree"] = string(e.autoSource)
	}
	detail, err := json.Marshal(d)
	if err != nil {
		return core.AuditEntry{}, fmt.Errorf("encode audit detail: %w", err)
	}
	return auditEntry(id, actor, a.Action, wtSubject(wt.ID), now, detail), nil
}

// persistConfirmed stores the confirmed hash with the audit entry in one
// commit. It writes only when the repo's compose settings are still the ones
// the hash was computed for, so a settings change in between is never undone.
func (s *ComposeService) persistConfirmed(ctx context.Context, repo core.RepoID, e *composeEntry, audit core.AuditEntry, at time.Time) error {
	err := s.c.Store.UpdateComposeSettings(ctx, repo, at, audit, func(set ComposeSettings) (ComposeSettings, bool, error) {
		if !set.Enabled || set.File != e.file {
			return set, false, &Error{Msg: "The compose settings of this repo changed while you were confirming. Reload and confirm again.", Err: ErrComposeStale}
		}
		set.ConfirmedHash = e.hash
		return set, true, nil
	})
	if err != nil {
		var ce *Error
		if errors.As(err, &ce) {
			return err
		}
		return fmt.Errorf("save the confirmed compose file of repo %s: %w", repo, err)
	}
	return nil
}

// start launches one operation in a goroutine owned by the service context.
// The goroutine ends in exactly one OpFinished, whatever goes wrong, so the
// state is never left in Starting or Stopping without a goroutine. It runs
// with the lock held by the caller; it takes the lock itself.
func (s *ComposeService) start(wt core.Worktree, e *composeEntry, rc env.RunCompose) {
	file := e.file
	confirmed, _ := confirmedOf(e.svc)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		res, changed := s.execute(wt, file, rc, confirmed)
		s.finish(wt, env.OpFinished{Attempt: rc.Attempt, Failure: res.Failure, Summary: res.Summary, Now: s.c.Now().UTC()}, changed)
	}()
}

// execute reads the compose file and the `.env` again right before the run
// and runs the operation with exactly those bytes. For `up` the hash must
// still be the confirmed one: when it is not, nothing runs and changed is the
// new hash. (`stop` and `down` need no confirmation: they only act on what
// ygg started, so they run with the file as it is now.)
func (s *ComposeService) execute(wt core.Worktree, file env.RelPath, rc env.RunCompose, confirmed string) (res ComposeResult, changed string) {
	failed := func(msg string) ComposeResult { return ComposeResult{Failure: env.FailComposeError, Summary: msg} }
	project, err := s.project(wt.ID)
	if err != nil {
		return failed("This worktree's name cannot be used as a compose project name."), ""
	}
	snap := s.snapshot(wt.Path, file)
	switch {
	case snap.err != nil:
		return failed("The compose file could not be read inside this worktree, so nothing was run."), ""
	case snap.refused != nil:
		return failed(msgIndirect), ""
	case rc.Op == env.OpUp && snap.hash != confirmed:
		return failed("The compose file changed after you confirmed it, so nothing was started. Review it and confirm it again."), snap.hash
	}
	call, err := s.call(s.ctx, wt, file, project, rc.Op, snap)
	if err != nil {
		s.c.Logger.Warn("prepare a compose operation", "worktree", wt.ID, "err", err)
		return failed("ygg could not find the ports reserved for this worktree, so nothing was run. Try again."), ""
	}
	return s.runSafely(call), ""
}

// finish feeds the end of an operation to the core. The audit write is tried
// twice; when it still fails the transition is made in memory anyway, so the
// state cannot stay Starting or Stopping.
func (s *ComposeService) finish(wt core.Worktree, in env.OpFinished, changed string) {
	// The audit write must outlive a cancelled service context.
	actx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), 10*time.Second)
	defer cancel()
	s.flushAudits(10 * time.Second) // its own context, not the apply retries' deadline
	cur := s.lookup(wt.ID)
	notify := false
	if cur != nil {
		cur.mu.Lock()
		var err error
		var r applyResult
		for try := 0; try < 2; try++ {
			if r, err = s.apply(actx, wt, cur, in, "system"); err == nil {
				break
			}
			s.c.Logger.Warn("record the end of a compose operation", "worktree", wt.ID, "try", try+1, "err", err)
		}
		if err != nil {
			next, cmds := env.Compose(cur.svc, in)
			s.queueAudits(wt, cur, in, next, cmds)
			cur.svc, cur.at = next, s.c.Now().UTC()
			r.publish = true
		}
		notify = r.publish
		if changed != "" {
			if r, err := s.apply(actx, wt, cur, env.FileChanged{Hash: changed}, "system"); err != nil {
				s.c.Logger.Warn("record a changed compose file", "worktree", wt.ID, "err", err)
			} else {
				notify = notify || r.publish
			}
		}
		cur.mu.Unlock()
	}
	s.notify(wt.ID, notify) // never with the lock held
}

// queueAudits keeps the audit entries of a transition that was made in memory
// because its audit write failed, so they are written later (invariant 8).
func (s *ComposeService) queueAudits(wt core.Worktree, e *composeEntry, in env.ComposeInput, next env.Service, cmds []env.ComposeCommand) {
	project, perr := s.project(wt.ID)
	now := s.c.Now().UTC()
	idx := -1
	for _, c := range cmds {
		a, ok := c.(env.ComposeAudit)
		if !ok {
			continue
		}
		idx++
		id, det := s.auditID(wt, in, idx)
		var entry core.AuditEntry
		var err error
		if perr == nil {
			entry, err = s.buildAudit(id, wt, e, next, a, project, "system", now)
		}
		if perr != nil || err != nil {
			// Better a minimal entry than none (invariant 8).
			s.c.Logger.Error("compose audit entry reduced to its minimum", "worktree", wt.ID, "audit", id)
			entry = auditEntry(id, "system", a.Action, wtSubject(wt.ID), now, []byte(`{"degraded":true}`))
		}
		s.queueAudit(entry, det)
	}
}

func (s *ComposeService) runSafely(call ComposeCall) (res ComposeResult) {
	defer func() {
		if r := recover(); r != nil {
			res = ComposeResult{Failure: env.FailComposeError, Summary: "Docker compose stopped unexpectedly."}
		}
	}()
	return s.c.Runner.Run(s.ctx, call)
}

// snapshot reads the compose file and the `.env` next to it, scans the file
// and hashes both.
func (s *ComposeService) snapshot(root string, file env.RelPath) composeSnap {
	data, err := s.c.Files.Read(root, file)
	if err != nil {
		return composeSnap{err: err}
	}
	dotenv, hasEnv, err := s.c.Files.ReadOptional(root, env.EnvFilePath(file))
	if err != nil {
		return composeSnap{err: err}
	}
	if err := env.ScanComposeFile(data); err != nil {
		return composeSnap{refused: err}
	}
	return composeSnap{
		compose: data, hasEnv: hasEnv, hash: env.ComposeHash(data, dotenv),
		noPorts: !bytes.Contains(data, []byte("YGG_PORT")),
	}
}

// call builds the runner call: the project, the hashed bytes and the port
// block as environment variables.
func (s *ComposeService) call(ctx context.Context, wt core.Worktree, file env.RelPath, project env.ProjectName, op env.ComposeOp, snap composeSnap) (ComposeCall, error) {
	vars := map[string]string{}
	b, ok, err := s.c.Block(ctx, wt.ID)
	if err != nil {
		return ComposeCall{}, fmt.Errorf("find the port block of worktree %s: %w", wt.ID, err)
	}
	if ok {
		vars["YGG_PORT_BASE"] = strconv.Itoa(b.Base)
		for i := 0; i < b.Size; i++ {
			vars["YGG_PORT_"+strconv.Itoa(i)] = strconv.Itoa(b.Base + i)
		}
	}
	return ComposeCall{Op: op, Project: project, File: file, Compose: snap.compose, HasEnvFile: snap.hasEnv, Dir: wt.Path, Env: vars}, nil
}

// project is ygg-<name> for the worktree. The name is the worktree ID made
// lower case with other characters turned into '-'; when that changed it, or
// it is too long, it is shortened and gets a hash suffix so two worktrees never
// share a project (and so never `down` each other).
func (s *ComposeService) project(w core.WorktreeID) (env.ProjectName, error) {
	raw := string(w)
	var b strings.Builder
	for _, r := range strings.ToLower(raw) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	name := b.String()
	if name != raw || len(name) > env.MaxProjectName {
		sum := sha256.Sum256([]byte(raw))
		suffix := hex.EncodeToString(sum[:4])
		if len(name) > env.MaxProjectName-9 {
			name = name[:env.MaxProjectName-9]
		}
		name = name + "-" + suffix
	}
	p, err := env.ParseProjectName(name)
	if err != nil {
		return "", &Error{Msg: "This worktree's name cannot be used as a compose project name.", Err: err}
	}
	return p, nil
}

// statusLocked brings the entry in line with the settings and the file. It
// returns a recovery job when `docker compose ps` has to run; the caller runs
// it after releasing the lock.
func (s *ComposeService) statusLocked(ctx context.Context, wid core.WorktreeID, e *composeEntry) (ComposeStatus, bool, *psJob, error) {
	wt, err := s.c.Worktree(ctx, wid)
	if err != nil {
		return ComposeStatus{}, false, nil, err
	}
	set, err := s.c.Store.ComposeSettings(ctx, wt.RepoID)
	if err != nil {
		return ComposeStatus{}, false, nil, fmt.Errorf("load compose settings of repo %s: %w", wt.RepoID, err)
	}
	notify := false
	feed := func(in env.ComposeInput) error {
		r, err := s.apply(ctx, wt, e, in, "system")
		notify = notify || r.publish
		return err
	}
	fail := func(err error) (ComposeStatus, bool, *psJob, error) { return ComposeStatus{}, notify, nil, err }
	if !set.Enabled {
		e.file, e.hash, e.fileGone, e.noPorts, e.refused = "", "", false, false, false
		if !isOff(e.svc) {
			if err := feed(env.Disabled{}); err != nil && !errors.Is(err, ErrComposeBusy) {
				return fail(err)
			}
		}
		if isOff(e.svc) {
			e.loaded, e.fed = false, "" // a later Enabled looks at docker again
		}
		return s.viewLocked(e), notify, nil, nil
	}
	// The stored settings are the truth: when the repo now names another file,
	// or the confirmation the entry rests on is not the stored one, start from
	// Off so the entry is rebuilt from the settings (and docker is looked at again).
	if !isOff(e.svc) && ((e.file != "" && e.file != set.File) || restConfirmedDiffers(e.svc, set.ConfirmedHash)) {
		if err := feed(env.Disabled{}); err != nil && !errors.Is(err, ErrComposeBusy) {
			return fail(err)
		}
		if isOff(e.svc) {
			e.loaded, e.fed = false, ""
		}
	}
	e.file = set.File
	snap := s.snapshot(wt.Path, set.File)
	e.fileGone, e.refused = snap.err != nil, snap.refused != nil
	e.hash, e.noPorts = snap.hash, snap.noPorts
	if e.hash == "" {
		return s.viewLocked(e), notify, nil, nil
	}
	autoConfirm := set.ConfirmedHash == e.hash
	e.auto, e.autoSource = autoConfirm, s.confirmedByOf(wt.RepoID)
	defer func() { e.auto = false }()
	switch e.svc.(type) {
	case env.Off:
		if err := feed(env.Enabled{Hash: e.hash, Confirmed: set.ConfirmedHash}); err != nil {
			return fail(err)
		}
	case env.NeedsConfirm:
		if autoConfirm {
			// Confirmed earlier (another worktree of the repo, same content).
			if err := feed(env.Confirmed{Hash: e.hash}); err != nil {
				return fail(err)
			}
		} else if err := feed(env.FileChanged{Hash: e.hash}); err != nil {
			return fail(err)
		}
	default:
		if cf, ok := fileHashOf(e.svc); ok && cf != e.hash && e.fed != e.hash {
			e.fed = e.hash
			if err := feed(env.FileChanged{Hash: e.hash}); err != nil {
				return fail(err)
			}
		}
	}
	var job *psJob
	if !e.loaded && !e.loading && !isOff(e.svc) {
		e.loading = true
		job = &psJob{wt: wt, file: e.file, snap: snap}
	}
	return s.viewLocked(e), notify, job, nil
}

// restConfirmedDiffers reports a state at rest (Down, ComposeFailed) whose
// confirmed hash is not the stored one.
func restConfirmedDiffers(s env.Service, stored string) bool {
	switch v := s.(type) {
	case env.Down:
		return v.Confirmed != stored
	case env.ComposeFailed:
		return v.Confirmed != stored
	}
	return false
}

// fileHashOf is the hash the state was built for, when it has one.
func fileHashOf(s env.Service) (string, bool) {
	switch v := s.(type) {
	case env.Down:
		return v.Confirmed, true
	case env.Up:
		return v.Confirmed, true
	case env.ComposeFailed:
		return v.Confirmed, true
	}
	return "", false
}

// runPs asks docker what runs for the project (startup recovery). It owns its
// own timeout and is bound to the service context, not to a request. It runs
// without the service lock.
func (s *ComposeService) runPs(job *psJob) env.PsResult {
	project, err := s.project(job.wt.ID)
	if err != nil {
		return env.PsUnavailable
	}
	call, err := s.call(s.ctx, job.wt, job.file, project, env.OpPs, job.snap)
	if err != nil {
		return env.PsUnavailable
	}
	pctx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
	defer cancel()
	res := s.c.Runner.Run(pctx, call)
	if res.Failure != "" {
		return env.PsUnavailable
	}
	r, err := env.ParsePs(res.Output)
	if err != nil {
		return env.PsUnavailable
	}
	return r
}

// viewLocked is the display form of an entry.
func (s *ComposeService) viewLocked(e *composeEntry) ComposeStatus {
	st := ComposeStatus{State: e.svc, File: e.file, Hash: e.hash, UpdatedAt: e.at}
	if e.refused {
		st.Note = msgIndirect
		return st
	}
	switch v := e.svc.(type) {
	case env.Off:
		if e.file != "" && e.fileGone {
			st.Note = "The compose file could not be read inside this worktree. Check the file name in the repo settings."
		}
	case env.Up:
		if e.hash != "" && v.Confirmed != e.hash {
			st.Note = "The compose file changed since you confirmed it. Stop the project and confirm the new file before starting it again."
		}
	case env.ComposeFailed:
		switch v.Kind {
		case env.FailDockerMissing:
			st.Note = MsgDockerMissing
		case env.FailDaemonDown:
			st.Note = MsgDaemonDown
		default:
			st.Note = v.Summary
		}
	}
	if st.Note == "" && e.noPorts && e.hash != "" {
		if _, off := e.svc.(env.Off); !off {
			st.Note = "The compose file does not use YGG_PORT_0, YGG_PORT_1 and so on, so its ports are not offset into this worktree's block and may clash with another worktree."
		}
	}
	return st
}
