package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db/gen"
)

// NewVerification is the expected version SaveVerification takes to insert a
// verification that does not exist yet. Stored versions start at 0.
const NewVerification int64 = -1

// VerificationRow is a verifications row as plain values. This package does
// not interpret it: internal/verify parses it into core/verify types at the
// boundary (StateFromRow) and rejects inconsistent rows.
type VerificationRow struct {
	ID       string
	Task     core.TaskID
	Repo     core.RepoID
	Worktree core.WorktreeID
	Version  int64
	BootHash string
	State    string
	Turn     string
	Attempt  int
	// Reason is the skip reason, unverifiable reason or cancel cause.
	Reason    string
	Stage     string
	Build     string
	NextBuild string
	DepsHash  string
	FailStep  int
	FailKind  string
	ExitCode  int
	FailText  string
	// Plan is a JSON object (never empty).
	Plan      json.RawMessage
	Since     *time.Time
	At        *time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ReviewTurn is the last turn of a task that is idle in review.
type ReviewTurn struct {
	Task core.TaskID
	Turn string
	Seq  int
}

func verificationFrom(r gen.Verification) (VerificationRow, error) {
	since, err := parseNullTime(r.Since)
	if err != nil {
		return VerificationRow{}, fmt.Errorf("verification %s: %w", r.ID, err)
	}
	at, err := parseNullTime(r.At)
	if err != nil {
		return VerificationRow{}, fmt.Errorf("verification %s: %w", r.ID, err)
	}
	created, err := parseTime(r.CreatedAt)
	if err != nil {
		return VerificationRow{}, fmt.Errorf("verification %s: %w", r.ID, err)
	}
	upd, err := parseTime(r.UpdatedAt)
	if err != nil {
		return VerificationRow{}, fmt.Errorf("verification %s: %w", r.ID, err)
	}
	return VerificationRow{
		ID: r.ID, Task: core.TaskID(r.TaskID), Repo: core.RepoID(r.RepoID), Worktree: core.WorktreeID(r.WorktreeID),
		Version: r.Version, BootHash: r.BootHash, State: r.State, Turn: r.TurnID, Attempt: int(r.Attempt),
		Reason: r.Reason, Stage: r.Stage, Build: r.BuildID, NextBuild: r.NextBuildID, DepsHash: r.DepsHash,
		FailStep: int(r.FailStep), FailKind: r.FailKind, ExitCode: int(r.ExitCode), FailText: r.FailText,
		Plan: json.RawMessage(r.Plan), Since: since, At: at, CreatedAt: created, UpdatedAt: upd,
	}, nil
}

func verificationsFrom(rows []gen.Verification) ([]VerificationRow, error) {
	out := make([]VerificationRow, 0, len(rows))
	for _, r := range rows {
		v, err := verificationFrom(r)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// GetVerification returns one verification, or ErrNotFound.
func (s *Store) GetVerification(ctx context.Context, id string) (VerificationRow, error) {
	r, err := s.db.reader().GetVerification(ctx, id)
	if err := found("get verification "+id, err); err != nil {
		return VerificationRow{}, err
	}
	return verificationFrom(r)
}

// GetVerificationByTaskRepo returns the verification of a (task, repo), or
// ErrNotFound.
func (s *Store) GetVerificationByTaskRepo(ctx context.Context, task core.TaskID, repo core.RepoID) (VerificationRow, error) {
	r, err := s.db.reader().GetVerificationByTaskRepo(ctx, gen.GetVerificationByTaskRepoParams{TaskID: string(task), RepoID: string(repo)})
	if err := found("get verification of "+string(task)+"/"+string(repo), err); err != nil {
		return VerificationRow{}, err
	}
	return verificationFrom(r)
}

// GetVerificationByBuild returns the live verification (bootstrapping or
// verifying) that waits for the build, or ErrNotFound.
func (s *Store) GetVerificationByBuild(ctx context.Context, build core.BuildID) (VerificationRow, error) {
	r, err := s.db.reader().GetVerificationByBuild(ctx, string(build))
	if err := found("get verification of build "+string(build), err); err != nil {
		return VerificationRow{}, err
	}
	return verificationFrom(r)
}

// ListVerificationsByTask returns the verifications of a task, by repo.
func (s *Store) ListVerificationsByTask(ctx context.Context, task core.TaskID) ([]VerificationRow, error) {
	rows, err := s.db.reader().ListVerificationsByTask(ctx, string(task))
	if err != nil {
		return nil, fmt.Errorf("list verifications of %s: %w", task, err)
	}
	return verificationsFrom(rows)
}

// ListLiveVerifications returns the bootstrapping and verifying ones, oldest
// update first.
func (s *Store) ListLiveVerifications(ctx context.Context) ([]VerificationRow, error) {
	rows, err := s.db.reader().ListLiveVerifications(ctx)
	if err != nil {
		return nil, fmt.Errorf("list live verifications: %w", err)
	}
	return verificationsFrom(rows)
}

// ListInterruptedVerifications returns the interrupted ones, oldest update
// first.
func (s *Store) ListInterruptedVerifications(ctx context.Context) ([]VerificationRow, error) {
	rows, err := s.db.reader().ListInterruptedVerifications(ctx)
	if err != nil {
		return nil, fmt.Errorf("list interrupted verifications: %w", err)
	}
	return verificationsFrom(rows)
}

// ListReviewTurns returns, for every task idle in review, its last turn when
// that turn ended ready for review.
func (s *Store) ListReviewTurns(ctx context.Context) ([]ReviewTurn, error) {
	rows, err := s.db.reader().ListReviewTurns(ctx)
	if err != nil {
		return nil, fmt.Errorf("list review turns: %w", err)
	}
	out := make([]ReviewTurn, 0, len(rows))
	for _, r := range rows {
		out = append(out, ReviewTurn{Task: core.TaskID(r.TaskID), Turn: r.TurnID, Seq: int(r.TurnSeq)})
	}
	return out, nil
}

// LoadVerificationByTaskRepo is GetVerificationByTaskRepo inside a write
// transaction.
func (tx *Tx) LoadVerificationByTaskRepo(ctx context.Context, task core.TaskID, repo core.RepoID) (VerificationRow, error) {
	r, err := tx.q.GetVerificationByTaskRepo(ctx, gen.GetVerificationByTaskRepoParams{TaskID: string(task), RepoID: string(repo)})
	if err := found("load verification of "+string(task)+"/"+string(repo), err); err != nil {
		return VerificationRow{}, err
	}
	return verificationFrom(r)
}

// LoadVerification is GetVerification inside a write transaction.
func (tx *Tx) LoadVerification(ctx context.Context, id string) (VerificationRow, error) {
	r, err := tx.q.GetVerification(ctx, id)
	if err := found("load verification "+id, err); err != nil {
		return VerificationRow{}, err
	}
	return verificationFrom(r)
}

// SaveVerification inserts (expected == NewVerification; ErrConflict when the
// ID or the (task, repo) exists) or updates a verification that is still at
// expected (ErrConflict if the version moved, ErrNotFound if it is gone). now
// is the write time. It returns the new version (0 for an insert). Call it
// with AppendAudit in the same Store.Commit (invariant 8).
func (tx *Tx) SaveVerification(ctx context.Context, v VerificationRow, expected int64, now time.Time) (int64, error) {
	if err := needTime("verification", now); err != nil {
		return 0, err
	}
	if v.ID == "" {
		return 0, errors.New("save verification: the verification has no ID")
	}
	plan := jsonText(v.Plan, "{}")
	if !json.Valid([]byte(plan)) {
		return 0, errors.New("save verification: the plan is not valid JSON")
	}
	what := "save verification " + v.ID
	if expected == NewVerification {
		err := tx.q.InsertVerification(ctx, gen.InsertVerificationParams{
			ID: v.ID, TaskID: string(v.Task), RepoID: string(v.Repo), WorktreeID: string(v.Worktree),
			BootHash: v.BootHash, State: v.State, TurnID: v.Turn, Attempt: int64(v.Attempt), Reason: v.Reason,
			Stage: v.Stage, BuildID: v.Build, NextBuildID: v.NextBuild, DepsHash: v.DepsHash,
			FailStep: int64(v.FailStep), FailKind: v.FailKind, ExitCode: int64(v.ExitCode), FailText: v.FailText,
			Plan: plan, Since: fmtNullTime(v.Since), At: fmtNullTime(v.At),
			CreatedAt: fmtTime(now), UpdatedAt: fmtTime(now),
		})
		if err != nil {
			return 0, fmt.Errorf("%s: %w", what, err)
		}
		return 0, nil
	}
	n, err := tx.q.UpdateVerification(ctx, gen.UpdateVerificationParams{
		WorktreeID: string(v.Worktree), BootHash: v.BootHash, State: v.State, TurnID: v.Turn,
		Attempt: int64(v.Attempt), Reason: v.Reason, Stage: v.Stage, BuildID: v.Build, NextBuildID: v.NextBuild,
		DepsHash: v.DepsHash, FailStep: int64(v.FailStep), FailKind: v.FailKind, ExitCode: int64(v.ExitCode),
		FailText: v.FailText, Plan: plan, Since: fmtNullTime(v.Since), At: fmtNullTime(v.At),
		UpdatedAt: fmtTime(now), ID: v.ID, Version: expected,
	})
	return bumped(what, n, err, expected, func() error {
		_, err := tx.q.GetVerification(ctx, v.ID)
		return err
	})
}
