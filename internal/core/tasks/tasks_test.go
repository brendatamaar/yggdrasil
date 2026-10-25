package tasks

import (
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

func TestOutcomeCodeRoundTrip(t *testing.T) {
	all := []Outcome{
		Answered{}, ReadyForReview{}, Cancelled{}, Dismissed{},
		NeedsYou{Why: NeedsIntegrityViolated}, NeedsYou{Why: NeedsCapExceeded},
		Failed{Kind: FailPrepare}, Failed{Kind: FailStart}, Failed{Kind: FailRun}, Failed{Kind: FailInterrupted},
	}
	seen := map[string]bool{}
	for _, o := range all {
		code := OutcomeCode(o)
		if seen[code] {
			t.Fatalf("duplicate code %q", code)
		}
		seen[code] = true
		got, err := ParseOutcomeCode(code)
		if err != nil || got != o {
			t.Errorf("ParseOutcomeCode(%q) = %#v, %v; want %#v", code, got, err, o)
		}
	}
	for _, bad := range []string{"", "needs_", "failed_x", "Answered", "needs_you"} {
		if _, err := ParseOutcomeCode(bad); err == nil {
			t.Errorf("ParseOutcomeCode(%q) accepted", bad)
		}
	}
}

func TestStatusOfStates(t *testing.T) {
	cases := []struct {
		s    State
		want core.TaskStatus
	}{
		{Draft{}, core.TaskDraft},
		{Waiting{}, core.TaskQueued},
		{Starting{}, core.TaskRunning},
		{Running{}, core.TaskRunning},
		{Stopping{}, core.TaskRunning},
		{Idle{Outcome: Answered{}}, core.TaskDone},
		{Idle{Outcome: ReadyForReview{}}, core.TaskReview},
		{Idle{Outcome: NeedsYou{Why: NeedsCapExceeded}}, core.TaskNeedsYou},
		{Idle{Outcome: Failed{Kind: FailRun}}, core.TaskFailed},
		{Idle{Outcome: Cancelled{}}, core.TaskCancelled},
		{Idle{Outcome: Dismissed{}}, core.TaskDone},
	}
	for _, c := range cases {
		if got := c.s.Status(); got != c.want {
			t.Errorf("%#v.Status() = %q, want %q", c.s, got, c.want)
		}
	}
}

func TestParseMax(t *testing.T) {
	for _, ok := range []string{"1", "2", "16"} {
		n, err := ParseMax(ok)
		if err != nil || FormatMax(n) != ok {
			t.Errorf("ParseMax(%q) = %d, %v", ok, n, err)
		}
	}
	for _, bad := range []string{"", "0", "17", "-1", "x", "1.5", " 2"} {
		if _, err := ParseMax(bad); err == nil {
			t.Errorf("ParseMax(%q) accepted", bad)
		}
	}
}

func TestRoomHintTable(t *testing.T) {
	cases := []struct {
		cpus, mem, max, active, want int
	}{
		{16, 16384, 2, 2, 3},
		{16, 16384, 2, 1, 0}, // a slot is free
		{8, 16384, 2, 2, 0},  // 8 < 4*3
		{16, 4096, 2, 2, 0},  // 4096 < 2048*3
		{64, 65536, 8, 8, 0}, // ceiling
		{64, 65536, 7, 7, 8}, // up to the ceiling
		{64, 65536, 0, 0, 0}, // invalid max
	}
	for _, c := range cases {
		if got := RoomHint(c.cpus, c.mem, c.max, c.active).RaiseTo; got != c.want {
			t.Errorf("RoomHint(%d,%d,%d,%d) = %d, want %d", c.cpus, c.mem, c.max, c.active, got, c.want)
		}
	}
}

func TestStubsChangeNothing(t *testing.T) {
	task := Task{ID: "t1", State: Draft{}}
	got, cmds := Step(task, Message{})
	if got.State != task.State || cmds != nil {
		t.Fatalf("Step stub changed something: %#v %v", got, cmds)
	}
	b := Board{Max: 2}
	gb, bc := Schedule(b, Wake{})
	if gb.Max != 2 || bc != nil {
		t.Fatalf("Schedule stub changed something")
	}
}
