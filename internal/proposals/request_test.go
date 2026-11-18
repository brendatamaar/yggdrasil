package proposals

import (
	"errors"
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
)

// reqItem writes file f<i> in the worktree and returns a Commit proposal for
// it with its pins left empty, as the action service sends it.
func (w *world) reqItem(i int, summary string) RequestProposal {
	w.t.Helper()
	file := fmt.Sprintf("f%d.txt", i)
	w.git.Write(w.wtDir, file, "content of "+file+"\n")
	kind, payload := coreprop.RowOf(coreprop.Commit{
		Where: coreprop.InWorktree{ID: wtID}, Paths: []coreprop.RelPath{coreprop.RelPath(file)}, Message: fmt.Sprintf("feat: change %d", i),
	})
	return RequestProposal{Task: w.task.ID, Repo: w.repo.ID, Summary: summary, Kind: kind, Payload: payload}
}

func (w *world) reqItems(n int) []RequestProposal {
	out := make([]RequestProposal, n)
	for i := range out {
		out[i] = w.reqItem(i, fmt.Sprintf("because %d", i))
	}
	return out
}

func (w *world) createFor(req core.RequestID, items []RequestProposal) []core.ProposalID {
	w.t.Helper()
	ids, err := w.svc.CreateForRequest(w.ctx, req, items)
	if err != nil {
		w.t.Fatalf("CreateForRequest: %v", err)
	}
	return ids
}

func (w *world) taskProposals() []View {
	w.t.Helper()
	vs, err := w.svc.List(w.ctx, ListFilter{Task: w.task.ID})
	if err != nil {
		w.t.Fatal(err)
	}
	return vs
}

func (w *world) find(req core.RequestID) []core.ProposalID {
	w.t.Helper()
	ids, err := w.svc.FindForRequest(w.ctx, req)
	if err != nil {
		w.t.Fatal(err)
	}
	return ids
}

func countOf(w *world, action string) int {
	w.t.Helper()
	n := 0
	for _, v := range w.taskProposals() {
		n += strings.Count(strings.Join(w.auditActions(v.ID), ","), action)
	}
	return n
}

func forSeedsWorld(t *testing.T, seeds int, f func(t *testing.T, w *world, r *rand.Rand)) {
	t.Helper()
	for seed := int64(1); seed <= int64(seeds); seed++ {
		ok := t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) { f(t, newWorld(t), rand.New(rand.NewSource(seed))) })
		if !ok {
			return
		}
	}
}

// A retried call (any number of times, with the service restarted in between
// and the branch tip moved) returns the same IDs and creates nothing twice.
func TestPropCreateForRequestIdempotent(t *testing.T) {
	forSeedsWorld(t, 25, func(t *testing.T, w *world, r *rand.Rand) {
		req := core.RequestID(fmt.Sprintf("req_%d", r.Int63()))
		n := 1 + r.Intn(5)
		first := w.createFor(req, w.reqItems(n))
		if len(first) != n {
			t.Fatalf("ids = %v, want %d", first, n)
		}
		for range 1 + r.Intn(3) {
			if r.Intn(2) == 0 {
				w.start() // a restart: a new service on the same database
			}
			if r.Intn(2) == 0 { // the branch moves; the retry must still be the same request
				f := fmt.Sprintf("moved%d.txt", r.Intn(1000))
				w.git.Write(w.wtDir, f, "x\n")
				if _, err := w.git.Commit(w.ctx, w.wtDir, []string{f}, "chore: move", w.head(w.wtDir)); err != nil {
					t.Fatal(err)
				}
			}
			again := w.createFor(req, w.reqItems(n))
			if !slices.Equal(again, first) {
				t.Fatalf("retry ids = %v, first = %v", again, first)
			}
		}
		if got := w.taskProposals(); len(got) != n {
			t.Fatalf("%d proposals stored, want %d", len(got), n)
		}
		if got := w.find(req); !slices.Equal(got, first) {
			t.Fatalf("FindForRequest = %v, want %v", got, first)
		}
		if got := countOf(w, "proposal.create"); got != n {
			t.Fatalf("%d create audit entries, want %d", got, n)
		}
	})
}

// A changed list under the same RequestID is refused and changes nothing.
func TestPropChangedItemsRefused(t *testing.T) {
	forSeedsWorld(t, 25, func(t *testing.T, w *world, r *rand.Rand) {
		req := core.RequestID(fmt.Sprintf("req_%d", r.Int63()))
		n := 2 + r.Intn(4)
		first := w.createFor(req, w.reqItems(n))
		items := w.reqItems(n)
		switch r.Intn(4) {
		case 0:
			items[r.Intn(n)].Summary += " changed"
		case 1:
			items[0] = w.reqItem(100+r.Intn(50), "other file")
		case 2:
			items = items[:n-1]
		case 3:
			items = append(items, w.reqItem(200, "extra"))
		}
		_, err := w.svc.CreateForRequest(w.ctx, req, items)
		var changed *RequestChangedError
		if !errors.Is(err, ErrRequestChanged) || !errors.As(err, &changed) || changed.UserMessage() == "" {
			t.Fatalf("err = %v, want ErrRequestChanged", err)
		}
		if got := w.taskProposals(); len(got) != n {
			t.Fatalf("%d proposals after a refused retry, want %d", len(got), n)
		}
		if got := w.find(req); !slices.Equal(got, first) {
			t.Fatalf("FindForRequest = %v, want %v", got, first)
		}
	})
}

// Creation only stores pending proposals: nothing is confirmed, no executor
// ran (invariant 2).
func TestPropNeverExecutes(t *testing.T) {
	forSeedsWorld(t, 15, func(t *testing.T, w *world, r *rand.Rand) {
		items := w.reqItems(1 + r.Intn(5))
		kind, payload := coreprop.RowOf(coreprop.Install{Repo: w.repo.ID, Step: "install"})
		items = append(items, RequestProposal{Task: w.task.ID, Repo: w.repo.ID, Summary: "deps", Kind: kind, Payload: payload})
		ids := w.createFor(core.RequestID(fmt.Sprintf("req_%d", r.Int63())), items)
		for _, id := range ids {
			v, err := w.svc.Get(w.ctx, id)
			if err != nil || v.Status != core.ProposalPending || v.Attempt != 0 || v.DecidedAt != nil {
				t.Fatalf("proposal %s = %+v, %v: want pending and untouched", id, v, err)
			}
		}
		if w.git.commitCount() != 0 || len(w.git.pushes) != 0 || len(w.git.adds) != 0 {
			t.Fatal("a git executor ran")
		}
		if len(w.builds.owners) != 0 || len(w.builds.steps) != 0 {
			t.Fatal("an install build was queued")
		}
		for _, e := range w.pub.events {
			if e.Status != core.ProposalPending {
				t.Fatalf("event %+v: want pending", e)
			}
		}
		for _, v := range w.taskProposals() {
			for _, a := range w.auditActions(v.ID) {
				if a != "proposal.create" {
					t.Fatalf("audit %q: creation must only write proposal.create", a)
				}
			}
		}
	})
}

// An untracked, reserved or unknown repo is refused and the whole request,
// including its valid items, is not created.
func TestPropReservedAndUntrackedRefused(t *testing.T) {
	t.Run("untracked repo through the real resolver", func(t *testing.T) {
		w := newWorld(t)
		w.cfg.Resolver = StoreResolver{Store: w.store}
		w.start()
		kind, payload := coreprop.RowOf(coreprop.Install{Repo: w.repo.ID, Step: "install"})
		item := RequestProposal{Task: w.task.ID, Repo: w.repo.ID, Summary: "deps", Kind: kind, Payload: payload}
		if ids, err := w.svc.CreateForRequest(w.ctx, "req_tracked", []RequestProposal{item}); err != nil || len(ids) != 1 {
			t.Fatalf("tracked repo: %v, %v", ids, err)
		}
		dbtest.UntrackRepo(t, w.store, w.repo)
		_, err := w.svc.CreateForRequest(w.ctx, "req_untracked", []RequestProposal{item})
		assertRefused(t, err, coreprop.RefuseRepoUntracked)
		if got := w.find("req_untracked"); len(got) != 0 {
			t.Fatalf("created %v for an untracked repo", got)
		}
	})
	t.Run("unknown or reserved repo is invisible", func(t *testing.T) {
		w := newWorld(t)
		w.res.repo.ID = "rep_reserved" // the resolver no longer sees the task's repo, as for a reserved one
		_, err := w.svc.CreateForRequest(w.ctx, "req_reserved", w.reqItems(1))
		assertRefused(t, err, coreprop.RefuseRepoUntracked)
		if len(w.taskProposals()) != 0 || len(w.find("req_reserved")) != 0 {
			t.Fatal("something was created")
		}
	})
	t.Run("one bad item refuses the whole request", func(t *testing.T) {
		w := newWorld(t)
		items := w.reqItems(3)
		items[2].Repo = "rep_other" // names a repo its action does not act on
		_, err := w.svc.CreateForRequest(w.ctx, "req_mixed", items)
		assertRefused(t, err, coreprop.RefuseBadFacts)
		if len(w.taskProposals()) != 0 {
			t.Fatal("a valid item was created next to a refused one")
		}
	})
}

func assertRefused(t *testing.T, err error, reason coreprop.RefuseReason) {
	t.Helper()
	var r *Refusal
	if !errors.Is(err, ErrRefused) || !errors.As(err, &r) || r.Reason != reason || r.UserMessage() == "" {
		t.Fatalf("err = %v, want refusal %s", err, reason)
	}
}

func TestCreateForRequestRefusesDisabledAndInvalidItems(t *testing.T) {
	w := newWorld(t)
	good := w.reqItem(0, "ok")
	cases := map[string]struct {
		items  []RequestProposal
		reason coreprop.RefuseReason
	}{
		"disabled kind":   {[]RequestProposal{{Task: w.task.ID, Repo: w.repo.ID, Kind: core.ProposalReviewComment, Payload: []byte(`{}`)}}, coreprop.RefuseUnsupported},
		"unknown kind":    {[]RequestProposal{{Task: w.task.ID, Repo: w.repo.ID, Kind: "nuke", Payload: []byte(`{}`)}}, coreprop.RefuseUnsupported},
		"bad payload":     {[]RequestProposal{{Task: w.task.ID, Repo: w.repo.ID, Kind: core.ProposalCommit, Payload: []byte(`{"x":1}`)}}, coreprop.RefuseBadFacts},
		"no items":        {nil, coreprop.RefuseBadFacts},
		"too many":        {slices.Repeat([]RequestProposal{good}, coreprop.MaxRequestProposals+1), coreprop.RefuseTooMany},
		"foreign task":    {[]RequestProposal{{Task: "tsk_other", Repo: w.repo.ID, Kind: good.Kind, Payload: good.Payload}}, coreprop.RefuseBadFacts},
		"good plus other": {[]RequestProposal{good, {Task: w.task.ID, Repo: w.repo.ID, Kind: core.ProposalInitRepo, Payload: []byte(`{}`)}}, coreprop.RefuseUnsupported},
	}
	for name, c := range cases {
		_, err := w.svc.CreateForRequest(w.ctx, core.RequestID("req_"+strings.ReplaceAll(name, " ", "_")), c.items)
		if !errors.Is(err, ErrRefused) {
			t.Errorf("%s: err = %v", name, err)
			continue
		}
		var r *Refusal
		if errors.As(err, &r) && r.Reason != c.reason {
			t.Errorf("%s: reason = %s, want %s", name, r.Reason, c.reason)
		}
	}
	if len(w.taskProposals()) != 0 {
		t.Fatal("a refused request created proposals")
	}
	if _, err := w.svc.CreateForRequest(w.ctx, "", []RequestProposal{good}); !errors.Is(err, ErrRefused) {
		t.Fatalf("empty request id: %v", err)
	}
	if _, err := w.svc.FindForRequest(w.ctx, ""); !errors.Is(err, ErrRefused) {
		t.Fatalf("find with empty request id: %v", err)
	}
}

// All items are written in one transaction: a failure at a later item leaves
// nothing behind, and the request can then be created in full with the same
// IDs a retry returns after a crash that lost the reply.
func TestCreateForRequestCrashBetweenItems(t *testing.T) {
	w := newWorld(t)
	// Fill the task to 40 pending, then ask for 20 more: the 11th hits the cap.
	for i := range 2 {
		w.createFor(core.RequestID(fmt.Sprintf("req_fill%d", i)), w.reqItemsFrom(i*20, 20))
	}
	before := len(w.taskProposals())
	_, err := w.svc.CreateForRequest(w.ctx, "req_over", w.reqItemsFrom(40, 20))
	assertRefused(t, err, coreprop.RefuseTooMany)
	if got := len(w.taskProposals()); got != before {
		t.Fatalf("%d proposals after a failed request, want %d: items before the failure must roll back", got, before)
	}
	if got := w.find("req_over"); len(got) != 0 {
		t.Fatalf("FindForRequest = %v for a request that failed", got)
	}
	auditBefore := countOf(w, "proposal.create")
	if auditBefore != before {
		t.Fatalf("%d create audits for %d proposals: a rolled-back item left an audit entry", auditBefore, before)
	}
	// A smaller list under the same request now succeeds (nothing was stored).
	ids := w.createFor("req_over", w.reqItemsFrom(40, 5))
	if len(ids) != 5 {
		t.Fatalf("ids = %v", ids)
	}
	// The reply is lost; a new process retries and gets the same IDs.
	w.start()
	if again := w.createFor("req_over", w.reqItemsFrom(40, 5)); !slices.Equal(again, ids) {
		t.Fatalf("retry after restart = %v, want %v", again, ids)
	}
	if got := len(w.taskProposals()); got != before+5 {
		t.Fatalf("%d proposals, want %d", got, before+5)
	}
}

func (w *world) reqItemsFrom(start, n int) []RequestProposal {
	out := make([]RequestProposal, n)
	for i := range out {
		out[i] = w.reqItem(start+i, fmt.Sprintf("because %d", start+i))
	}
	return out
}

// Concurrent calls for one request create one set.
func TestCreateForRequestConcurrentCallsCreateOnce(t *testing.T) {
	w := newWorld(t)
	items := w.reqItems(3)
	var wg sync.WaitGroup
	results := make([][]core.ProposalID, 6)
	errs := make([]error, 6)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = w.svc.CreateForRequest(w.ctx, "req_race", items)
		}()
	}
	wg.Wait()
	for i := range results {
		if errs[i] != nil || !slices.Equal(results[i], results[0]) {
			t.Fatalf("call %d: %v, %v (first %v)", i, results[i], errs[i], results[0])
		}
	}
	if got := len(w.taskProposals()); got != 3 {
		t.Fatalf("%d proposals, want 3", got)
	}
}

// Each creation writes its audit entry in the same transaction, naming the
// request and no summary text.
func TestCreateForRequestAuditsEachCreation(t *testing.T) {
	w := newWorld(t)
	ids := w.createFor("req_audit", w.reqItems(3))
	for _, id := range ids {
		rows, err := w.store.ListAudit(w.ctx, "proposal:"+string(id), 10)
		if err != nil || len(rows) != 1 {
			t.Fatalf("audit of %s = %d rows, %v", id, len(rows), err)
		}
		e := rows[0]
		if e.Action != "proposal.create" || e.Actor != "user" {
			t.Fatalf("audit = %+v", e)
		}
		if !strings.Contains(string(e.Detail), `"request":"req_audit"`) || strings.Contains(string(e.Detail), "because") {
			t.Fatalf("audit detail = %s", e.Detail)
		}
	}
}

// A token in a summary is redacted before it reaches a row or the audit log
// (invariant 4), and does not make a retry look like a changed request.
func TestCreateForRequestKeepsTokensOutOfRowsAndAudit(t *testing.T) {
	w := newWorld(t, testToken)
	items := w.reqItems(2)
	items[0].Summary = "push with " + testToken + " please"
	ids := w.createFor("req_secret", items)
	if again := w.createFor("req_secret", items); !slices.Equal(again, ids) {
		t.Fatalf("retry = %v, want %v", again, ids)
	}
	for _, id := range ids {
		v, err := w.svc.Get(w.ctx, id)
		if err != nil || strings.Contains(v.Summary, testToken) {
			t.Fatalf("summary %q, %v", v.Summary, err)
		}
		rows, err := w.store.ListAudit(w.ctx, "proposal:"+string(id), 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range rows {
			if strings.Contains(string(e.Detail), testToken) || strings.Contains(e.Subject, testToken) {
				t.Fatalf("token in audit %+v", e)
			}
		}
	}
	all, err := w.store.ListAudit(w.ctx, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range all {
		if strings.Contains(string(e.Detail), testToken) {
			t.Fatalf("token in audit %+v", e)
		}
	}
}

// FindForRequest reads only.
func TestFindForRequestIsReadOnly(t *testing.T) {
	w := newWorld(t)
	if got := w.find("req_none"); len(got) != 0 {
		t.Fatalf("found %v for an unknown request", got)
	}
	if len(w.taskProposals()) != 0 {
		t.Fatal("a read created something")
	}
	if _, err := w.store.GetProposalRecord(w.ctx, coreprop.RequestProposalID("req_none", 0)); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}
