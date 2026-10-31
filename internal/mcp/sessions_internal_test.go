package mcp

import (
	"crypto/sha256"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// A late end of attempt 1 does not revoke attempt 2, nor drop its gate.
func TestLateRevokeKeepsNewerAttempt(t *testing.T) {
	s := &Server{sessions: NewSessions(nil, nil), gates: map[core.TaskID]*gate{}}
	_, _ = s.Issue("t1", 1)
	tok2, _ := s.Issue("t1", 2)
	g := s.gateFor("t1")
	s.Revoke("t1", 1)
	if _, ok := s.sessions.Lookup(tok2); !ok || !s.sessions.Live("t1") {
		t.Fatal("a late revoke ended attempt 2")
	}
	if s.gateFor("t1") != g {
		t.Fatal("a late revoke dropped the gate of attempt 2")
	}
	s.Revoke("t1", 2)
	if s.sessions.Live("t1") {
		t.Fatal("revoke of the live attempt did nothing")
	}
}

// A call that was in flight when the session was revoked must not recreate the
// gate that Revoke removed.
func TestRevokedSessionGetsNoGate(t *testing.T) {
	s := &Server{sessions: NewSessions(nil, nil), gates: map[core.TaskID]*gate{}}
	if _, err := s.Issue("t1", 1); err != nil {
		t.Fatal(err)
	}
	if s.gateFor("t1") == nil || len(s.gates) != 1 {
		t.Fatal("no gate for a live session")
	}
	s.Revoke("t1", 1)
	if len(s.gates) != 0 {
		t.Fatal("gate survived revoke")
	}
	if g := s.gateFor("t1"); g != nil || len(s.gates) != 0 {
		t.Fatalf("revoked session recreated its gate: %v", s.gates)
	}
}

// Only the hash of a token is kept.
func TestSessionsKeepOnlyTheHash(t *testing.T) {
	s := NewSessions(nil, nil)
	tok, _ := s.Issue("t1", 1)
	want := sha256.Sum256([]byte(tok))
	if _, ok := s.byHash[want]; !ok || len(s.byHash) != 1 {
		t.Fatalf("registry holds %d entries and not the hash", len(s.byHash))
	}
	for k := range s.byHash {
		if strings.Contains(string(k[:]), tok) {
			t.Fatal("the token is stored")
		}
	}
}

// No randomness, no token.
func TestIssueFailsWithoutRandomness(t *testing.T) {
	s := NewSessions(nil, iotest.ErrReader(iotest.ErrTimeout))
	if tok, err := s.Issue("t1", 1); err == nil || tok != "" {
		t.Fatalf("issued %q, %v", tok, err)
	}
	if s.Count() != 0 {
		t.Fatal("a session exists without a token")
	}
}

// M7/M9 on the sanitizer itself: random text with secrets and paths never
// comes out with them, and the cap holds on a rune boundary.
func TestSanitizeAndCap(t *testing.T) {
	srv := &Server{cfg: Config{Redactor: stubRedactor{}}}
	for _, in := range []string{
		"see C:\\Users\\x\\a.go", "at /Users/x/a.go:1", "(/home/x/y)", "UNC \\\\h\\s\\f", "plain a/b/c.go and b/x",
		"D:/work/repo", `"C:\\tmp\\x"`,
	} {
		out := srv.sanitize(in)
		for _, bad := range []string{"C:\\", "D:/", "/Users/", "/home/", "\\\\h"} {
			if strings.Contains(out, bad) {
				t.Errorf("%q -> %q still has %q", in, out, bad)
			}
		}
	}
	if out := srv.sanitize("diff a/src/x.go b/src/x.go"); out != "diff a/src/x.go b/src/x.go" {
		t.Errorf("relative paths changed: %q", out)
	}
	big := strings.Repeat("é", 80000)
	text, cut := srv.finish(big)
	if !cut || len(text) > 64*1024+20 || !strings.HasSuffix(text, "[truncated]") {
		t.Errorf("cap: cut=%v len=%d", cut, len(text))
	}
	if strings.ContainsRune(strings.TrimSuffix(text, "\n[truncated]"), '\uFFFD') {
		t.Error("the cap split a rune")
	}
}

type stubRedactor struct{}

func (stubRedactor) Redact(s string) string { return s }
