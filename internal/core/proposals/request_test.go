package proposals

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

func reqItems(r *rand.Rand, n int) []RequestItem {
	out := make([]RequestItem, n)
	for i := range out {
		out[i] = RequestItem{
			Task: "tsk_one", Repo: "rep_one", Summary: fmt.Sprintf("summary %d %d", i, r.Intn(1000)),
			Action: Commit{Where: InWorktree{ID: "wt_one"}, Paths: []RelPath{RelPath(fmt.Sprintf("f%d.txt", i))},
				Message: fmt.Sprintf("feat: %d %d", i, r.Intn(1000)), ExpectHead: "h1", ExpectDiffHash: "d1"},
		}
	}
	return out
}

// An empty store creates every item under the derived IDs; the same items
// against what that created reuse the same IDs.
func TestPropPlanRequestIdempotent(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		req := core.RequestID(fmt.Sprintf("req_%d", r.Int63()))
		items := reqItems(r, 1+r.Intn(MaxRequestProposals))
		first, ok := PlanRequest(req, items, nil).(CreateAll)
		if !ok || len(first.IDs) != len(items) {
			t.Fatalf("first plan = %#v", first)
		}
		again, ok := PlanRequest(req, items, items).(ReuseAll)
		if !ok {
			t.Fatalf("retry plan = %#v", again)
		}
		for i := range items {
			if again.IDs[i] != first.IDs[i] || first.IDs[i] != RequestProposalID(req, i) {
				t.Fatalf("id %d changed: %s vs %s", i, first.IDs[i], again.IDs[i])
			}
		}
	})
}

// Any change to a stored list (one field of one item, an added or a removed
// item) is refused and never creates anything.
func TestPropPlanRequestChangedItemsRefused(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		req := core.RequestID(fmt.Sprintf("req_%d", r.Int63()))
		stored := reqItems(r, 2+r.Intn(5))
		items := append([]RequestItem(nil), stored...)
		switch r.Intn(5) {
		case 0:
			items[r.Intn(len(items))].Summary += "!"
		case 1:
			items[r.Intn(len(items))].Task = "tsk_two"
		case 2:
			c := items[0].Action.(Commit)
			c.Message += "!"
			items[0].Action = c
		case 3:
			items = items[:len(items)-1]
		case 4:
			items = append(items, reqItems(r, 1)[0])
		}
		if got, ok := PlanRequest(req, items, stored).(RefuseRequest); !ok || got.Reason != RequestChanged {
			t.Fatalf("plan = %#v, want changed", got)
		}
	})
}

// A pin the first call filled in does not make a retry look different, but a
// pin the caller sets to another value does.
func TestPlanRequestAdoptsPinsOnlyWhenEmpty(t *testing.T) {
	req := core.RequestID("req_pins")
	stored := RequestItem{Task: "tsk_one", Repo: "rep_one", Summary: "s", Action: Commit{
		Where: InWorktree{ID: "wt_one"}, Paths: []RelPath{"a.txt"}, Message: "m", ExpectHead: "h1", ExpectDiffHash: "d1"}}
	in := stored
	in.Action = Commit{Where: InWorktree{ID: "wt_one"}, Paths: []RelPath{"a.txt"}, Message: "m"}
	if _, ok := PlanRequest(req, []RequestItem{in}, []RequestItem{stored}).(ReuseAll); !ok {
		t.Fatal("an empty pin must match the stored one")
	}
	c := in.Action.(Commit)
	c.ExpectHead = "h2"
	in.Action = c
	if _, ok := PlanRequest(req, []RequestItem{in}, []RequestItem{stored}).(RefuseRequest); !ok {
		t.Fatal("a different pin must be refused")
	}
}

func TestPlanRequestRefusesBadInput(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	ok := reqItems(r, 1)
	cases := map[string]struct {
		req   core.RequestID
		items []RequestItem
		want  RequestRefusal
	}{
		"empty request id": {"", ok, RequestBad},
		"no items":         {"req_a", nil, RequestBad},
		"too many":         {"req_a", reqItems(r, MaxRequestProposals+1), RequestTooMany},
		"no action":        {"req_a", []RequestItem{{Task: "tsk_one", Repo: "rep_one"}}, RequestBad},
		"no task":          {"req_a", []RequestItem{{Repo: "rep_one", Action: ok[0].Action}}, RequestBad},
		"no repo":          {"req_a", []RequestItem{{Task: "tsk_one", Action: ok[0].Action}}, RequestBad},
	}
	for name, c := range cases {
		got, isRef := PlanRequest(c.req, c.items, nil).(RefuseRequest)
		if !isRef || got.Reason != c.want {
			t.Errorf("%s: plan = %#v, want %s", name, got, c.want)
		}
	}
}

func TestRequestProposalIDsAreDistinctAndValid(t *testing.T) {
	seen := map[core.ProposalID]bool{}
	for _, req := range []core.RequestID{"req_a", "req_b"} {
		for i := 0; i < MaxRequestProposals; i++ {
			id := RequestProposalID(req, i)
			if id.Validate() != nil || seen[id] {
				t.Fatalf("id %q invalid or repeated", id)
			}
			seen[id] = true
		}
	}
}
