package git

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// Fetch fetches remote, or the default remote when remote is empty (WS-06).
// It never prunes: remote-tracking branches are only added or moved.
func (g *Git) Fetch(ctx context.Context, dir, remote string) error {
	args := []string{"fetch"}
	if remote != "" {
		if err := checkName("remote", remote); err != nil {
			return err
		}
		args = append(args, "--end-of-options", remote)
	}
	_, err := g.run(ctx, dir, "fetch", false, args...)
	return err
}

// FFReason says why a fast-forward pull cannot run.
type FFReason string

const (
	FFDetached   FFReason = "detached"
	FFNoUpstream FFReason = "no-upstream"
	FFDiverged   FFReason = "diverged"
	FFDirty      FFReason = "dirty"
	FFOther      FFReason = "other"
)

// FFError is returned by PullFFOnly when the branch cannot be fast-forwarded.
// The repo is unchanged.
type FFError struct {
	Reason        FFReason
	Branch        string
	Upstream      string
	Ahead, Behind int
	Err           error // the underlying git error, nil for pre-checks
}

func (e *FFError) Error() string {
	s := fmt.Sprintf("git pull --ff-only on %q: cannot fast-forward (%s)", e.Branch, e.Reason)
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

func (e *FFError) Unwrap() error { return e.Err }

// UserMessage says what to do next.
func (e *FFError) UserMessage() string {
	switch e.Reason {
	case FFDetached:
		return "HEAD is detached, so there is no branch to update. Check out a branch first."
	case FFNoUpstream:
		return fmt.Sprintf("Branch %s does not track a remote branch. Set an upstream in your terminal, then pull again.", e.Branch)
	case FFDiverged:
		return fmt.Sprintf("Branch %s and %s have both moved on (%d ahead, %d behind). Yggdrasil only fast-forwards: merge or rebase it yourself in your terminal.", e.Branch, e.Upstream, e.Ahead, e.Behind)
	case FFDirty:
		return "Your uncommitted changes would be overwritten by the pull. Commit or stash them, then pull again."
	}
	return "The pull could not fast-forward. See the details and fix the problem in your terminal."
}

// PullFFOnly pulls the current branch with --ff-only (WS-06). When it cannot
// fast-forward it returns an *FFError and leaves the repo unchanged.
func (g *Git) PullFFOnly(ctx context.Context, dir string) error {
	st, err := g.Status(ctx, dir)
	if err != nil {
		return err
	}
	ff := &FFError{Branch: st.Branch, Upstream: st.Upstream, Ahead: st.Ahead, Behind: st.Behind}
	switch {
	case st.Detached:
		ff.Reason = FFDetached
		return ff
	case !st.HasUpstream:
		ff.Reason = FFNoUpstream
		return ff
	}
	_, err = g.run(ctx, dir, "pull", false, "pull", "--ff-only", "--no-rebase", "--no-edit")
	if err == nil {
		return nil
	}
	var ge *Error
	if !errors.As(err, &ge) || ge.Code == 0 {
		return err // cancelled or not started: not a fast-forward problem
	}
	ff.Err = err
	// The fetch inside pull moved the tracking branch: ask again.
	if after, serr := g.Status(ctx, dir); serr == nil {
		ff.Ahead, ff.Behind = after.Ahead, after.Behind
	}
	switch low := strings.ToLower(ge.Stderr); {
	case ff.Ahead > 0 && ff.Behind > 0:
		ff.Reason = FFDiverged
	case strings.Contains(low, "would be overwritten"):
		ff.Reason = FFDirty
	case strings.Contains(low, "not possible to fast-forward"):
		ff.Reason = FFDiverged
	default:
		return err // network, auth or lock problem: keep the generic message
	}
	return ff
}

// Checkout switches to an existing branch (WS-06). It refuses when tracked
// files have uncommitted changes. A name that is not a branch is refused by git.
func (g *Git) Checkout(ctx context.Context, dir, branch string) error {
	if err := checkName("branch", branch); err != nil {
		return err
	}
	st, err := g.Status(ctx, dir)
	if err != nil {
		return err
	}
	if st.Dirty() {
		return &Error{Op: "checkout", Err: ErrDirty,
			UserMsg: "This repo has uncommitted changes. Commit or stash them before switching branches."}
	}
	_, err = g.run(ctx, dir, "checkout", false, "switch", "--end-of-options", branch)
	return err
}

// ErrDirty is returned by Checkout when the tree has uncommitted changes.
var ErrDirty = errors.New("working tree has uncommitted changes")

// Worktree is one entry of git worktree list.
type Worktree struct {
	Path     string `json:"path"`
	Head     string `json:"head"`
	Branch   string `json:"branch"` // "" when detached or bare
	Detached bool   `json:"detached"`
	Bare     bool   `json:"bare"`
	Locked   bool   `json:"locked"`
}

// WorktreeAdd creates a worktree at path on a new branch started from
// startPoint (HEAD when empty), as WE-04 asks. path must be absolute and
// should be short: use platform.ShortDirName for its last element. It never
// reuses an existing branch.
func (g *Git) WorktreeAdd(ctx context.Context, repoDir, path, newBranch, startPoint string) (Worktree, error) {
	if !filepath.IsAbs(path) {
		return Worktree{}, &Error{Op: "worktree add", Err: ErrInvalidArg,
			UserMsg: "The worktree folder must be a full path."}
	}
	if err := platform.CheckPath(path); err != nil {
		return Worktree{}, &Error{Op: "worktree add", Err: err, UserMsg: err.Error()}
	}
	if err := checkName("branch", newBranch); err != nil {
		return Worktree{}, err
	}
	args := []string{"worktree", "add", "-b", newBranch, "--", path}
	if startPoint != "" {
		if err := checkName("start point", startPoint); err != nil {
			return Worktree{}, err
		}
		args = append(args, startPoint)
	}
	if _, err := g.run(ctx, repoDir, "worktree add", false, args...); err != nil {
		return Worktree{}, err
	}
	wts, err := g.WorktreeList(ctx, repoDir)
	if err != nil {
		return Worktree{}, err
	}
	for _, w := range wts {
		if w.Branch == newBranch {
			return w, nil
		}
	}
	return Worktree{}, &Error{Op: "worktree add", Err: errors.New("new worktree not in list"),
		UserMsg: "The worktree was created but Yggdrasil could not find it. Refresh the repo."}
}

// WorktreeList lists the main worktree and all linked worktrees of the repo.
func (g *Git) WorktreeList(ctx context.Context, repoDir string) ([]Worktree, error) {
	out, err := g.run(ctx, repoDir, "worktree list", true, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	return parseWorktrees(out), nil
}

func parseWorktrees(out string) []Worktree {
	var res []Worktree
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSuffix(line, "\r")
		key, val, _ := strings.Cut(line, " ")
		switch key {
		case "worktree":
			res = append(res, Worktree{Path: filepath.Clean(val)})
		case "HEAD", "branch", "detached", "bare", "locked":
			if len(res) == 0 {
				continue
			}
			w := &res[len(res)-1]
			switch key {
			case "HEAD":
				w.Head = val
			case "branch":
				w.Branch = strings.TrimPrefix(val, "refs/heads/")
			case "detached":
				w.Detached = true
			case "bare":
				w.Bare = true
			case "locked":
				w.Locked = true
			}
		}
	}
	return res
}
