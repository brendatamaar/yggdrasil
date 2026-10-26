package db

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/tasks"
)

var at0 = time.Date(2026, 10, 26, 9, 0, 0, 123_000_000, time.UTC)

// saveState writes st the way the tasks service does: the task row and the
// turn row that carry it.
func saveState(t *testing.T, s *Store, id core.TaskID, st tasks.State, expected int64, newTurn bool) int64 {
	t.Helper()
	ctx := context.Background()
	trow, turn := tasks.RowsOf(st)
	if i, ok := st.(tasks.Idle); ok && i.Turn != "" {
		a := at0
		turn = &tasks.TurnRow{ID: i.Turn, Attempt: 1, State: tasks.TurnFinished, StartedAt: &a, FinishedAt: &a, Outcome: tasks.OutcomeCode(i.Outcome)}
	}
	var v int64
	err := s.Commit(ctx, func(tx *Tx) error {
		var err error
		if v, err = tx.SaveTask(ctx, id, trow, "sess-1", expected, at0); err != nil {
			return err
		}
		if turn != nil {
			turn.Seq, turn.Mode, turn.Prompt = 1, core.ModeCode, "do it"
			return tx.SaveTurn(ctx, id, *turn, newTurn)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("save %T: %v", st, err)
	}
	return v
}

func allStates() []tasks.State {
	u := tasks.Usage{InputTokens: 5, OutputTokens: 7, CostMicroUSD: 90, CostKnown: true, DurationMS: 12}
	states := []tasks.State{
		tasks.Draft{},
		tasks.Waiting{Turn: "trn_1", Attempt: 0, BlockedBy: tasks.BlockedNone{}},
		tasks.Waiting{Turn: "trn_1", Attempt: 2, BlockedBy: tasks.BlockedNone{}},
		tasks.Starting{Turn: "trn_1", Attempt: 1, Since: at0},
		tasks.Running{Turn: "trn_1", Attempt: 1, Since: at0, Usage: u},
		tasks.Stopping{Turn: "trn_1", Attempt: 1, Since: at0, Cause: tasks.CauseUserCancel, Usage: u},
		tasks.Idle{}, // a task from before turns (no last turn)
	}
	for _, o := range []tasks.Outcome{
		tasks.Answered{}, tasks.ReadyForReview{}, tasks.Cancelled{}, tasks.Dismissed{},
		tasks.NeedsYou{Why: tasks.NeedsIntegrityViolated}, tasks.NeedsYou{Why: tasks.NeedsCapExceeded},
		tasks.Failed{Kind: tasks.FailPrepare}, tasks.Failed{Kind: tasks.FailStart},
		tasks.Failed{Kind: tasks.FailRun}, tasks.Failed{Kind: tasks.FailInterrupted},
	} {
		states = append(states, tasks.Idle{Turn: "trn_1", Outcome: o, At: at0})
	}
	return states
}

// TestTaskStateRoundTrip: every state survives save then load.
func TestTaskStateRoundTrip(t *testing.T) {
	ctx := context.Background()
	for _, st := range allStates() {
		if i, ok := st.(tasks.Idle); ok && i.Outcome == nil {
			continue // Idle{} has no stored form; the legacy case has its own test
		}
		s := NewStore(openTest(t), nil)
		p, r := seed(t, s)
		task, err := s.CreateTask(ctx, newTask(p, r, nil))
		if err != nil {
			t.Fatal(err)
		}
		_, isDraft := st.(tasks.Draft)
		ver := saveState(t, s, task.ID, st, task.Version, !isDraft)
		got, err := s.GetTaskState(ctx, task.ID)
		if err != nil {
			t.Fatalf("%#v: %v", st, err)
		}
		if !reflect.DeepEqual(got.Task.State, st) {
			t.Errorf("round trip:\n got %#v\nwant %#v", got.Task.State, st)
		}
		if got.Row.Version != ver {
			t.Errorf("%T: version %d, want %d", st, got.Row.Version, ver)
		}
		if got.Task.SessionID != "sess-1" || got.Task.Turns != map[bool]int{true: 0, false: 1}[isDraft] {
			t.Errorf("%T: session %q turns %d", st, got.Task.SessionID, got.Task.Turns)
		}
		if i, ok := st.(tasks.Idle); ok {
			if got.Row.FinishedAt == nil || !got.Row.FinishedAt.Equal(i.At) {
				t.Errorf("idle at %v not stored in finished_at (%v)", i.At, got.Row.FinishedAt)
			}
			if got.Row.Status != i.Status() {
				t.Errorf("status %q, want %q", got.Row.Status, i.Status())
			}
		}
	}
}

func TestTaskStateVersionConflict(t *testing.T) {
	ctx := context.Background()
	s := NewStore(openTest(t), nil)
	p, r := seed(t, s)
	task, _ := s.CreateTask(ctx, newTask(p, r, nil))
	v := saveState(t, s, task.ID, tasks.Waiting{Turn: "trn_1", BlockedBy: tasks.BlockedNone{}}, task.Version, true)
	err := s.Commit(ctx, func(tx *Tx) error {
		_, err := tx.SaveTask(ctx, task.ID, tasks.TaskRow{Status: core.TaskDraft}, "", task.Version, at0) // stale
		return err
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale save: got %v, want ErrConflict", err)
	}
	err = s.Commit(ctx, func(tx *Tx) error {
		_, err := tx.SaveTask(ctx, "tsk_gone", tasks.TaskRow{Status: core.TaskDraft}, "", 0, at0)
		return err
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing task: got %v, want ErrNotFound", err)
	}
	// The stale write changed nothing.
	got, _ := s.GetTaskState(ctx, task.ID)
	if got.Row.Version != v || got.Task.State.Status() != core.TaskQueued {
		t.Fatalf("state after the stale write: %+v", got)
	}
}

func TestSaveTurnInsertTwiceConflicts(t *testing.T) {
	ctx := context.Background()
	s := NewStore(openTest(t), nil)
	p, r := seed(t, s)
	task, _ := s.CreateTask(ctx, newTask(p, r, nil))
	saveState(t, s, task.ID, tasks.Waiting{Turn: "trn_1", BlockedBy: tasks.BlockedNone{}}, task.Version, true)
	err := s.Commit(ctx, func(tx *Tx) error {
		return tx.SaveTurn(ctx, task.ID, tasks.TurnRow{ID: "trn_2", Seq: 1, Mode: core.ModeAsk, Prompt: "x", State: tasks.TurnWaiting}, true)
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("same (task, seq) twice: got %v, want ErrConflict", err)
	}
}

// TestLegacyRows: a Phase 2 queued or running row has no turn, does not load,
// and is marked interrupted in one transaction with its audit entry.
func TestLegacyRows(t *testing.T) {
	ctx := context.Background()
	s := NewStore(openTest(t), nil)
	p, r := seed(t, s)
	var ids []core.TaskID
	for _, st := range []core.TaskStatus{core.TaskQueued, core.TaskRunning, core.TaskDone} {
		nt := newTask(p, r, nil)
		nt.Status = st
		task, err := s.CreateTask(ctx, nt)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, task.ID)
	}
	if _, err := s.GetTaskState(ctx, ids[0]); err == nil {
		t.Fatal("a queued task with no turn loaded")
	}
	if _, err := s.ListActiveTaskStates(ctx); err == nil {
		t.Fatal("ListActiveTaskStates accepted a legacy row")
	}
	// The finished legacy row loads through its status.
	if got, err := s.GetTaskState(ctx, ids[2]); err != nil || got.Task.State.Status() != core.TaskDone {
		t.Fatalf("legacy done row: %v %+v", err, got)
	}
	err := s.Commit(ctx, func(tx *Tx) error {
		legacy, err := tx.ListLegacyLiveTasks(ctx)
		if err != nil {
			return err
		}
		if len(legacy) != 2 {
			t.Errorf("found %d legacy rows, want 2", len(legacy))
		}
		for _, l := range legacy {
			if err := tx.MarkLegacyInterrupted(ctx, l, at0); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids[:2] {
		got, err := s.GetTaskState(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		want := tasks.Idle{Outcome: tasks.Failed{Kind: tasks.FailInterrupted}, At: at0}
		if !reflect.DeepEqual(got.Task.State, want) {
			t.Errorf("legacy task: got %#v, want %#v", got.Task.State, want)
		}
	}
	// A stale version is a conflict, not a silent overwrite.
	err = s.Commit(ctx, func(tx *Tx) error {
		return tx.MarkLegacyInterrupted(ctx, LegacyTask{ID: ids[0], Version: 0}, at0)
	})
	if err == nil {
		t.Fatal("marking an already handled row succeeded")
	}
	if got, err := s.ListActiveTaskStates(ctx); err != nil || len(got) != 0 {
		t.Fatalf("active after marking: %v %v", got, err)
	}
}

func withTurn(st tasks.State, id tasks.TurnID) tasks.State {
	switch v := st.(type) {
	case tasks.Waiting:
		v.Turn = id
		return v
	case tasks.Running:
		v.Turn = id
		return v
	case tasks.Idle:
		v.Turn = id
		return v
	}
	return st
}

func TestListActiveTaskStates(t *testing.T) {
	n := 0
	ctx := context.Background()
	s := NewStore(openTest(t), nil)
	p, r := seed(t, s)
	mk := func(st tasks.State) core.TaskID {
		n++
		st = withTurn(st, tasks.TurnID("trn_"+string(rune('a'+n))))
		task, err := s.CreateTask(ctx, newTask(p, r, nil))
		if err != nil {
			t.Fatal(err)
		}
		_, draft := st.(tasks.Draft)
		saveState(t, s, task.ID, st, task.Version, !draft)
		return task.ID
	}
	mk(tasks.Draft{})
	w := mk(tasks.Waiting{Turn: "trn_1", BlockedBy: tasks.BlockedNone{}})
	ru := mk(tasks.Running{Turn: "trn_1", Attempt: 1, Since: at0})
	mk(tasks.Idle{Turn: "trn_1", Outcome: tasks.Answered{}, At: at0})
	got, err := s.ListActiveTaskStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Row.ID != w || got[1].Row.ID != ru {
		t.Fatalf("active = %v", got)
	}
}

func TestLoadTaskInTx(t *testing.T) {
	ctx := context.Background()
	s := NewStore(openTest(t), nil)
	p, r := seed(t, s)
	task, _ := s.CreateTask(ctx, newTask(p, r, nil))
	saveState(t, s, task.ID, tasks.Starting{Turn: "trn_1", Attempt: 1, Since: at0}, task.Version, true)
	err := s.Commit(ctx, func(tx *Tx) error {
		got, err := tx.LoadTask(ctx, task.ID)
		if err != nil {
			return err
		}
		if _, ok := got.Task.State.(tasks.Starting); !ok || got.Task.Mode != core.ModeCode || len(got.Row.RepoIDs) != 1 {
			t.Errorf("tx load: %+v", got)
		}
		_, err = tx.LoadTask(ctx, "tsk_missing")
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("missing: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRepoTrustAndEventSeq(t *testing.T) {
	ctx := context.Background()
	s := NewStore(openTest(t), nil)
	p, r := seed(t, s)
	hash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := s.Commit(ctx, func(tx *Tx) error { return tx.SetRepoTrust(ctx, r.ID, true, "abc") }); err == nil {
		t.Fatal("a short hash was accepted")
	}
	if err := s.Commit(ctx, func(tx *Tx) error { return tx.SetRepoTrust(ctx, r.ID, true, hash) }); err != nil {
		t.Fatal(err)
	}
	if tr, h, err := s.GetRepoTrust(ctx, r.ID); err != nil || !tr || h != hash {
		t.Fatalf("trust = %v %q %v", tr, h, err)
	}
	if err := s.Commit(ctx, func(tx *Tx) error { return tx.SetRepoTrust(ctx, r.ID, false, hash) }); err != nil {
		t.Fatal(err)
	}
	if tr, h, _ := s.GetRepoTrust(ctx, r.ID); tr || h != "" {
		t.Fatalf("revoked trust = %v %q", tr, h)
	}
	task, _ := s.CreateTask(ctx, newTask(p, r, nil))
	if n, err := s.GetMaxTaskEventSeq(ctx, task.ID); err != nil || n != 0 {
		t.Fatalf("max seq of no events = %d %v", n, err)
	}
	for _, seq := range []int64{1, 2, 7} {
		if err := s.AppendTaskEvent(ctx, TaskEvent{TaskID: task.ID, Seq: seq, Kind: "message", At: at0}); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := s.GetMaxTaskEventSeq(ctx, task.ID); n != 7 {
		t.Fatalf("max seq = %d, want 7", n)
	}
}
