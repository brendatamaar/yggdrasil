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
	c := Config{Prompt: "Summarize README.md", Model: "haiku", MaxBudgetUSD: 0.1, SettingsPath: "C:/run/settings.json",
		SettingSources: "project", PermissionMode: "dontAsk", ExtraArgs: []string{"--effort", "low"}}
	want := []string{"-p", "--output-format", "stream-json", "--verbose", "--forward-subagent-text",
		"--settings", "C:/run/settings.json", "--setting-sources", "project", "--permission-mode", "dontAsk",
		"--model", "haiku", "--max-budget-usd", "0.1", "--effort", "low", "--", "Summarize README.md"}
	if got := c.Args(); !slices.Equal(got, want) {
		t.Errorf("args = %q\nwant   %q", got, want)
	}
	got := (Config{Prompt: "hi"}).Args()
	for _, flag := range []string{"--max-budget-usd", "--model", "--settings", "--setting-sources", "--permission-mode"} {
		if slices.Contains(got, flag) {
			t.Errorf("zero options added %s: %q", flag, got)
		}
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
	if len(s.Hooks) != len(hookEvents) {
		t.Errorf("events = %v", s.Hooks)
	}
	for _, e := range hookEvents {
		m := s.Hooks[e.event]
		if len(m) != 1 || m[0].Matcher != e.matcher || len(m[0].Hooks) != 1 || m[0].Hooks[0].Type != "command" {
			t.Fatalf("%s hooks = %+v", e.event, m)
		}
		c := m[0].Hooks[0]
		// Exec form: the exe as is, every argument its own element.
		wantArgs := []string{"hook", "--event", e.event, "--log", h.LogPath, "--deny-mode", "json", "--max-depth", "2", "--max-concurrent", "4"}
		if c.Command != h.Exe || !slices.Equal(c.Args, wantArgs) || c.Timeout != hookTimeoutSec {
			t.Errorf("%s handler = %+v", e.event, c)
		}
		if want := map[bool]string{true: "block", false: ""}[blocking(e.event)]; c.OnFailure != want {
			t.Errorf("%s onFailure = %q, want %q", e.event, c.OnFailure, want)
		}
	}
	if s.Hooks[EventPreToolUse][0].Hooks[0].OnFailure != "block" || s.Hooks[EventWorktreeCreate][0].Hooks[0].OnFailure != "block" {
		t.Error("enforcing hooks must fail closed")
	}
	if !strings.Contains(string(data), `"args"`) {
		t.Error("args missing: the CLI would run the command through a shell")
	}
	if s.Env[EnvMaxSubagentDepth] != "2" || s.Env[EnvMaxConcurrentSubagent] != "4" || s.Env[EnvDisableBackgroundTasks] != "1" {
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

func TestScrubberRedactsAccountAndAgentIDs(t *testing.T) {
	s := NewScrubber(nil)
	init := s.Line(`{"type":"system","subtype":"init","model":"claude-haiku","mcp_servers":[{"name":"claude.ai Mail","status":"connected"}],` +
		`"plugins":[{"name":"acme-internal","path":"x"}],"skills":["s1"],"memory_paths":["m"],"messaging_socket_path":"p"}`)
	for _, leak := range []string{"claude.ai Mail", "acme-internal", "s1", `"m"`, `"p"`} {
		if strings.Contains(init, leak) {
			t.Errorf("%s left in %s", leak, init)
		}
	}
	if !strings.Contains(init, `"model":"claude-haiku"`) || !strings.Contains(init, `"mcp_servers":[]`) {
		t.Errorf("init = %s", init)
	}
	if cc := s.Line(`{"type":"system","subtype":"commands_changed","commands":[{"name":"claude.ai Mail:compose (MCP)"}]}`); strings.Contains(cc, "Mail") {
		t.Errorf("commands left in %s", cc)
	}
	rl := s.Line(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","unifiedWindows":{"five_hour":{"utilization":0.41}}}}`)
	if strings.Contains(rl, "0.41") || !strings.Contains(rl, `"resetsAt":1700000000`) {
		t.Errorf("rate limit = %s", rl)
	}
	ids := s.Line(`{"type":"system","subtype":"task_progress","task_id":"a315afbcd73855512","run_id":"0mv95pkpb-e0bcc57c","agent_id":"a315afbcd73855512"}`)
	if want := `{"type":"system","subtype":"task_progress","task_id":"a0000000000000001","run_id":"0scrub001-00000001","agent_id":"a0000000000000001"}`; ids != want {
		t.Errorf("ids = %s\nwant  %s", ids, want)
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
