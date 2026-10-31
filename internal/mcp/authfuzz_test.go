package mcp_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// authStatus sends one request straight into the handler (no network) and
// returns the status and body.
func authStatus(e *env, auth string, hasAuth bool, origin string) (int, string) {
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_repos","arguments":{}}}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if hasAuth {
		req.Header["Authorization"] = []string{auth}
	}
	if origin != "" {
		req.Header["Origin"] = []string{origin}
	}
	rec := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// FuzzAuthorizationHeader: whatever the header holds, a request is served only
// when it is "Bearer <the task's token>" (scheme case-insensitive, surrounding
// white space ignored); anything else is 401 with an empty body, and any Origin
// is 403 whatever the token.
func FuzzAuthorizationHeader(f *testing.F) {
	e := newEnv(f)
	tok := e.token
	for _, s := range []string{
		"", "Bearer", "Bearer ", "bearer " + tok, "BEARER " + tok, "Bearer " + tok, "Bearer  " + tok + " ",
		"Bearer " + tok + "x", "Bearer " + tok[:len(tok)-1], "Basic " + tok, tok, "Bearer\t" + tok, "Bearer \x00" + tok,
		"Bearer " + tok + "\n", "Bearer Bearer " + tok, " Bearer " + tok, "Bearer " + strings.ToUpper(tok),
		"Bearer \xff", strings.Repeat("A", 70000),
	} {
		f.Add(s, true, "")
	}
	f.Add("Bearer "+tok, true, "http://localhost")
	f.Add("", false, "")
	f.Fuzz(func(t *testing.T, auth string, hasAuth bool, origin string) {
		code, body := authStatus(e, auth, hasAuth, origin)
		if origin != "" {
			if code != http.StatusForbidden {
				t.Fatalf("Origin %q got %d, want 403", origin, code)
			}
			return
		}
		valid := false
		if hasAuth && len(auth) > len("bearer ") && strings.EqualFold(auth[:len("bearer ")], "bearer ") {
			valid = strings.TrimSpace(auth[len("bearer "):]) == tok
		}
		switch {
		case valid && code == http.StatusUnauthorized:
			t.Fatalf("valid header %q refused", auth)
		case !valid && (code != http.StatusUnauthorized || body != ""):
			t.Fatalf("header %q (set=%v) got %d %q, want 401 and an empty body", auth, hasAuth, code, body)
		}
	})
}
