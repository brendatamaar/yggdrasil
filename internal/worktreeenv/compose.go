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
)

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
}

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

	mu     sync.Mutex
	states map[core.WorktreeID]*composeEntry
}

type composeEntry struct {
	svc     env.Service
	attempt int
	loaded  bool   // startup recovery (ps) has run
	fed     string // last file hash given to the core as FileChanged, so a poll does not repeat it
	// view of the file at the last sync
	file     env.RelPath
	hash     string
	noPorts  bool
	fileGone bool
	at       time.Time
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
	return &ComposeService{c: c, ctx: ctx, states: map[core.WorktreeID]*composeEntry{}}, nil
}

// Wait blocks until the running operations have ended (after ctx is cancelled).
func (s *ComposeService) Wait() { s.wg.Wait() }

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
	old, err := s.c.Store.ComposeSettings(ctx, repo)
	if err != nil {
		return fmt.Errorf("load compose settings of repo %s: %w", repo, err)
	}
	next := ComposeSettings{Enabled: enabled, File: rel, ConfirmedHash: old.ConfirmedHash}
	if rel != old.File {
		next.ConfirmedHash = ""
	}
	if next == old {
		return nil
	}
	detail, err := json.Marshal(map[string]any{"enabled": enabled, "file": string(rel)})
	if err != nil {
		return fmt.Errorf("encode audit detail: %w", err)
	}
	now := s.c.Now().UTC()
	e := auditEntry(s.c.NewID("aud"), "user", "worktreeenv.compose_settings", "repo:"+string(repo), now, detail)
	if err := s.c.Store.PutComposeSettings(ctx, repo, next, now, e); err != nil {
		return fmt.Errorf("save compose settings of repo %s: %w", repo, err)
	}
	return nil
}

// Status returns the compose state of a worktree, bringing it in line with the
// settings and the file first.
func (s *ComposeService) Status(ctx context.Context, wid core.WorktreeID) (ComposeStatus, error) {
	s.mu.Lock()
	st, notify, err := s.statusLocked(ctx, wid)
	s.mu.Unlock()
	s.notify(wid, notify)
	return st, err
}

// Confirm records the user's confirmation of the compose file. hash is the
// hash the user was shown; when it is not "" and the file has changed since,
// nothing is confirmed (ErrComposeStale).
func (s *ComposeService) Confirm(ctx context.Context, wid core.WorktreeID, hash string) (ComposeStatus, error) {
	return s.act(ctx, wid, func(e *composeEntry, now time.Time) (env.ComposeInput, error) {
		if hash != "" && hash != e.hash {
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
	s.mu.Lock()
	notify := false
	st, n, err := s.statusLocked(ctx, wid)
	notify = n
	if err == nil {
		wt, _ := s.c.Worktree(ctx, wid) // tracked: checked by statusLocked
		e := s.states[wid]
		if _, off := e.svc.(env.Off); off {
			err = &Error{Msg: "Docker compose is not turned on for this repo. Turn it on in the repo settings first.", Err: ErrComposeOff}
		} else if e.hash == "" {
			err = &Error{Msg: "The compose file could not be read inside this worktree. Check the file name in the repo settings.", Err: ErrComposeFile}
		} else {
			var in env.ComposeInput
			if in, err = mk(e, s.c.Now().UTC()); err == nil {
				var changed bool
				changed, err = s.apply(ctx, wt, e, in, "user")
				notify = notify || changed
			}
		}
		if err == nil {
			st = s.viewLocked(wid)
		}
	}
	s.mu.Unlock()
	s.notify(wid, notify)
	return st, err
}

// apply feeds one input to the core and interprets the commands. The audit
// entries are written first; when that fails the new state is dropped and no
// command runs. It reports whether subscribers should be told.
func (s *ComposeService) apply(ctx context.Context, wt core.Worktree, e *composeEntry, in env.ComposeInput, actor string) (bool, error) {
	next, cmds := env.Compose(e.svc, in)
	var run []env.RunCompose
	var rejected error
	publish := false
	for _, c := range cmds {
		switch v := c.(type) {
		case env.RunCompose:
			run = append(run, v)
		case env.ComposePublish:
			publish = true
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
		return false, perr
	}
	now := s.c.Now().UTC()
	for _, c := range cmds {
		a, ok := c.(env.ComposeAudit)
		if !ok {
			continue
		}
		fail, summary := "", ""
		switch v := next.(type) {
		case env.ComposeFailed:
			fail, summary = string(v.Kind), v.Summary
		}
		detail, err := json.Marshal(map[string]any{
			"project": string(project), "file": string(e.file), "hash": e.hash, "attempt": e.attempt,
			"failure": fail, "summary": s.c.Redact.Redact(summary),
		})
		if err != nil {
			return false, fmt.Errorf("encode audit detail: %w", err)
		}
		entry := auditEntry(s.c.NewID("aud"), actor, a.Action, wtSubject(wt.ID), now, detail)
		if a.Action == env.ActionComposeConfirmed {
			if err := s.persistConfirmed(ctx, wt.RepoID, e, entry, now); err != nil {
				return false, err
			}
			continue
		}
		if err := s.c.Audit.Append(ctx, entry); err != nil {
			return false, fmt.Errorf("write audit entry: %w", err)
		}
	}
	e.svc, e.at = next, now
	for _, rc := range run {
		if err := s.start(wt, e, project, rc); err != nil {
			return publish, err
		}
	}
	return publish, rejected
}

// persistConfirmed stores the confirmed hash with the audit entry in one commit.
func (s *ComposeService) persistConfirmed(ctx context.Context, repo core.RepoID, e *composeEntry, audit core.AuditEntry, at time.Time) error {
	set, err := s.c.Store.ComposeSettings(ctx, repo)
	if err != nil {
		return fmt.Errorf("load compose settings of repo %s: %w", repo, err)
	}
	set.ConfirmedHash = e.hash
	if err := s.c.Store.PutComposeSettings(ctx, repo, set, at, audit); err != nil {
		return fmt.Errorf("save the confirmed compose file of repo %s: %w", repo, err)
	}
	return nil
}

// start launches one operation in a goroutine owned by the service context.
func (s *ComposeService) start(wt core.Worktree, e *composeEntry, project env.ProjectName, rc env.RunCompose) error {
	call, err := s.call(s.ctx, wt, e.file, project, rc.Op)
	if err != nil {
		return err
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		res := s.runSafely(call)
		in := env.OpFinished{Attempt: rc.Attempt, Failure: res.Failure, Summary: res.Summary, Now: s.c.Now().UTC()}
		// The audit write must outlive a cancelled service context.
		actx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), 10*time.Second)
		defer cancel()
		s.mu.Lock()
		changed := false
		if cur := s.states[wt.ID]; cur != nil {
			var err error
			if changed, err = s.apply(actx, wt, cur, in, "system"); err != nil {
				s.c.Logger.Warn("record the end of a compose operation", "worktree", wt.ID, "err", err)
			}
		}
		s.mu.Unlock()
		s.notify(wt.ID, changed) // never with the lock held
	}()
	return nil
}

func (s *ComposeService) runSafely(call ComposeCall) (res ComposeResult) {
	defer func() {
		if r := recover(); r != nil {
			res = ComposeResult{Failure: env.FailComposeError, Summary: "Docker compose stopped unexpectedly."}
		}
	}()
	return s.c.Runner.Run(s.ctx, call)
}

// call builds the runner call: the project, the file relative to the worktree
// and the port block as environment variables.
func (s *ComposeService) call(ctx context.Context, wt core.Worktree, file env.RelPath, project env.ProjectName, op env.ComposeOp) (ComposeCall, error) {
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
	return ComposeCall{Op: op, Project: project, File: file, Dir: wt.Path, Env: vars}, nil
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

// statusLocked brings the entry in line with the settings and the file.
func (s *ComposeService) statusLocked(ctx context.Context, wid core.WorktreeID) (ComposeStatus, bool, error) {
	wt, err := s.c.Worktree(ctx, wid)
	if err != nil {
		return ComposeStatus{}, false, err
	}
	set, err := s.c.Store.ComposeSettings(ctx, wt.RepoID)
	if err != nil {
		return ComposeStatus{}, false, fmt.Errorf("load compose settings of repo %s: %w", wt.RepoID, err)
	}
	e := s.states[wid]
	if e == nil {
		e = &composeEntry{svc: env.Off{}}
		s.states[wid] = e
	}
	notify := false
	feed := func(in env.ComposeInput) error {
		ch, err := s.apply(ctx, wt, e, in, "system")
		notify = notify || ch
		return err
	}
	if !set.Enabled {
		e.file, e.hash, e.fileGone, e.noPorts = "", "", false, false
		if _, off := e.svc.(env.Off); !off {
			if err := feed(env.Disabled{}); err != nil && !errors.Is(err, ErrComposeBusy) {
				return ComposeStatus{}, notify, err
			}
		}
		return s.viewLocked(wid), notify, nil
	}
	e.file = set.File
	data, rerr := s.c.Files.Read(wt.Path, set.File)
	e.fileGone = rerr != nil
	e.hash, e.noPorts = "", false
	if rerr == nil {
		sum := sha256.Sum256(data)
		e.hash = hex.EncodeToString(sum[:])
		e.noPorts = !bytes.Contains(data, []byte("YGG_PORT"))
	}
	if e.hash == "" {
		return s.viewLocked(wid), notify, nil
	}
	switch e.svc.(type) {
	case env.Off:
		if err := feed(env.Enabled{Hash: e.hash, Confirmed: set.ConfirmedHash}); err != nil {
			return ComposeStatus{}, notify, err
		}
	case env.NeedsConfirm:
		if set.ConfirmedHash == e.hash {
			// Confirmed earlier (another worktree of the repo, same file content).
			if err := feed(env.Confirmed{Hash: e.hash}); err != nil {
				return ComposeStatus{}, notify, err
			}
		} else if err := feed(env.FileChanged{Hash: e.hash}); err != nil {
			return ComposeStatus{}, notify, err
		}
	default:
		if cf, ok := fileHashOf(e.svc); ok && cf != e.hash && e.fed != e.hash {
			e.fed = e.hash
			if err := feed(env.FileChanged{Hash: e.hash}); err != nil {
				return ComposeStatus{}, notify, err
			}
		}
	}
	if !e.loaded {
		e.loaded = true
		if _, off := e.svc.(env.Off); !off {
			if err := feed(env.RecoveredPs{Result: s.ps(ctx, wt, e), Now: s.c.Now().UTC()}); err != nil {
				return ComposeStatus{}, notify, err
			}
		}
	}
	return s.viewLocked(wid), notify, nil
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

// ps asks docker what runs for the project (startup recovery).
func (s *ComposeService) ps(ctx context.Context, wt core.Worktree, e *composeEntry) env.PsResult {
	project, err := s.project(wt.ID)
	if err != nil {
		return env.PsUnavailable
	}
	call, err := s.call(ctx, wt, e.file, project, env.OpPs)
	if err != nil {
		return env.PsUnavailable
	}
	pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
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
func (s *ComposeService) viewLocked(wid core.WorktreeID) ComposeStatus {
	e := s.states[wid]
	st := ComposeStatus{State: e.svc, File: e.file, Hash: e.hash, UpdatedAt: e.at}
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
