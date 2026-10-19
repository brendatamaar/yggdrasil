package repostest

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/repos"
)

// GitScenario is one situation the GitRunner contract describes.
type GitScenario string

const (
	// GitFetch: a repo with a remote; fetch works.
	GitFetch GitScenario = "fetch"
	// GitPullAdvance: the remote is one commit ahead of main.
	GitPullAdvance GitScenario = "pull_advance"
	// GitPullUpToDate: main equals its upstream.
	GitPullUpToDate GitScenario = "pull_up_to_date"
	// GitPullDiverged: one commit ahead and one behind.
	GitPullDiverged GitScenario = "pull_diverged"
	// GitPullDirty: the remote changed a file with local changes.
	GitPullDirty GitScenario = "pull_dirty"
	// GitPullDetached: HEAD is detached.
	GitPullDetached GitScenario = "pull_detached"
	// GitCheckoutClean: a clean tree and a local branch "feature".
	GitCheckoutClean GitScenario = "checkout_clean"
	// GitCheckoutRemoteOnly: a clean tree; "feature" exists only on origin.
	GitCheckoutRemoteOnly GitScenario = "checkout_remote_only"
	// GitCheckoutDirty: uncommitted changes to a tracked file.
	GitCheckoutDirty GitScenario = "checkout_dirty"
	// GitBranches: local "local-only", remote-only "remote-only", both "main".
	GitBranches GitScenario = "branches"
)

// GitSetup prepares a scenario for the runner under test and returns the repo
// folder and the branch name the scenario is about ("feature" for checkouts).
// The fake scripts a result; the real runner gets git repos built in
// t.TempDir(), with a local bare remote.
type GitSetup interface {
	Prepare(t *testing.T, s GitScenario) (dir, branch string)
}

// GitRunnerContract is the contract of repos.GitRunner, seen through
// repos.RunGitOp (which maps results to outcomes). It runs against FakeGit and
// against real git.
func GitRunnerContract(t *testing.T, newRunner func(t *testing.T) (repos.GitRunner, GitSetup)) {
	t.Helper()
	ctx := context.Background()

	op := func(t *testing.T, s GitScenario, o corerepos.GitOp) (corerepos.ActOutcome, repos.GitRunner, string) {
		t.Helper()
		g, setup := newRunner(t)
		dir, branch := setup.Prepare(t, s)
		return repos.RunGitOp(ctx, g, o, dir, branch), g, dir
	}

	t.Run("fetch", func(t *testing.T) {
		t.Parallel()
		out, _, _ := op(t, GitFetch, corerepos.OpFetch)
		if _, ok := out.(corerepos.Fetched); !ok {
			t.Fatalf("fetch = %#v", out)
		}
	})
	t.Run("pull fast-forwards", func(t *testing.T) {
		t.Parallel()
		out, g, dir := op(t, GitPullAdvance, corerepos.OpPull)
		p, ok := out.(corerepos.Pulled)
		if !ok || p.From == "" || p.To == "" || p.From == p.To {
			t.Fatalf("pull = %#v", out)
		}
		if st, err := g.Status(ctx, dir); err != nil || st.Behind != 0 {
			t.Fatalf("after pull: %+v, %v", st, err)
		}
	})
	t.Run("pull up to date", func(t *testing.T) {
		t.Parallel()
		out, _, _ := op(t, GitPullUpToDate, corerepos.OpPull)
		if _, ok := out.(corerepos.UpToDate); !ok {
			t.Fatalf("pull = %#v", out)
		}
	})
	for _, c := range []struct {
		name string
		sc   GitScenario
		want corerepos.PullReason
	}{
		{"pull diverged is refused", GitPullDiverged, corerepos.PullDiverged},
		{"pull over local changes is refused", GitPullDirty, corerepos.PullDirty},
		{"pull on a detached HEAD is refused", GitPullDetached, corerepos.PullDetached},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			out, g, dir := op(t, c.sc, corerepos.OpPull)
			r, ok := out.(corerepos.PullRefused)
			if !ok || r.Reason != c.want || r.Message == "" {
				t.Fatalf("pull = %#v, want refused %s", out, c.want)
			}
			if st, err := g.Status(ctx, dir); err != nil || (c.sc == GitPullDiverged && (st.Ahead != 1 || st.Behind != 1)) {
				t.Fatalf("a refused pull changed the repo: %+v, %v", st, err)
			}
		})
	}
	t.Run("checkout of a local branch", func(t *testing.T) {
		t.Parallel()
		out, g, dir := op(t, GitCheckoutClean, corerepos.OpCheckout)
		c, ok := out.(corerepos.CheckedOut)
		if !ok || c.Branch != "feature" || c.Created {
			t.Fatalf("checkout = %#v", out)
		}
		if st, err := g.Status(ctx, dir); err != nil || st.Branch != "feature" {
			t.Fatalf("after checkout: %+v, %v", st, err)
		}
	})
	t.Run("checkout of a remote-only branch creates a local one", func(t *testing.T) {
		t.Parallel()
		out, g, dir := op(t, GitCheckoutRemoteOnly, corerepos.OpCheckout)
		c, ok := out.(corerepos.CheckedOut)
		if !ok || c.Branch != "feature" || !c.Created {
			t.Fatalf("checkout = %#v", out)
		}
		if st, err := g.Status(ctx, dir); err != nil || st.Branch != "feature" {
			t.Fatalf("after checkout: %+v, %v", st, err)
		}
	})
	t.Run("checkout with local changes is refused", func(t *testing.T) {
		t.Parallel()
		out, g, dir := op(t, GitCheckoutDirty, corerepos.OpCheckout)
		r, ok := out.(corerepos.CheckoutRefused)
		if !ok || r.Reason != corerepos.CheckoutDirtyConflict || r.Message == "" {
			t.Fatalf("checkout = %#v", out)
		}
		if st, err := g.Status(ctx, dir); err != nil || st.Branch != "main" {
			t.Fatalf("a refused checkout moved HEAD: %+v, %v", st, err)
		}
	})
	t.Run("BranchExists", func(t *testing.T) {
		t.Parallel()
		g, setup := newRunner(t)
		dir, _ := setup.Prepare(t, GitBranches)
		for _, c := range []struct {
			name          string
			local, remote bool
		}{
			{"main", true, true}, {"local-only", true, false}, {"remote-only", false, true}, {"nope", false, false},
		} {
			l, r, err := g.BranchExists(ctx, dir, c.name)
			if err != nil || l != c.local || r != c.remote {
				t.Errorf("BranchExists(%q) = %v, %v, %v; want %v, %v", c.name, l, r, err, c.local, c.remote)
			}
		}
	})
	t.Run("an expired context ends as a timeout", func(t *testing.T) {
		t.Parallel()
		g, setup := newRunner(t)
		dir, _ := setup.Prepare(t, GitFetch)
		expired, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
		defer cancel()
		out := repos.RunGitOp(expired, g, corerepos.OpFetch, dir, "")
		if f, ok := out.(corerepos.Failed); !ok || f.Kind != corerepos.FailTimeout {
			t.Fatalf("fetch = %#v", out)
		}
	})
}

// FakeGit is a scripted repos.GitRunner. Its Prepare implements GitSetup.
type FakeGit struct {
	mu    sync.Mutex
	repos map[string]*fakeRepo
	n     int
}

type fakeRepo struct {
	oid, remoteOID            string
	branch                    string
	detached, upstream        bool
	dirty, pullDirty          bool
	diverged                  bool
	local, remote             map[string]bool
	fetches, pulls, checkouts int
}

// NewFakeGit returns an empty fake.
func NewFakeGit() *FakeGit { return &FakeGit{repos: map[string]*fakeRepo{}} }

// Calls says how often the fake ran each operation in dir.
func (f *FakeGit) Calls(dir string) (fetches, pulls, checkouts int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.repos[dir]
	if r == nil {
		return 0, 0, 0
	}
	return r.fetches, r.pulls, r.checkouts
}

// Prepare implements GitSetup.
func (f *FakeGit) Prepare(t *testing.T, s GitScenario) (string, string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	dir := "/fake/" + string(s) + "-" + strconv.Itoa(f.n)
	r := &fakeRepo{oid: "1111111111111111", remoteOID: "1111111111111111", branch: "main", upstream: true,
		local: map[string]bool{"main": true}, remote: map[string]bool{"main": true}}
	branch := ""
	switch s {
	case GitPullAdvance:
		r.remoteOID = "2222222222222222"
	case GitPullDiverged:
		r.diverged, r.remoteOID = true, "3333333333333333"
	case GitPullDirty:
		r.dirty, r.pullDirty, r.remoteOID = true, true, "2222222222222222"
	case GitPullDetached:
		r.detached, r.branch = true, ""
	case GitCheckoutClean:
		r.local["feature"], branch = true, "feature"
	case GitCheckoutRemoteOnly:
		r.remote["feature"], branch = true, "feature"
	case GitCheckoutDirty:
		r.dirty, branch = true, "feature"
		r.local["feature"] = true
	case GitBranches:
		r.local["local-only"], r.remote["remote-only"] = true, true
	}
	f.repos[dir] = r
	return dir, branch
}

func (f *FakeGit) get(ctx context.Context, dir, op string) (*fakeRepo, error) {
	if err := ctx.Err(); err != nil {
		return nil, &git.Error{Op: op, Err: err, UserMsg: "git " + op + " was cancelled."}
	}
	r := f.repos[dir]
	if r == nil {
		return nil, &git.Error{Op: op, Code: 128, Stderr: "fatal: not a git repository", Err: errors.New("exit status 128")}
	}
	return r, nil
}

// Fetch implements repos.GitRunner.
func (f *FakeGit) Fetch(ctx context.Context, dir, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.get(ctx, dir, "fetch")
	if err != nil {
		return err
	}
	r.fetches++
	return nil
}

// PullFFOnly implements repos.GitRunner.
func (f *FakeGit) PullFFOnly(ctx context.Context, dir string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.get(ctx, dir, "pull")
	if err != nil {
		return err
	}
	r.pulls++
	ff := &git.FFError{Branch: r.branch}
	switch {
	case r.detached:
		ff.Reason = git.FFDetached
		return ff
	case !r.upstream:
		ff.Reason = git.FFNoUpstream
		return ff
	case r.diverged:
		ff.Reason, ff.Ahead, ff.Behind = git.FFDiverged, 1, 1
		return ff
	case r.pullDirty:
		ff.Reason = git.FFDirty
		return ff
	}
	r.oid = r.remoteOID
	return nil
}

// Checkout implements repos.GitRunner.
func (f *FakeGit) Checkout(ctx context.Context, dir, branch string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.get(ctx, dir, "checkout")
	if err != nil {
		return err
	}
	r.checkouts++
	if r.dirty {
		return &git.Error{Op: "checkout", Err: git.ErrDirty, UserMsg: "This repo has uncommitted changes."}
	}
	if !r.local[branch] && !r.remote[branch] {
		return &git.Error{Op: "checkout", Code: 128, Stderr: "fatal: invalid reference: " + branch, Err: errors.New("exit status 128")}
	}
	r.local[branch], r.branch, r.detached = true, branch, false
	return nil
}

// Status implements repos.GitRunner.
func (f *FakeGit) Status(ctx context.Context, dir string) (git.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.get(ctx, dir, "status")
	if err != nil {
		return git.Status{}, err
	}
	st := git.Status{Branch: r.branch, Detached: r.detached, OID: r.oid, HasUpstream: r.upstream}
	if r.dirty {
		st.Unstaged = 1
	}
	if r.diverged {
		st.Ahead, st.Behind = 1, 1
	} else if r.oid != r.remoteOID {
		st.Behind = 1
	}
	return st, nil
}

// BranchExists implements repos.GitRunner.
func (f *FakeGit) BranchExists(ctx context.Context, dir, name string) (bool, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.get(ctx, dir, "branch exists")
	if err != nil {
		return false, false, err
	}
	return r.local[name], r.remote[name], nil
}
