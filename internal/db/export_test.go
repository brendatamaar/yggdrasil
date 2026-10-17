package db

import (
	"context"
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db/gen"
)

// Test-only single-write helpers. Production code writes only through
// Store.Commit, with the audit entry in the same transaction (invariant 8);
// these skip the entry and exist so database tests can set up rows.

func commit1[T any](ctx context.Context, s *Store, fn func(*Tx) (T, error)) (T, error) {
	var out T
	err := s.Commit(ctx, func(tx *Tx) error {
		v, err := fn(tx)
		out = v
		return err
	})
	return out, err
}

func now() time.Time { return time.Now().UTC() }

func (s *Store) CreateProject(ctx context.Context, p core.Project) (core.Project, error) {
	if p.ID == "" {
		p.ID = core.ProjectID(NewID("prj"))
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now()
	}
	return commit1(ctx, s, func(tx *Tx) (core.Project, error) { return tx.CreateProject(ctx, p) })
}

func (s *Store) CreateRepo(ctx context.Context, r core.Repo) (core.Repo, error) {
	if r.ID == "" {
		r.ID = core.RepoID(NewID("rep"))
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now()
	}
	return commit1(ctx, s, func(tx *Tx) (core.Repo, error) { return tx.CreateRepo(ctx, r) })
}

func (s *Store) SetRepoTrusted(ctx context.Context, id core.RepoID, trusted bool) error {
	return s.Commit(ctx, func(tx *Tx) error { return tx.SetRepoTrusted(ctx, id, trusted) })
}

func (s *Store) CreateTask(ctx context.Context, t core.Task) (core.Task, error) {
	if t.ID == "" {
		t.ID = core.TaskID(NewID("tsk"))
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now()
	}
	if t.UpdatedAt.IsZero() {
		t.UpdatedAt = t.CreatedAt
	}
	return commit1(ctx, s, func(tx *Tx) (core.Task, error) { return tx.CreateTask(ctx, t) })
}

func (s *Store) UpdateTaskStatus(ctx context.Context, id core.TaskID, status core.TaskStatus, version int64) (int64, error) {
	return commit1(ctx, s, func(tx *Tx) (int64, error) { return tx.UpdateTaskStatus(ctx, id, status, version, now()) })
}

func (s *Store) SetTaskSession(ctx context.Context, id core.TaskID, session string, version int64) (int64, error) {
	return commit1(ctx, s, func(tx *Tx) (int64, error) { return tx.SetTaskSession(ctx, id, session, version, now()) })
}

func (s *Store) AddTaskDependency(ctx context.Context, task, dependsOn core.TaskID) error {
	return s.Commit(ctx, func(tx *Tx) error { return tx.AddTaskDependency(ctx, task, dependsOn) })
}

func (s *Store) CreateWorktree(ctx context.Context, w core.Worktree) (core.Worktree, error) {
	if w.ID == "" {
		w.ID = core.WorktreeID(NewID("wt"))
	}
	if w.CreatedAt.IsZero() {
		w.CreatedAt = now()
	}
	return commit1(ctx, s, func(tx *Tx) (core.Worktree, error) { return tx.CreateWorktree(ctx, w) })
}

func (s *Store) UpdateWorktreeStatus(ctx context.Context, id core.WorktreeID, status core.WorktreeStatus) error {
	return s.Commit(ctx, func(tx *Tx) error { return tx.UpdateWorktreeStatus(ctx, id, status) })
}

func (s *Store) CreateChangeSet(ctx context.Context, c core.ChangeSet) (core.ChangeSet, error) {
	if c.ID == "" {
		c.ID = core.ChangeSetID(NewID("cs"))
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now()
	}
	return commit1(ctx, s, func(tx *Tx) (core.ChangeSet, error) { return tx.CreateChangeSet(ctx, c) })
}

func (s *Store) UpdateChangeSetStatus(ctx context.Context, id core.ChangeSetID, status core.ChangeSetStatus) error {
	return s.Commit(ctx, func(tx *Tx) error { return tx.UpdateChangeSetStatus(ctx, id, status) })
}

func (s *Store) CreateProposal(ctx context.Context, p core.Proposal) (core.Proposal, error) {
	if p.ID == "" {
		p.ID = core.ProposalID(NewID("prop"))
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now()
	}
	return commit1(ctx, s, func(tx *Tx) (core.Proposal, error) { return tx.CreateProposal(ctx, p) })
}

func (s *Store) UpdateProposalStatus(ctx context.Context, id core.ProposalID, status core.ProposalStatus, version int64) (int64, error) {
	return commit1(ctx, s, func(tx *Tx) (int64, error) { return tx.UpdateProposalStatus(ctx, id, status, version, now()) })
}

// InsertBuild writes a build row and its steps for read tests. Builds are
// written in production by SaveBuild (core/builds); this only fills the rows
// the read mapping is tested against.
func (s *Store) InsertBuild(ctx context.Context, b core.Build) (core.Build, error) {
	if b.ID == "" {
		b.ID = core.BuildID(NewID("bld"))
	}
	if b.Status == "" {
		b.Status = core.BuildQueued
	}
	if b.CreatedAt.IsZero() {
		b.CreatedAt = now()
	}
	b.CreatedAt = b.CreatedAt.UTC().Truncate(time.Millisecond)
	if b.Steps == nil {
		b.Steps = []core.BuildStep{}
	}
	target := "main"
	if b.WorktreeID != nil {
		target = "worktree"
	}
	err := s.Commit(ctx, func(tx *Tx) error {
		if err := tx.q.CreateBuild(ctx, gen.CreateBuildParams{
			ID: string(b.ID), WorktreeID: nullStr(b.WorktreeID), TaskID: nullStr(b.TaskID), Target: target,
			Status: string(b.Status), CreatedAt: fmtTime(b.CreatedAt), FinishedAt: fmtNullTime(b.FinishedAt),
		}); err != nil {
			return fmt.Errorf("insert build: %w", err)
		}
		for i, st := range b.Steps {
			b.Steps[i].Index = i
			argv, err := argvText(st.Argv)
			if err != nil {
				return err
			}
			if err := tx.q.CreateBuildStep(ctx, gen.CreateBuildStepParams{
				BuildID: string(b.ID), Idx: int64(i), RepoID: string(st.RepoID), Name: st.Name, Status: string(st.Status),
				ExitCode: nullInt(st.ExitCode), LogPath: st.LogPath, StartedAt: fmtNullTime(st.StartedAt), FinishedAt: fmtNullTime(st.FinishedAt),
				Argv: argv, Attempt: int64(st.Attempt), Outcome: nullText(string(st.Outcome)),
			}); err != nil {
				return fmt.Errorf("insert build step: %w", err)
			}
		}
		return nil
	})
	return b, err
}
