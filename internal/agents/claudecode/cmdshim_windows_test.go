//go:build windows

package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// The real CLI is often an npm shim (claude.cmd). The adapter's flags must
// reach it intact through platform.Start, including the empty list in the
// equals form (S4).
func TestCmdShimReceivesTheFlagsIntact(t *testing.T) {
	dir := t.TempDir()
	shim := filepath.Join(dir, "claude.cmd")
	out := filepath.Join(dir, "args.txt")
	if err := os.WriteFile(shim, []byte("@echo off\r\necho %*> \"%~dp0args.txt\"\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env, err := safety.NewEnvBuilder().Build(safety.EnvOptions{})
	if err != nil {
		t.Fatal(err)
	}
	o := startOpts(t, "stream", nil)
	args := buildArgs(filepath.Join(dir, "settings.json"), filepath.Join(dir, "mcp.json"), o)
	p, err := platform.Start(context.Background(), platform.Spec{Argv: append([]string{shim}, args...), Dir: dir, Env: env})
	if err != nil {
		t.Fatalf("start shim: %v", err)
	}
	if code, err := p.Wait(); err != nil || code != 0 {
		t.Fatalf("shim exit %d, %v", code, err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(string(data))
	// platform.Start quotes every argument; the empty-list flag stays one piece.
	for _, want := range []string{`"--setting-sources=" "--strict-mcp-config"`, `"--permission-mode" "dontAsk"`, `"--allowedTools" "Read,Grep,Glob"`} {
		if !strings.Contains(got, want) {
			t.Errorf("shim got %q, missing %q", got, want)
		}
	}
}
