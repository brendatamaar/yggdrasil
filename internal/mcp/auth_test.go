package mcp_test

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

// A request without a valid token for a running task gets 401 and an empty
// body: no tool data, no hint (M6).
func TestTokenWrongExpiredOrOtherTaskIs401(t *testing.T) {
	e := newEnv(t)
	other, err := e.srv.Issue("t2", 1)
	if err != nil {
		t.Fatal(err)
	}
	e.srv.Revoke("t2")
	revoked := other

	e.srv.Revoke(e.task)
	cases := map[string]string{
		"none":                 "",
		"wrong":                "not-a-token",
		"prefix of real":       e.token[:10],
		"revoked after run":    e.token,
		"revoked other task's": revoked,
	}
	for name, tok := range cases {
		r := e.post(t, tok, "tools/call", map[string]any{"name": "list_repos", "arguments": map[string]any{}})
		if r.Status != http.StatusUnauthorized || r.Raw != "" {
			t.Errorf("%s: status %d body %q, want 401 and an empty body", name, r.Status, r.Raw)
		}
		if strings.Contains(r.Raw, "shop-api") {
			t.Errorf("%s: tool data leaked", name)
		}
	}
}

// A token is valid only for its own task: task t2's token never reads t1.
func TestTokenIsBoundToItsOwnTask(t *testing.T) {
	e := newEnv(t)
	tok2, err := e.srv.Issue("t2", 1)
	if err != nil {
		t.Fatal(err)
	}
	// t2 has no snapshot of its own: it must not see t1's repos.
	r := e.post(t, tok2, "tools/call", map[string]any{"name": "list_repos", "arguments": map[string]any{}})
	if strings.Contains(r.Raw, "shop-api") || strings.Contains(r.Raw, "r-api") {
		t.Fatalf("t2 saw t1's data: %s", r.Raw)
	}
	// A tool argument cannot name another task: the task comes from the token.
	r = e.callTool(t, "list_repos", map[string]any{"task": "t2"})
	if r.Err == nil || r.Err.Code != -32602 {
		t.Fatalf("an unknown argument must be invalid params, got %+v", r)
	}
}

func TestRevokeStopsTheTokenAtOnce(t *testing.T) {
	e := newEnv(t)
	if r := e.callTool(t, "list_repos", nil); r.Status != 200 || r.Result.IsError {
		t.Fatalf("before revoke: %+v", r)
	}
	e.srv.Revoke(e.task)
	if r := e.callTool(t, "list_repos", nil); r.Status != http.StatusUnauthorized {
		t.Fatalf("after revoke: status %d", r.Status)
	}
}

// An MCP client is not a browser: any Origin header is refused, even with a
// good token.
func TestOriginHeaderIsRefused(t *testing.T) {
	e := newEnv(t)
	req, _ := http.NewRequest(http.MethodPost, e.http.URL, bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer "+e.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403", resp.StatusCode)
	}
}

func TestOversizedBodyIsRefused(t *testing.T) {
	e := newEnv(t)
	big := strings.Repeat("a", (1<<20)+10)
	req, _ := http.NewRequest(http.MethodPost, e.http.URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"propose_agent_file","arguments":{"repo":"shop-api","path":"CLAUDE.md","content":"`+big+`"}}}`))
	req.Header.Set("Authorization", "Bearer "+e.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("a body over 1 MiB was served")
	}
	if len(e.prop.Created) != 0 {
		t.Fatal("an oversized request created a proposal")
	}
}
