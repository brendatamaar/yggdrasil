package main

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
	"github.com/brendatamaar/yggdrasil/internal/mcp"
	"github.com/brendatamaar/yggdrasil/internal/mcp/mcptest"
)

// dbWorkspace runs the Workspace contract against the adapter the server uses
// (mcpWorkspace over a real SQLite store).
type dbWorkspace struct {
	mcp.Workspace
	store *db.Store
	repos map[core.RepoID]core.Repo
	n     int
}

func newDBWorkspace(t *testing.T) *dbWorkspace {
	t.Helper()
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "ygg.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	s := db.NewStore(d, nil)
	return &dbWorkspace{Workspace: mcpWorkspace{store: s}, store: s, repos: map[core.RepoID]core.Repo{}}
}

func (w *dbWorkspace) Seed(t *testing.T, mode core.TaskMode, repos []mcptest.WSRepo) (core.TaskID, map[string]core.RepoID) {
	t.Helper()
	w.n++
	root := t.TempDir()
	p := dbtest.Project(t, w.store, core.Project{Name: fmt.Sprintf("acme-shop-%d", w.n), Path: root})
	ids := map[string]core.RepoID{}
	var scope []core.RepoID
	var made []core.Repo
	for _, r := range repos {
		rp := dbtest.Repo(t, w.store, core.Repo{ProjectID: p.ID, Name: r.Name, Path: filepath.Join(p.Path, r.Name)})
		w.repos[rp.ID] = rp
		ids[r.Name] = rp.ID
		made = append(made, rp)
		if r.InScope {
			scope = append(scope, rp.ID)
		}
	}
	sc := core.ScopeRepo
	if len(scope) > 1 {
		sc = core.ScopeProject
	}
	task := dbtest.Task(t, w.store, core.Task{ProjectID: p.ID, Scope: sc, RepoIDs: scope, Mode: mode})
	for i, r := range repos {
		if r.Worktree {
			dbtest.TaskWorktree(t, w.store, task.ID, made[i], "wt", "ygg/"+r.Name)
		}
	}
	return task.ID, ids
}

func (w *dbWorkspace) Untrack(t *testing.T, repo core.RepoID) {
	t.Helper()
	dbtest.UntrackRepo(t, w.store, w.repos[repo])
}

func TestDBWorkspaceContract(t *testing.T) {
	mcptest.WorkspaceContract(t, func(t *testing.T) mcptest.WorkspaceHarness { return newDBWorkspace(t) })
}
