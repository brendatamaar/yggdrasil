package repos_test

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/repos"
)

var at = time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC)

// validSnapshot is the invariant SnapshotFrom promises.
func validSnapshot(s corerepos.Snapshot) error {
	for name, n := range map[string]int{"ahead": s.Ahead, "behind": s.Behind, "staged": s.Staged,
		"unstaged": s.Unstaged, "untracked": s.Untracked, "conflicted": s.Conflicted} {
		if n < 0 {
			return fmt.Errorf("negative %s", name)
		}
	}
	if s.Detached && s.Branch != "" {
		return fmt.Errorf("detached with branch %q", s.Branch)
	}
	return nil
}

// gitOf is the inverse of SnapshotFrom, for the round trip.
func gitOf(s corerepos.Snapshot) (git.Status, *git.Commit) {
	st := git.Status{Branch: s.Branch, Detached: s.Detached, Upstream: s.Upstream, HasUpstream: s.HasUpstream,
		Ahead: s.Ahead, Behind: s.Behind, Staged: s.Staged, Unstaged: s.Unstaged, Untracked: s.Untracked, Conflicted: s.Conflicted}
	if s.LastCommit == nil {
		return st, nil
	}
	st.OID = s.LastCommit.OID
	return st, &git.Commit{Hash: s.LastCommit.OID, Subject: s.LastCommit.Subject, Author: s.LastCommit.Author, Date: s.LastCommit.At}
}

func randomSnapshot(rng *rand.Rand) corerepos.Snapshot {
	s := corerepos.Snapshot{
		Branch: "feature/x", HasUpstream: rng.IntN(2) == 0, Ahead: rng.IntN(5), Behind: rng.IntN(5),
		Staged: rng.IntN(5), Unstaged: rng.IntN(5), Untracked: rng.IntN(5), Conflicted: rng.IntN(2),
	}
	if s.HasUpstream {
		s.Upstream = "origin/feature/x"
	}
	if rng.IntN(4) == 0 {
		s.Branch, s.Detached = "", true
	}
	if rng.IntN(2) == 0 {
		s.LastCommit = &corerepos.CommitRef{OID: "0123abc", Subject: "fix: a thing", Author: "Ada Test", At: at}
	}
	return s
}

func TestSnapshotFromRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 11))
	for range 200 {
		want := randomSnapshot(rng)
		st, c := gitOf(want)
		got, err := repos.SnapshotFrom(st, c)
		if err != nil {
			t.Fatalf("SnapshotFrom(%+v): %v", want, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("round trip: got %+v, want %+v", got, want)
		}
		if err := validSnapshot(got); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSnapshotFromRejects(t *testing.T) {
	cases := map[string]git.Status{
		"negative ahead":      {Ahead: -1},
		"negative behind":     {Behind: -1},
		"negative staged":     {Staged: -1},
		"negative unstaged":   {Unstaged: -1},
		"negative untracked":  {Untracked: -1},
		"negative conflicted": {Conflicted: -1},
		"detached on branch":  {Detached: true, Branch: "main"},
	}
	for name, st := range cases {
		if got, err := repos.SnapshotFrom(st, nil); err == nil {
			t.Errorf("%s: accepted %+v", name, got)
		}
	}
	if _, err := repos.SnapshotFrom(git.Status{}, nil); err != nil {
		t.Errorf("an empty status was rejected: %v", err)
	}
}

func FuzzSnapshotFrom(f *testing.F) {
	f.Add("main", false, 0, 0, 0, 0, 0, 0)
	f.Add("", true, -1, 2, 3, 4, 5, 6)
	f.Add("main", true, 1, 1, 1, 1, 1, 1)
	f.Fuzz(func(t *testing.T, branch string, detached bool, ahead, behind, staged, unstaged, untracked, conflicted int) {
		st := git.Status{Branch: branch, Detached: detached, Ahead: ahead, Behind: behind,
			Staged: staged, Unstaged: unstaged, Untracked: untracked, Conflicted: conflicted}
		got, err := repos.SnapshotFrom(st, nil)
		if err == nil {
			if verr := validSnapshot(got); verr != nil {
				t.Fatalf("accepted %+v but it is invalid: %v", st, verr)
			}
		}
	})
}

func TestParseRefreshRoundTripAndDefaults(t *testing.T) {
	for _, r := range []corerepos.RefreshReason{corerepos.ReasonUser, corerepos.ReasonFocus, corerepos.ReasonGitAction,
		corerepos.ReasonBuild, corerepos.ReasonTracking, corerepos.ReasonTick} {
		got, err := repos.ParseRefresh([]string{"rep_1", "rep_2"}, string(r), at)
		if err != nil {
			t.Fatalf("reason %s: %v", r, err)
		}
		if got.Reason != r || !got.Now.Equal(at) || len(got.IDs) != 2 || got.IDs[0] != "rep_1" || got.IDs[1] != "rep_2" {
			t.Errorf("reason %s: got %+v", r, got)
		}
	}
	got, err := repos.ParseRefresh(nil, "", at)
	if err != nil || got.Reason != corerepos.ReasonUser || len(got.IDs) != 0 {
		t.Errorf("empty request = %+v, %v; want user, all repos", got, err)
	}
	got, err = repos.ParseRefresh([]string{"rep_1", "rep_2", "rep_1"}, "build", at)
	if err != nil || len(got.IDs) != 2 {
		t.Errorf("duplicates = %+v, %v; want each ID once", got, err)
	}
}

func TestParseRefreshRejects(t *testing.T) {
	many := make([]string, repos.MaxRefreshIDs+1)
	for i := range many {
		many[i] = fmt.Sprintf("rep_%d", i)
	}
	cases := map[string]struct {
		ids    []string
		reason string
	}{
		"unknown reason":     {nil, "whenever"},
		"upper case reason":  {nil, "USER"},
		"too many ids":       {many, "user"},
		"empty id":           {[]string{""}, "user"},
		"id with a space":    {[]string{"rep 1"}, "user"},
		"id with a slash":    {[]string{"rep/1"}, "user"},
		"id with a newline":  {[]string{"rep_1\n"}, "user"},
		"one bad among good": {[]string{"rep_1", "", "rep_3"}, "user"},
	}
	for name, c := range cases {
		_, err := repos.ParseRefresh(c.ids, c.reason, at)
		if class, ok := apperr.ClassOf(err); !ok || class != apperr.Invalid {
			t.Errorf("%s: err = %v, want an Invalid error", name, err)
		}
	}
	if _, err := repos.ParseRefresh(many[:repos.MaxRefreshIDs], "user", at); err != nil {
		t.Errorf("exactly %d ids were rejected: %v", repos.MaxRefreshIDs, err)
	}
}

func FuzzParseRefresh(f *testing.F) {
	f.Add("rep_1", "user")
	f.Add("", "")
	f.Add("a b", "tick")
	f.Fuzz(func(t *testing.T, id, reason string) {
		got, err := repos.ParseRefresh([]string{id}, reason, at)
		if err != nil {
			return
		}
		if got.Reason.Validate() != nil {
			t.Fatalf("accepted reason %q", got.Reason)
		}
		for _, x := range got.IDs {
			if x.Validate() != nil {
				t.Fatalf("accepted id %q", x)
			}
		}
	})
}

func TestParseFilter(t *testing.T) {
	for _, f := range []corerepos.Filter{corerepos.FilterAll, corerepos.FilterUncommitted, corerepos.FilterBehind,
		corerepos.FilterAhead, corerepos.FilterFailedPipeline, corerepos.FilterProblems} {
		if got, err := repos.ParseFilter(string(f)); err != nil || got != f {
			t.Errorf("ParseFilter(%q) = %q, %v", f, got, err)
		}
	}
	if got, err := repos.ParseFilter(""); err != nil || got != corerepos.FilterAll {
		t.Errorf("empty filter = %q, %v; want all", got, err)
	}
	for _, bad := range []string{"everything", "ALL", " all", "all ", "failed-pipeline"} {
		_, err := repos.ParseFilter(bad)
		if class, ok := apperr.ClassOf(err); !ok || class != apperr.Invalid {
			t.Errorf("ParseFilter(%q) err = %v, want Invalid", bad, err)
		}
	}
}

func FuzzParseFilter(f *testing.F) {
	f.Add("all")
	f.Add("")
	f.Add("problems\x00")
	f.Fuzz(func(t *testing.T, s string) {
		got, err := repos.ParseFilter(s)
		if err == nil && got.Validate() != nil {
			t.Fatalf("accepted %q as invalid filter %q", s, got)
		}
		if err == nil && s != "" && string(got) != s {
			t.Fatalf("ParseFilter(%q) = %q", s, got)
		}
	})
}
