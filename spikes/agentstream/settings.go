package agentstream

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Sub-agent env vars. Verified against code.claude.com/docs/en/env-vars and
// /sub-agents (CLI 2.1.299): depth is the number of sub-agent layers below the
// main conversation (default 3, 1 turns nesting off), concurrency is how many
// sub-agents may run at once in a session (default 20). Both need CLI 2.1.217+
// and accept a positive integer only: they can lower a cap but not remove it.
const (
	EnvMaxSubagentDepth      = "CLAUDE_CODE_MAX_SUBAGENT_SPAWN_DEPTH"
	EnvMaxConcurrentSubagent = "CLAUDE_CODE_MAX_CONCURRENT_SUBAGENTS"
	// EnvDisableBackgroundTasks=1 runs every sub-agent in the foreground, so
	// its messages stream while it works and PostToolUse on the Agent call
	// marks its end (a background one returns at once, "async_launched").
	EnvDisableBackgroundTasks = "CLAUDE_CODE_DISABLE_BACKGROUND_TASKS"
)

// SubagentEnv returns the sub-agent env vars; a cap of 0 is left out.
// Background sub-agents are always turned off.
func SubagentEnv(maxDepth, maxConcurrent int) map[string]string {
	env := map[string]string{EnvDisableBackgroundTasks: "1"}
	if maxDepth > 0 {
		env[EnvMaxSubagentDepth] = strconv.Itoa(maxDepth)
	}
	if maxConcurrent > 0 {
		env[EnvMaxConcurrentSubagent] = strconv.Itoa(maxConcurrent)
	}
	return env
}

// HookSettings describe the hook entries of the generated settings file.
type HookSettings struct {
	// Exe is the agentstream binary; hook mode is the same binary.
	Exe           string
	LogPath       string
	Mode          DenyMode
	MaxDepth      int
	MaxConcurrent int
}

// hookTimeoutSec bounds one hook call; the CLI default is 600 s.
const hookTimeoutSec = 30

// Handler is the hook entry for one event, in exec form: with args set the
// CLI spawns Exe directly, no shell, so no quoting (hooks docs, "Exec form and
// shell form"). Enforcing events set onFailure "block" (CLI 2.1.295+): without
// it a hook that can't start, crashes or times out lets the call through.
func (h HookSettings) Handler(event string) (hookCommand, error) {
	for _, p := range []string{h.Exe, h.LogPath} {
		if p == "" || strings.ContainsAny(p, "\"\r\n\x00") {
			return hookCommand{}, fmt.Errorf("path %q can't be put in a hook command", p)
		}
	}
	c := hookCommand{
		Type:    "command",
		Command: h.Exe,
		Args: []string{"hook", "--event", event, "--log", h.LogPath, "--deny-mode", string(h.Mode),
			"--max-depth", strconv.Itoa(h.MaxDepth), "--max-concurrent", strconv.Itoa(h.MaxConcurrent)},
		Timeout: hookTimeoutSec,
	}
	if blocking(event) {
		c.OnFailure = "block"
	}
	return c, nil
}

type settingsFile struct {
	Hooks map[string][]hookMatcher `json:"hooks"`
	Env   map[string]string        `json:"env,omitempty"`
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

// hookEvents are the events registered, with their matcher. Tool events get
// "*": every tool, also inside sub-agents (hooks from settings files fire for
// a sub-agent's tool calls, per /sub-agents). The rest take no matcher.
var hookEvents = []struct{ event, matcher string }{
	{EventPreToolUse, "*"},
	{EventPostToolUse, "*"},
	{EventPostToolUseFailure, "*"},
	{EventSubagentStart, ""},
	{EventSubagentStop, ""},
	{EventWorktreeCreate, ""},
}

// WriteSettings writes the settings file passed with --settings. Verified
// against the hooks and settings docs: shape hooks.<Event>[].{matcher,
// hooks[].{type,command,args}}; --settings merges with the other sources (a
// key overrides, hook entries from every level run), so the runner also passes
// --setting-sources to keep user hooks out.
func WriteSettings(path string, h HookSettings, env map[string]string) error {
	s := settingsFile{Hooks: map[string][]hookMatcher{}, Env: env}
	for _, e := range hookEvents {
		cmd, err := h.Handler(e.event)
		if err != nil {
			return err
		}
		s.Hooks[e.event] = []hookMatcher{{Matcher: e.matcher, Hooks: []hookCommand{cmd}}}
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	return nil
}
