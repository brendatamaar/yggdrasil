//go:build windows

package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/agents"
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

// On Windows `claude` is often an npm shim, claude.cmd, in a folder with a
// space (C:\Program Files\nodejs). The whole chain has to work: adapter,
// platform.Start, cmd.exe, the program behind the shim, with the prompt on
// stdin. This is the shape of the failure seen with the real CLI
// ('C:\Program' is not recognized).
func TestRunThroughACmdShimInAFolderWithSpaces(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Program Files", "nodejs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(dir, "claude.cmd")
	body := "@echo off\r\n\"" + testExe(t) + "\" %*\r\n"
	if err := os.WriteFile(shim, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	a := New(Options{Binary: shim, HookExecutable: testExe(t), TempDir: t.TempDir()})
	out := filepath.Join(t.TempDir(), "capture")
	o := startOpts(t, "capture", map[string]string{fakeOut: out})
	o.Prompt = `Read "README.md" & tell me what it says (100%)`
	res, err := agents.Execute(context.Background(), a, o, &collectSink{})
	if err != nil || res.Status != agents.RunCompleted {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if got := read(t, out+".stdin"); got != o.Prompt {
		t.Errorf("stdin = %q, want %q", got, o.Prompt)
	}
	argv := strings.Split(read(t, out+".args"), "\x00")
	if !slices.Contains(argv, "--forward-subagent-text") || !slices.Contains(argv, "-p") {
		t.Errorf("arguments through the shim = %q", argv)
	}
	// The settings path (a folder with a hyphen and digits, under the temp
	// folder) arrived as one argument.
	if i := slices.Index(argv, "--settings"); i < 0 || !strings.HasSuffix(argv[i+1], "settings.json") {
		t.Errorf("--settings through the shim = %q", argv)
	}
}
