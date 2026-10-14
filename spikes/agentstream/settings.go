package agentstream

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Sub-agent limit env vars. UNVERIFIED names, from PRD AG-22 and this repo's
// .claude/settings.json.
const (
	EnvMaxSubagentDepth      = "CLAUDE_CODE_MAX_SUBAGENT_SPAWN_DEPTH"
	EnvMaxConcurrentSubagent = "CLAUDE_CODE_MAX_CONCURRENT_SUBAGENTS"
)

// SubagentEnv returns the limit env vars; a cap of 0 is left out.
func SubagentEnv(maxDepth, maxConcurrent int) map[string]string {
	env := map[string]string{}
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

// Command is the hook command line for one event. Paths use forward slashes
// and double quotes, which both Git Bash and cmd.exe accept. UNVERIFIED: which
// shell the CLI runs hook commands with on Windows.
func (h HookSettings) Command(event string) (string, error) {
	exe, err := quoteArg(h.Exe)
	if err != nil {
		return "", err
	}
	log, err := quoteArg(h.LogPath)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s hook --event %s --log %s --deny-mode %s --max-depth %d --max-concurrent %d",
		exe, event, log, h.Mode, h.MaxDepth, h.MaxConcurrent), nil
}

func quoteArg(s string) (string, error) {
	if s == "" || strings.ContainsAny(s, "\"\r\n") {
		return "", fmt.Errorf("path %q can't be put in a hook command", s)
	}
	return `"` + filepath.ToSlash(s) + `"`, nil
}

type settingsFile struct {
	Hooks map[string][]hookMatcher `json:"hooks"`
	Env   map[string]string        `json:"env,omitempty"`
}

type hookMatcher struct {
	Matcher string        `json:"matcher"`
	Hooks   []hookCommand `json:"hooks"`
}

type hookCommand struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

// WriteSettings writes the settings file passed with --settings: PreToolUse
// and PostToolUse hooks on every tool (so calls made inside sub-agents show up
// in the log too) and env. UNVERIFIED: the file shape, matcher "*" meaning
// every tool, and that --settings merges with (not replaces) user settings.
func WriteSettings(path string, h HookSettings, env map[string]string) error {
	s := settingsFile{Hooks: map[string][]hookMatcher{}, Env: env}
	for _, event := range []string{EventPreToolUse, EventPostToolUse} {
		cmd, err := h.Command(event)
		if err != nil {
			return err
		}
		s.Hooks[event] = []hookMatcher{{Matcher: "*", Hooks: []hookCommand{{Type: "command", Command: cmd}}}}
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
