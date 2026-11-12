package tasks

import (
	"errors"
	"strings"
	"testing"

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
		"no request":    func(d *DraftSpec) { d.RequestID = "" },
		"no title":      func(d *DraftSpec) { d.Title = "  " },
		"long title":    func(d *DraftSpec) { d.Title = strings.Repeat("a", MaxDraftTitle+1) },
		"control title": func(d *DraftSpec) { d.Title = "a\nb" },
		"huge brief":    func(d *DraftSpec) { d.Brief = strings.Repeat("a", MaxDraftPrompt+1) },
		"no agent":      func(d *DraftSpec) { d.Agent = "" },
		"issue zero":    func(d *DraftSpec) { d.Issue.Number = 0 },
		"issue blank":   func(d *DraftSpec) { d.Issue.Project = "a b" },
		"no repo":       func(d *DraftSpec) { d.Repo = "" },
	} {
		d := validDraft()
		f(&d)
		if _, err := ParseDraftSpec(d); !errors.Is(err, ErrInvalidDraft) {
			t.Errorf("%s: %v, want ErrInvalidDraft", name, err)
		}
	}
}

// PlanDraft is a documented stub: it refuses and creates nothing, so an import
// can never start an agent or claim success before the task lands.
func TestPlanDraftIsAnInertRefusal(t *testing.T) {
	out := PlanDraft(validDraft(), DraftFacts{RepoTracked: true, RepoReady: true})
	r, ok := out.(DraftRefuse)
	if !ok || r.Reason != DraftPending {
		t.Fatalf("PlanDraft = %#v", out)
	}
}
