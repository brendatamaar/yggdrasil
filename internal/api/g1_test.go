package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/landing"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/proposals"
)

type fakeWtGit struct {
	branch    string
	branchErr error
	st        map[string]git.Status
}

func (f fakeWtGit) CurrentBranch(context.Context, string) (string, error) { return f.branch, f.branchErr }
func (f fakeWtGit) Status(_ context.Context, dir string) (git.Status, error) {
	st, ok := f.st[dir]
	if !ok {
		return git.Status{}, errors.New("no such folder")
	}
	return st, nil
}

func TestWorktreeListCarriesTargetBranchAndClean(t *testing.T) {
	e := newEnv(t)
	task := e.seedTask(t, core.TaskRunning)
	ctx := context.Background()
	at := time.Now().UTC()
	w := core.Worktree{ID: "wt_1", RepoID: e.repo.ID, TaskID: task.ID, Name: "calm-pine", Path: `C:\wt\calm-pine`, Branch: "ygg/calm-pine", CreatedAt: at}
	audit := core.AuditEntry{ID: "aud_wt1", At: at, Actor: "system", Action: "worktree.create", Subject: "task:" + string(task.ID)}
	if err := e.store.Commit(ctx, func(tx *db.Tx) error { _, err := tx.RecordWorktree(ctx, w, "abc123", audit); return err }); err != nil {
		t.Fatal(err)
	}
	get := func(g WorktreeGit) WorktreeDTO {
		e.srv.worktrees = NewWorktreeServiceWithGit(e.store, g)
		resp, data := e.do(t, http.MethodGet, "/api/tasks/"+string(task.ID)+"/worktrees", nil)
		var out WorktreesResponse
		if resp.StatusCode != 200 || json.Unmarshal(data, &out) != nil || len(out.Worktrees) != 1 {
			t.Fatalf("worktrees = %d %s", resp.StatusCode, data)
		}
		return out.Worktrees[0]
	}

	got := get(fakeWtGit{branch: "develop", st: map[string]git.Status{w.Path: {Untracked: 1}}})
	if got.TargetBranch != "develop" || got.Clean == nil || *got.Clean {
		t.Errorf("dirty worktree = %+v", got)
	}
	got = get(fakeWtGit{branch: "main", st: map[string]git.Status{w.Path: {}}})
	if got.TargetBranch != "main" || got.Clean == nil || !*got.Clean {
		t.Errorf("clean worktree = %+v", got)
	}
	// Git cannot tell: both are left out and the list still answers.
	got = get(fakeWtGit{branchErr: errors.New("boom")})
	if got.TargetBranch != "" || got.Clean != nil || got.Name != "calm-pine" {
		t.Errorf("unknown = %+v", got)
	}
}

type diffStore struct {
	LandingStore
	wts []core.Worktree
}

func (s diffStore) GetRepo(context.Context, core.RepoID) (core.Repo, error) {
	return core.Repo{ID: "rep_1"}, nil
}
func (s diffStore) ListWorktrees(context.Context, core.TaskID) ([]core.Worktree, error) {
	return s.wts, nil
}
func (s diffStore) GetWorktreeBaseCommit(context.Context, core.WorktreeID) (string, error) {
	return "base", nil
}

type diffGit struct{ LandingGit }

func (diffGit) DiffAgainstBase(context.Context, string, string, git.BaseDiffOptions) (git.BaseDiff, error) {
	return git.BaseDiff{HasChanges: true}, nil
}

func TestWorktreeDiffServesActiveAndMergedOnly(t *testing.T) {
	for _, c := range []struct {
		status core.WorktreeStatus
		ok     bool
	}{
		{core.WorktreeActive, true}, {core.WorktreeMerged, true},
		{core.WorktreeRemoving, false}, {core.WorktreeRemoved, false},
	} {
		d := NewWorktreeDiffer(diffStore{wts: []core.Worktree{{ID: "wt_1", RepoID: "rep_1", Status: c.status, Path: "x"}}}, diffGit{})
		bd, err := d.DiffWorktree(context.Background(), "tsk_1", "rep_1")
		if c.ok && (err != nil || !bd.HasChanges) {
			t.Errorf("%s: err=%v diff=%+v", c.status, err, bd)
		}
		if !c.ok {
			if cl, _ := apperr.ClassOf(err); cl != apperr.NotFound {
				t.Errorf("%s: err = %v, want NotFound", c.status, err)
			}
		}
	}
}

// The executors put the conflicting files and the programs holding a locked
// folder into the failure message; the API passes it through unchanged.
func TestFailedLandingProposalsCarryFilesAndHolders(t *testing.T) {
	conflict := "The branch conflicts with main in src/a.go, src/b.go. Nothing was merged."
	locked := landing.Locked{Class: landing.LockOther, Holders: []string{"Code.exe", "node.exe"}}.Message()
	for _, c := range []struct {
		kind core.ProposalKind
		msg  string
		want []string
	}{
		{"merge_local", conflict, []string{"src/a.go", "src/b.go"}},
		{"remove_worktree", locked, []string{"Code.exe", "node.exe"}},
	} {
		d := proposalDTO(proposals.View{ID: "prp_1", Kind: c.kind, Status: "failed", FailKind: "exec", Message: c.msg, Retryable: c.kind == "remove_worktree"})
		for _, w := range c.want {
			if !strings.Contains(d.Message, w) {
				t.Errorf("%s: message %q misses %q", c.kind, d.Message, w)
			}
		}
		if c.kind == "remove_worktree" && !d.Retryable {
			t.Error("a lock failure must stay retryable")
		}
	}
}
