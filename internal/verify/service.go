// Package verify is the shell of automatic verification (VF-01 to VF-03,
// WE-05, BL-09; docs/design/verify.md). The decisions are pure and live in
// internal/core/verify (Step). This package parses outside data into core
// types, loads the state, calls Step, persists the new state and its audit
// entries in one transaction, and only then runs the commands: it queues
// builds in the task's worktree through the builds service (which starts every
// process through platform.Start with the clean environment), cancels them,
// and tells the UI.
//
// The reconcile loop works on stored state: a missed wake or a restart is
// picked up by the next pass.
package verify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/audit"
	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	cv "github.com/brendatamaar/yggdrasil/internal/core/verify"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// BuildRunner is what the service needs from the builds service
// (*builds.Service satisfies it). Get of an unknown build is NotFound (an
// apperr NotFound or db.ErrNotFound).
type BuildRunner interface {
	// EnqueueWithID is idempotent by ID: a known ID returns the existing build.
	EnqueueWithID(ctx context.Context, id core.BuildID, spec corebuilds.Spec, actor corebuilds.Actor) (core.BuildID, error)
	// Cancel is idempotent; cancelling a finished or unknown build is a no-op.
	Cancel(ctx context.Context, id core.BuildID, actor corebuilds.Actor) error
	Get(ctx context.Context, id core.BuildID) (corebuilds.State, error)
}

// BuildLister is optional: a BuildRunner that also lists builds (*builds.Service
// does) lets the reconcile pass cancel builds that verification started and no
// verification waits for any more (a lost cancel).
type BuildLister interface {
	List(ctx context.Context, f db.BuildFilter) ([]corebuilds.State, error)
}

// LogReader reads the log of one build step (*builds.Service satisfies it):
// the bytes from offset after and the offset to ask for next.
type LogReader interface {
	StepLog(ctx context.Context, id core.BuildID, step int, after int64) ([]byte, int64, error)
}

// TaskMessenger sends text to the task's agent as a new message in the same
// mode (VF-03). It is not part of Step: tasks owns the new turn and its audit.
// A busy task is a refusal in plain language.
type TaskMessenger interface {
	SendMessage(ctx context.Context, task core.TaskID, text string) error
}

// DiffProbe says whether a worktree differs from its base commit, untracked
// files included (*git.Git satisfies it).
type DiffProbe interface {
	HasChanges(ctx context.Context, dir, base string) (bool, error)
}

// Publisher tells the UI that a verification changed (hub topic
// "verifications"). It carries no log, path or command.
type Publisher interface {
	PublishVerification(id cv.VerificationID, task core.TaskID, repo core.RepoID, state string)
}

// Deps are what the service needs. Store, Builds, Probe, Messenger and
// Toolchains are required.
type Deps struct {
	Store      Store
	Builds     BuildRunner
	Logs       LogReader // optional: without it "send failure" has no log tail
	Messenger  TaskMessenger
	Probe      DiffProbe
	Toolchains []builds.Toolchain
	Hub        Publisher // optional
	Redactor   safety.Redactor
	// OpenFS returns the file system of a folder. Default os.DirFS.
	OpenFS func(dir string) fs.FS
	Clock  func() time.Time
	IDs    func(prefix string) string
	Log    *slog.Logger
	// Interval is the time between reconcile passes. Default 5 s.
	Interval time.Duration
	// EnqueueBackoff is the first wait between enqueue retries; it doubles.
	// Default 100 ms.
	EnqueueBackoff time.Duration
}

const (
	systemActor    = "system"
	userActor      = "user"
	enqueueTries   = 3
	maxRunAttempts = 4
	logReadLimit   = 10000
	// logCutMargin is longer than any secret: dropped from a cut log that has
	// no line break.
	logCutMargin    = 1024
	failureLogWatch = 3
	// buildIDPrefix starts the ID of every build verification queues, so the
	// sweep for orphaned builds knows which builds are its own.
	buildIDPrefix = "vbld"
)

// Service runs verifications.
type Service struct {
	d    Deps
	mu   sync.Mutex // serializes every pass and every user action
	wake chan struct{}
	// factFailures counts consecutive failures to read the facts of one
	// aggregate, to log only a persistent one.
	factFailures map[key]int
}

type key struct {
	task core.TaskID
	repo core.RepoID
	wt   core.WorktreeID
}

// New checks deps and returns the service. Nothing runs until Run.
func New(d Deps) (*Service, error) {
	switch {
	case d.Store == nil:
		return nil, errors.New("verify: a store is required")
	case d.Builds == nil:
		return nil, errors.New("verify: a build runner is required")
	case d.Messenger == nil:
		return nil, errors.New("verify: a task messenger is required")
	case d.Probe == nil:
		return nil, errors.New("verify: a diff probe is required")
	case len(d.Toolchains) == 0:
		return nil, errors.New("verify: at least one toolchain is required")
	}
	if d.Redactor == nil {
		d.Redactor = safety.NewRedactor()
	}
	if d.OpenFS == nil {
		d.OpenFS = func(dir string) fs.FS { return os.DirFS(dir) }
	}
	if d.Clock == nil {
		d.Clock = func() time.Time { return time.Now().UTC() }
	}
	if d.IDs == nil {
		d.IDs = db.NewID
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.Interval <= 0 {
		d.Interval = 5 * time.Second
	}
	if d.EnqueueBackoff <= 0 {
		d.EnqueueBackoff = 100 * time.Millisecond
	}
	return &Service{d: d, wake: make(chan struct{}, 1), factFailures: map[key]int{}}, nil
}

// Run reconciles until ctx is done: once at start (which resumes interrupted
// runs) and then on every tick or Wake. The loop goroutine is owned by ctx
// through an errgroup.
func (s *Service) Run(ctx context.Context) error {
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		t := time.NewTicker(s.d.Interval)
		defer t.Stop()
		for {
			if gctx.Err() != nil {
				return nil
			}
			if err := s.Reconcile(gctx); err != nil && gctx.Err() == nil {
				s.d.Log.Warn("verification pass failed", "err", err)
			}
			select {
			case <-gctx.Done():
				return nil
			case <-t.C:
			case <-s.wake:
			}
		}
	})
	return g.Wait()
}

// Wake asks the loop for a pass soon (a task changed, a build changed). It
// never blocks.
func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) now() time.Time { return s.d.Clock().UTC().Truncate(time.Millisecond) }

// isNotFound reports whether err means "does not exist".
func isNotFound(err error) bool {
	if errors.Is(err, db.ErrNotFound) {
		return true
	}
	c, ok := apperr.ClassOf(err)
	return ok && c == apperr.NotFound
}

func isConflict(err error) bool { return errors.Is(err, db.ErrConflict) }

func keyOf(r Record) key { return key{task: r.Task, repo: r.Repo, wt: r.Worktree} }

// load returns the stored record of an aggregate, or a new None one.
func (s *Service) load(ctx context.Context, k key) (Record, error) {
	row, err := s.d.Store.GetByTaskRepo(ctx, k.task, k.repo)
	if err != nil {
		if isNotFound(err) {
			return Record{
				Verification: cv.Verification{ID: cv.VerificationID(s.d.IDs("ver")), Task: k.task, Repo: k.repo, Worktree: k.wt, State: cv.None{}},
				Version:      db.NewVerification,
			}, nil
		}
		return Record{}, fmt.Errorf("load verification of %s/%s: %w", k.task, k.repo, err)
	}
	return VerificationFromRow(row)
}

// prepareFn builds the input for a record, reading whatever it needs. ok is
// false when there is nothing to do.
type prepareFn func(rec Record) (in cv.Input, note string, ok bool, err error)

// run is the one path every input takes: load, prepare (reads outside data),
// then commit Step with the audit entries in one transaction, then interpret
// the commands. A version conflict reloads and starts again.
func (s *Service) run(ctx context.Context, k key, actor string, prep prepareFn) (Record, bool, error) {
	var last Record
	for try := 0; try < maxRunAttempts; try++ {
		rec, err := s.load(ctx, k)
		if err != nil {
			return Record{}, false, err
		}
		last = rec
		in, note, ok, err := prep(rec)
		if err != nil {
			return rec, false, err
		}
		if !ok {
			return rec, false, nil
		}
		after, cmds, err := s.commit(ctx, rec, in, note, actor)
		if isConflict(err) {
			continue
		}
		if err != nil {
			return rec, false, err
		}
		if len(cmds) == 0 {
			return rec, false, nil
		}
		s.interpret(ctx, after, cmds)
		return after, true, nil
	}
	return last, false, fmt.Errorf("verification of %s/%s changed while it was being updated: %w", k.task, k.repo, db.ErrConflict)
}

// commit steps the record and writes the new state and its audit entries in
// one transaction. With no command nothing is written.
func (s *Service) commit(ctx context.Context, rec Record, in cv.Input, note, actor string) (Record, []cv.Command, error) {
	after := rec
	var cmds []cv.Command
	err := s.d.Store.Commit(ctx, func(tx Tx) error {
		after, cmds = rec, nil
		row, lerr := tx.LoadByTaskRepo(ctx, rec.Task, rec.Repo)
		switch {
		case lerr == nil:
			if row.Version != rec.Version {
				return fmt.Errorf("verification %s moved to version %d: %w", rec.ID, row.Version, db.ErrConflict)
			}
		case isNotFound(lerr):
			if rec.Version != db.NewVerification {
				return fmt.Errorf("verification %s is gone: %w", rec.ID, db.ErrConflict)
			}
		default:
			return lerr
		}
		next, out := cv.Step(rec.Verification, in)
		if len(out) == 0 {
			return nil
		}
		if u, isU := next.State.(cv.Unverifiable); !isU || u.Reason != cv.UnverifiableNoCommand {
			note = ""
		}
		nrow, ferr := RowFromVerification(next, note)
		if ferr != nil {
			return ferr
		}
		now := s.now()
		ver, serr := tx.Save(ctx, nrow, rec.Version, now)
		if serr != nil {
			return serr
		}
		for _, c := range out {
			w, isAudit := c.(cv.WriteAudit)
			if !isAudit {
				continue
			}
			e, aerr := s.auditEntry(actor, next, w, now)
			if aerr != nil {
				return aerr
			}
			if aerr := tx.AppendAudit(ctx, e); aerr != nil {
				return aerr
			}
		}
		after = Record{Verification: next, Version: ver, Note: note, CreatedAt: rec.CreatedAt, UpdatedAt: now}
		cmds = out
		return nil
	})
	if err != nil {
		return rec, nil, err
	}
	return after, cmds, nil
}

func (s *Service) auditEntry(actor string, v cv.Verification, w cv.WriteAudit, now time.Time) (core.AuditEntry, error) {
	detail, err := json.Marshal(map[string]string{"task": string(v.Task), "repo": string(v.Repo), "turn": string(w.Turn), "outcome": w.Outcome})
	if err != nil {
		return core.AuditEntry{}, fmt.Errorf("audit detail: %w", err)
	}
	return prepareAudit(s, core.AuditEntry{Actor: actor, Action: w.Action, Subject: "verification:" + string(v.ID), Detail: detail}, now)
}

// prepareAudit redacts and validates an audit entry and gives it an ID.
func prepareAudit(s *Service, e core.AuditEntry, now time.Time) (core.AuditEntry, error) {
	p, err := audit.Prepare(e, s.d.Redactor, core.AuditID(s.d.IDs("aud")), now)
	if err != nil {
		return core.AuditEntry{}, fmt.Errorf("prepare audit entry: %w", err)
	}
	return p, nil
}

// buildSpec is the builds spec of one stage: the steps in the task's worktree.
func buildSpec(wt core.WorktreeID, repo core.RepoID, steps []cv.StepSpec) (corebuilds.Spec, error) {
	spec := corebuilds.Spec{Target: corebuilds.InWorktree{WorktreeID: wt}}
	for _, st := range steps {
		spec.Steps = append(spec.Steps, corebuilds.StepSpec{
			RepoID: repo, Name: st.Name, Argv: append([]string(nil), st.Argv...), Env: append([]string(nil), st.Env...),
		})
	}
	return spec, spec.Validate()
}

// interpret runs the commands of a committed step. The state and the audit
// entries are already stored, so a failure here is a log line or a new input
// (EnqueueFailed), never a rollback. A panic is recovered and logged.
func (s *Service) interpret(ctx context.Context, rec Record, cmds []cv.Command) {
	defer func() {
		if r := recover(); r != nil {
			s.d.Log.Error("verification command panicked", "verification", rec.ID, "panic", fmt.Sprint(r))
		}
	}()
	for _, c := range cmds {
		switch c := c.(type) {
		case cv.EnqueueBuild:
			s.enqueue(ctx, rec, c)
		case cv.CancelBuild:
			if err := s.d.Builds.Cancel(ctx, c.Build, systemActor); err != nil && ctx.Err() == nil {
				s.d.Log.Warn("cancel verification build failed", "verification", rec.ID, "build", c.Build, "err", err)
			}
		case cv.Publish:
			if s.d.Hub != nil {
				s.d.Hub.PublishVerification(rec.ID, rec.Task, rec.Repo, StateCode(rec.State))
			}
		case cv.WriteAudit:
			// Stored with the state in commit.
		}
	}
}

// enqueue queues a build, idempotent by ID, with a few retries for transient
// errors. A spec the builds service rejects, or a persistent error, becomes an
// EnqueueFailed input.
func (s *Service) enqueue(ctx context.Context, rec Record, c cv.EnqueueBuild) {
	spec, err := buildSpec(c.Spec.Worktree, rec.Repo, c.Spec.Steps)
	if err == nil {
		wait := s.d.EnqueueBackoff
		for try := 1; ; try++ {
			if _, err = s.d.Builds.EnqueueWithID(ctx, c.Build, spec, systemActor); err == nil {
				return
			}
			if ctx.Err() != nil {
				return // shutting down: the next start queues it again, same ID
			}
			if class, ok := apperr.ClassOf(err); ok && class == apperr.Invalid {
				break
			}
			if try >= enqueueTries {
				break
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			wait *= 2
		}
	}
	s.d.Log.Warn("could not queue verification build", "verification", rec.ID, "build", c.Build, "err", err)
	_, _, rerr := s.run(ctx, keyOf(rec), systemActor, func(Record) (cv.Input, string, bool, error) {
		return cv.EnqueueFailed{Build: c.Build, Reason: "the build could not be queued", Now: s.now()}, "", true, nil
	})
	if rerr != nil && ctx.Err() == nil {
		s.d.Log.Error("could not record a failed enqueue", "verification", rec.ID, "err", rerr)
	}
}
