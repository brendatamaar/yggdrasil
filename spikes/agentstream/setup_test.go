package agentstream

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestArgs(t *testing.T) {
	c := Config{Prompt: "Summarize README.md", Model: "haiku", MaxBudgetUSD: 0.1, SettingsPath: "C:/run/settings.json", ExtraArgs: []string{"--permission-mode", "default"}}
	want := []string{"-p", "--output-format", "stream-json", "--verbose", "--forward-subagent-text",
		"--settings", "C:/run/settings.json", "--model", "haiku", "--max-budget-usd", "0.1",
		"--permission-mode", "default", "Summarize README.md"}
	if got := c.Args(); !slices.Equal(got, want) {
		t.Errorf("args = %q\nwant   %q", got, want)
	}
	if got := (Config{Prompt: "hi"}).Args(); slices.Contains(got, "--max-budget-usd") || slices.Contains(got, "--model") {
		t.Errorf("zero options added flags: %q", got)
	}
}

func TestStartRefusesFlagPrompt(t *testing.T) {
	if _, err := Start(t.Context(), Config{Binary: "claude", Prompt: "--dangerously-skip-permissions"}); err == nil {
		t.Fatal("prompt starting with '-' accepted")
	}
}

func TestWriteSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	h := HookSettings{Exe: filepath.Join(dir, "agentstream.exe"), LogPath: filepath.Join(dir, "hook.log"), Mode: DenyJSON, MaxDepth: 2, MaxConcurrent: 4}
	if err := WriteSettings(path, h, SubagentEnv(2, 4)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var s settingsFile
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{EventPreToolUse, EventPostToolUse} {
		m := s.Hooks[event]
		if len(m) != 1 || m[0].Matcher != "*" || len(m[0].Hooks) != 1 || m[0].Hooks[0].Type != "command" {
			t.Fatalf("%s hooks = %+v", event, m)
		}
		cmd := m[0].Hooks[0].Command
		for _, part := range []string{" hook ", "--event " + event, "--log \"" + filepath.ToSlash(h.LogPath) + "\"", "--deny-mode json", "--max-depth 2", "--max-concurrent 4"} {
			if !strings.Contains(cmd, part) {
				t.Errorf("%s command %q lacks %q", event, cmd, part)
			}
		}
	}
	if s.Env[EnvMaxSubagentDepth] != "2" || s.Env[EnvMaxConcurrentSubagent] != "4" {
		t.Errorf("env = %v", s.Env)
	}
	if err := WriteSettings(path, HookSettings{Exe: `C:\a "b"\x.exe`, LogPath: "l", Mode: DenyExit2}, nil); err == nil {
		t.Error("quote in exe path accepted")
	}
}

func TestScrubber(t *testing.T) {
	s := NewScrubber(map[string]string{`C:\Users\jdoe`: `C:\Users\dev`, "jdoe": "dev"})
	in := `{"cwd":"C:\\Users\\jdoe\\AppData\\Local\\Temp\\agentstream-1\\acme-shop","path":"C:/Users/jdoe/x","who":"jdoe",` +
		`"session_id":"9F1C2B3A-1111-4222-8333-444455556666","id":"toolu_01AbCdEf","parent_tool_use_id":"toolu_01AbCdEf","m":"msg_01XyZ123"}`
	out := s.Line(in)
	for _, leak := range []string{"jdoe", "9F1C2B3A", "toolu_01AbCdEf", "msg_01XyZ123"} {
		if strings.Contains(out, leak) {
			t.Errorf("%q left in %s", leak, out)
		}
	}
	for _, want := range []string{`C:\\Users\\dev\\AppData`, `C:/Users/dev/x`, `"who":"dev"`, "00000000-0000-4000-8000-000000000001",
		`"id":"toolu_scrub0001","parent_tool_use_id":"toolu_scrub0001"`, "msg_scrub0001"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q missing from %s", want, out)
		}
	}
	// Same id in a later line maps to the same placeholder.
	if got := s.Line(`"toolu_01AbCdEf"`); got != `"toolu_scrub0001"` {
		t.Errorf("second line = %s", got)
	}
	var buf bytes.Buffer
	if err := s.Copy(strings.NewReader("a jdoe\nb\n"), &buf); err != nil || buf.String() != "a dev\nb\n" {
		t.Errorf("copy = %q, %v", buf.String(), err)
	}
}

func TestScratchRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo, err := NewScratchRepo(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(repo, "README.md")); err != nil || !strings.Contains(string(data), "acme-shop") {
		t.Fatalf("README: %q, %v", data, err)
	}
	out, err := exec.Command("git", "-C", repo, "log", "--format=%an %s").Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "Acme Dev Initial acme-shop" {
		t.Errorf("log = %q", got)
	}
	out, err = exec.Command("git", "-C", repo, "status", "--porcelain").Output()
	if err != nil || len(bytes.TrimSpace(out)) != 0 {
		t.Errorf("worktree not clean: %q, %v", out, err)
	}
}
