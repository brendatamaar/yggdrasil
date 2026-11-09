// Package worktrees creates the git worktree each task works in (AG-07,
// WE-04). It is the shell around internal/git: names come from names.go,
// folders are short, and nothing here ever removes a worktree or a file
// (invariant 1).
package worktrees

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	env "github.com/brendatamaar/yggdrasil/internal/core/worktreeenv"
	cw "github.com/brendatamaar/yggdrasil/internal/core/worktrees"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// RepoRef is a tracked repo a worktree is made for. The caller passes only
// tracked repos (invariant 6).
type RepoRef struct {
	ID   core.RepoID
	Path string
	// DefaultBranch is where the worktree starts; HEAD of the repo when empty.
	DefaultBranch string
}

// Git is what Ensure needs from internal/git.
type Git interface {
	WorktreeAdd(ctx context.Context, repoDir, path, newBranch, startPoint string) (git.Worktree, error)
	ResolveCommit(ctx context.Context, dir, ref string) (string, error)
	BranchExists(ctx context.Context, dir, name string) (local, remote bool, err error)
}

// Store keeps the worktree rows.
type Store interface {
	// List returns the worktrees recorded for the task.
	List(ctx context.Context, task core.TaskID) ([]core.Worktree, error)
	// Record stores the worktree, its base commit and the audit entry in one
	// commit (invariant 8).
	Record(ctx context.Context, w core.Worktree, baseCommit string, audit core.AuditEntry) error
}

// Preparer gets a newly recorded worktree ready: a port block and the repo's
// bootstrap profile files (WE-01, WE-02; internal/worktreeenv). The trigger
// says who started the worktree.
type Preparer interface {
	Prepare(ctx context.Context, w core.Worktree, trigger env.Trigger) error
}

// Config wires an Ensurer. All fields are required except Prepare and
// OnPrepareError.
type Config struct {
	Git     Git
	Store   Store
	Rand    Rand
	Now     func() time.Time
	NewID   func(prefix string) string
	DataDir string
	// Prepare, when set, is called once for every worktree created by Ensure
	// (not for ones that already existed), inline right after the worktree is
	// recorded: Ensure waits for it, so it must stay quick (a port block and a
	// few small files). A failure never fails Ensure: the worktree works
	// without it.
	Prepare Preparer
	// OnPrepareError, when set, receives the failure of Prepare. When nil the
	// failure is logged as a warning (Logger) with the worktree and repo IDs
	// and the plain-language message only, never paths.
	OnPrepareError func(w core.Worktree, err error)
	// Logger receives that warning. Default: slog.Default().
	Logger *slog.Logger
}

// longPath is platform.LongPath; a test replaces it to make it fail.
var longPath = platform.LongPath

// Ensurer creates worktrees.
type Ensurer struct{ c Config }

// New returns an Ensurer.
func New(c Config) (*Ensurer, error) {
	if c.Git == nil || c.Store == nil || c.Rand == nil || c.Now == nil || c.NewID == nil || c.DataDir == "" {
		return nil, errors.New("worktrees: git, store, rand, clock, id source and data directory are required")
	}
	if err := platform.CheckPath(c.DataDir); err != nil {
		return nil, fmt.Errorf("worktrees: data directory: %w", err)
	}
	return &Ensurer{c: c}, nil
}

// Error is a failure with a plain-language message that holds no paths.
type Error struct {
	Msg string
	Err error
}

func (e *Error) Error() string       { return e.Msg + ": " + e.Err.Error() }
func (e *Error) Unwrap() error       { return e.Err }
func (e *Error) UserMessage() string { return e.Msg }

// ErrNameTaken is returned when 5 generated names were all taken.
var ErrNameTaken = errors.New("no free worktree name")

// Ensure returns one worktree per repo for the task. Repos that already have
// an active worktree for the task keep it; the others get a new one on a new
// branch ygg/<name> started from the repo's default branch. On failure it
// returns the worktrees that exist so far together with the error, and
// removes nothing.
//
// The decisions (which repos need one, the name retry, the partial-failure
// outcome) are made by internal/core/worktrees; this function runs the
// commands it returns and feeds the answers back.
func (e *Ensurer) Ensure(ctx context.Context, task core.TaskID, repos []RepoRef) ([]core.Worktree, error) {
	return e.EnsureFor(ctx, task, repos, env.TriggerAgentTask)
}

// EnsureFor is Ensure with the trigger that says who started the task. Use
// TriggerUserStarted for a task the user started or confirmed; Ensure uses
// TriggerAgentTask, which copies no profile files unless the repo allows it.
func (e *Ensurer) EnsureFor(ctx context.Context, task core.TaskID, repos []RepoRef, trigger env.Trigger) ([]core.Worktree, error) {
	if err := task.Validate(); err != nil {
		return nil, err
	}
	ids := make([]core.RepoID, len(repos))
	refs := make(map[core.RepoID]RepoRef, len(repos))
	for i, r := range repos {
		ids[i] = r.ID
		if _, ok := refs[r.ID]; !ok {
			refs[r.ID] = r
		}
	}
	x := &exec{e: e, ctx: ctx, refs: refs, dirs: map[cw.Name]string{}, trigger: trigger}
	state, cmds := cw.Step(cw.Idle{}, cw.Begin{Task: task, Repos: ids})
	for {
		if placed, failure, done := cw.Result(state); done {
			out := append(make([]core.Worktree, 0, len(repos)), placed...)
			if failure == nil {
				return out, nil
			}
			return out, x.errorFor(*failure)
		}
		if len(cmds) != 1 {
			return nil, fmt.Errorf("worktrees: internal error: %d commands outstanding", len(cmds))
		}
		state, cmds = cw.Step(state, x.run(cmds[0]))
	}
}

// exec carries out the core's commands.
type exec struct {
	e        *Ensurer
	ctx      context.Context
	refs     map[core.RepoID]RepoRef
	dirs     map[cw.Name]string
	existing []core.Worktree
	cause    error // the detail of the last failed command
	trigger  env.Trigger
}

func (x *exec) gitCause(err error) cw.Cause {
	x.cause = err
	if x.ctx.Err() != nil {
		return cw.CauseCanceled
	}
	return cw.CauseGit
}

func (x *exec) run(c cw.Command) cw.Input {
	e := x.e
	switch v := c.(type) {
	case cw.LoadExisting:
		rows, err := e.c.Store.List(x.ctx, v.Task)
		if err != nil {
			x.cause = err
			return cw.LoadFailed{Seq: v.Seq}
		}
		x.existing = rows
		known := make([]cw.Known, len(rows))
		for i, w := range rows {
			known[i] = cw.Known{Worktree: w, InRoot: e.underWorktreeRoot(w.Path)}
		}
		return cw.ExistingLoaded{Seq: v.Seq, Existing: known}
	case cw.PrepareRepo:
		r := x.refs[v.Repo]
		if err := platform.CheckPath(r.Path); err != nil {
			x.cause = err
			return cw.PrepareFailed{Seq: v.Seq, Cause: cw.CauseUnusable}
		}
		base, err := e.c.Git.ResolveCommit(x.ctx, r.Path, r.DefaultBranch)
		if err != nil {
			return cw.PrepareFailed{Seq: v.Seq, Cause: x.gitCause(err)}
		}
		if err := os.MkdirAll(filepath.Join(e.c.DataDir, "wt"), 0o750); err != nil {
			return cw.PrepareFailed{Seq: v.Seq, Cause: x.gitCause(err)}
		}
		return cw.Prepared{Seq: v.Seq, Base: base}
	case cw.DrawName:
		return cw.NameDrawn{Seq: v.Seq, Name: cw.Name(Generate(e.c.Rand, v.Attempt))}
	case cw.CheckName:
		dir := filepath.Join(e.c.DataDir, "wt", platform.ShortDirName(string(v.Name), MaxNameLen))
		x.dirs[v.Name] = dir
		taken, err := e.taken(x.ctx, x.refs[v.Repo].Path, Name(v.Name), dir)
		if err != nil {
			return cw.ProbeFailed{Seq: v.Seq, Cause: x.gitCause(err)}
		}
		return cw.Probed{Seq: v.Seq, Taken: taken}
	case cw.AddWorktree:
		r := x.refs[v.Repo]
		dir := x.dirs[v.Name]
		if _, err := e.c.Git.WorktreeAdd(x.ctx, r.Path, dir, v.Branch, v.Base); err != nil {
			return cw.AddFailed{Seq: v.Seq, Cause: x.gitCause(err)}
		}
		long, err := longPath(dir)
		if err != nil {
			// The worktree exists on disk: record it with the plain path rather
			// than orphan it (a failed AddFailed here would leave it unrecorded).
			long = dir
		}
		now := e.c.Now().UTC()
		return cw.Added{
			Seq: v.Seq, ID: core.WorktreeID(e.c.NewID("wt")), AuditID: core.AuditID(e.c.NewID("aud")),
			Path: long, At: now,
		}
	case cw.RecordWorktree:
		d := v.Audit.Detail
		detail, err := json.Marshal(map[string]string{"worktree": string(d.Worktree), "repo": string(d.Repo), "branch": d.Branch, "base": d.Base})
		if err != nil {
			x.cause = err
			return cw.RecordFailed{Seq: v.Seq}
		}
		audit := core.AuditEntry{
			ID: v.Audit.ID, At: v.Audit.At, Actor: v.Audit.Actor, Action: v.Audit.Action,
			Subject: v.Audit.Subject, Detail: detail,
		}
		if err := e.c.Store.Record(x.ctx, v.Worktree, v.Base, audit); err != nil {
			x.cause = fmt.Errorf("record worktree %s: %w", v.Worktree.ID, err)
			return cw.RecordFailed{Seq: v.Seq}
		}
		if e.c.Prepare != nil {
			if err := e.c.Prepare.Prepare(x.ctx, v.Worktree, x.trigger); err != nil {
				e.prepareFailed(v.Worktree, err)
			}
		}
		return cw.Recorded{Seq: v.Seq}
	}
	// Unreachable: the core issues only the commands above.
	return nil
}

// errorFor turns the core's failure into the error callers see.
func (x *exec) errorFor(f cw.Failure) error {
	switch f.Kind {
	case cw.FailLoad:
		return &Error{Msg: "Could not read the task's worktrees. Try again.", Err: x.cause}
	case cw.FailOutsideRoot:
		var id core.WorktreeID
		for _, w := range x.existing {
			if w.RepoID == f.Repo && w.Status == core.WorktreeActive {
				id = w.ID
			}
		}
		return &Error{
			Msg: "A saved worktree for this task is not in Yggdrasil's worktree folder, so it was not used. Check the data folder setting or restore the folder, then try again.",
			Err: fmt.Errorf("worktree %s of repo %s is outside the worktree root", id, f.Repo),
		}
	case cw.FailUnusablePath:
		// The detail (it may hold the path) stays in Err, never in the message.
		return &Error{
			Msg: "Yggdrasil can't use this repo's folder. Folders inside WSL are not supported yet; move the repository to a normal Windows folder and add it again.",
			Err: x.cause,
		}
	case cw.FailCanceled:
		return fmt.Errorf("create worktree: %w", x.ctx.Err())
	case cw.FailNameTaken:
		return &Error{
			Msg: "Yggdrasil could not find a free worktree name. Try again.",
			Err: fmt.Errorf("repo %s: %w", f.Repo, ErrNameTaken),
		}
	case cw.FailRecord:
		return &Error{Msg: "The worktree was created but Yggdrasil could not save it. Try again.", Err: x.cause}
	}
	return &Error{
		Msg: "Git could not create the worktree. Check that the repo is healthy and no other git operation is running, then try again.",
		Err: fmt.Errorf("create worktree for repo %s: %w", f.Repo, x.cause),
	}
}

// underWorktreeRoot reports whether p lies under DataDir/wt (plain or long
// form), the only place Yggdrasil makes worktrees.
func (e *Ensurer) underWorktreeRoot(p string) bool {
	roots := []string{filepath.Join(e.c.DataDir, "wt")}
	if long, err := platform.LongPath(e.c.DataDir); err == nil {
		roots = append(roots, filepath.Join(long, "wt"))
	}
	if long, err := platform.LongPath(roots[0]); err == nil {
		roots = append(roots, long)
	}
	p = filepath.Clean(p)
	for _, root := range roots {
		root = filepath.Clean(root)
		if platform.FoldCase() {
			root, p = strings.ToLower(root), strings.ToLower(p)
		}
		rel, err := filepath.Rel(root, p)
		if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}

// taken reports whether the branch or the folder for name already exists.
func (e *Ensurer) taken(ctx context.Context, repo string, name Name, dir string) (bool, error) {
	if _, err := os.Lstat(dir); err == nil {
		return true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	local, remote, err := e.c.Git.BranchExists(ctx, repo, name.Branch())
	if err != nil {
		return false, err
	}
	return local || remote, nil
}

// prepareFailed reports a failed Prepare. It never fails Ensure.
func (e *Ensurer) prepareFailed(w core.Worktree, err error) {
	if e.c.OnPrepareError != nil {
		e.c.OnPrepareError(w, err)
		return
	}
	lg := e.c.Logger
	if lg == nil {
		lg = slog.Default()
	}
	// The error text can hold file names; log only a message made for users.
	msg := "setting up the worktree (ports, profile files) failed"
	var ue interface{ UserMessage() string }
	if errors.As(err, &ue) {
		msg = ue.UserMessage()
	}
	lg.Warn("worktree setup failed; the worktree works without it", "worktree", w.ID, "repo", w.RepoID, "reason", msg)
}
