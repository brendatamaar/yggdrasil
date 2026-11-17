package actions

import (
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Every attention reason has a primary action (E6: no stranded reason), and
// every primary action is a known kind with the mode the design gives it.
func TestEveryReasonHasAPrimaryAction(t *testing.T) {
	for _, r := range Reasons() {
		k, ok := PrimaryKind(r)
		if !ok {
			t.Fatalf("reason %s has no action", r)
		}
		if err := k.Validate(); err != nil {
			t.Errorf("reason %s: %v", r, err)
		}
	}
	if _, ok := PrimaryKind("bogus"); ok {
		t.Error("an unknown reason has an action")
	}
}

// Diagnose and review kinds are Ask; only FixPipeline asks for Code; an import
// and a pull start no turn.
func TestKindModes(t *testing.T) {
	want := map[Kind]core.TaskMode{
		DiagnosePipeline: core.ModeAsk, DiagnoseBuild: core.ModeAsk, DiagnoseRepo: core.ModeAsk,
		ReviewMR: core.ModeAsk, ProposeCommits: core.ModeAsk, ProposeIgnore: core.ModeAsk,
		FixPipeline: core.ModeCode, ImportIssue: "", PullAndCheck: "",
	}
	if len(want) != len(Kinds()) {
		t.Fatalf("the table covers %d kinds, there are %d", len(want), len(Kinds()))
	}
	for _, k := range Kinds() {
		if got := k.Mode(); got != want[k] {
			t.Errorf("%s runs in mode %q, want %q", k, got, want[k])
		}
	}
}

func TestParseRequest(t *testing.T) {
	ok, err := ParseRequest("req_1", "rep_1", "diagnose_pipeline", "12345", "mock", "")
	if err != nil {
		t.Fatal(err)
	}
	if ok.Kind != DiagnosePipeline || len(ok.Digest()) != 64 {
		t.Fatalf("parsed %+v, digest %q", ok, ok.Digest())
	}
	bad := map[string][6]string{
		"no request id":         {"", "rep_1", "diagnose_repo", "", "mock", ""},
		"unknown kind":          {"req_1", "rep_1", "force_push", "", "mock", ""},
		"url as source":         {"req_1", "rep_1", "diagnose_pipeline", "https://x/y", "mock", ""},
		"path as source":        {"req_1", "rep_1", "diagnose_pipeline", "../x", "mock", ""},
		"missing source":        {"req_1", "rep_1", "review_mr", "", "mock", ""},
		"source on repo kind":   {"req_1", "rep_1", "diagnose_repo", "7", "mock", ""},
		"no agent":              {"req_1", "rep_1", "diagnose_repo", "", "", ""},
		"agent with slash":      {"req_1", "rep_1", "diagnose_repo", "", "a/b", ""},
		"control in model":      {"req_1", "rep_1", "diagnose_repo", "", "mock", "m\x00"},
		"huge source":           {"req_1", "rep_1", "diagnose_pipeline", strings.Repeat("1", 65), "mock", ""},
		"repo id with space":    {"req_1", "rep 1", "diagnose_repo", "", "mock", ""},
		"request id with slash": {"a/b", "rep_1", "diagnose_repo", "", "mock", ""},
	}
	for name, a := range bad {
		if _, err := ParseRequest(a[0], a[1], a[2], a[3], a[4], a[5]); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// The digest covers every field but the request ID, and a field change moves it.
func TestDigestBindsTheRequest(t *testing.T) {
	base := Request{RequestID: "req_1", RepoID: "rep_1", Kind: DiagnosePipeline, SourceID: "1", Agent: "mock", Model: "m"}
	same := base
	same.RequestID = "req_2"
	if base.Digest() != same.Digest() {
		t.Error("the digest depends on the request ID")
	}
	for name, mut := range map[string]func(*Request){
		"repo": func(r *Request) { r.RepoID = "rep_2" }, "kind": func(r *Request) { r.Kind = FixPipeline },
		"source": func(r *Request) { r.SourceID = "2" }, "agent": func(r *Request) { r.Agent = "other" },
		"model": func(r *Request) { r.Model = "n" },
	} {
		r := base
		mut(&r)
		if r.Digest() == base.Digest() {
			t.Errorf("the digest ignores the %s", name)
		}
	}
}
