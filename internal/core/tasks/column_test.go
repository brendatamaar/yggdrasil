package tasks

import (
	"math/rand"
	"testing"
)

func TestColumnTable(t *testing.T) {
	cases := []struct {
		name    string
		s       State
		pending int
		want    BoardColumn
	}{
		{"draft", Draft{}, 0, ColumnDone},
		{"draft with pending", Draft{}, 2, ColumnDone},
		{"waiting", Waiting{}, 0, ColumnWorking},
		{"starting", Starting{}, 0, ColumnWorking},
		{"running", Running{}, 0, ColumnWorking},
		{"running with pending stays working", Running{}, 3, ColumnWorking},
		{"stopping", Stopping{}, 1, ColumnWorking},
		{"needs you", Idle{Outcome: NeedsYou{Why: NeedsCapExceeded}}, 0, ColumnNeedsYou},
		{"needs you with pending", Idle{Outcome: NeedsYou{Why: NeedsIntegrityViolated}}, 2, ColumnNeedsYou},
		{"review", Idle{Outcome: ReadyForReview{}}, 0, ColumnReview},
		{"review with pending proposals", Idle{Outcome: ReadyForReview{}}, 1, ColumnNeedsYou},
		{"answered", Idle{Outcome: Answered{}}, 0, ColumnDone},
		{"answered with pending", Idle{Outcome: Answered{}}, 1, ColumnNeedsYou},
		{"dismissed", Idle{Outcome: Dismissed{}}, 0, ColumnDone},
		{"failed", Idle{Outcome: Failed{Kind: FailRun}}, 0, ColumnDone},
		{"failed with pending", Idle{Outcome: Failed{Kind: FailRun}}, 4, ColumnNeedsYou},
		{"cancelled", Idle{Outcome: Cancelled{}}, 0, ColumnDone},
		{"negative pending counts as none", Idle{Outcome: ReadyForReview{}}, -1, ColumnReview},
	}
	for _, c := range cases {
		if got := Column(c.s, c.pending); got != c.want {
			t.Errorf("%s: Column = %q, want %q", c.name, got, c.want)
		}
	}
}

// K14: Working iff a run could be live; Column is a function of its inputs.
func TestPropColumnWorkingIffLive(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		states := []State{Draft{}, Waiting{}, Starting{}, Running{}, Stopping{},
			Idle{Outcome: Answered{}}, Idle{Outcome: ReadyForReview{}}, Idle{Outcome: NeedsYou{Why: NeedsCapExceeded}},
			Idle{Outcome: Failed{Kind: FailStart}}, Idle{Outcome: Cancelled{}}, Idle{Outcome: Dismissed{}}}
		s, n := states[r.Intn(len(states))], r.Intn(4)
		c := Column(s, n)
		if (c == ColumnWorking) != liveState(s) {
			t.Fatalf("Column(%#v, %d) = %q", s, n, c)
		}
		if Column(s, n) != c {
			t.Fatal("Column is not deterministic")
		}
		if i, idle := s.(Idle); idle {
			_, needs := i.Outcome.(NeedsYou)
			if (c == ColumnNeedsYou) != (needs || n > 0) {
				t.Fatalf("NeedsYou iff NeedsYou outcome or pending proposals: %#v %d -> %q", s, n, c)
			}
		}
	})
}
