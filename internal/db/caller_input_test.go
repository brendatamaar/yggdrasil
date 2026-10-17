package db

import (
	"context"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// The store neither generates IDs nor reads the clock: what the caller leaves
// empty is an error, and what the caller passes is what is stored.
func TestStoreTakesIDsAndTimesFromTheCaller(t *testing.T) {
	d := openTest(t)
	s := NewStore(d)
	ctx := context.Background()
	p, r := seed(t, s)
	task, err := s.CreateTask(ctx, newTask(p, r, nil))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	taskWith := func(id core.TaskID, created, updated time.Time) core.Task {
		tk := newTask(p, r, nil)
		tk.ID, tk.CreatedAt, tk.UpdatedAt = id, created, updated
		return tk
	}

	cases := map[string]func(tx *Tx) error{
		"project without an ID": func(tx *Tx) error {
			_, err := tx.CreateProject(ctx, core.Project{Name: "n", CreatedAt: at})
			return err
		},
		"project without a time": func(tx *Tx) error {
			_, err := tx.CreateProject(ctx, core.Project{ID: "p9", Name: "n"})
			return err
		},
		"repo without an ID": func(tx *Tx) error {
			_, err := tx.CreateRepo(ctx, core.Repo{ProjectID: p.ID, Name: "n", Path: "/x", CreatedAt: at})
			return err
		},
		"repo without a time": func(tx *Tx) error {
			_, err := tx.CreateRepo(ctx, core.Repo{ID: "r9", ProjectID: p.ID, Name: "n", Path: "/x"})
			return err
		},
		"task without an ID": func(tx *Tx) error {
			_, err := tx.CreateTask(ctx, taskWith("", at, at))
			return err
		},
		"task without CreatedAt": func(tx *Tx) error {
			_, err := tx.CreateTask(ctx, taskWith("t9", time.Time{}, at))
			return err
		},
		"task without UpdatedAt": func(tx *Tx) error {
			_, err := tx.CreateTask(ctx, taskWith("t9", at, time.Time{}))
			return err
		},
		"worktree without an ID": func(tx *Tx) error {
			_, err := tx.CreateWorktree(ctx, core.Worktree{RepoID: r.ID, TaskID: task.ID, Name: "w", Path: "/w", Branch: "b", CreatedAt: at})
			return err
		},
		"worktree without a time": func(tx *Tx) error {
			_, err := tx.CreateWorktree(ctx, core.Worktree{ID: "w9", RepoID: r.ID, TaskID: task.ID, Name: "w", Path: "/w", Branch: "b"})
			return err
		},
		"change set without an ID": func(tx *Tx) error {
			_, err := tx.CreateChangeSet(ctx, core.ChangeSet{TaskID: task.ID, CreatedAt: at})
			return err
		},
		"change set without a time": func(tx *Tx) error {
			_, err := tx.CreateChangeSet(ctx, core.ChangeSet{ID: "c9", TaskID: task.ID})
			return err
		},
		"proposal without an ID": func(tx *Tx) error {
			_, err := tx.CreateProposal(ctx, core.Proposal{Kind: core.ProposalCommit, CreatedAt: at})
			return err
		},
		"proposal without a time": func(tx *Tx) error {
			_, err := tx.CreateProposal(ctx, core.Proposal{ID: "pr9", Kind: core.ProposalCommit})
			return err
		},
		"task status without a time": func(tx *Tx) error {
			_, err := tx.UpdateTaskStatus(ctx, task.ID, core.TaskDone, task.Version, time.Time{})
			return err
		},
		"task session without a time": func(tx *Tx) error {
			_, err := tx.SetTaskSession(ctx, task.ID, "s", task.Version, time.Time{})
			return err
		},
		"proposal decision without a time": func(tx *Tx) error {
			_, err := tx.UpdateProposalStatus(ctx, "pr1", core.ProposalConfirmed, 0, time.Time{})
			return err
		},
	}
	for name, run := range cases {
		if err := s.Commit(ctx, run); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	var v int64
	if err := s.Commit(ctx, func(tx *Tx) (err error) {
		v, err = tx.UpdateTaskStatus(ctx, task.ID, core.TaskDone, task.Version, at)
		return err
	}); err != nil || v != 1 {
		t.Fatalf("update with a time = %d, %v", v, err)
	}
	got, err := s.GetTask(ctx, task.ID)
	if err != nil || got.FinishedAt == nil || !got.FinishedAt.Equal(at) || !got.UpdatedAt.Equal(at) {
		t.Fatalf("task = %+v, %v; want finished and updated at the time given", got, err)
	}
}
