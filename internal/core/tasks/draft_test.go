package tasks

import (
	"errors"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

func validDraft() DraftSpec {
	return DraftSpec{
		RequestID: "req_1", Project: "prj_1", Repo: "rep_1", Title: "Fix login",
		Issue: IssueRef{Instance: "git.acme.dev", Project: "acme/shop-api", Number: 12}, Brief: "Users cannot log in.", Agent: "mock",
	}
}

func TestParseDraftSpec(t *testing.T) {
	s, err := ParseDraftSpec(validDraft())
	if err != nil {
		t.Fatal(err)
	}
	src := s.Issue.Source()
	if err := src.Validate(); err != nil || src.Kind != core.SourceIssue {
		t.Fatalf("source %+v: %v", src, err)
	}
	if src.DedupKey != "issue:git.acme.dev/acme/shop-api#12" || src.DedupKey != s.Issue.DedupKey() {
		t.Errorf("dedup key %q", src.DedupKey)
	}
	for name, f := range map[string]func(*DraftSpec){
		"no request":          func(d *DraftSpec) { d.RequestID = "" },
		"no title":            func(d *DraftSpec) { d.Title = "  " },
		"long title":          func(d *DraftSpec) { d.Title = strings.Repeat("a", MaxDraftTitle+1) },
		"control title":       func(d *DraftSpec) { d.Title = "a\nb" },
		"huge brief":          func(d *DraftSpec) { d.Brief = strings.Repeat("a", MaxDraftPrompt+1) },
		"no agent":            func(d *DraftSpec) { d.Agent = "" },
		"issue zero":          func(d *DraftSpec) { d.Issue.Number = 0 },
		"issue blank":         func(d *DraftSpec) { d.Issue.Project = "a b" },
		"null brief":          func(d *DraftSpec) { d.Brief = "a\x00b" },
		"bad agent":           func(d *DraftSpec) { d.Agent = "--agent" },
		"bad model":           func(d *DraftSpec) { d.Model = "--model" },
		"long request":        func(d *DraftSpec) { d.RequestID = core.RequestID(strings.Repeat("x", 101)) },
		"url instance":        func(d *DraftSpec) { d.Issue.Instance = "https://git.acme.dev" },
		"credential instance": func(d *DraftSpec) { d.Issue.Instance = "token@git.acme.dev" },
		"ambiguous project":   func(d *DraftSpec) { d.Issue.Project = "a#12" },
		"bad project":         func(d *DraftSpec) { d.Project = "" },
		"split invalid UTF8":  func(d *DraftSpec) { d.Issue.Instance = "\xc3"; d.Issue.Project = "\xa9" },
		"no repo":             func(d *DraftSpec) { d.Repo = "" },
	} {
		d := validDraft()
		f(&d)
		if _, err := ParseDraftSpec(d); !errors.Is(err, ErrInvalidDraft) {
			t.Errorf("%s: %v, want ErrInvalidDraft", name, err)
		}
	}
}

func TestPlanDraftScopeAndDedup(t *testing.T) {
	for _, tracked := range []bool{false, true} {
		for _, ready := range []bool{false, true} {
			for _, existing := range []core.TaskID{"", "tsk_old"} {
				facts := DraftFacts{RepoTracked: tracked, RepoReady: ready, Existing: existing, TaskID: "tsk_new", Now: epoch}
				out := PlanDraft(validDraft(), facts)
				if !tracked || !ready {
					refuse, ok := out.(DraftRefuse)
					want := DraftRepoUntracked
					if tracked {
						want = DraftRepoNotReady
					}
					if !ok || refuse.Reason != want {
						t.Fatalf("scope bypass: %#v", out)
					}
				} else if existing != "" {
					if out != (DraftExists{Task: existing}) {
						t.Fatalf("dedup: %#v", out)
					}
				} else {
					task := out.(DraftCreate).Task
					if err := task.Validate(); err != nil {
						t.Fatal(err)
					}
					if task.Mode != core.ModeAsk || task.Status != core.TaskDraft || task.SessionID != "" || task.Source == nil || task.Source.Kind != core.SourceIssue || task.Source.DedupKey != validDraft().Issue.DedupKey() {
						t.Fatalf("import starts or loses provenance: %+v", task)
					}
				}
			}
		}
	}
}

// A zero Now or an empty or malformed TaskID is invalid input: nothing is
// created and nothing is reported as existing, but scope still comes first.
func TestPlanDraftRefusesZeroNowAndBadTaskID(t *testing.T) {
	for name, f := range map[string]DraftFacts{
		"zero now":      {RepoTracked: true, RepoReady: true, TaskID: "tsk_new"},
		"empty task id": {RepoTracked: true, RepoReady: true, Now: epoch},
		"bad task id":   {RepoTracked: true, RepoReady: true, TaskID: "a b", Now: epoch},
		"both":          {RepoTracked: true, RepoReady: true},
		"existing too":  {RepoTracked: true, RepoReady: true, Existing: "tsk_old"},
	} {
		out := PlanDraft(validDraft(), f)
		if out != (DraftRefuse{Reason: DraftBadFacts}) {
			t.Errorf("%s: %#v, want refusal %s", name, out, DraftBadFacts)
		}
	}
	// Scope refusals are not masked by bad facts.
	out := PlanDraft(validDraft(), DraftFacts{RepoTracked: false})
	if out != (DraftRefuse{Reason: DraftRepoUntracked}) {
		t.Errorf("scope first: %#v", out)
	}
}

func TestPropDraftNeverCreatesFromBadFacts(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		for i := 0; i < 100; i++ {
			f := DraftFacts{RepoTracked: true, RepoReady: true, TaskID: "tsk_one", Now: epoch}
			if r.Intn(2) == 0 {
				f.Now = time.Time{}
			}
			if r.Intn(2) == 0 {
				f.TaskID = ""
			}
			if r.Intn(2) == 0 {
				f.Existing = "tsk_old"
			}
			bad := f.Now.IsZero() || f.TaskID == ""
			switch PlanDraft(validDraft(), f).(type) {
			case DraftCreate, DraftExists:
				if bad {
					t.Fatalf("accepted bad facts %+v", f)
				}
			case DraftRefuse:
			case DraftReplayed:
				t.Fatalf("replay without a stored request: %+v", f)
			}
		}
	})
}

func TestDraftRefusalMessagesArePlain(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range []DraftRefusal{DraftRepoNotReady, DraftRepoUntracked, DraftBadFacts} {
		m := r.Message()
		if m == "" || strings.Contains(m, string(r)) || seen[m] {
			t.Errorf("%s: bad message %q", r, m)
		}
		seen[m] = true
	}
}

func FuzzDraftSpec(f *testing.F) {
	f.Add("Fix login", "Users cannot log in.", "mock", "", "req_1")
	f.Add("", "\x00", "--bad", "--bad", "")
	f.Fuzz(func(t *testing.T, title, brief, agent, model, request string) {
		raw := validDraft()
		raw.Title, raw.Brief, raw.Agent, raw.Model, raw.RequestID = title, brief, agent, model, core.RequestID(request)
		got, err := ParseDraftSpec(raw)
		if err != nil {
			return
		}
		again, err := ParseDraftSpec(got)
		if err != nil || got != again {
			t.Fatal("parse round trip failed")
		}
		if got.Title == "" || len(got.Brief) > MaxDraftPrompt || strings.ContainsRune(got.Brief, 0) || !ValidModelName(got.Agent) || (got.Model != "" && !ValidModelName(got.Model)) {
			t.Fatal("accepted invalid draft")
		}
	})
}

func TestPropImportNeverStarts(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		original := validDraft()
		facts := DraftFacts{RepoTracked: true, RepoReady: true, TaskID: "tsk_one", Now: epoch}
		for i := 0; i < 50; i++ {
			spec := original
			spec.Title = strings.Repeat("x", 1+r.Intn(100))
			if r.Intn(4) == 0 {
				facts.RepoTracked = false
			} else {
				facts.RepoTracked = true
			}
			out := PlanDraft(spec, facts)
			switch v := out.(type) {
			case DraftCreate:
				if v.Task.Mode != core.ModeAsk || v.Task.Status != core.TaskDraft || v.Task.SessionID != "" || v.Task.Source == nil || v.Task.Source.DedupKey != original.Issue.DedupKey() {
					t.Fatal("execution or provenance invariant broken")
				}
				facts.Existing = v.Task.ID
			case DraftExists:
				if v.Task != "tsk_one" {
					t.Fatal("dedup identity changed")
				}
			case DraftRefuse:
				if facts.RepoTracked {
					t.Fatal("unexpected refusal")
				}
			case DraftReplayed:
				t.Fatal("replay without a stored request")
			}
		}
	})
}
