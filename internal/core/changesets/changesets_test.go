package changesets

import (
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

func openSet() Open {
	return Open{
		Set:     Header{ID: "cs1", Task: "t1", Repos: []core.RepoID{"a", "b"}, Order: []core.RepoID{"b", "a"}},
		Members: []Member{{ID: "p1", Repo: "a", Kind: core.ProposalCommit, Status: core.ProposalPending, Hash: strings.Repeat("0", 64)}},
		LastRun: RunIdle{},
	}
}

func TestOpenValidate(t *testing.T) {
	s := openSet()
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := openSet()
	bad.Set.Order = []core.RepoID{"a", "a"}
	if bad.Validate() == nil {
		t.Error("order that is not a permutation accepted")
	}
	bad = openSet()
	bad.Members[0].Repo = "zzz"
	if bad.Validate() == nil {
		t.Error("member outside the set accepted")
	}
	bad = openSet()
	bad.LastRun = nil
	if bad.Validate() == nil {
		t.Error("nil last run accepted")
	}
}

func TestStatusMapping(t *testing.T) {
	o := openSet()
	cases := map[core.ChangeSetStatus]State{
		core.ChangeSetOpen:    Confirming{Open: o, Scope: ScopeAll{}, Current: "p1"},
		core.ChangeSetShipped: Shipped{o},
		core.ChangeSetClosed:  Closed{o},
	}
	for want, s := range cases {
		if s.Status() != want {
			t.Errorf("%T: status %s, want %s", s, s.Status(), want)
		}
	}
}

func TestLinkMarker(t *testing.T) {
	if LinkMarker != "<!-- ygg:links -->" {
		t.Fatal("marker changed")
	}
}
