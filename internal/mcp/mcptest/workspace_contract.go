package mcptest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/mcp"
)

// WSRepo describes one repo of a seeded task.
type WSRepo struct {
	Name string
	// InScope: the task may read and act on this repo (at least one repo of a
	// seeded task is in scope).
	InScope bool
	// Worktree: the task has an active worktree of this repo.
	Worktree bool
}

// WorkspaceHarness is a Workspace plus the means to seed and change the world
// it reads, so one contract can run against the fake and the database-backed
// adapter in cmd/ygg.
type WorkspaceHarness interface {
	mcp.Workspace
	// Seed creates a project holding the repos and a task in it, and returns the
	// task and the ID given to each repo name.
	Seed(t *testing.T, mode core.TaskMode, repos []WSRepo) (core.TaskID, map[string]core.RepoID)
	// Untrack stops tracking a repo.
	Untrack(t *testing.T, repo core.RepoID)
}

// WorkspaceContract is the contract of mcp.Workspace (SF-02, invariant 6): a
// snapshot holds only tracked repos, mirrors scope, worktrees and mode, is
// rebuilt on every call so an untracked repo disappears at once, and an unknown
// task is ErrNoTask.
func WorkspaceContract(t *testing.T, mk func(t *testing.T) WorkspaceHarness) {
	t.Helper()
	names := func(s mcp.Snapshot) []string {
		var out []string
		for _, r := range s.Repos {
			out = append(out, r.Name)
		}
		slices.Sort(out)
		return out
	}
	view := func(t *testing.T, s mcp.Snapshot, name string) mcp.RepoView {
		t.Helper()
		for _, r := range s.Repos {
			if r.Name == name {
				return r
			}
		}
		t.Fatalf("repo %q is not in the snapshot %v", name, names(s))
		return mcp.RepoView{}
	}
	trio := []WSRepo{
		{Name: "shop-api", InScope: true, Worktree: true},
		{Name: "shop-web", InScope: true},
		{Name: "shop-admin"},
	}

	t.Run("a snapshot mirrors scope, worktrees and mode", func(t *testing.T) {
		w := mk(t)
		task, ids := w.Seed(t, core.ModeCode, trio)
		s, err := w.Snapshot(context.Background(), task)
		if err != nil {
			t.Fatal(err)
		}
		if s.Mode != core.ModeCode || s.Project == "" {
			t.Errorf("mode %q project %q", s.Mode, s.Project)
		}
		if got := names(s); !slices.Equal(got, []string{"shop-admin", "shop-api", "shop-web"}) {
			t.Fatalf("repos = %v", got)
		}
		for _, r := range trio {
			v := view(t, s, r.Name)
			if v.ID != ids[r.Name] || v.InScope != r.InScope {
				t.Errorf("%s: %+v (want id %s, in scope %v)", r.Name, v, ids[r.Name], r.InScope)
			}
			if r.Worktree != (v.Worktree != "" && v.WorktreeBranch != "") {
				t.Errorf("%s: worktree %q branch %q, want a worktree: %v", r.Name, v.Worktree, v.WorktreeBranch, r.Worktree)
			}
			if !r.Worktree && (v.Worktree != "" || v.WorktreeBranch != "") {
				t.Errorf("%s: invented a worktree %q", r.Name, v.Worktree)
			}
		}
	})

	t.Run("mode is the task's mode", func(t *testing.T) {
		w := mk(t)
		task, _ := w.Seed(t, core.ModeAsk, trio[:2])
		s, err := w.Snapshot(context.Background(), task)
		if err != nil || s.Mode != core.ModeAsk {
			t.Fatalf("snapshot %+v, %v", s, err)
		}
	})

	t.Run("an unknown task is ErrNoTask", func(t *testing.T) {
		w := mk(t)
		w.Seed(t, core.ModeAsk, trio[:2])
		if _, err := w.Snapshot(context.Background(), "tsk_nothere"); !errors.Is(err, mcp.ErrNoTask) {
			t.Fatalf("err = %v, want ErrNoTask", err)
		}
	})

	t.Run("an untracked repo is gone at the next call and the rest stay", func(t *testing.T) {
		w := mk(t)
		task, ids := w.Seed(t, core.ModeCode, trio)
		if _, err := w.Snapshot(context.Background(), task); err != nil {
			t.Fatal(err)
		}
		w.Untrack(t, ids["shop-admin"])
		s, err := w.Snapshot(context.Background(), task)
		if err != nil {
			t.Fatal(err)
		}
		if got := names(s); !slices.Equal(got, []string{"shop-api", "shop-web"}) {
			t.Fatalf("after untracking shop-admin: %v", got)
		}
		w.Untrack(t, ids["shop-api"])
		s, err = w.Snapshot(context.Background(), task)
		if err != nil {
			t.Fatal(err)
		}
		if got := names(s); !slices.Equal(got, []string{"shop-web"}) {
			t.Fatalf("after untracking shop-api: %v", got)
		}
		if v := view(t, s, "shop-web"); !v.InScope {
			t.Errorf("shop-web lost its scope: %+v", v)
		}
	})

	t.Run("a task sees only its own project", func(t *testing.T) {
		w := mk(t)
		a, _ := w.Seed(t, core.ModeAsk, []WSRepo{{Name: "alpha-api", InScope: true}})
		b, _ := w.Seed(t, core.ModeAsk, []WSRepo{{Name: "beta-api", InScope: true}})
		sa, err := w.Snapshot(context.Background(), a)
		if err != nil {
			t.Fatal(err)
		}
		sb, err := w.Snapshot(context.Background(), b)
		if err != nil {
			t.Fatal(err)
		}
		if got := names(sa); !slices.Equal(got, []string{"alpha-api"}) {
			t.Errorf("task a sees %v", got)
		}
		if got := names(sb); !slices.Equal(got, []string{"beta-api"}) {
			t.Errorf("task b sees %v", got)
		}
	})

	t.Run("a returned snapshot is a copy", func(t *testing.T) {
		w := mk(t)
		task, _ := w.Seed(t, core.ModeCode, trio)
		s, _ := w.Snapshot(context.Background(), task)
		for i := range s.Repos {
			s.Repos[i].InScope = !s.Repos[i].InScope
			s.Repos[i].Name = "changed"
		}
		s2, err := w.Snapshot(context.Background(), task)
		if err != nil {
			t.Fatal(err)
		}
		if got := names(s2); !slices.Equal(got, []string{"shop-admin", "shop-api", "shop-web"}) {
			t.Fatalf("a caller's edit reached the workspace: %v", got)
		}
	})
}

// FakeWorkspaceHarness adapts FakeWorkspace to WorkspaceHarness.
type FakeWorkspaceHarness struct {
	*FakeWorkspace
	n atomic.Int64
}

// NewFakeWorkspaceHarness returns an empty harness.
func NewFakeWorkspaceHarness() *FakeWorkspaceHarness {
	return &FakeWorkspaceHarness{FakeWorkspace: NewFakeWorkspace()}
}

// Seed implements WorkspaceHarness.
func (h *FakeWorkspaceHarness) Seed(_ *testing.T, mode core.TaskMode, repos []WSRepo) (core.TaskID, map[string]core.RepoID) {
	n := h.n.Add(1)
	task := core.TaskID(fmt.Sprintf("tsk_%d", n))
	ids := map[string]core.RepoID{}
	snap := mcp.Snapshot{Project: core.ProjectID(fmt.Sprintf("prj_%d", n)), Mode: mode}
	for i, r := range repos {
		id := core.RepoID(fmt.Sprintf("rep_%d_%d", n, i))
		ids[r.Name] = id
		v := mcp.RepoView{ID: id, Name: r.Name, DefaultBranch: "main", InScope: r.InScope}
		if r.Worktree {
			v.Worktree, v.WorktreeBranch = core.WorktreeID(fmt.Sprintf("wt_%d_%d", n, i)), "ygg/"+r.Name
		}
		snap.Repos = append(snap.Repos, v)
	}
	h.Set(task, snap)
	return task, ids
}

// Untrack implements WorkspaceHarness.
func (h *FakeWorkspaceHarness) Untrack(_ *testing.T, repo core.RepoID) { h.FakeWorkspace.Untrack(repo) }

var _ WorkspaceHarness = (*FakeWorkspaceHarness)(nil)
