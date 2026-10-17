package db

import (
	"context"

	"github.com/brendatamaar/yggdrasil/internal/core"
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

func (s *Store) CreateProject(ctx context.Context, p core.Project) (core.Project, error) {
	return commit1(ctx, s, func(tx *Tx) (core.Project, error) { return tx.CreateProject(ctx, p) })
}

func (s *Store) CreateRepo(ctx context.Context, r core.Repo) (core.Repo, error) {
	return commit1(ctx, s, func(tx *Tx) (core.Repo, error) { return tx.CreateRepo(ctx, r) })
}

func (s *Store) SetRepoTrusted(ctx context.Context, id core.RepoID, trusted bool) error {
	return s.Commit(ctx, func(tx *Tx) error { return tx.SetRepoTrusted(ctx, id, trusted) })
}

func (s *Store) CreateTask(ctx context.Context, t core.Task) (core.Task, error) {
	return commit1(ctx, s, func(tx *Tx) (core.Task, error) { return tx.CreateTask(ctx, t) })
}

func (s *Store) UpdateTaskStatus(ctx context.Context, id core.TaskID, status core.TaskStatus, version int64) (int64, error) {
	return commit1(ctx, s, func(tx *Tx) (int64, error) { return tx.UpdateTaskStatus(ctx, id, status, version) })
}

func (s *Store) SetTaskSession(ctx context.Context, id core.TaskID, session string, version int64) (int64, error) {
	return commit1(ctx, s, func(tx *Tx) (int64, error) { return tx.SetTaskSession(ctx, id, session, version) })
}

func (s *Store) AddTaskDependency(ctx context.Context, task, dependsOn core.TaskID) error {
	return s.Commit(ctx, func(tx *Tx) error { return tx.AddTaskDependency(ctx, task, dependsOn) })
}

func (s *Store) CreateWorktree(ctx context.Context, w core.Worktree) (core.Worktree, error) {
	return commit1(ctx, s, func(tx *Tx) (core.Worktree, error) { return tx.CreateWorktree(ctx, w) })
}

func (s *Store) UpdateWorktreeStatus(ctx context.Context, id core.WorktreeID, status core.WorktreeStatus) error {
	return s.Commit(ctx, func(tx *Tx) error { return tx.UpdateWorktreeStatus(ctx, id, status) })
}

func (s *Store) CreateChangeSet(ctx context.Context, c core.ChangeSet) (core.ChangeSet, error) {
	return commit1(ctx, s, func(tx *Tx) (core.ChangeSet, error) { return tx.CreateChangeSet(ctx, c) })
}

func (s *Store) UpdateChangeSetStatus(ctx context.Context, id core.ChangeSetID, status core.ChangeSetStatus) error {
	return s.Commit(ctx, func(tx *Tx) error { return tx.UpdateChangeSetStatus(ctx, id, status) })
}

func (s *Store) CreateProposal(ctx context.Context, p core.Proposal) (core.Proposal, error) {
	return commit1(ctx, s, func(tx *Tx) (core.Proposal, error) { return tx.CreateProposal(ctx, p) })
}

func (s *Store) UpdateProposalStatus(ctx context.Context, id core.ProposalID, status core.ProposalStatus, version int64) (int64, error) {
	return commit1(ctx, s, func(tx *Tx) (int64, error) { return tx.UpdateProposalStatus(ctx, id, status, version) })
}

func (s *Store) CreateBuild(ctx context.Context, b core.Build) (core.Build, error) {
	return commit1(ctx, s, func(tx *Tx) (core.Build, error) { return tx.CreateBuild(ctx, b) })
}

func (s *Store) UpdateBuildStatus(ctx context.Context, id core.BuildID, status core.BuildStatus, version int64) (int64, error) {
	return commit1(ctx, s, func(tx *Tx) (int64, error) { return tx.UpdateBuildStatus(ctx, id, status, version) })
}

func (s *Store) UpdateBuildStep(ctx context.Context, id core.BuildID, st core.BuildStep) error {
	return s.Commit(ctx, func(tx *Tx) error { return tx.UpdateBuildStep(ctx, id, st) })
}
