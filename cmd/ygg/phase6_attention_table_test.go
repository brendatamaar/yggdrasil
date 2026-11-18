package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/actions"
	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/core"
	coreactions "github.com/brendatamaar/yggdrasil/internal/core/actions"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/search/searchtest"
)

// hostStub is the cached host data of the repos of one test: which have a
// failed pipeline, an open merge request, a linked host and a token. It has no
// network and no token of its own.
type hostStub struct {
	mu      sync.Mutex
	repos   map[core.RepoID]*hostState
	failAll error
}

type hostState struct {
	failedCI bool
	runURL   string
	linked   bool
	token    bool
	mr       *corehosts.MergeRequest
}

func newHostStub() *hostStub { return &hostStub{repos: map[core.RepoID]*hostState{}} }

func (h *hostStub) state(id core.RepoID) *hostState {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.repos[id] == nil {
		h.repos[id] = &hostState{}
	}
	return h.repos[id]
}

func (h *hostStub) RepoHost(_ context.Context, repo core.RepoID) (api.RepoHostView, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.failAll != nil {
		return api.RepoHostView{}, h.failAll
	}
	st := h.repos[repo]
	if st == nil || !st.linked {
		return api.RepoHostView{NotLinked: "no host"}, nil
	}
	pipe := &corehosts.Pipeline{Status: corehosts.PipelineSuccess, URL: "https://github.com/acme/shop-api/actions/runs/1"}
	if st.failedCI {
		pipe = &corehosts.Pipeline{Status: corehosts.PipelineFailed, URL: st.runURL}
	}
	return api.RepoHostView{
		Linked: true, Instance: corehosts.Instance{ID: "github.com", Kind: corehosts.KindGitHub, Token: st.token}, Project: "acme/shop-api",
		Entry: corehosts.Entry{Phase: corehosts.Idle{Data: corehosts.RepoState{Pipeline: pipe, MR: st.mr}}},
	}, nil
}

func (h *hostStub) FailedPipelines(context.Context) (map[core.RepoID]struct{}, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.failAll != nil {
		return nil, h.failAll
	}
	out := map[core.RepoID]struct{}{}
	for id, st := range h.repos {
		if st.failedCI {
			out[id] = struct{}{}
		}
	}
	return out, nil
}

// bindActions builds the real action service over the real scope reader with
// the stub as host cache, and binds it to the API.
func (r *p6Rig) bindActions(t *testing.T, h *hostStub) *scopeReader {
	t.Helper()
	scope := &scopeReader{store: r.a.store, status: r.a.status, hosts: h, git: r.a.git}
	svc, err := actions.New(actions.Deps{
		Scope: scope, Context: &contextReader{scope: scope, host: noHostReader{}, store: r.a.store, logs: r.a.builds, git: r.a.git, redact: redactorForTest()},
		Tasks: r.a.tasks, Proposals: &proposalCreator{svc: r.a.proposals, drafts: r.a.tasks, store: r.a.store}, Requests: r.a.store,
		Hub: r.a.srv, Drafts: folderDrafts{svc: r.a.tasks}, Redactor: redactorForTest(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	r.a.srv.SetPhase6Services(api.Phase6Services{Actions: actionsAPI{svc: svc}})
	return scope
}

var buildSeq int

func (r *p6Rig) failBuild(t *testing.T, p *p6Repo) {
	t.Helper()
	ctx := context.Background()
	buildSeq++
	started, now := time.Now().UTC().Add(-time.Minute), time.Now().UTC()
	err := r.a.store.Commit(ctx, func(tx *db.Tx) error {
		_, err := tx.SaveBuild(ctx, corebuilds.Finished{
			ID: core.BuildID(fmt.Sprintf("bld_tbl%d", buildSeq)), Spec: corebuilds.Spec{Target: corebuilds.MainCheckout{}, Steps: []corebuilds.StepSpec{{RepoID: p.id, Name: "verify", Argv: []string{"false"}}}},
			QueuedAt: started, Actor: "user", StartedAt: &started, FinishedAt: now,
			Steps:  []corebuilds.StepState{corebuilds.StepDone{Attempt: 1, StartedAt: started, FinishedAt: now, Outcome: corebuilds.Failed{ExitCode: 1}}},
			Result: corebuilds.ResultFailed{Step: 0},
		}, db.NewBuild)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (r *p6Rig) conflict(t *testing.T, p *p6Repo) {
	t.Helper()
	blob := func(s string) string {
		return searchtest.GitOutput(t, r.env, p.dir, s, "hash-object", "-w", "--stdin")
	}
	idx := "100644 " + blob("base\n") + " 1\tc.txt\n100644 " + blob("ours\n") + " 2\tc.txt\n100644 " + blob("theirs\n") + " 3\tc.txt\n"
	searchtest.GitOutput(t, r.env, p.dir, idx, "update-index", "--index-info")
}

// attentionSetups put one repo into the state of one attention reason. They
// compose: each touches a different part of the repo or of the stores.
var attentionSetups = map[api.AttentionReason]func(t *testing.T, r *p6Rig, p *p6Repo, h *hostStub){
	api.ReasonBehind: func(t *testing.T, r *p6Rig, p *p6Repo, _ *hostStub) { r.remoteCommit(t, p) },
	api.ReasonDiverged: func(t *testing.T, r *p6Rig, p *p6Repo, _ *hostStub) {
		r.remoteCommit(t, p)
		r.localCommit(t, p, "local.txt")
	},
	api.ReasonDirty: func(t *testing.T, _ *p6Rig, p *p6Repo, _ *hostStub) {
		if err := os.WriteFile(filepath.Join(p.dir, "notes.txt"), []byte("wip\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	},
	api.ReasonConflicts: func(t *testing.T, r *p6Rig, p *p6Repo, _ *hostStub) { r.conflict(t, p) },
	api.ReasonFailedCI: func(_ *testing.T, _ *p6Rig, p *p6Repo, h *hostStub) {
		st := h.state(p.id)
		st.failedCI, st.runURL, st.linked, st.token = true, "https://github.com/acme/shop-api/actions/runs/9001", true, true
	},
	api.ReasonFailedBuild: func(t *testing.T, r *p6Rig, p *p6Repo, _ *hostStub) { r.failBuild(t, p) },
	api.ReasonUnreadable: func(t *testing.T, _ *p6Rig, p *p6Repo, _ *hostStub) {
		if err := os.RemoveAll(p.dir); err != nil {
			t.Fatal(err)
		}
	},
}

var allReasons = []api.AttentionReason{
	api.ReasonUnreadable, api.ReasonConflicts, api.ReasonDiverged, api.ReasonFailedCI, api.ReasonFailedBuild, api.ReasonBehind, api.ReasonDirty,
}

// implied are reasons a state shows besides the one it was built for: a
// diverged branch is also behind its upstream; unmerged paths count as changes.
var implied = map[api.AttentionReason][]api.AttentionReason{
	api.ReasonDiverged:  {api.ReasonBehind},
	api.ReasonConflicts: {api.ReasonDirty},
}

// localOnly reasons cannot coexist with an unreadable folder: the state they
// describe is in the folder.
func localOnly(r api.AttentionReason) bool {
	switch r {
	case api.ReasonConflicts, api.ReasonDiverged, api.ReasonBehind, api.ReasonDirty:
		return true
	}
	return false
}

func setupRank(r api.AttentionReason) int {
	switch r {
	case api.ReasonConflicts:
		return 1
	case api.ReasonUnreadable:
		return 2
	}
	return 0
}

func reasonSet(rs ...api.AttentionReason) map[api.AttentionReason]bool {
	m := map[api.AttentionReason]bool{}
	for _, x := range rs {
		m[x] = true
	}
	return m
}

func sortedReasons(m map[api.AttentionReason]bool) []string {
	var out []string
	for k := range m {
		out = append(out, string(k))
	}
	sort.Strings(out)
	return out
}

// combos is every single reason and every pair of reasons that can coexist.
func combos() [][]api.AttentionReason {
	var out [][]api.AttentionReason
	for i, a := range allReasons {
		out = append(out, []api.AttentionReason{a})
		for _, b := range allReasons[i+1:] {
			if (a == api.ReasonUnreadable && localOnly(b)) || (b == api.ReasonUnreadable && localOnly(a)) {
				continue
			}
			if (a == api.ReasonDiverged && b == api.ReasonBehind) || (a == api.ReasonBehind && b == api.ReasonDiverged) {
				continue // diverged already is behind
			}
			out = append(out, []api.AttentionReason{a, b})
		}
	}
	return out
}

func expectedMode(k api.ActionKind) core.TaskMode {
	switch coreactions.Kind(k) {
	case coreactions.FixPipeline:
		return core.ModeCode
	case coreactions.PullAndCheck, coreactions.ImportIssue:
		return ""
	}
	return core.ModeAsk
}

// TestPhase6NeedsAttentionTable is exit criterion E6 over the real wiring: for
// every attention reason alone and every pair that can coexist, the repo is put
// into that state (temp git repos, the store, a stub host cache), and the
// catalogue is read through the real HTTP handler.
//
//   - the reasons the server derives (Facts) and the reasons with an offering
//     are the same set: no reason without an action (E6);
//   - every requested reason has its primary kind, with the expected mode (Ask,
//     except the separate Code task of FixPipeline and the proposal-only pull);
//   - every offering has a title; each previews with the same kind and mode;
//   - an unreadable repo's offerings wait for the folder and never claim to run.
func TestPhase6NeedsAttentionTable(t *testing.T) {
	r := newP6Rig(t)
	h := newHostStub()
	scope := r.bindActions(t, h)
	for _, combo := range combos() {
		names := make([]string, len(combo))
		for i, c := range combo {
			names[i] = string(c)
		}
		t.Run(strings.Join(names, "+"), func(t *testing.T) {
			p := r.newRepo(t)
			// Conflicts block further commits and an unreadable folder blocks
			// everything in it, so those two are applied last.
			ordered := append([]api.AttentionReason(nil), combo...)
			sort.SliceStable(ordered, func(i, j int) bool { return setupRank(ordered[i]) < setupRank(ordered[j]) })
			for _, reason := range ordered {
				attentionSetups[reason](t, r, p, h)
			}
			r.refresh(t, p)

			facts, err := scope.Facts(context.Background(), p.id)
			if err != nil {
				t.Fatal(err)
			}
			derived := map[api.AttentionReason]bool{}
			for _, x := range facts.Reasons {
				derived[api.AttentionReason(x)] = true
			}
			want := reasonSet(combo...)
			allowed := reasonSet(combo...)
			for _, c := range combo {
				for _, x := range implied[c] {
					allowed[x] = true
				}
			}
			for x := range want {
				if !derived[x] {
					t.Errorf("the reasons derived are %v, missing %s", sortedReasons(derived), x)
				}
			}
			for x := range derived {
				if !allowed[x] {
					t.Errorf("the reasons derived are %v, unexpected %s", sortedReasons(derived), x)
				}
			}

			cat := r.catalogue(t, p)
			offered := map[api.AttentionReason]bool{}
			for _, o := range cat.Offerings {
				if o.Title == "" {
					t.Errorf("offering %+v has no title", o)
				}
				if o.Mode != expectedMode(o.Kind) {
					t.Errorf("%s: mode %q, want %q", o.Kind, o.Mode, expectedMode(o.Kind))
				}
				if o.Reason != "" {
					offered[o.Reason] = true
				}
				if derived[api.ReasonUnreadable] && !o.WaitingForFolder {
					t.Errorf("%s of an unreadable repo is not marked as waiting for the folder", o.Kind)
				}
			}
			for x := range derived {
				if !offered[x] {
					t.Errorf("E6: reason %s has no offering: %+v", x, cat.Offerings)
				}
				primary, _ := coreactions.PrimaryKind(coreactions.Reason(x))
				if x == api.ReasonFailedCI {
					primary = coreactions.DiagnosePipeline
				}
				found := false
				for _, o := range cat.Offerings {
					found = found || o.Reason == x && o.Kind == api.ActionKind(primary)
				}
				if !found {
					t.Errorf("reason %s: no %s offering in %+v", x, primary, cat.Offerings)
				}
			}
			for _, o := range cat.Offerings {
				var pv api.ActionPreviewDTO
				req := api.ActionRequestDTO{RequestID: core.RequestID(fmt.Sprintf("req_tbl_%s_%s", p.id, o.Kind)), RepoID: p.id, Kind: o.Kind, SourceID: o.SourceID, Agent: "mock"}
				if code, data := r.post(t, "/api/actions/preview", req, &pv); code != 200 {
					t.Errorf("preview of %s = %d %s", o.Kind, code, data)
					continue
				}
				if pv.Kind != o.Kind || pv.Mode != o.Mode || pv.Digest == "" || pv.WaitingForFolder != o.WaitingForFolder {
					t.Errorf("preview of %s = %+v, offering %+v", o.Kind, pv, o)
				}
			}
		})
	}
}

// Starting the primary action of every single reason does what the catalogue
// said: an Ask task (never changing the tree), a waiting draft for an
// unreadable folder, or one pending proposal and no task for a pull.
func TestPhase6NeedsAttentionPrimaryActionsRun(t *testing.T) {
	r := newP6Rig(t)
	h := newHostStub()
	r.bindActions(t, h)
	for _, reason := range allReasons {
		t.Run(string(reason), func(t *testing.T) {
			p := r.newRepo(t)
			attentionSetups[reason](t, r, p, h)
			r.refresh(t, p)
			primary, _ := coreactions.PrimaryKind(coreactions.Reason(reason))
			o := r.offering(t, p, api.ActionKind(primary))
			before := ""
			if reason != api.ReasonUnreadable {
				before = r.tree(t, p)
			}
			_, res := r.execute(t, api.ActionRequestDTO{RequestID: core.RequestID("req_run_" + string(p.id)), RepoID: p.id, Kind: o.Kind, SourceID: o.SourceID, Agent: "mock"})
			if res.State != api.ActionStateCompleted {
				t.Fatalf("result = %+v", res)
			}
			if o.Kind == api.ActionPullAndCheck {
				if res.TaskID != "" || len(res.ProposalIDs) != 1 {
					t.Fatalf("a pull gave %+v, want one proposal and no task", res)
				}
			} else {
				task, err := r.a.store.GetTask(context.Background(), res.TaskID)
				if err != nil || task.Mode != core.ModeAsk {
					t.Fatalf("task = %+v, %v; want an Ask task", task, err)
				}
				if reason == api.ReasonUnreadable {
					if task.Status != core.TaskDraft {
						t.Fatalf("an unreadable folder gave status %s, want a waiting draft", task.Status)
					}
				} else {
					waitTaskSettled(t, r, res.TaskID)
				}
			}
			if reason != api.ReasonUnreadable {
				if after := r.tree(t, p); after != before {
					t.Fatalf("the action changed the tree:\n%s\n%s", before, after)
				}
			}
		})
	}
}

// A missing host or token is a labelled fallback, never a missing action.
func TestPhase6NeedsAttentionFallbacks(t *testing.T) {
	r := newP6Rig(t)
	h := newHostStub()
	r.bindActions(t, h)

	t.Run("no token: the pipeline diagnosis stays, with metadata only", func(t *testing.T) {
		p := r.newRepo(t)
		attentionSetups[api.ReasonFailedCI](t, r, p, h)
		h.state(p.id).token = false
		r.refresh(t, p)
		o := r.offering(t, p, api.ActionDiagnosePipeline)
		if o.Context != api.ContextMetadataOnly || o.Note == "" || o.SourceID != "9001" {
			t.Fatalf("offering = %+v, want metadata only with a note", o)
		}
		pv, res := r.execute(t, api.ActionRequestDTO{RequestID: "req_nt_1", RepoID: p.id, Kind: o.Kind, SourceID: o.SourceID, Agent: "mock"})
		if len(pv.Unavailable) == 0 || res.State != api.ActionStateCompleted || res.TaskID == "" {
			t.Fatalf("preview %+v, result %+v; want a labelled gap and a started task", pv, res)
		}
		// The fix is offered too, and is the only Code offering.
		if fix := r.offering(t, p, api.ActionFixPipeline); fix.Mode != core.ModeCode {
			t.Fatalf("fix offering = %+v", fix)
		}
	})

	t.Run("no linked host: a repo diagnosis with the reason", func(t *testing.T) {
		p := r.newRepo(t)
		attentionSetups[api.ReasonFailedCI](t, r, p, h)
		h.state(p.id).linked = false
		r.refresh(t, p)
		var found bool
		for _, o := range r.catalogue(t, p).Offerings {
			if o.Reason == api.ReasonFailedCI {
				found = true
				if o.Kind != api.ActionDiagnoseRepo || o.Context != api.ContextMetadataOnly {
					t.Fatalf("offering = %+v, want a metadata-only repo diagnosis", o)
				}
			}
		}
		if !found {
			t.Fatal("a failed pipeline without a linked host has no offering")
		}
	})

	t.Run("a pipeline address with no run ID falls back to a repo diagnosis", func(t *testing.T) {
		p := r.newRepo(t)
		attentionSetups[api.ReasonFailedCI](t, r, p, h)
		h.state(p.id).runURL = "https://github.com/acme/shop-api/actions"
		r.refresh(t, p)
		if o := r.offering(t, p, api.ActionDiagnoseRepo); o.Reason != api.ReasonFailedCI {
			t.Fatalf("offering = %+v", o)
		}
	})

	t.Run("a failed host read removes only the host reasons", func(t *testing.T) {
		p := r.newRepo(t)
		attentionSetups[api.ReasonDirty](t, r, p, h)
		attentionSetups[api.ReasonFailedCI](t, r, p, h)
		r.refresh(t, p)
		h.mu.Lock()
		h.failAll = errors.New("host cache unavailable")
		h.mu.Unlock()
		defer func() { h.mu.Lock(); h.failAll = nil; h.mu.Unlock() }()
		var kinds []api.ActionKind
		for _, o := range r.catalogue(t, p).Offerings {
			kinds = append(kinds, o.Kind)
			if o.Reason == api.ReasonFailedCI {
				t.Fatalf("a failed host read still shows failed CI: %+v", o)
			}
		}
		if len(kinds) == 0 || kinds[0] != api.ActionProposeCommits {
			t.Fatalf("offerings = %v, want the commit proposal kept", kinds)
		}
	})
}

// A cached open merge request with a head yields the review offering; with no
// head, a closed one, or none, there is none (the review needs a pinned head).
func TestPhase6NeedsAttentionReviewOffering(t *testing.T) {
	r := newP6Rig(t)
	h := newHostStub()
	r.bindActions(t, h)
	head := strings.Repeat("c", 40)
	for name, c := range map[string]struct {
		mr   *corehosts.MergeRequest
		want bool
	}{
		"open with a head":    {&corehosts.MergeRequest{Number: 3, State: corehosts.MROpen, Head: head}, true},
		"open without a head": {&corehosts.MergeRequest{Number: 3, State: corehosts.MROpen}, false},
		"closed with a head":  {&corehosts.MergeRequest{Number: 3, State: corehosts.MRClosed, Head: head}, false},
		"no merge request":    {nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			p := r.newRepo(t)
			st := h.state(p.id)
			st.linked, st.token, st.mr = true, true, c.mr
			r.refresh(t, p)
			var review *api.ActionOfferingDTO
			for _, o := range r.catalogue(t, p).Offerings {
				if o.Kind == api.ActionReviewMR {
					review = &o
				}
			}
			if (review != nil) != c.want {
				t.Fatalf("review offering = %+v, want present %v", review, c.want)
			}
			if review != nil {
				if review.SourceID != "3" || review.Mode != core.ModeAsk || review.Reason != "" || review.Context != api.ContextAvailable {
					t.Fatalf("review = %+v", review)
				}
				var pv api.ActionPreviewDTO
				req := api.ActionRequestDTO{RequestID: "req_rv_1", RepoID: p.id, Kind: review.Kind, SourceID: review.SourceID, Agent: "mock"}
				if code, data := r.post(t, "/api/actions/preview", req, &pv); code != 200 || pv.ExpectedHead != head {
					t.Fatalf("preview = %d %s %+v; want the pin to be the MR head", code, data, pv)
				}
			}
		})
	}
}
