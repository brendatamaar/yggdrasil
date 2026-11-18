package actions

import (
	"context"
	"fmt"
	"math/rand"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreactions "github.com/brendatamaar/yggdrasil/internal/core/actions"
	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// The shell holds no copy of a safety decision: whether an action is still
// offered, whether its pin is still current and which mode may run are all
// answered by the core. These tests drive the whole service over random facts
// and check that its outcome is exactly the core's answer.

func randomFacts(r *rand.Rand, repo core.RepoID) coreactions.Facts {
	heads := []string{head40, head40b}
	pick := func(opts ...string) string { return opts[r.Intn(len(opts))] }
	f := coreactions.Facts{
		Repo: repo, Readable: r.Intn(4) != 0, Head: heads[r.Intn(2)],
		CIRunID: pick("", "9001"), CIHead: pick("", head40, head40b),
		BuildID: pick("", "bld_1"), MRHead: pick("", head40, head40b),
	}
	if r.Intn(2) == 0 {
		f.MRNumber = 3
	}
	if r.Intn(3) == 0 {
		f.UntrackedCount = 150
	}
	f.Caps = coreactions.Capabilities{
		HostLogs: r.Intn(2) == 0, HostIssues: r.Intn(2) == 0, HostMR: r.Intn(2) == 0,
		CommentPost: r.Intn(2) == 0, BuildLog: r.Intn(2) == 0,
	}
	for _, x := range coreactions.Reasons() {
		if r.Intn(2) == 0 {
			f.Reasons = append(f.Reasons, x)
		}
	}
	return f
}

// What the core says about a dispatch whose facts moved from f1 (preview) to f2
// (the recheck before the effect).
func coreVerdict(f1, f2 coreactions.Facts, req coreactions.Request) (refused bool, why coreactions.RefuseReason) {
	if _, why, ok := coreactions.Admit(f1, req); !ok {
		return true, why
	}
	if _, why, ok := coreactions.Admit(f2, req); !ok {
		return true, why
	}
	if !coreactions.StillCurrent(coreactions.Pin(f1, req.Kind), coreactions.Pin(f2, req.Kind)) {
		return true, coreactions.RefuseStaleHead
	}
	return false, ""
}

func TestShellOutcomeAgreesWithCoreForEveryKindAndFactPair(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	seen := map[coreactions.Kind]int{}
	stale, notOffered := 0, 0
	for i := 0; i < 160; i++ {
		e := newEnv(t)
		f1, f2 := randomFacts(r, e.repo.ID), randomFacts(r, e.repo.ID)
		if r.Intn(3) == 0 {
			f2 = f1 // an unchanged repo must go through
		}
		cands := []coreactions.Request{e.req("req_x", coreactions.ImportIssue, "7")}
		for _, o := range coreactions.Catalogue(f1) {
			cands = append(cands, e.req("req_x", o.Kind, o.SourceID))
		}
		if r.Intn(4) == 0 {
			cands = append(cands, e.req("req_x", coreactions.PullAndCheck, "")) // possibly not offered
		}
		req := cands[r.Intn(len(cands))]
		seen[req.Kind]++

		e.scope.set(func(f *coreactions.Facts) { *f = f1 })
		e.scope.pull.Head = f1.Head
		e.ctxr.diff = DiffContext{Head: f1.Head, DiffHash: head40}
		e.ctxr.host[corehosts.ContextIssue] = ready(corehosts.IssueDetail{Summary: corehosts.IssueSummary{Title: "Fix cart total"}})
		e.scope.onFacts = func(n int, s *fakeScope) {
			if n >= 2 {
				s.set(func(f *coreactions.Facts) { *f = f2 })
			}
		}
		refused, why := coreVerdict(f1, f2, req)
		out, err := e.svc.Dispatch(context.Background(), Input{Request: req})
		label := fmt.Sprintf("kind %s source %q\nf1 %+v\nf2 %+v", req.Kind, req.SourceID, f1, f2)
		if err != nil {
			t.Fatalf("%s\ndispatch: %v", label, err)
		}
		if refused {
			if why == coreactions.RefuseStaleHead {
				stale++
			} else {
				notOffered++
			}
			if out.State != db.ActionRefused || out.Refusal != why {
				t.Fatalf("%s\nshell %+v, core refuses with %s", label, out, why)
			}
			if e.tasks.agentCalls()+e.tasks.drafts != 0 || e.props.creates != 0 || e.drafts.calls != 0 {
				t.Fatalf("%s\nrefused but had effects: %s", label, e.effects())
			}
			continue
		}
		if out.State != db.ActionCompleted {
			t.Fatalf("%s\nshell %+v, core admits it", label, out)
		}
	}
	if stale == 0 || notOffered == 0 || len(seen) < 6 {
		t.Fatalf("the generator is too narrow: stale %d, not offered %d, kinds %v", stale, notOffered, seen)
	}
}

// A task is submitted in a mode only when the core allows that mode for the
// kind: the shell never decides it. Driving the effect directly shows it for
// every kind, including those that start no task at all.
func TestSubmitTaskModeIsTheCoresDecision(t *testing.T) {
	for _, k := range coreactions.Kinds() {
		t.Run(string(k), func(t *testing.T) {
			e := newEnv(t)
			run := &run{s: e.svc, l: &loaded{target: Target{Project: e.project.ID}}}
			c := coreactions.Dispatch{Request: e.req("req_1", k, sourceFor(k))}
			_, ferr := run.submitTask(context.Background(), c)
			want := coreactions.ModeAllowed(k, k.Mode())
			if want != (ferr == nil) || want != (e.tasks.submits == 1) {
				t.Fatalf("kind %s: core allows %v, shell failure %v, submits %d", k, want, ferr, e.tasks.submits)
			}
			if k.Mode() == core.ModeCode && k != coreactions.FixPipeline {
				t.Fatalf("only FixPipeline may ask for Code")
			}
		})
	}
}

func sourceFor(k coreactions.Kind) string {
	switch k {
	case coreactions.DiagnosePipeline, coreactions.FixPipeline:
		return "9001"
	case coreactions.ReviewMR:
		return "3"
	case coreactions.DiagnoseBuild:
		return "bld_1"
	case coreactions.ImportIssue:
		return "7"
	}
	return ""
}
