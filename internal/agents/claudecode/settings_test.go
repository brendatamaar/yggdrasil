package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/agents"
)

func TestBuildArgs(t *testing.T) {
	o := agents.StartOptions{Prompt: "-x look at the cart"}
	args := buildArgs(`C:\tmp\settings.json`, o)
	want := []string{
		"-p", "--output-format", "stream-json", "--verbose", "--forward-subagent-text",
		"--settings", `C:\tmp\settings.json`,
		"--setting-sources", "project",
		"--permission-mode", "dontAsk",
		"--allowedTools", "Read,Grep,Glob",
		"--disallowedTools", "Edit,Write,MultiEdit,NotebookEdit,Bash,PowerShell",
	}
	if !slices.Equal(args, want) {
		t.Errorf("args =\n%q\nwant\n%q", args, want)
	}
	// The prompt is free text and is never an argument: it goes on stdin.
	for _, a := range args {
		if strings.Contains(a, "look at the cart") {
			t.Errorf("the prompt is on the command line: %q", a)
		}
	}

	o.Model, o.BudgetUSD, o.ResumeSessionID = "haiku", 0.25, "sess-1"
	args = buildArgs("s.json", o)
	for _, pair := range [][2]string{{"--model", "haiku"}, {"--max-budget-usd", "0.25"}, {"--resume", "sess-1"}} {
		i := slices.Index(args, pair[0])
		if i < 0 || args[i+1] != pair[1] {
			t.Errorf("missing %s %s in %q", pair[0], pair[1], args)
		}
	}
	// Nothing left on the command line holds a character a .cmd shim would
	// misread (cmd.exe runs claude.cmd): quotes, percent signs, & and so on.
	for _, a := range args {
		if strings.ContainsAny(a, "\"%&|<>^!\r\n") {
			t.Errorf("argument %q would be refused by cmd.exe", a)
		}
	}
	// Ask mode never lists a tool that writes or runs commands.
	for _, tool := range askTools {
		if slices.Contains([]string{"Bash", "Edit", "Write", "MultiEdit", "NotebookEdit", "PowerShell"}, tool) {
			t.Errorf("Ask mode allows %s", tool)
		}
	}
}

func TestBuildEnv(t *testing.T) {
	base := []string{"PATH=C:\\bin", "Claude_Code_Disable_Background_Tasks=0", "HOME=C:\\home"}
	o := agents.StartOptions{MaxSubagentDepth: 2, MaxConcurrentSubagents: 4}
	env := buildEnv(base, o, `C:\tmp\hook.json`)

	got := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if _, dup := got[strings.ToUpper(k)]; dup {
			t.Errorf("duplicate variable %s", k)
		}
		got[strings.ToUpper(k)] = v
	}
	for k, v := range map[string]string{
		"PATH": `C:\bin`, "HOME": `C:\home`, HookConfigEnv: `C:\tmp\hook.json`,
		envMaxSubagentDepth: "2", envMaxConcurrentSubagents: "4",
		envDisableBackground: "1", // replaced the inherited 0, whatever its case
	} {
		if got[strings.ToUpper(k)] != v {
			t.Errorf("%s = %q, want %q", k, got[strings.ToUpper(k)], v)
		}
	}
	// The caller's slice is not changed.
	if base[1] != "Claude_Code_Disable_Background_Tasks=0" || len(base) != 3 {
		t.Errorf("base was modified: %q", base)
	}
	// A zero cap leaves the variable out, so the CLI's own default applies.
	env = buildEnv(base, agents.StartOptions{}, "h.json")
	for _, kv := range env {
		if strings.HasPrefix(kv, envMaxSubagentDepth) || strings.HasPrefix(kv, envMaxConcurrentSubagents) {
			t.Errorf("zero cap set %s", kv)
		}
	}
}

func TestWriteConfigFiles(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "ygg.exe")
	settings, hookCfg, err := writeConfigFiles(dir, exe, HookConfig{LogPath: filepath.Join(dir, "hook.log"), MaxDepth: 2, MaxConcurrent: 4})
	if err != nil {
		t.Fatal(err)
	}
	var s settingsFile
	data, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	for _, e := range []string{"PreToolUse", "PostToolUse", "PostToolUseFailure", "SubagentStart", "SubagentStop", "WorktreeCreate"} {
		ms := s.Hooks[e]
		if len(ms) != 1 || len(ms[0].Hooks) != 1 {
			t.Fatalf("hook entry for %s = %+v", e, ms)
		}
		h := ms[0].Hooks[0]
		if h.Type != "command" || h.Command != exe {
			t.Errorf("%s: command = %q", e, h.Command)
		}
		// Exec form, and nothing but the event name in the arguments.
		if !slices.Equal(h.Args, []string{"hook", "--event", e}) {
			t.Errorf("%s: args = %q", e, h.Args)
		}
		wantBlock := e == "PreToolUse" || e == "WorktreeCreate"
		if (h.OnFailure == "block") != wantBlock {
			t.Errorf("%s: onFailure = %q, want block=%v", e, h.OnFailure, wantBlock)
		}
		if h.Timeout <= 0 {
			t.Errorf("%s: no timeout", e)
		}
	}
	if s.Hooks["PreToolUse"][0].Matcher != "*" || s.Hooks["WorktreeCreate"][0].Matcher != "" {
		t.Errorf("matchers: %+v", s.Hooks)
	}
	// No path, limit or setting other than the executable is in the settings.
	if strings.Contains(string(data), "hook.log") || strings.Contains(string(data), "maxDepth") {
		t.Errorf("settings file leaks the hook configuration: %s", data)
	}

	var cfg HookConfig
	raw, err := os.ReadFile(hookCfg)
	if err != nil || json.Unmarshal(raw, &cfg) != nil || cfg.MaxDepth != 2 || cfg.MaxConcurrent != 4 || !strings.HasSuffix(cfg.LogPath, "hook.log") {
		t.Errorf("hook config = %s, %v", raw, err)
	}
}

func TestWriteConfigFilesRefusesBadExecutable(t *testing.T) {
	for _, exe := range []string{"", "ygg.exe", "./ygg", `C:\a"b\ygg.exe`, "C:\\a\nb\\ygg.exe"} {
		if _, _, err := writeConfigFiles(t.TempDir(), exe, HookConfig{LogPath: "x"}); err == nil {
			t.Errorf("executable %q was accepted", exe)
		}
	}
}
