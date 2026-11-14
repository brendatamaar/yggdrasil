package db

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db/gen"
)

func validSubmissionID(id core.RequestID) error {
	if err := id.Validate(); err != nil {
		return err
	}
	if len(id) > 100 {
		return errors.New("submission request id is too long")
	}
	return nil
}

func validSubmissionDigest(digest string) error {
	if len(digest) != 64 || strings.ContainsFunc(digest, func(c rune) bool { return (c < '0' || c > '9') && (c < 'a' || c > 'f') }) {
		return errors.New("submission digest must be 64 lowercase hex digits")
	}
	return nil
}

func submissionFrom(r gen.TaskSubmission) (core.TaskID, string, error) {
	if err := validSubmissionID(core.RequestID(r.RequestID)); err != nil {
		return "", "", err
	}
	if err := validSubmissionDigest(r.Digest); err != nil {
		return "", "", err
	}
	id := core.TaskID(r.TaskID)
	if err := id.Validate(); err != nil {
		return "", "", err
	}
	return id, r.Digest, nil
}

// GetTaskSubmission is the read-only restart reconciliation lookup. ErrNotFound
// means no committed submission exists; admission must still check inside Commit.
func (s *Store) GetTaskSubmission(ctx context.Context, id core.RequestID) (core.TaskID, string, error) {
	return getTaskSubmission(ctx, s.db.reader(), id)
}

// GetTaskSubmission checks the durable request link in the committing transaction.
// Call before creating a task/turn. On a duplicate, reload the original task;
// never admit a second run. Never call Store methods from inside Commit.
func (tx *Tx) GetTaskSubmission(ctx context.Context, id core.RequestID) (core.TaskID, string, error) {
	return getTaskSubmission(ctx, tx.q, id)
}

func getTaskSubmission(ctx context.Context, q *gen.Queries, id core.RequestID) (core.TaskID, string, error) {
	if err := validSubmissionID(id); err != nil {
		return "", "", err
	}
	r, err := q.GetTaskSubmission(ctx, string(id))
	if err := found("get task submission", err); err != nil {
		return "", "", err
	}
	return submissionFrom(r)
}

// LinkTaskSubmission binds one request to its immutable digest and existing task.
// Create task, turn, link and audit in ONE Commit, propagating every error so a
// losing contender rolls back all its writes. Digest changes are ErrDigestMismatch;
// an equal digest with a different task is ErrConflict. Reload the winning link
// after a conflict outside the failed Commit before returning its result. Links
// never expire; deleting a referenced task is refused by its foreign key.
func (tx *Tx) LinkTaskSubmission(ctx context.Context, id core.RequestID, digest string, task core.TaskID) error {
	if err := validSubmissionID(id); err != nil {
		return err
	}
	if err := validSubmissionDigest(digest); err != nil {
		return err
	}
	if err := task.Validate(); err != nil {
		return err
	}
	oldTask, oldDigest, err := tx.GetTaskSubmission(ctx, id)
	if err == nil {
		if oldDigest != digest {
			return ErrDigestMismatch
		}
		if oldTask != task {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, ErrNotFound) {
		return err
	}
	if err := tx.q.LinkTaskSubmission(ctx, gen.LinkTaskSubmissionParams{RequestID: string(id), Digest: digest, TaskID: string(task)}); err != nil {
		return fmt.Errorf("link task submission: %w", err)
	}
	return nil
}

// GetTrackedReadyRepo enforces issue-import scope inside the same Commit that
// creates the draft and audit. Reserved, untracked and foreign-project repos
// (including repos of an untracked project) are ErrNotFound.
func (tx *Tx) GetTrackedReadyRepo(ctx context.Context, project core.ProjectID, repo core.RepoID) (core.Repo, error) {
	if err := project.Validate(); err != nil {
		return core.Repo{}, err
	}
	if err := repo.Validate(); err != nil {
		return core.Repo{}, err
	}
	r, err := tx.q.GetTrackedReadyRepo(ctx, gen.GetTrackedReadyRepoParams{ID: string(repo), ProjectID: string(project)})
	if err := found("get tracked ready repo", err); err != nil {
		return core.Repo{}, err
	}
	return repoFrom(r)
}
