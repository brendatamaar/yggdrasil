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
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// maxAttempts is how many names are tried before a collision is an error.
const maxAttempts = 5

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

// Config wires an Ensurer. All fields are required.
type Config struct {
	Git     Git
	Store   Store
	Rand    Rand
	Now     func() time.Time
	NewID   func(prefix string) string
	DataDir string
}

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
func (e *Ensurer) Ensure(ctx context.Context, task core.TaskID, repos []RepoRef) ([]core.Worktree, error) {
	if err := task.Validate(); err != nil {
		return nil, err
	}
	existing, err := e.c.Store.List(ctx, task)
	if err != nil {
		return nil, &Error{Msg: "Could not read the task's worktrees. Try again.", Err: err}
	}
	byRepo := map[core.RepoID]core.Worktree{}
	for _, w := range existing {
		if w.Status == core.WorktreeActive {
			byRepo[w.RepoID] = w
		}
	}
	out := make([]core.Worktree, 0, len(repos))
	for _, r := range repos {
		if w, ok := byRepo[r.ID]; ok {
			if !e.underWorktreeRoot(w.Path) {
				return out, &Error{
					Msg: "A saved worktree for this task is not in Yggdrasil's worktree folder, so it was not used. Check the data folder setting or restore the folder, then try again.",
					Err: fmt.Errorf("worktree %s of repo %s is outside the worktree root", w.ID, r.ID),
				}
			}
			out = append(out, w)
			continue
		}
		w, err := e.create(ctx, task, r)
		if err != nil {
			return out, err
		}
		out = append(out, w)
	}
	return out, nil
}

func (e *Ensurer) create(ctx context.Context, task core.TaskID, r RepoRef) (core.Worktree, error) {
	if err := platform.CheckPath(r.Path); err != nil {
		// The detail (it may hold the path) stays in Err, never in the message.
		return core.Worktree{}, &Error{
			Msg: "Yggdrasil can't use this repo's folder. Folders inside WSL are not supported yet; move the repository to a normal Windows folder and add it again.",
			Err: err,
		}
	}
	fail := func(err error) (core.Worktree, error) {
		if ctx.Err() != nil {
			return core.Worktree{}, fmt.Errorf("create worktree: %w", ctx.Err())
		}
		return core.Worktree{}, &Error{
			Msg: "Git could not create the worktree. Check that the repo is healthy and no other git operation is running, then try again.",
			Err: fmt.Errorf("create worktree for repo %s: %w", r.ID, err),
		}
	}
	base, err := e.c.Git.ResolveCommit(ctx, r.Path, r.DefaultBranch)
	if err != nil {
		return fail(err)
	}
	wtRoot := filepath.Join(e.c.DataDir, "wt")
	if err := os.MkdirAll(wtRoot, 0o750); err != nil {
		return fail(err)
	}
	for attempt := range maxAttempts {
		name := Generate(e.c.Rand, attempt)
		dir := filepath.Join(wtRoot, platform.ShortDirName(string(name), MaxNameLen))
		taken, err := e.taken(ctx, r.Path, name, dir)
		if err != nil {
			return fail(err)
		}
		if taken {
			continue
		}
		if _, err := e.c.Git.WorktreeAdd(ctx, r.Path, dir, name.Branch(), base); err != nil {
			return fail(err)
		}
		long, err := platform.LongPath(dir)
		if err != nil {
			return fail(err)
		}
		now := e.c.Now().UTC()
		w := core.Worktree{
			ID: core.WorktreeID(e.c.NewID("wt")), RepoID: r.ID, TaskID: task, Name: string(name),
			Path: long, Branch: name.Branch(), Status: core.WorktreeActive, CreatedAt: now,
		}
		detail, err := json.Marshal(map[string]string{"worktree": string(w.ID), "repo": string(r.ID), "branch": w.Branch, "base": base})
		if err != nil {
			return fail(err)
		}
		audit := core.AuditEntry{
			ID: core.AuditID(e.c.NewID("aud")), At: now, Actor: "system", Action: "worktree.create",
			Subject: "task:" + string(task), Detail: detail,
		}
		if err := e.c.Store.Record(ctx, w, base, audit); err != nil {
			return core.Worktree{}, &Error{
				Msg: "The worktree was created but Yggdrasil could not save it. Try again.",
				Err: fmt.Errorf("record worktree %s: %w", w.ID, err),
			}
		}
		return w, nil
	}
	return core.Worktree{}, &Error{
		Msg: "Yggdrasil could not find a free worktree name. Try again.",
		Err: fmt.Errorf("repo %s: %w", r.ID, ErrNameTaken),
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
