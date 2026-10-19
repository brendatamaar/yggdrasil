package repos

import (
	"testing"
	"time"
)

func readStatuses(cmds []RefreshCommand) []ReadStatus {
	var out []ReadStatus
	for _, c := range cmds {
		if rs, ok := c.(ReadStatus); ok {
			out = append(out, rs)
		}
	}
	return out
}

// After a restart (the shell rebuilds the state after a panic) a read that was
// in flight survives as an orphan: it keeps its slot, its result is ignored,
// and the next attempt is greater than every attempt handed out before.
func TestRestartedKeepsReadAsOrphan(t *testing.T) {
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	active := []RepoRef{{ID: "rep_a", Path: "/work/a"}}
	s, cmds := Refresh(NewRefresher(2), ReposChanged{Active: active})
	started := readStatuses(cmds)
	if len(started) != 1 {
		t.Fatalf("adding a repo started %d reads, want 1", len(started))
	}
	old := started[0].Attempt

	r := s.Restarted()
	if r.Seq != s.Seq || r.Seq < old {
		t.Errorf("Seq after restart = %d, want %d (never reset)", r.Seq, s.Seq)
	}
	if len(r.InFlight) != 0 || r.Orphans["rep_a"] != old {
		t.Fatalf("restart state: InFlight %v, Orphans %v; want the read as an orphan of attempt %d", r.InFlight, r.Orphans, old)
	}

	// The same repo comes back; its old read is still running, so no new read.
	r, cmds = Refresh(r, ReposChanged{Active: active})
	if got := readStatuses(cmds); len(got) != 0 {
		t.Fatalf("started %v while the orphan still runs", got)
	}
	if r.reads() != 1 {
		t.Errorf("reads = %d, want the orphan counted", r.reads())
	}

	// The old result frees the slot, is not applied, and the repo is read again.
	r, cmds = Refresh(r, StatusRead{ID: "rep_a", Attempt: old, Snap: Snapshot{Branch: "main"}, Now: now})
	if _, ok := r.Entries["rep_a"].View.(Fresh); ok {
		t.Error("the orphan's result was applied")
	}
	next := readStatuses(cmds)
	if len(next) != 1 || next[0].Attempt <= old {
		t.Fatalf("after the orphan ended: %v, want one read with an attempt greater than %d", next, old)
	}
}
