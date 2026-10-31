package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

type fakeMCP struct{ hits int }

func (f *fakeMCP) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { f.hits++; w.WriteHeader(http.StatusAccepted) })
}
func (f *fakeMCP) Status(context.Context) McpStatusDTO { return McpStatusDTO{Enabled: true, ActiveSessions: 2} }

// /mcp refuses any Origin before the service sees the request; the status
// endpoint reports the service.
func TestMCPMountRefusesOriginAndServes(t *testing.T) {
	f := &fakeMCP{}
	e := newEnv(t, func(d *Deps) { d.MCP = f })
	req, _ := http.NewRequest("POST", e.ts.URL+"/mcp", strings.NewReader("{}"))
	req.Header.Set("Origin", e.ts.URL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || f.hits != 0 {
		t.Fatalf("status %d hits %d", resp.StatusCode, f.hits)
	}
	if resp, _ := e.do(t, "POST", "/mcp", nil, noSession); resp.StatusCode != http.StatusAccepted || f.hits != 1 {
		t.Fatalf("status %d hits %d", resp.StatusCode, f.hits)
	}
	if _, data := e.do(t, "GET", "/api/mcp/status", nil); !strings.Contains(string(data), `"activeSessions":2`) {
		t.Fatalf("status body %s", data)
	}
}
