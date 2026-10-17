package db

import (
	"context"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// The store refuses values the core calls invalid, before they reach SQL.
func TestStoreRejectsInvalidValues(t *testing.T) {
	d := openTest(t)
	s := NewStore(d, nil)
	ctx := context.Background()
	p, r := seed(t, s)
	task, err := s.CreateTask(ctx, newTask(p, r, nil))
	if err != nil {
		t.Fatal(err)
	}
	wt, err := s.CreateWorktree(ctx, core.Worktree{RepoID: r.ID, TaskID: task.ID, Name: "w", Path: "/wt", Branch: "b"})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := s.CreateChangeSet(ctx, core.ChangeSet{TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string]func() error{
		"project without a name": func() error {
			_, err := s.CreateProject(ctx, core.Project{Name: "  ", Path: "/work"})
			return err
		},
		"repo with a relative path": func() error {
			_, err := s.CreateRepo(ctx, core.Repo{ProjectID: p.ID, Name: "x", Path: `work\x`})
			return err
		},
		"repo with an empty path": func() error {
			_, err := s.CreateRepo(ctx, core.Repo{ProjectID: p.ID, Name: "x"})
			return err
		},
		"repo without a name": func() error {
			_, err := s.CreateRepo(ctx, core.Repo{ProjectID: p.ID, Path: "/work/x"})
			return err
		},
		"repo without a project": func() error {
			_, err := s.CreateRepo(ctx, core.Repo{Name: "x", Path: "/work/x"})
			return err
		},
		"worktree with an unknown status": func() error {
			_, err := s.CreateWorktree(ctx, core.Worktree{RepoID: r.ID, TaskID: task.ID, Name: "w2", Path: "/wt2", Branch: "b", Status: "gone"})
			return err
		},
		"unknown worktree status update": func() error {
			return s.UpdateWorktreeStatus(ctx, wt.ID, "gone")
		},
		"change set with an unknown status": func() error {
			_, err := s.CreateChangeSet(ctx, core.ChangeSet{TaskID: task.ID, Status: "lost"})
			return err
		},
		"unknown change set status update": func() error {
			return s.UpdateChangeSetStatus(ctx, cs.ID, "lost")
		},
		"audit entry with a bad ID": func() error {
			return s.Commit(ctx, func(tx *Tx) error {
				return tx.AppendAudit(ctx, core.AuditEntry{ID: "a b", Actor: "system", Action: "x", Subject: "task:1"})
			})
		},
		"audit entry without an ID": func() error {
			return s.Commit(ctx, func(tx *Tx) error {
				return tx.AppendAudit(ctx, core.AuditEntry{Actor: "system", Action: "x", Subject: "task:1"})
			})
		},
	}
	for name, run := range cases {
		if err := run(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if ps, err := s.ListProjects(ctx); err != nil || len(ps) != 1 {
		t.Errorf("projects = %+v, %v; a refused project was stored", ps, err)
	}
	if rows, err := s.ListAudit(ctx, "", 10); err != nil || len(rows) != 0 {
		t.Errorf("audit rows = %+v, %v; a refused entry was stored", rows, err)
	}
}
