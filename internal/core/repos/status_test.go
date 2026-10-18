package repos

import (
	"reflect"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

var t0 = time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

func fresh(s Snapshot) View { return Fresh{Snap: s, At: t0} }

func TestFlags(t *testing.T) {
	cases := []struct {
		name string
		view View
		want []Flag
	}{
		{"unknown", Unknown{}, nil},
		{"clean", fresh(Snapshot{Branch: "main", HasUpstream: true}), nil},
		{"detached and no upstream", fresh(Snapshot{Detached: true}), []Flag{FlagDetached, FlagNoUpstream}},
		{"conflicts", fresh(Snapshot{HasUpstream: true, Conflicted: 2}), []Flag{FlagConflicts}},
		{"diverged", fresh(Snapshot{HasUpstream: true, Ahead: 1, Behind: 1}), []Flag{FlagDiverged}},
		{"only ahead", fresh(Snapshot{HasUpstream: true, Ahead: 3}), nil},
		{"missing", Problem{Kind: ProblemMissing, At: t0}, []Flag{FlagMissing}},
		{"not a repo", Problem{Kind: ProblemNotARepo}, []Flag{FlagNotARepo}},
		{"timed out keeps numbers", Problem{Kind: ProblemTimedOut, Last: &Snapshot{Conflicted: 1}}, []Flag{FlagTimedOut}},
		{"git failed", Problem{Kind: ProblemGitFailed}, []Flag{FlagGitFailed}},
	}
	for _, c := range cases {
		if got := Flags(c.view); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: Flags = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestFilterMatches(t *testing.T) {
	dirty := fresh(Snapshot{HasUpstream: true, Untracked: 1})
	behind := fresh(Snapshot{HasUpstream: true, Behind: 2})
	ahead := fresh(Snapshot{HasUpstream: true, Ahead: 2})
	clean := fresh(Snapshot{HasUpstream: true})
	stale := Problem{Kind: ProblemTimedOut, Last: &Snapshot{HasUpstream: true, Behind: 1, Staged: 1}}
	cases := []struct {
		f      Filter
		v      View
		failed bool
		want   bool
	}{
		{FilterAll, Unknown{}, false, true},
		{FilterUncommitted, dirty, false, true},
		{FilterUncommitted, clean, false, false},
		{FilterUncommitted, Unknown{}, false, false},
		{FilterUncommitted, stale, false, true},
		{FilterBehind, behind, false, true},
		{FilterBehind, ahead, false, false},
		{FilterBehind, stale, false, true},
		{FilterAhead, ahead, false, true},
		{FilterAhead, behind, false, false},
		{FilterFailedPipeline, clean, true, true},
		{FilterFailedPipeline, clean, false, false},
		{FilterProblems, clean, false, false},
		{FilterProblems, stale, false, true},
		{FilterProblems, fresh(Snapshot{}), false, true}, // no upstream
		{Filter("nope"), clean, true, false},
	}
	for _, c := range cases {
		if got := c.f.Matches(c.v, c.failed); got != c.want {
			t.Errorf("%s.Matches(%#v, %v) = %v, want %v", c.f, c.v, c.failed, got, c.want)
		}
	}
}

func TestCountEntriesPerProject(t *testing.T) {
	entries := map[core.RepoID]Entry{
		"a": {View: fresh(Snapshot{HasUpstream: true, Ahead: 1})},
		"b": {View: fresh(Snapshot{HasUpstream: true, Behind: 1, Staged: 1})},
		"c": {View: Unknown{}},
		"d": {View: Problem{Kind: ProblemMissing}},
	}
	got := CountEntries(entries, map[core.RepoID]struct{}{"a": {}, "gone": {}})
	want := Counts{All: 4, Uncommitted: 1, Behind: 1, Ahead: 1, FailedPipeline: 1, Problems: 1}
	if got != want {
		t.Errorf("CountEntries = %+v, want %+v", got, want)
	}
	one := CountEntries(map[core.RepoID]Entry{"b": entries["b"]}, nil)
	if (one != Counts{All: 1, Uncommitted: 1, Behind: 1}) {
		t.Errorf("per-project counts = %+v", one)
	}
}

func refs(ids ...string) ReposChanged {
	var r ReposChanged
	for _, id := range ids {
		r.Active = append(r.Active, RepoRef{ID: core.RepoID(id), Path: CleanPath("/work/" + id)})
	}
	return r
}

func reads(cmds []RefreshCommand) []ReadStatus {
	var out []ReadStatus
	for _, c := range cmds {
		if r, ok := c.(ReadStatus); ok {
			out = append(out, r)
		}
	}
	return out
}

func TestRefreshStartup(t *testing.T) {
	r, cmds := Refresh(NewRefresher(2), refs("a", "b", "c"))
	if got := reads(cmds); len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" || got[0].Attempt != 1 {
		t.Fatalf("reads = %+v", got)
	}
	if !reflect.DeepEqual(r.Queue, []core.RepoID{"c"}) || len(r.InFlight) != 2 {
		t.Fatalf("queue %v in flight %v", r.Queue, r.InFlight)
	}
	last := cmds[len(cmds)-1]
	if _, ok := last.(PublishCounts); !ok {
		t.Errorf("last command = %#v, want PublishCounts (three repos appeared)", last)
	}
	// A result frees a slot, which takes the waiting repo.
	r, cmds = Refresh(r, StatusRead{ID: "a", Attempt: 1, Snap: Snapshot{Branch: "main"}, Now: t0})
	if got := reads(cmds); len(got) != 1 || got[0].ID != "c" {
		t.Fatalf("after a result, reads = %+v", got)
	}
	if _, ok := r.Entries["a"].View.(Fresh); !ok {
		t.Errorf("a = %#v, want Fresh", r.Entries["a"].View)
	}
}

func TestRefreshReasons(t *testing.T) {
	r, _ := Refresh(NewRefresher(8), refs("a", "b"))
	for _, id := range []core.RepoID{"a", "b"} {
		r, _ = Refresh(r, StatusRead{ID: id, Attempt: 1, Snap: Snapshot{HasUpstream: true}, Now: t0})
	}
	ids := func(c []RefreshCommand) []core.RepoID {
		var out []core.RepoID
		for _, rs := range reads(c) {
			out = append(out, rs.ID)
		}
		return out
	}
	// Focus 10 s later: too young. Tick 1 minute later: too young for a tick.
	if _, cmds := Refresh(r, RefreshRequested{Reason: ReasonFocus, Now: t0.Add(10 * time.Second)}); len(cmds) != 0 {
		t.Errorf("young focus refresh emitted %v", cmds)
	}
	if _, cmds := Refresh(r, Tick{Now: t0.Add(time.Minute)}); len(cmds) != 0 {
		t.Errorf("young tick emitted %v", cmds)
	}
	// Focus 20 s later reads; a tick at 20 s does not.
	if _, cmds := Refresh(r, RefreshRequested{Reason: ReasonFocus, Now: t0.Add(20 * time.Second)}); len(ids(cmds)) != 2 {
		t.Errorf("focus after MinAgeFocus read %v", ids(cmds))
	}
	if _, cmds := Refresh(r, Tick{Now: t0.Add(20 * time.Second)}); len(cmds) != 0 {
		t.Errorf("tick after 20 s emitted %v", cmds)
	}
	if _, cmds := Refresh(r, Tick{Now: t0.Add(3 * time.Minute)}); len(ids(cmds)) != 2 {
		t.Errorf("tick after MinAgeTick read %v", ids(cmds))
	}
	// A user request has no minimum age and an unknown ID is ignored.
	_, cmds := Refresh(r, RefreshRequested{IDs: []core.RepoID{"b", "zzz"}, Reason: ReasonUser, Now: t0})
	if got := ids(cmds); !reflect.DeepEqual(got, []core.RepoID{"b"}) {
		t.Errorf("user refresh of b read %v", got)
	}
}

func TestRefreshUserGoesToFront(t *testing.T) {
	r, _ := Refresh(NewRefresher(1), refs("a", "b", "c"))
	r, _ = Refresh(r, RefreshRequested{IDs: []core.RepoID{"c"}, Reason: ReasonUser, Now: t0})
	if !reflect.DeepEqual(r.Queue, []core.RepoID{"c", "b"}) {
		t.Errorf("queue = %v, want c before b", r.Queue)
	}
	r, _ = Refresh(r, RefreshRequested{IDs: []core.RepoID{"a"}, Reason: ReasonBuild, Now: t0}) // a is in flight, build goes to the back
	if !reflect.DeepEqual(r.Queue, []core.RepoID{"c", "b", "a"}) {
		t.Errorf("queue = %v, want a at the back", r.Queue)
	}
}

func TestRefreshRemovedAndMovedRepos(t *testing.T) {
	r, _ := Refresh(NewRefresher(4), refs("a", "b"))
	// b is untracked while it is being read: its entry and slot go, and its late result is ignored.
	r, cmds := Refresh(r, refs("a"))
	if _, ok := r.Entries["b"]; ok || len(r.InFlight) != 1 {
		t.Fatalf("b not dropped: %v %v", r.Entries, r.InFlight)
	}
	if len(cmds) != 2 {
		t.Errorf("cmds = %#v, want PublishStatus(b) and PublishCounts", cmds)
	}
	same, out := Refresh(r, StatusRead{ID: "b", Attempt: 1, Snap: Snapshot{}, Now: t0})
	if len(out) != 0 || !reflect.DeepEqual(same, r) {
		t.Errorf("late result for a removed repo changed something: %v", out)
	}
	// a moves: the read in flight goes stale and a new attempt starts on the new path.
	moved := ReposChanged{Active: []RepoRef{{ID: "a", Path: "/elsewhere/a"}}}
	r, cmds = Refresh(r, moved)
	got := reads(cmds)
	if len(got) != 1 || got[0].Attempt != 2 || got[0].Path != "/elsewhere/a" {
		t.Fatalf("reads after a move = %+v", got)
	}
	if same, out := Refresh(r, StatusRead{ID: "a", Attempt: 1, Now: t0}); len(out) != 0 || !reflect.DeepEqual(same, r) {
		t.Errorf("the old attempt was accepted")
	}
}

func TestRefreshNoPublishWhenNothingChanged(t *testing.T) {
	r, _ := Refresh(NewRefresher(4), refs("a"))
	snap := Snapshot{Branch: "main", HasUpstream: true, LastCommit: &CommitRef{OID: "abc", At: t0}}
	r, cmds := Refresh(r, StatusRead{ID: "a", Attempt: 1, Snap: snap, Now: t0})
	if !hasPublish(cmds, "a") {
		t.Fatalf("first result not published: %v", cmds)
	}
	r, _ = Refresh(r, RefreshRequested{IDs: []core.RepoID{"a"}, Reason: ReasonUser, Now: t0})
	again := snap
	again.LastCommit = &CommitRef{OID: "abc", At: t0}
	_, cmds = Refresh(r, StatusRead{ID: "a", Attempt: 2, Snap: again, Now: t0.Add(time.Hour)})
	if len(cmds) != 0 {
		t.Errorf("an identical re-read emitted %v", cmds)
	}
}

func hasPublish(cmds []RefreshCommand, id core.RepoID) bool {
	for _, c := range cmds {
		if p, ok := c.(PublishStatus); ok && p.ID == id {
			return true
		}
	}
	return false
}
