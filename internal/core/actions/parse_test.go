package actions

import (
	"errors"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

func fields(r Request) [6]string {
	return [6]string{string(r.RequestID), string(r.RepoID), string(r.Kind), r.SourceID, r.Agent, r.Model}
}

func parse(f [6]string) (Request, error) { return ParseRequest(f[0], f[1], f[2], f[3], f[4], f[5]) }

// parse(fields(x)) == x for every kind's valid request.
func TestParseRequestRoundTrip(t *testing.T) {
	for _, k := range Kinds() {
		for _, model := range []string{"", "claude-haiku-4", "name with spaces é"} {
			x := reqFor(k)
			x.Model = model
			got, err := parse(fields(x))
			if err != nil {
				t.Fatalf("%s: %v", k, err)
			}
			if got != x {
				t.Errorf("%s: round trip gave %+v, want %+v", k, got, x)
			}
			if got.Digest() != x.Digest() || len(got.Digest()) != 64 {
				t.Errorf("%s: unstable digest", k)
			}
		}
	}
	// A kind that starts no task may omit the agent.
	x := reqFor(PullAndCheck)
	x.Agent = ""
	if got, err := parse(fields(x)); err != nil || got != x {
		t.Errorf("pull without agent: %+v, %v", got, err)
	}
}

// One test per rejection rule of Request.Validate, each wrapping ErrInvalidRequest.
func TestParseRequestRejectsEachRule(t *testing.T) {
	base := reqFor(DiagnosePipeline)
	with := func(f func(*Request)) [6]string {
		r := base
		f(&r)
		return fields(r)
	}
	cases := map[string][6]string{
		"empty request id":     with(func(r *Request) { r.RequestID = "" }),
		"request id too long":  with(func(r *Request) { r.RequestID = core.RequestID(strings.Repeat("a", 101)) }),
		"request id slash":     with(func(r *Request) { r.RequestID = "a/b" }),
		"empty repo":           with(func(r *Request) { r.RepoID = "" }),
		"repo with space":      with(func(r *Request) { r.RepoID = "rep 1" }),
		"unknown kind":         with(func(r *Request) { r.Kind = "force_push" }),
		"empty kind":           with(func(r *Request) { r.Kind = "" }),
		"source too long":      with(func(r *Request) { r.SourceID = strings.Repeat("1", MaxSourceIDLen+1) }),
		"source url":           with(func(r *Request) { r.SourceID = "https://x.example/y" }),
		"source path":          with(func(r *Request) { r.SourceID = "../x" }),
		"source with space":    with(func(r *Request) { r.SourceID = "1 2" }),
		"source non-ascii":     with(func(r *Request) { r.SourceID = "é" }),
		"source missing":       with(func(r *Request) { r.SourceID = "" }),
		"source on a repo job": fields(Request{RequestID: "req_1", RepoID: "rep_1", Kind: DiagnoseRepo, SourceID: "7", Agent: "mock"}),
		"source on a pull":     fields(Request{RequestID: "req_1", RepoID: "rep_1", Kind: PullAndCheck, SourceID: "7"}),
		"agent missing":        with(func(r *Request) { r.Agent = "" }),
		"agent too long":       with(func(r *Request) { r.Agent = strings.Repeat("a", MaxAgentLen+1) }),
		"agent slash":          with(func(r *Request) { r.Agent = "a/b" }),
		"agent bad on a pull":  fields(Request{RequestID: "req_1", RepoID: "rep_1", Kind: PullAndCheck, Agent: "a b"}),
		"import without agent": fields(Request{RequestID: "req_1", RepoID: "rep_1", Kind: ImportIssue, SourceID: "5"}),
		"model too long":       with(func(r *Request) { r.Model = strings.Repeat("m", MaxModelLen+1) }),
		"model control":        with(func(r *Request) { r.Model = "m\x00" }),
		"model newline":        with(func(r *Request) { r.Model = "m\n" }),
		"model delete char":    with(func(r *Request) { r.Model = "m\x7f" }),
		"model invalid utf8":   with(func(r *Request) { r.Model = "m\xff" }),
	}
	for name, f := range cases {
		_, err := parse(f)
		if err == nil {
			t.Errorf("%s was accepted: %v", name, f)
			continue
		}
		if !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("%s: error %v does not wrap ErrInvalidRequest", name, err)
		}
	}
}

// What is accepted is valid, bounded and re-parses to itself.
func FuzzParseRequest(f *testing.F) {
	for _, k := range Kinds() {
		x := reqFor(k)
		f.Add(string(x.RequestID), string(x.RepoID), string(x.Kind), x.SourceID, x.Agent, x.Model)
	}
	f.Add("req_1", "rep_1", "diagnose_pipeline", "https://x/y", "mock", "")
	f.Add("a/b", "rep 1", "force_push", "../x", "a b", "m\x00")
	f.Fuzz(func(t *testing.T, id, repo, kind, src, agent, model string) {
		r, err := ParseRequest(id, repo, kind, src, agent, model)
		if err != nil {
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("error %v does not wrap ErrInvalidRequest", err)
			}
			return
		}
		if err := r.Validate(); err != nil {
			t.Fatalf("accepted %+v but it is invalid: %v", r, err)
		}
		if len(r.SourceID) > MaxSourceIDLen || len(r.Agent) > MaxAgentLen || len(r.Model) > MaxModelLen {
			t.Fatalf("accepted an oversized field: %+v", r)
		}
		if strings.ContainsAny(r.SourceID, "/\\:. ") && strings.Contains(r.SourceID, "/") {
			t.Fatalf("accepted a path as source: %q", r.SourceID)
		}
		again, err := parse(fields(r))
		if err != nil || again != r || again.Digest() != r.Digest() {
			t.Fatalf("round trip of %+v gave %+v, %v", r, again, err)
		}
	})
}
