package tasks

import (
	"errors"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

func anchorSpec(repo core.RepoID) WaitingDraftSpec {
	return WaitingDraftSpec{RequestID: AnchorRequest(repo), Project: "prj_1", Repo: repo,
		Title: AnchorTitle, Brief: AnchorBrief, Agent: AnchorAgent}
}

func TestStartRefusalAnchorOnly(t *testing.T) {
	base := core.Task{ID: "tsk_1", Agent: "mock", Status: core.TaskDraft}
	for name, tc := range map[string]struct {
		task   core.Task
		refuse bool
	}{
		"anchor":         {core.Task{ID: "tsk_1", Agent: AnchorAgent}, true},
		"anchor titled":  {core.Task{ID: "tsk_1", Agent: AnchorAgent, Title: "My task"}, true},
		"repo setup":     {core.Task{ID: "tsk_1", Agent: "mock", Source: &core.TaskSource{Kind: core.SourceRepoSetup}}, true},
		"ordinary":       {base, false},
		"same title":     {core.Task{ID: "tsk_1", Agent: "mock", Title: AnchorTitle}, false},
		"issue":          {core.Task{ID: "tsk_1", Agent: "mock", Source: &core.TaskSource{Kind: core.SourceIssue}}, false},
		"waiting repo-x": {core.Task{ID: "tsk_1", Agent: "claude-code"}, false},
	} {
		got := StartRefusal(tc.task)
		if (got != nil) != tc.refuse {
			t.Errorf("%s: refusal %v, want refuse=%v", name, got, tc.refuse)
		}
		if got != nil && got.Message == "" {
			t.Errorf("%s: empty message", name)
		}
	}
	if got := StartRefusal(core.Task{Agent: AnchorAgent}); got == nil || !strings.Contains(got.Message, "only holds proposals") {
		t.Fatalf("anchor message: %v", got)
	}
}

func TestParseClientRequestIDRejectsReserved(t *testing.T) {
	for _, v := range []string{"anchor-rep_1", "anchor-", "ANCHOR-rep_1", "Anchor-x"} {
		if _, err := ParseClientRequestID(v, 100); err == nil {
			t.Errorf("reserved id %q accepted", v)
		}
	}
	for _, v := range []string{"req_1", "anchor", "anchor_x", "x-anchor-y", "an"} {
		if id, err := ParseClientRequestID(v, 100); err != nil || string(id) != v {
			t.Errorf("ordinary id %q: %v %q", v, err, id)
		}
	}
	if _, err := ParseClientRequestID("", 100); err == nil {
		t.Error("empty accepted")
	}
	if _, err := ParseClientRequestID(strings.Repeat("a", 101), 100); err == nil {
		t.Error("long accepted")
	}
}

func TestParseWaitingDraftSpecAnchorRules(t *testing.T) {
	if _, err := ParseWaitingDraftSpec(anchorSpec("rep_1")); err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(*WaitingDraftSpec){
		"other request":   func(s *WaitingDraftSpec) { s.RequestID = "req_x" },
		"other repo id":   func(s *WaitingDraftSpec) { s.Repo = "rep_2" },
		"custom title":    func(s *WaitingDraftSpec) { s.Title = "Workspace actions for shop-api" },
		"custom brief":    func(s *WaitingDraftSpec) { s.Brief += "." },
		"anchor w/ model": func(s *WaitingDraftSpec) { s.Model = "m" },
	} {
		s := anchorSpec("rep_1")
		edit(&s)
		if _, err := ParseWaitingDraftSpec(s); !errors.Is(err, ErrInvalidDraft) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
	// An ordinary waiting draft can neither use the reserved ID nor the agent.
	s := validWaitingSpec()
	s.RequestID = AnchorRequest(s.Repo)
	if _, err := ParseWaitingDraftSpec(s); !errors.Is(err, ErrInvalidDraft) {
		t.Errorf("reserved request id accepted for an ordinary draft: %v", err)
	}
	s = validWaitingSpec()
	s.Agent = AnchorAgent
	if _, err := ParseWaitingDraftSpec(s); !errors.Is(err, ErrInvalidDraft) {
		t.Errorf("reserved agent accepted for an ordinary draft: %v", err)
	}
}

func TestParseSubmissionRefusesReservedAgent(t *testing.T) {
	raw := RawMessage{Prompt: "p", Mode: "ask", Scope: "repo", Agent: AnchorAgent, Repos: []string{"rep_1"}}
	if _, _, err := ParseSubmission("prj_1", raw, nil); err == nil {
		t.Fatal("a client chose the reserved anchor agent")
	}
	raw.Agent = "mock"
	if _, _, err := ParseSubmission("prj_1", raw, nil); err != nil {
		t.Fatalf("ordinary agent: %v", err)
	}
}

// Property: for random request IDs, repos and agents, a parsed waiting draft is
// an anchor exactly when it has the reserved agent and the one request ID of
// its repo, and no accepted client ID is in the reserved namespace.
func TestAnchorParsersProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	alpha := []string{"anchor-", "rep_1", "rep_2", "req_", "a", "-", "ANCHOR-", "x"}
	pick := func() string {
		var b strings.Builder
		for n := rng.Intn(4) + 1; n > 0; n-- {
			b.WriteString(alpha[rng.Intn(len(alpha))])
		}
		return b.String()
	}
	for i := 0; i < 3000; i++ {
		v := pick()
		if id, err := ParseClientRequestID(v, 100); err == nil && HasReservedPrefix(id) {
			t.Fatalf("client id %q accepted in the reserved namespace", v)
		}
		spec := validWaitingSpec()
		spec.RequestID, spec.Repo = core.RequestID(v), core.RepoID([]string{"rep_1", "rep_2"}[rng.Intn(2)])
		if rng.Intn(2) == 0 {
			spec.Agent, spec.Title, spec.Brief, spec.Model = AnchorAgent, AnchorTitle, AnchorBrief, ""
		}
		got, err := ParseWaitingDraftSpec(spec)
		if err != nil {
			continue
		}
		isAnchor := got.Agent == AnchorAgent
		reservedID := HasReservedPrefix(got.RequestID)
		if (isAnchor || reservedID) && (!isAnchor || got.RequestID != AnchorRequest(got.Repo)) {
			t.Fatalf("accepted %+v: anchor=%v", got, isAnchor)
		}
	}
}

func TestPlanWaitingDraftAnchorCreatesFixedDraft(t *testing.T) {
	f := DraftFacts{RepoTracked: true, RepoReady: true, TaskID: "tsk_a", Now: time.Unix(1, 0).UTC(), Replay: NoReplay{}}
	out, ok := PlanWaitingDraft(anchorSpec("rep_1"), f).(DraftCreate)
	if !ok {
		t.Fatal("anchor not created")
	}
	if !IsAnchor(out.Task) || out.Task.Title != AnchorTitle || out.Task.Status != core.TaskDraft || out.Task.Mode != core.ModeAsk {
		t.Fatalf("anchor task: %+v", out.Task)
	}
	if StartRefusal(out.Task) == nil {
		t.Fatal("the planned anchor could start")
	}
}
