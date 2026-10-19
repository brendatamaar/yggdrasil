package db

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/db/gen"
)

var (
	t0 = time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	t1 = t0.Add(time.Hour)
)

func commitWS(t *testing.T, s *Store, fn func(ctx context.Context, w *WorkspaceTx) error) error {
	t.Helper()
	ctx := context.Background()
	return s.Commit(ctx, func(tx *Tx) error { return fn(ctx, tx.Workspace()) })
}

func mustWS(t *testing.T, s *Store, fn func(ctx context.Context, w *WorkspaceTx) error) {
	t.Helper()
	if err := commitWS(t, s, fn); err != nil {
		t.Fatal(err)
	}
}

// WorkspaceStoreContract is the contract of the workspace store. The only
// implementation is SQLite, which is fast enough for the tests, so there is
// no fake; the suite takes a constructor to keep that door open.
func WorkspaceStoreContract(t *testing.T, newStore func(t *testing.T) *Store) {
	ctx := context.Background()
	project := func(id, name, path string) corerepos.InsertProject {
		return corerepos.InsertProject{Project: corerepos.Project{
			ID: core.ProjectID(id), Name: name, Path: corerepos.CleanPath(path), Life: corerepos.Active{Since: t0},
		}}
	}
	repo := func(project, id, name, rel string, v int64) corerepos.InsertRepo {
		return corerepos.InsertRepo{ProjectID: core.ProjectID(project), ExpectVersion: v, Repo: corerepos.Repo{
			ID: core.RepoID(id), Name: name, RelPath: corerepos.RelPath(rel), Life: corerepos.Active{Since: t0},
		}}
	}
	setup := func(t *testing.T) *Store {
		s := newStore(t)
		mustWS(t, s, func(ctx context.Context, w *WorkspaceTx) error {
			if err := w.InsertProject(ctx, project("prj_1", "shop", `C:\work\shop`)); err != nil {
				return err
			}
			if err := w.InsertRepo(ctx, repo("prj_1", "rep_api", "api", "api", 0)); err != nil {
				return err
			}
			return w.InsertRepo(ctx, repo("prj_1", "rep_web", "web", "libs/web", 0))
		})
		return s
	}

	t.Run("round trip of Active and Untracked", func(t *testing.T) {
		s := setup(t)
		ws, err := s.GetWorkspace(ctx)
		if err != nil {
			t.Fatal(err)
		}
		p := ws.Projects["prj_1"]
		if p.Name != "shop" || p.Path != `C:\work\shop` || p.Version != 0 || p.Life != (corerepos.Active{Since: t0}) {
			t.Errorf("project = %#v", p)
		}
		if got := p.Repos["rep_web"]; got.RelPath != "libs/web" || got.Name != "web" || got.Life != (corerepos.Active{Since: t0}) || got.Trusted {
			t.Errorf("repo = %#v", got)
		}
		mustWS(t, s, func(ctx context.Context, w *WorkspaceTx) error {
			if err := w.MarkRepoUntracked(ctx, corerepos.MarkRepoUntracked{ProjectID: "prj_1", RepoID: "rep_web", At: t1, ExpectVersion: 0}); err != nil {
				return err
			}
			if err := w.MarkRepoUntracked(ctx, corerepos.MarkRepoUntracked{ProjectID: "prj_1", RepoID: "rep_api", At: t1, ExpectVersion: 0}); err != nil {
				return err
			}
			return w.MarkProjectUntracked(ctx, corerepos.MarkProjectUntracked{ProjectID: "prj_1", At: t1, ExpectVersion: 0})
		})
		ws, _ = s.GetWorkspace(ctx)
		if got := ws.Projects["prj_1"]; got.Life != (corerepos.Untracked{At: t1}) || got.Version != 1 {
			t.Errorf("untracked project = %#v", got)
		}
		if got := ws.Projects["prj_1"].Repos["rep_web"]; got.Life != (corerepos.Untracked{At: t1}) {
			t.Errorf("untracked repo = %#v", got)
		}
	})

	t.Run("the repo path is the project folder plus the relative path", func(t *testing.T) {
		s := setup(t)
		r, err := s.GetRepo(ctx, "rep_web")
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(`C:\work\shop`, filepath.FromSlash("libs/web")); r.Path != want {
			t.Errorf("Path = %q, want %q", r.Path, want)
		}
	})

	t.Run("untracked rows are invisible to every active read", func(t *testing.T) {
		s := setup(t)
		mustWS(t, s, func(ctx context.Context, w *WorkspaceTx) error {
			if err := w.MarkRepoUntracked(ctx, corerepos.MarkRepoUntracked{ProjectID: "prj_1", RepoID: "rep_web", At: t1, ExpectVersion: 0}); err != nil {
				return err
			}
			return nil
		})
		if _, err := s.GetRepo(ctx, "rep_web"); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetRepo = %v, want ErrNotFound", err)
		}
		if rs, _ := s.ListRepos(ctx, "prj_1"); len(rs) != 1 || rs[0].ID != "rep_api" {
			t.Errorf("ListRepos = %v", rs)
		}
		if _, err := s.GetRepoIncludingUntracked(ctx, "rep_web"); err != nil {
			t.Errorf("history read must still work: %v", err)
		}
		mustWS(t, s, func(ctx context.Context, w *WorkspaceTx) error {
			return w.MarkProjectUntracked(ctx, corerepos.MarkProjectUntracked{ProjectID: "prj_1", At: t1, ExpectVersion: 1})
		})
		if _, err := s.GetProject(ctx, "prj_1"); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetProject = %v, want ErrNotFound", err)
		}
		if ps, _ := s.ListProjects(ctx); len(ps) != 0 {
			t.Errorf("ListProjects = %v", ps)
		}
	})

	t.Run("restore keeps the row, clears trust and renames", func(t *testing.T) {
		s := setup(t)
		mustWS(t, s, func(ctx context.Context, w *WorkspaceTx) error {
			if _, err := w.q.SetRepoTrusted(ctx, gen.SetRepoTrustedParams{Trusted: 1, ID: "rep_api"}); err != nil {
				return err
			}
			if err := w.MarkRepoUntracked(ctx, corerepos.MarkRepoUntracked{ProjectID: "prj_1", RepoID: "rep_api", At: t1, ExpectVersion: 0}); err != nil {
				return err
			}
			if err := w.MarkRepoUntracked(ctx, corerepos.MarkRepoUntracked{ProjectID: "prj_1", RepoID: "rep_web", At: t1, ExpectVersion: 0}); err != nil {
				return err
			}
			return w.MarkProjectUntracked(ctx, corerepos.MarkProjectUntracked{ProjectID: "prj_1", At: t1, ExpectVersion: 0})
		})
		mustWS(t, s, func(ctx context.Context, w *WorkspaceTx) error {
			if err := w.RestoreProject(ctx, corerepos.RestoreProject{ProjectID: "prj_1", Name: "shop2", Since: t1, ExpectVersion: 1}); err != nil {
				return err
			}
			return w.RestoreRepo(ctx, corerepos.RestoreRepo{ProjectID: "prj_1", RepoID: "rep_api", Since: t1, ExpectVersion: 1})
		})
		ws, _ := s.GetWorkspace(ctx)
		p := ws.Projects["prj_1"]
		if _, ok := p.Life.(corerepos.Active); !ok || p.Name != "shop2" || p.Version != 2 {
			t.Errorf("project = %#v", p)
		}
		if r := p.Repos["rep_api"]; !isActiveLife(r.Life) || r.Trusted {
			t.Errorf("restored repo = %#v, want Active and not trusted", r)
		}
	})

	t.Run("a stale version is a conflict", func(t *testing.T) {
		s := setup(t)
		mustWS(t, s, func(ctx context.Context, w *WorkspaceTx) error {
			return w.UpdateProject(ctx, corerepos.UpdateProject{ProjectID: "prj_1", Name: "renamed", ExpectVersion: 0})
		})
		err := commitWS(t, s, func(ctx context.Context, w *WorkspaceTx) error {
			return w.UpdateProject(ctx, corerepos.UpdateProject{ProjectID: "prj_1", Name: "again", ExpectVersion: 0})
		})
		if !errors.Is(err, ErrConflict) {
			t.Errorf("stale update = %v, want ErrConflict", err)
		}
		err = commitWS(t, s, func(ctx context.Context, w *WorkspaceTx) error {
			return w.UpdateProject(ctx, corerepos.UpdateProject{ProjectID: "prj_missing", Name: "x", ExpectVersion: 0})
		})
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("unknown project = %v, want ErrNotFound", err)
		}
		ws, _ := s.GetWorkspace(ctx)
		if ws.Projects["prj_1"].Name != "renamed" || ws.Projects["prj_1"].Version != 1 {
			t.Errorf("the failed commit left a trace: %#v", ws.Projects["prj_1"])
		}
	})

	t.Run("one decision bumps the version once", func(t *testing.T) {
		s := setup(t)
		ws, _ := s.GetWorkspace(ctx)
		if v := ws.Projects["prj_1"].Version; v != 0 {
			t.Fatalf("a new project with two repos has version %d, want 0", v)
		}
		mustWS(t, s, func(ctx context.Context, w *WorkspaceTx) error {
			if err := w.InsertRepo(ctx, repo("prj_1", "rep_x", "x", "x", 0)); err != nil {
				return err
			}
			return w.InsertRepo(ctx, repo("prj_1", "rep_y", "y", "y", 0))
		})
		ws, _ = s.GetWorkspace(ctx)
		if v := ws.Projects["prj_1"].Version; v != 1 {
			t.Errorf("version = %d, want 1", v)
		}
	})

	t.Run("a unique key clash is a conflict", func(t *testing.T) {
		s := setup(t)
		for name, fn := range map[string]func(ctx context.Context, w *WorkspaceTx) error{
			"same folder": func(ctx context.Context, w *WorkspaceTx) error {
				return w.InsertRepo(ctx, repo("prj_1", "rep_z", "z", "api", 0))
			},
			"same name": func(ctx context.Context, w *WorkspaceTx) error {
				return w.InsertRepo(ctx, repo("prj_1", "rep_z", "api", "z", 0))
			},
			"same active project name": func(ctx context.Context, w *WorkspaceTx) error {
				return w.InsertProject(ctx, project("prj_2", "shop", `C:\work\elsewhere`))
			},
		} {
			if err := commitWS(t, s, fn); !errors.Is(err, ErrConflict) {
				t.Errorf("%s = %v, want ErrConflict", name, err)
			}
		}
	})

	t.Run("busy repos are those with a queued or running build of the main checkout", func(t *testing.T) {
		s := setup(t)
		d := s.db
		exec := func(q string, args ...any) {
			t.Helper()
			if _, err := d.write.ExecContext(ctx, q, args...); err != nil {
				t.Fatal(err)
			}
		}
		exec(`INSERT INTO builds (id, target, status, created_at) VALUES ('b_run', 'main', 'running', '2026-03-01T00:00:00.000Z')`)
		exec(`INSERT INTO build_steps (build_id, idx, repo_id, name, status) VALUES ('b_run', 0, 'rep_api', 'verify', 'running')`)
		exec(`INSERT INTO builds (id, target, status, created_at) VALUES ('b_done', 'main', 'succeeded', '2026-03-01T00:00:00.000Z')`)
		exec(`INSERT INTO build_steps (build_id, idx, repo_id, name, status) VALUES ('b_done', 0, 'rep_web', 'verify', 'succeeded')`)
		ws, err := s.GetWorkspace(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(ws.Busy) != 1 || ws.Busy["rep_api"] != corerepos.BusyBuildRunning {
			t.Errorf("Busy = %v, want only rep_api", ws.Busy)
		}
	})

	t.Run("busy repos include those of a queued, running or waiting task", func(t *testing.T) {
		s := setup(t)
		exec := func(q string, args ...any) {
			t.Helper()
			if _, err := s.db.write.ExecContext(ctx, q, args...); err != nil {
				t.Fatal(err)
			}
		}
		var pid string
		if err := s.db.write.QueryRowContext(ctx, `SELECT project_id FROM repos WHERE id = 'rep_api'`).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		for i, st := range []string{"queued", "running", "needs_you", "review", "done", "draft"} {
			id := "t_" + st
			exec(`INSERT INTO tasks (id, project_id, title, prompt, scope, mode, agent, status, created_at, updated_at)
			      VALUES (?, ?, 't', 'p', 'repo', 'ask', 'mock', ?, '2026-03-01T00:00:00.000Z', '2026-03-01T00:00:00.000Z')`, id, pid, st)
			repo := "rep_api"
			if i >= 3 {
				repo = "rep_web"
			}
			exec(`INSERT INTO task_repos (task_id, repo_id) VALUES (?, ?)`, id, repo)
		}
		ws, err := s.GetWorkspace(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(ws.Busy) != 1 || ws.Busy["rep_api"] != corerepos.BusyTaskRunning {
			t.Errorf("Busy = %v, want only rep_api as task_running (finished and draft tasks don't count)", ws.Busy)
		}
	})
}

func isActiveLife(l corerepos.Life) bool { _, ok := l.(corerepos.Active); return ok }

func TestWorkspaceStoreContractSQLite(t *testing.T) {
	WorkspaceStoreContract(t, func(t *testing.T) *Store { return NewStore(openTest(t), nil) })
}

// workspaceFromRows rejects each inconsistent combination.
func TestWorkspaceFromRowsRejects(t *testing.T) {
	proj := func(id, path string, untracked bool) gen.Project {
		p := gen.Project{ID: id, Name: "n" + id, Path: path, CreatedAt: "2026-03-01T00:00:00.000Z"}
		if untracked {
			p.UntrackedAt = sql.NullString{String: "2026-03-02T00:00:00.000Z", Valid: true}
		}
		return p
	}
	rep := func(id, project, name, rel string, untracked bool) gen.Repo {
		r := gen.Repo{ID: id, ProjectID: project, Name: name, RelPath: rel, CreatedAt: "2026-03-01T00:00:00.000Z"}
		if untracked {
			r.UntrackedAt = sql.NullString{String: "2026-03-02T00:00:00.000Z", Valid: true}
		}
		return r
	}
	cases := map[string]struct {
		ps []gen.Project
		rs []gen.Repo
	}{
		"relative project path":      {[]gen.Project{proj("p", "work/shop", false)}, nil},
		"device project path":        {[]gen.Project{proj("p", `\\?\C:\work`, false)}, nil},
		"repo of an unknown project": {[]gen.Project{proj("p", "/w", false)}, []gen.Repo{rep("r", "q", "a", ".", false)}},
		"duplicate folder":           {[]gen.Project{proj("p", "/w", false)}, []gen.Repo{rep("r1", "p", "a", "x", false), rep("r2", "p", "b", "x", false)}},
		"duplicate name":             {[]gen.Project{proj("p", "/w", false)}, []gen.Repo{rep("r1", "p", "a", "x", false), rep("r2", "p", "a", "y", false)}},
		"escaping folder":            {[]gen.Project{proj("p", "/w", false)}, []gen.Repo{rep("r1", "p", "a", "../x", false)}},
		"active repo in untracked":   {[]gen.Project{proj("p", "/w", true)}, []gen.Repo{rep("r1", "p", "a", "x", false)}},
		"bad untracked time":         {[]gen.Project{{ID: "p", Name: "n", Path: "/w", CreatedAt: "x"}}, nil},
		"blank project name":         {[]gen.Project{{ID: "p", Name: " ", Path: "/w", CreatedAt: "2026-03-01T00:00:00.000Z"}}, nil},
	}
	for name, c := range cases {
		if _, err := workspaceFromRows(c.ps, c.rs, nil, nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	ok, err := workspaceFromRows([]gen.Project{proj("p", "/w", true)}, []gen.Repo{rep("r1", "p", "a", "x", true)}, []string{"r1"}, nil)
	if err != nil || len(ok.Projects) != 1 || ok.Busy["r1"] != corerepos.BusyBuildRunning {
		t.Errorf("a consistent untracked project is rejected: %v", err)
	}
}

func FuzzWorkspaceFromRows(f *testing.F) {
	f.Add("/w", "a", ".", "2026-03-01T00:00:00.000Z", "", false)
	f.Add(`C:\work\x`, "api", "libs/api", "2026-03-01T00:00:00.000Z", "2026-03-02T00:00:00.000Z", true)
	f.Add("rel", "", "../x", "garbage", "garbage", true)
	f.Fuzz(func(t *testing.T, path, name, rel, created, untracked string, projUntracked bool) {
		p := gen.Project{ID: "p", Name: "proj", Path: path, CreatedAt: created}
		if projUntracked {
			p.UntrackedAt = sql.NullString{String: untracked, Valid: true}
		}
		r := gen.Repo{ID: "r", ProjectID: "p", Name: name, RelPath: rel, CreatedAt: created}
		if untracked != "" {
			r.UntrackedAt = sql.NullString{String: untracked, Valid: true}
		}
		ws, err := workspaceFromRows([]gen.Project{p}, []gen.Repo{r}, nil, nil)
		if err != nil {
			return
		}
		// Accepted implies valid.
		pr := ws.Projects["p"]
		if _, err := corerepos.NewCleanPath(string(pr.Path)); err != nil {
			t.Fatalf("accepted a bad path %q", pr.Path)
		}
		for _, rr := range pr.Repos {
			if _, err := corerepos.NewRelPath(string(rr.RelPath)); err != nil {
				t.Fatalf("accepted a bad folder %q", rr.RelPath)
			}
			if _, active := rr.Life.(corerepos.Active); active && !isActiveLife(pr.Life) {
				t.Fatal("accepted an Active repo in an Untracked project")
			}
		}
	})
}
