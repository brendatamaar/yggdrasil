package claudecode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Sub-agent environment variables (docs/spikes.md B4, CLI 2.1.299): depth is
// the number of sub-agent layers below the main conversation, concurrency how
// many sub-agents may run at once. Both accept a positive integer only, so
// they can lower a cap but not remove it.
const (
	envMaxSubagentDepth       = "CLAUDE_CODE_MAX_SUBAGENT_SPAWN_DEPTH"
	envMaxConcurrentSubagents = "CLAUDE_CODE_MAX_CONCURRENT_SUBAGENTS"
	// envDisableBackground=1 runs every sub-agent in the foreground, so its
	// messages stream while it works and its end is visible.
	envDisableBackground = "CLAUDE_CODE_DISABLE_BACKGROUND_TASKS"
)

// askTools are the only tools an Ask-mode run may use: read-only. The hook
// judges every call through core/guard too; the flags below only ask.
var askTools = []string{"Read", "Grep", "Glob"}

// askDisallowed are tools named in --disallowedTools as a second request.
var askDisallowed = []string{"Edit", "Write", "MultiEdit", "NotebookEdit", "Bash", "PowerShell"}

// codeTools are the tools the guard can judge in the writing modes. Bash and
// PowerShell are both listed: which one the CLI offers depends on the machine,
// and the hook judges either. NotebookEdit and the web tools stay off.
var codeTools = []string{"Read", "Grep", "Glob", "Edit", "Write", "MultiEdit", "Bash", "PowerShell", "Agent"}

// codeDisallowed are named in --disallowedTools as a second request.
var codeDisallowed = []string{"NotebookEdit", "WebFetch", "WebSearch"}

// hookTimeoutSec bounds one hook call; the CLI default is 600 s.
const hookTimeoutSec = 30

// buildArgs returns the claude arguments. Each flag is in `claude --help`
// (2.1.299). The prompt is not among them: it goes to the CLI on stdin. A
// prompt is free text, and on Windows the CLI is often an npm shim (claude.cmd)
// run by cmd.exe, which refuses or misreads quotes, percent signs, & and line
// breaks in arguments and has a line length limit. Nothing left on the command
// line comes from the person: flags, a validated model name, a session ID and
// our own file path.
//
// The flags are a second layer: they ask the CLI to refuse what the hook would
// refuse. The hook, which judges every call with core/guard, is the
// enforcement (docs/AGENTS.md 6, invariant 5). The only paths on the command
// line are the settings file, the MCP config file and the task's extra
// folders (project mode).
func buildArgs(settingsPath, mcpPath string, o agents.StartOptions) []string {
	allowed, disallowed := askTools, askDisallowed // anything but a writing mode is read-only
	switch o.Mode {
	case core.ModeCode, core.ModeProject, core.ModeCurrentBranch:
		allowed, disallowed = codeTools, codeDisallowed
	}
	args := []string{
		"-p", "--output-format", "stream-json", "--verbose", "--forward-subagent-text",
		"--settings", settingsPath,
		// An empty source list loads no user, project or local settings, so
		// no repo or user hook, MCP server, agent, skill or command runs
		// (docs/spikes.md B5). Repo config is inert in every mode.
		"--setting-sources", "",
		// Only our MCP file loads: none of the repo's, none of the account's
		// connectors. The file is empty unless the run has Yggdrasil's server.
		"--strict-mcp-config", "--mcp-config", mcpPath,
		"--permission-mode", "dontAsk",
		"--allowedTools", strings.Join(allowed, ","),
		"--disallowedTools", strings.Join(disallowed, ","),
	}
	for _, d := range o.ExtraDirs {
		args = append(args, "--add-dir", d)
	}
	if o.Model != "" {
		args = append(args, "--model", o.Model)
	}
	if o.BudgetUSD > 0 {
		args = append(args, "--max-budget-usd", strconv.FormatFloat(o.BudgetUSD, 'f', -1, 64))
	}
	if o.ResumeSessionID != "" {
		args = append(args, "--resume", o.ResumeSessionID)
	}
	return args
}

// buildEnv returns the child environment: the clean environment plus the
// sub-agent limits and the hook configuration path. The input is not changed.
func buildEnv(base []string, o agents.StartOptions, hookConfigPath string) []string {
	extra := map[string]string{
		envDisableBackground: "1",
		HookConfigEnv:        hookConfigPath,
	}
	if o.MaxSubagentDepth > 0 {
		extra[envMaxSubagentDepth] = strconv.Itoa(o.MaxSubagentDepth)
	}
	if o.MaxConcurrentSubagents > 0 {
		extra[envMaxConcurrentSubagents] = strconv.Itoa(o.MaxConcurrentSubagents)
	}
	out := make([]string, 0, len(base)+len(extra))
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		replaced := false
		for k := range extra {
			if strings.EqualFold(name, k) {
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, kv)
		}
	}
	for k, v := range extra {
		out = append(out, k+"="+v)
	}
	return out
}

type settingsFile struct {
	// DisableAllHooks is always written, as false: a repo setting of true would
	// otherwise switch our hook off (docs/spikes.md B5, runs A and B).
	DisableAllHooks bool                     `json:"disableAllHooks"`
	Hooks           map[string][]hookMatcher `json:"hooks"`
}

type hookMatcher struct {
	Matcher string        `json:"matcher,omitempty"`
	Hooks   []hookCommand `json:"hooks"`
}

type hookCommand struct {
	Type      string   `json:"type"`
	Command   string   `json:"command"`
	Args      []string `json:"args"`
	Timeout   int      `json:"timeout,omitempty"`
	OnFailure string   `json:"onFailure,omitempty"`
}

// hookEvents are the registered events. Tool events take "*": every tool,
// also inside sub-agents (hooks from settings files fire for a sub-agent's
// tool calls). The rest take no matcher.
var hookEvents = []struct{ event, matcher string }{
	{eventPreToolUse, "*"},
	{eventPostToolUse, "*"},
	{eventPostToolUseFailure, "*"},
	{eventSubagentStart, ""},
	{eventSubagentStop, ""},
	{eventWorktreeCreate, ""},
}

// writeConfigFiles writes the --settings file and the hook configuration into
// dir and returns their paths. The hook runs in exec form (command + args, no
// shell). Its arguments hold only the event name: no paths, no settings, no
// limits, because a denial shows the model the full command line. Enforcing
// events set onFailure "block" (CLI 2.1.295+); without it a hook that cannot
// start, crashes or times out lets the call through.
func writeConfigFiles(dir, hookExe string, cfg HookConfig) (settingsPath, hookConfigPath string, err error) {
	if hookExe == "" || !filepath.IsAbs(hookExe) || strings.ContainsAny(hookExe, "\"\r\n\x00") {
		return "", "", fmt.Errorf("the hook executable %q can't be put in a settings file", hookExe)
	}
	s := settingsFile{Hooks: map[string][]hookMatcher{}}
	for _, e := range hookEvents {
		c := hookCommand{Type: "command", Command: hookExe, Args: []string{"hook", "--event", e.event}, Timeout: hookTimeoutSec}
		if enforcing(e.event) {
			c.OnFailure = "block"
		}
		s.Hooks[e.event] = []hookMatcher{{Matcher: e.matcher, Hooks: []hookCommand{c}}}
	}
	settingsPath = filepath.Join(dir, "settings.json")
	hookConfigPath = filepath.Join(dir, "hook.json")
	if err := writeJSON(settingsPath, s); err != nil {
		return "", "", err
	}
	if err := writeJSON(hookConfigPath, cfg); err != nil {
		return "", "", err
	}
	return settingsPath, hookConfigPath, nil
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", filepath.Base(path), err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	return nil
}
