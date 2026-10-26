package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

func TestDBWorktreeServiceListsWithoutPaths(t *testing.T) {
	const secretPath = `C:\Users\someone\ygg\wt\calm-pine`
	e := newEnv(t)
	e.srv.worktrees = NewWorktreeService(e.store)
	task := e.seedTask(t, core.TaskRunning)
	ctx := context.Background()
	at := time.Now().UTC()
	w := core.Worktree{ID: "wt_1", RepoID: e.repo.ID, TaskID: task.ID, Name: "calm-pine", Path: secretPath, Branch: "ygg/calm-pine", CreatedAt: at}
	audit := core.AuditEntry{ID: "aud_wt1", At: at, Actor: "system", Action: "worktree.create", Subject: "task:" + string(task.ID)}
	if err := e.store.Commit(ctx, func(tx *db.Tx) error { _, err := tx.RecordWorktree(ctx, w, "abc123", audit); return err }); err != nil {
		t.Fatal(err)
	}

	resp, data := e.do(t, http.MethodGet, "/api/tasks/"+string(task.ID)+"/worktrees", nil)
	var out WorktreesResponse
	if resp.StatusCode != 200 || json.Unmarshal(data, &out) != nil || len(out.Worktrees) != 1 {
		t.Fatalf("worktrees = %d %s", resp.StatusCode, data)
	}
	got := out.Worktrees[0]
	if got.Name != "calm-pine" || got.Branch != "ygg/calm-pine" || got.RepoName != e.repo.Name || got.BaseCommit != "abc123" {
		t.Errorf("worktree = %+v", got)
	}
	if strings.Contains(string(data), "someone") {
		t.Errorf("response leaks the folder: %s", data)
	}

	if resp, _ := e.do(t, http.MethodGet, "/api/tasks/tsk_nope/worktrees", nil); resp.StatusCode != 404 {
		t.Errorf("unknown task = %d, want 404", resp.StatusCode)
	}
}
