package main

import (
	"net"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/agents/claudecode"
	"github.com/brendatamaar/yggdrasil/internal/mcp"
	"github.com/brendatamaar/yggdrasil/internal/mcp/mcptest"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

var bearerRe = regexp.MustCompile(`Bearer ([^"\s]+)`)

func newTestMCPService(t *testing.T) *mcpService {
	t.Helper()
	srv, err := mcp.New(mcp.Config{
		Workspace: mcptest.NewFakeWorkspace(), Reader: &mcptest.FakeReader{}, Builds: &mcptest.FakeBuilds{}, Git: &mcptest.FakeGit{},
		Hosts: &mcptest.FakeHosts{}, DevServers: mcp.NoDevServers{}, Proposer: &mcptest.FakeProposer{},
		Audit: &mcptest.FakeAudit{}, Redactor: safety.NewRedactor(),
	})
	if err != nil {
		t.Fatal(err)
	}
	m := &mcpService{Server: srv}
	m.setAddr(&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 7777})
	return m
}

// tokensOnDisk returns every bearer token found in any file under root.
func tokensOnDisk(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, m := range bearerRe.FindAllStringSubmatch(string(b), -1) {
			out = append(out, m[1])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// The config file of a run holds a live token only while the run lives: once
// EndRun has been called (run finished, cancelled, recovered), every token
// still on disk is revoked (invariant 4: a leftover file is not a credential).
func TestMCPConfigFilesLeftAfterARunHoldOnlyRevokedTokens(t *testing.T) {
	m := newTestMCPService(t)
	a := claudecode.New(claudecode.Options{})
	runs := t.TempDir()
	dirOf := func(name string) string {
		d := filepath.Join(runs, name)
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		return d
	}

	if _, err := m.BeginRun(a, "tsk_a", 1, dirOf("tsk_a-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.BeginRun(a, "tsk_b", 1, dirOf("tsk_b-1")); err != nil {
		t.Fatal(err)
	}
	live := tokensOnDisk(t, runs)
	if len(live) != 2 {
		t.Fatalf("found %d tokens on disk during the runs, want 2", len(live))
	}
	for _, tok := range live {
		if _, ok := m.Sessions().Lookup(tok); !ok {
			t.Fatal("a running task's token does not work")
		}
	}

	m.EndRun("tsk_a") // finished
	m.EndRun("tsk_b") // cancelled or recovered: same call
	m.EndRun("tsk_b") // a second end changes nothing
	left := tokensOnDisk(t, runs)
	if len(left) != 2 {
		t.Fatalf("the test needs the leftover files: found %d tokens", len(left))
	}
	for _, tok := range left {
		if _, ok := m.Sessions().Lookup(tok); ok {
			t.Fatal("a config file left on disk after the run holds a token that still works")
		}
	}

	// A second attempt gets a new token; the first attempt's file stays revoked.
	if _, err := m.BeginRun(a, "tsk_a", 2, dirOf("tsk_a-2")); err != nil {
		t.Fatal(err)
	}
	m.EndRun("tsk_a")
	for _, tok := range tokensOnDisk(t, runs) {
		if _, ok := m.Sessions().Lookup(tok); ok {
			t.Fatal("a token survived EndRun")
		}
	}
}

// A config that cannot be written revokes the session at once and leaves no
// file with a token.
func TestMCPConfigWriteFailureRevokesTheSession(t *testing.T) {
	m := newTestMCPService(t)
	a := claudecode.New(claudecode.Options{})
	missing := filepath.Join(t.TempDir(), "no", "such", "folder")
	if _, err := m.BeginRun(a, "tsk_a", 1, missing); err == nil {
		t.Fatal("BeginRun succeeded without a folder to write in")
	}
	if m.ActiveSessions() != 0 {
		t.Fatalf("%d sessions are live after a failed BeginRun", m.ActiveSessions())
	}
}
