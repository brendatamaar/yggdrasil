package mcp_test

import (
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/mcp"
	"github.com/brendatamaar/yggdrasil/internal/mcp/mcptest"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

func TestSessionsContract(t *testing.T) {
	mcptest.SessionsContract(t, func() mcptest.SessionRegistry { return mcp.NewSessions(nil, nil) })
}

func TestNoDevServersContract(t *testing.T) {
	mcptest.DevServersContract(t, func() mcp.DevServers { return mcp.NoDevServers{} })
	mcptest.DevServersContract(t, func() mcp.DevServers { return &mcptest.FakeDevServers{} })
}

// Every issued token is added to the redactor (invariant 4).
func TestIssuedTokenIsRedacted(t *testing.T) {
	r := safety.NewRedactor()
	s := mcp.NewSessions(r, nil)
	tok, err := s.Issue("t1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Redact("curl -H 'x: " + tok + "'"); strings.Contains(got, tok) {
		t.Fatalf("token not masked: %s", got)
	}
	if len(tok) < 43 { // 32 bytes in base64url
		t.Fatalf("token too short: %d", len(tok))
	}
}
