package api

import (
	"encoding/json"
	"testing"
)

// Every Phase 3 route exists and answers 501 with a plain message until the
// owning task replaces its stub.
func TestPhase3StubsAnswer501(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/tasks/tsk_1/worktrees"},
		{"GET", "/api/proposals"},
		{"GET", "/api/proposals/p1"},
		{"POST", "/api/proposals/p1/confirm"},
		{"POST", "/api/proposals/p1/reject"},
		{"POST", "/api/proposals/p1/retry"},
		{"GET", "/api/repos/r1/trust"},
		{"POST", "/api/repos/r1/trust"},
		{"DELETE", "/api/repos/r1/trust"},
		{"GET", "/api/board"},
		{"PUT", "/api/board/max-agents"},
		{"GET", "/api/composer/prompts"},
		{"POST", "/api/composer/prompts"},
		{"DELETE", "/api/composer/prompts/x"},
		{"GET", "/api/composer/mentions"},
		{"GET", "/api/composer/models"},
		{"POST", "/api/composer/drafts"},
		{"GET", "/api/mcp/status"},
		{"POST", "/mcp"},
	} {
		resp, data := e.do(t, c.method, c.path, nil)
		var b ErrorBody
		if resp.StatusCode != 501 || json.Unmarshal(data, &b) != nil || b.Code != ErrUnavailable || b.Message == "" {
			t.Errorf("%s %s = %d %s, want 501 with a plain message", c.method, c.path, resp.StatusCode, data)
		}
	}
}
