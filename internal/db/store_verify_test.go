package db

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/tasks"
)

// ListReviewTurns finds a task only when it is idle in review and its last
// turn ended ready for review.
func TestListReviewTurns(t *testing.T) {
	ctx := context.Background()
	s := NewStore(openTest(t), nil)
	p, r := seed(t, s)

	// A task that went idle ready for review.
	review, err := s.CreateTask(ctx, newTask(p, r, nil))
	if err != nil {
		t.Fatal(err)
	}
	v := saveState(t, s, review.ID, tasks.Waiting{Turn: "trn_1", BlockedBy: tasks.BlockedNone{}}, review.Version, true)
	saveState(t, s, review.ID, tasks.Idle{Turn: "trn_1", Outcome: tasks.ReadyForReview{}, At: at0}, v, false)

	// A task that was only answered.
	answered, err := s.CreateTask(ctx, newTask(p, r, nil))
	if err != nil {
		t.Fatal(err)
	}
	v = saveState(t, s, answered.ID, tasks.Waiting{Turn: "trn_2", BlockedBy: tasks.BlockedNone{}}, answered.Version, true)
	saveState(t, s, answered.ID, tasks.Idle{Turn: "trn_2", Outcome: tasks.Answered{}, At: at0}, v, false)

	got, err := s.ListReviewTurns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Task != review.ID || got[0].Turn != "trn_1" || got[0].Seq != 1 {
		t.Fatalf("review turns = %+v", got)
	}
}

func TestVerificationRowRoundTripAndUniqueness(t *testing.T) {
	ctx := context.Background()
	s := NewStore(openTest(t), nil)
	p, r := seed(t, s)
	task, err := s.CreateTask(ctx, newTask(p, r, nil))
	if err != nil {
		t.Fatal(err)
	}
	var wt core.Worktree
	if err := s.Commit(ctx, func(tx *Tx) error {
		wt, err = tx.CreateWorktree(ctx, core.Worktree{ID: "wt_1", RepoID: r.ID, TaskID: task.ID, Name: "w", Path: r.Path + "/w", Branch: "ygg/w", CreatedAt: at0})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	row := VerificationRow{
		ID: "ver_1", Task: task.ID, Repo: r.ID, Worktree: wt.ID, State: "passed", Turn: "t1", Attempt: 1, Build: "bld_1",
		At: &at0, Plan: json.RawMessage(`{"seq":1}`), BootHash: "h",
	}
	if err := s.Commit(ctx, func(tx *Tx) error {
		_, err := tx.SaveVerification(ctx, row, NewVerification, at0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetVerificationByTaskRepo(ctx, task.ID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "ver_1" || got.BootHash != "h" || got.At == nil || !got.At.Equal(at0) || string(got.Plan) != `{"seq":1}` || !got.CreatedAt.Equal(at0) {
		t.Fatalf("got %+v", got)
	}
	if err := s.Commit(ctx, func(tx *Tx) error {
		_, err := tx.SaveVerification(ctx, row, 0, at0.Add(time.Second))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// A second row for the same (task, repo) is a conflict.
	other := row
	other.ID = "ver_2"
	if err := s.Commit(ctx, func(tx *Tx) error {
		_, err := tx.SaveVerification(ctx, other, NewVerification, at0)
		return err
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want conflict", err)
	}
	// A write without a time is refused: the store never reads the clock.
	if err := s.Commit(ctx, func(tx *Tx) error {
		_, err := tx.SaveVerification(ctx, row, 1, time.Time{})
		return err
	}); err == nil {
		t.Fatal("a write without a time was accepted")
	}
}
