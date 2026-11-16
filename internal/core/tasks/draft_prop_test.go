package tasks

import (
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// oraclePlan is the documented decision table of an issue import, written
// independently of PlanDraft: scope first (tracked, then ready), then valid
// shell facts, then dedup, then create.
func oraclePlan(f DraftFacts) (kind string, reason DraftRefusal) {
	switch {
	case !f.RepoTracked:
		return "refuse", DraftRepoUntracked
	case !f.RepoReady:
		return "refuse", DraftRepoNotReady
	case f.Now.IsZero() || f.TaskID == "" || containsAny(string(f.TaskID), " \t\r\n/\\"):
		return "refuse", DraftBadFacts
	case f.Existing != "":
		return "exists", ""
	}
	return "create", ""
}

func containsAny(s, set string) bool {
	for _, c := range s {
		for _, d := range set {
			if c == d {
				return true
			}
		}
	}
	return false
}

func randFacts(r *rand.Rand) DraftFacts {
	f := DraftFacts{RepoTracked: r.Intn(4) != 0, RepoReady: r.Intn(4) != 0}
	if r.Intn(3) == 0 {
		f.Existing = core.TaskID([]string{"tsk_old", "tsk_older"}[r.Intn(2)])
	}
	f.TaskID = core.TaskID([]string{"tsk_new", "tsk_n2", "", "a b", "tsk/x"}[r.Intn(5)])
	if r.Intn(5) != 0 {
		f.Now = epoch.Add(time.Duration(r.Intn(1000)) * time.Second)
	}
	return f
}

// Invariant "import never runs work", "scope precedes dedup": over random
// sequences of facts, every outcome matches the decision table, a refusal
// never carries (and so never reveals) an existing task, and every created task
// is an inert Ask draft that retains the issue provenance and the chosen
// identity. The invariants are checked after every step.
func TestPropPlanDraftSequences(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		for i := 0; i < 80; i++ {
			spec, err := ParseDraftSpec(randValidSpec(r))
			if err != nil {
				t.Fatalf("generator made an invalid spec: %v", err)
			}
			before := spec
			f := randFacts(r)
			kind, reason := oraclePlan(f)
			out := PlanDraft(spec, f)
			if !reflect.DeepEqual(spec, before) {
				t.Fatal("PlanDraft changed its spec")
			}
			if !reflect.DeepEqual(out, PlanDraft(spec, f)) {
				t.Fatal("PlanDraft is not deterministic")
			}
			switch v := out.(type) {
			case DraftRefuse:
				if kind != "refuse" || v.Reason != reason {
					t.Fatalf("facts %+v: got refuse %s, oracle %s %s", f, v.Reason, kind, reason)
				}
				if v.Reason.Message() == "" {
					t.Fatal("refusal without a message")
				}
			case DraftExists:
				if kind != "exists" || v.Task != f.Existing {
					t.Fatalf("facts %+v: got exists %s, oracle %s", f, v.Task, kind)
				}
				if !f.RepoTracked || !f.RepoReady {
					t.Fatalf("dedup revealed %s outside scope (facts %+v)", v.Task, f)
				}
			case DraftCreate:
				if kind != "create" {
					t.Fatalf("facts %+v: got create, oracle %s", f, kind)
				}
				tk := v.Task
				if err := tk.Validate(); err != nil {
					t.Fatalf("created task is invalid: %v", err)
				}
				src := core.TaskSource{Kind: core.SourceIssue, Instance: spec.Issue.Instance, Project: spec.Issue.Project,
					Number: spec.Issue.Number, DedupKey: oracleKey(spec.Issue.Instance, spec.Issue.Project, spec.Issue.Number)}
				want := core.Task{ID: f.TaskID, ProjectID: spec.Project, RepoIDs: []core.RepoID{spec.Repo}, Title: spec.Title,
					Prompt: spec.Brief, Scope: core.ScopeRepo, Mode: core.ModeAsk, Agent: spec.Agent, Model: spec.Model,
					Status: core.TaskDraft, Source: &src, CreatedAt: f.Now, UpdatedAt: f.Now}
				if !reflect.DeepEqual(tk, want) {
					t.Fatalf("created task differs from the documented draft:\n got %+v\nwant %+v", tk, want)
				}
				if tk.SessionID != "" || tk.ParentTaskID != nil {
					t.Fatalf("import started work or made a sub-task: %+v", tk)
				}
			default:
				t.Fatalf("unknown outcome %T", out)
			}
		}
	})
}

// randValidSpec returns a spec that is valid by construction (its title is
// not trimmed, so Parse has something to normalize).
func randValidSpec(r *rand.Rand) DraftSpec {
	s := validSpec()
	s.Issue.Number = 1 + r.Intn(50)
	s.Issue.Project = []string{"acme/shop-api", "acme/shop-web", "acme/shop-admin"}[r.Intn(3)]
	s.Issue.Instance = []string{"git.acme.dev", "git.acme.dev:8443"}[r.Intn(2)]
	s.Title = " " + []string{"Fix login", "Add search", "日本語のタイトル"}[r.Intn(3)] + "\t"
	s.Brief = []string{"", "Users cannot log in.", "a\r\nb\r\n"}[r.Intn(3)]
	s.Model = []string{"", "model_one"}[r.Intn(2)]
	return s
}

// The scope check is total over the facts: no combination of Existing, TaskID
// and Now can turn an out-of-scope repo into anything but a scope refusal.
func TestPlanDraftScopeRefusalIsTotal(t *testing.T) {
	for _, ready := range []bool{false, true} {
		for _, existing := range []core.TaskID{"", "tsk_old"} {
			for _, id := range []core.TaskID{"", "tsk_new", "a b"} {
				for _, now := range []time.Time{{}, epoch} {
					f := DraftFacts{RepoTracked: false, RepoReady: ready, Existing: existing, TaskID: id, Now: now}
					if got := PlanDraft(validSpec(), f); got != (DraftRefuse{Reason: DraftRepoUntracked}) {
						t.Fatalf("facts %+v: %#v", f, got)
					}
				}
			}
		}
	}
}
