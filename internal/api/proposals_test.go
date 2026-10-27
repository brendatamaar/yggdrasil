package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/proposals"
)

var _ ProposalService = (*proposals.Service)(nil)

type fakeProposals struct {
	err    error
	views  []proposals.View
	filter proposals.ListFilter
	id     core.ProposalID
	hash   string
	by     string
	calls  int
}

func (f *fakeProposals) List(_ context.Context, fl proposals.ListFilter) ([]proposals.View, error) {
	f.calls++
	f.filter = fl
	return f.views, f.err
}
func (f *fakeProposals) Get(_ context.Context, id core.ProposalID) (proposals.View, error) {
	f.calls++
	f.id = id
	return f.views[0], f.err
}
func (f *fakeProposals) Confirm(_ context.Context, id core.ProposalID, hash, by string) (proposals.View, error) {
	f.calls++
	f.id, f.hash, f.by = id, hash, by
	return f.views[0], f.err
}
func (f *fakeProposals) Reject(_ context.Context, id core.ProposalID, by string) (proposals.View, error) {
	f.calls++
	f.id, f.by = id, by
	return f.views[0], f.err
}
func (f *fakeProposals) Retry(_ context.Context, id core.ProposalID, by string) (proposals.View, error) {
	f.calls++
	f.id, f.by = id, by
	return f.views[0], f.err
}

const propHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func proposalsEnv(t *testing.T) (*env, *fakeProposals) {
	t.Helper()
	at := time.Date(2025, 3, 1, 10, 0, 0, 0, time.UTC)
	f := &fakeProposals{views: []proposals.View{{
		ID: "prp_1", ChangeSet: "cs_1", Task: "tsk_1", Repo: "rep_1", Kind: core.ProposalKind("commit"),
		Status: core.ProposalStatus("failed"), FromAgent: true, Summary: "<b>Fix</b> it", ActionHash: propHash,
		Lines: []string{"Commit 2 files"}, CreatedAt: at, DecidedAt: &at, Attempt: 1,
		FailKind: "stale", Message: "The branch moved.", Retryable: true,
		Result: &coreprop.Result{CommitID: "abc", MovedPaths: []coreprop.RelPath{"a.txt"}},
	}}}
	return newEnv(t, func(d *Deps) { d.Proposals = f }), f
}

func TestProposalRoutesPassTheirArguments(t *testing.T) {
	e, f := proposalsEnv(t)
	resp, data := e.do(t, http.MethodGet, "/api/proposals?task=tsk_1&status=pending", nil)
	var list ProposalsResponse
	if resp.StatusCode != 200 || json.Unmarshal(data, &list) != nil || len(list.Proposals) != 1 {
		t.Fatalf("list %d %s", resp.StatusCode, data)
	}
	if f.filter.Task != "tsk_1" || f.filter.Status != "pending" {
		t.Errorf("filter = %+v", f.filter)
	}
	p := list.Proposals[0]
	if p.ID != "prp_1" || !p.FromAgent || p.Summary != "<b>Fix</b> it" || p.ActionHash != propHash || len(p.Lines) != 1 ||
		p.FailKind != "stale" || !p.Retryable || p.Result == nil || p.Result.CommitID != "abc" || p.DecidedAt == nil || p.Attempt != 1 ||
		p.ChangeSetID != "cs_1" || p.TaskID != "tsk_1" || p.RepoID != "rep_1" {
		t.Errorf("dto = %+v", p)
	}

	resp, data = e.do(t, http.MethodGet, "/api/proposals/prp_1", nil)
	if resp.StatusCode != 200 || f.id != "prp_1" {
		t.Fatalf("get %d %s", resp.StatusCode, data)
	}
	resp, data = e.do(t, http.MethodPost, "/api/proposals/prp_1/confirm", ConfirmProposalRequest{Hash: propHash})
	if resp.StatusCode != 200 || f.hash != propHash || f.by != "user" {
		t.Fatalf("confirm %d %s hash=%q by=%q", resp.StatusCode, data, f.hash, f.by)
	}
	f.by = ""
	resp, _ = e.do(t, http.MethodPost, "/api/proposals/prp_1/reject", map[string]any{})
	if resp.StatusCode != 200 || f.by != "user" {
		t.Fatalf("reject %d", resp.StatusCode)
	}
	f.by = ""
	resp, _ = e.do(t, http.MethodPost, "/api/proposals/prp_1/retry", map[string]any{})
	if resp.StatusCode != 200 || f.by != "user" {
		t.Fatalf("retry %d", resp.StatusCode)
	}
}

func TestProposalListNeedsAFilter(t *testing.T) {
	e, f := proposalsEnv(t)
	for _, q := range []string{"", "?status=bogus", "?task=bad%20id"} {
		resp, data := e.do(t, http.MethodGet, "/api/proposals"+q, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%q = %d %s, want 400", q, resp.StatusCode, data)
		}
	}
	if f.calls != 0 {
		t.Errorf("the service was called %d times", f.calls)
	}
}

func TestConfirmRejectsABadHash(t *testing.T) {
	e, f := proposalsEnv(t)
	for _, h := range []string{"", "abc", strings.ToUpper(propHash), propHash + "0", strings.Repeat("g", 64)} {
		resp, data := e.do(t, http.MethodPost, "/api/proposals/prp_1/confirm", ConfirmProposalRequest{Hash: h})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("hash %q = %d %s, want 400", h, resp.StatusCode, data)
		}
	}
	resp, _ := e.do(t, http.MethodPost, "/api/proposals/prp_1/confirm", map[string]any{"hash": propHash, "extra": 1})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown field = %d", resp.StatusCode)
	}
	if f.calls != 0 {
		t.Errorf("the service was called %d times", f.calls)
	}
}

func TestProposalErrorsMapToStatuses(t *testing.T) {
	refusal := func(r coreprop.RefuseReason) error {
		return fmt.Errorf("wrapped: %w", &proposals.Refusal{Reason: r, Message: "Plain words."})
	}
	cases := []struct {
		err    error
		status int
		want   string
	}{
		{refusal(coreprop.RefuseHashMismatch), 409, "what you were shown changed"},
		{refusal(coreprop.RefuseRepoUntracked), 409, "Plain words."},
		{proposals.ErrAlreadyDecided, 409, "already"},
		{fmt.Errorf("x: %w", db.ErrNotFound), 404, ""},
		{proposals.ErrNotStarted, 503, ""},
		{errors.New("disk exploded at C:\\secret"), 500, ""},
	}
	for _, c := range cases {
		e, f := proposalsEnv(t)
		f.err = c.err
		for _, r := range []struct{ method, path string }{
			{"GET", "/api/proposals/prp_1"}, {"GET", "/api/proposals?task=tsk_1"},
			{"POST", "/api/proposals/prp_1/confirm"}, {"POST", "/api/proposals/prp_1/reject"}, {"POST", "/api/proposals/prp_1/retry"},
		} {
			var body any
			if strings.HasSuffix(r.path, "confirm") {
				body = ConfirmProposalRequest{Hash: propHash}
			} else if r.method == "POST" {
				body = map[string]any{}
			}
			resp, data := e.do(t, r.method, r.path, body)
			if resp.StatusCode != c.status || !strings.Contains(strings.ToLower(string(data)), strings.ToLower(c.want)) {
				t.Errorf("%v %s = %d %s, want %d containing %q", c.err, r.path, resp.StatusCode, data, c.status, c.want)
			}
			if strings.Contains(string(data), "secret") {
				t.Errorf("leaks internal text: %s", data)
			}
		}
	}
}

func TestBadProposalIDIsNotFound(t *testing.T) {
	e, f := proposalsEnv(t)
	resp, _ := e.do(t, http.MethodGet, "/api/proposals/bad%20id", nil)
	if resp.StatusCode != http.StatusNotFound || f.calls != 0 {
		t.Errorf("= %d, calls %d", resp.StatusCode, f.calls)
	}
}

// Nothing an agent can reach confirms a proposal: the MCP mount has no
// proposal routes and the decisions need the browser's Host/Origin checks.
func TestNoAgentFacingRouteDecidesProposals(t *testing.T) {
	e, f := proposalsEnv(t)
	for _, p := range []string{"/mcp/proposals/prp_1/confirm", "/mcp/api/proposals/prp_1/confirm", "/mcp"} {
		resp, _ := e.do(t, http.MethodPost, p, ConfirmProposalRequest{Hash: propHash})
		if resp.StatusCode < 400 || f.calls != 0 {
			t.Errorf("POST %s = %d, service calls %d", p, resp.StatusCode, f.calls)
		}
	}
	// A foreign Origin is refused before the handler runs.
	req, _ := http.NewRequest(http.MethodPost, e.ts.URL+"/api/proposals/prp_1/confirm", strings.NewReader(`{"hash":"`+propHash+`"}`))
	req.Header.Set("Origin", "http://evil.example")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || f.calls != 0 {
		t.Errorf("foreign origin = %d, calls %d", resp.StatusCode, f.calls)
	}
}

func TestProposalPublisherSendsTheTopicMessage(t *testing.T) {
	e, _ := proposalsEnv(t)
	pub := &ProposalsHub{}
	pub.Bind(e.srv)
	pub.Publish(proposals.Event{ID: "prp_1", Task: "tsk_1", Repo: "rep_1", Status: "confirmed"})
	(&ProposalsHub{}).Publish(proposals.Event{}) // unbound: dropped, no panic
}

func FuzzDecodeConfirmBody(f *testing.F) {
	f.Add(`{"hash":"` + propHash + `"}`)
	f.Add(`{"hash":"x"}`)
	f.Add(`{}{}`)
	f.Add(`[`)
	f.Fuzz(func(t *testing.T, body string) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		hash, ok := decodeConfirm(w, r)
		if ok && !validActionHash(hash) {
			t.Fatalf("accepted %q from %q", hash, body)
		}
		if !ok && w.Code != http.StatusBadRequest {
			t.Fatalf("rejected with %d", w.Code)
		}
	})
}
