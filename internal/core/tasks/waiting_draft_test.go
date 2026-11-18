package tasks

import (
	"errors"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

func validWaitingSpec() WaitingDraftSpec {
	return WaitingDraftSpec{RequestID: "req_w", Project: "prj_1", Repo: "rep_1", Title: "Diagnose shop-api",
		Brief: "Folder unreadable.", Agent: "mock", Model: "model_one"}
}

func TestParseWaitingDraftSpecRejectsEachRule(t *testing.T) {
	if _, err := ParseWaitingDraftSpec(validWaitingSpec()); err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(*WaitingDraftSpec){
		"request id":    func(s *WaitingDraftSpec) { s.RequestID = "" },
		"long request":  func(s *WaitingDraftSpec) { s.RequestID = core.RequestID("r" + strings.Repeat("a", 100)) },
		"project":       func(s *WaitingDraftSpec) { s.Project = "" },
		"repo":          func(s *WaitingDraftSpec) { s.Repo = "" },
		"title empty":   func(s *WaitingDraftSpec) { s.Title = " " },
		"title long":    func(s *WaitingDraftSpec) { s.Title = strings.Repeat("a", MaxDraftTitle+1) },
		"title control": func(s *WaitingDraftSpec) { s.Title = "a\tb" },
		"brief empty":   func(s *WaitingDraftSpec) { s.Brief = "" },
		"brief large":   func(s *WaitingDraftSpec) { s.Brief = strings.Repeat("a", MaxDraftPrompt+1) },
		"brief nul":     func(s *WaitingDraftSpec) { s.Brief = "a\x00" },
		"brief utf8":    func(s *WaitingDraftSpec) { s.Brief = "\xff" },
		"agent":         func(s *WaitingDraftSpec) { s.Agent = "" },
		"model":         func(s *WaitingDraftSpec) { s.Model = "bad model" },
	} {
		s := validWaitingSpec()
		edit(&s)
		if _, err := ParseWaitingDraftSpec(s); !errors.Is(err, ErrInvalidDraft) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
}

func FuzzParseWaitingDraftSpec(f *testing.F) {
	f.Add("Diagnose", "Folder unreadable.", "mock")
	f.Fuzz(func(t *testing.T, title, brief, agent string) {
		s := validWaitingSpec()
		s.Title, s.Brief, s.Agent = title, brief, agent
		got, err := ParseWaitingDraftSpec(s)
		if err != nil {
			return
		}
		again, err := ParseWaitingDraftSpec(got)
		if err != nil || again != got {
			t.Fatalf("accepted spec is not stable: %+v %v", got, err)
		}
	})
}

// Invariant: over random facts, the decision is scope, then facts, then
// replay, then create; a created draft is an inert Ask draft with no source;
// a refusal never carries a task ID.
func TestPropPlanWaitingDraftSequences(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	spec := validWaitingSpec()
	for i := 0; i < 2000; i++ {
		f := DraftFacts{RepoTracked: r.Intn(4) != 0, RepoReady: r.Intn(4) != 0, Digest: "d1"}
		f.TaskID = core.TaskID([]string{"tsk_new", "", "a b"}[r.Intn(3)])
		if r.Intn(5) != 0 {
			f.Now = time.Unix(int64(1+r.Intn(1000)), 0)
		}
		if r.Intn(3) == 0 {
			f.Existing = "tsk_issue" // a waiting draft has no dedup: must be ignored
		}
		switch r.Intn(3) {
		case 1:
			f.Replay = Replayed{Task: "tsk_first", Digest: "d1"}
		case 2:
			f.Replay = Replayed{Task: "tsk_first", Digest: "d2"}
		}
		got := PlanWaitingDraft(spec, f)
		switch {
		case !f.RepoTracked:
			want(t, got, DraftRefuse{Reason: DraftRepoUntracked})
		case !f.RepoReady:
			want(t, got, DraftRefuse{Reason: DraftRepoNotReady})
		case f.Now.IsZero() || f.TaskID.Validate() != nil:
			want(t, got, DraftRefuse{Reason: DraftBadFacts})
		default:
			switch rep, _ := f.Replay.(Replayed); {
			case rep.Task != "" && rep.Digest != f.Digest:
				want(t, got, DraftRefuse{Reason: DraftPayloadChanged})
			case rep.Task != "":
				want(t, got, DraftReplayed{Task: "tsk_first"})
			default:
				c, ok := got.(DraftCreate)
				if !ok {
					t.Fatalf("facts %+v: %#v", f, got)
				}
				tk := c.Task
				if tk.Status != core.TaskDraft || tk.Mode != core.ModeAsk || tk.Source != nil || tk.Prompt != spec.Brief ||
					len(tk.RepoIDs) != 1 || tk.RepoIDs[0] != spec.Repo || tk.ID != f.TaskID {
					t.Fatalf("created task: %+v", tk)
				}
			}
		}
	}
}

func want(t *testing.T, got, exp DraftOutcome) {
	t.Helper()
	if got != exp {
		t.Fatalf("got %#v want %#v", got, exp)
	}
}
