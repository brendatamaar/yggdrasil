package claudecode

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"
)

// The hook is how Yggdrasil enforces sub-agent rules inside Claude Code
// (SF-06, AG-22, invariant 11): the CLI runs `ygg hook --event <Name>` for
// each tool call, sub-agent start and stop, and agent-native worktree
// creation. It denies by exiting 2 with the reason on stderr.
//
// What the hook may know is passed in a small JSON file whose path is in the
// environment variable HookConfigEnv, never in the command line: a denied
// call shows the model (and so the transcript) the whole hook command line.
// If the configuration is missing, an enforcing event is denied (fail closed).

// HookConfigEnv names the environment variable holding the path of the hook
// configuration file.
const HookConfigEnv = "YGG_HOOK_CONFIG"

// HookConfig is the content of the hook configuration file.
type HookConfig struct {
	// LogPath is the file the hook appends one JSON line per call to.
	LogPath string `json:"logPath"`
	// MaxDepth and MaxConcurrent cap sub-agent spawns; 0 means no cap. They
	// back up the CLI's own environment limits.
	MaxDepth      int `json:"maxDepth"`
	MaxConcurrent int `json:"maxConcurrent"`
}

// Hook event names (Claude Code hooks documentation).
const (
	eventPreToolUse         = "PreToolUse"
	eventPostToolUse        = "PostToolUse"
	eventPostToolUseFailure = "PostToolUseFailure"
	eventSubagentStart      = "SubagentStart"
	eventSubagentStop       = "SubagentStop"
	// eventWorktreeCreate fires for every agent-native worktree, including one
	// a sub-agent's own definition asks for, which PreToolUse cannot see.
	eventWorktreeCreate = "WorktreeCreate"
)

// enforcing reports whether the hook blocks on an event. The others only log:
// exit 2 on SubagentStop would keep the sub-agent running.
func enforcing(event string) bool {
	return event == "" || event == eventPreToolUse || event == eventWorktreeCreate
}

type hookInput struct {
	HookEventName string          `json:"hook_event_name"`
	SessionID     string          `json:"session_id"`
	ToolName      string          `json:"tool_name"`
	ToolInput     json.RawMessage `json:"tool_input"`
	ToolUseID     string          `json:"tool_use_id"`
	ToolResponse  json.RawMessage `json:"tool_response"`
	// AgentID and AgentType are set when the call happens inside a sub-agent.
	AgentID   string `json:"agent_id"`
	AgentType string `json:"agent_type"`
}

const (
	decisionAllow = "allow"
	decisionDeny  = "deny"
	decisionLog   = "log"
)

// hookRecord is one line of the hook log. It keeps no tool input.
type hookRecord struct {
	Time      string `json:"time"`
	Event     string `json:"event"`
	Tool      string `json:"tool,omitempty"`
	ToolUseID string `json:"toolUseId,omitempty"`
	AgentID   string `json:"agentId,omitempty"`
	Subagent  bool   `json:"subagent,omitempty"`
	// Async: PostToolUse of a sub-agent that went to the background (it is
	// still running, so it does not count as finished).
	Async     bool   `json:"async,omitempty"`
	Isolation string `json:"isolation,omitempty"`
	Depth     int    `json:"depth,omitempty"`
	Active    int    `json:"active,omitempty"`
	Decision  string `json:"decision"`
	Reason    string `json:"reason,omitempty"`
}

const (
	maxHookInput = 4 << 20
	staleLock    = 30 * time.Second
	lockTimeout  = 10 * time.Second
)

// RunHook is hook mode. args are the arguments after "hook" (--event NAME);
// getenv reads the environment. It returns the exit code: 0 allows, 2 blocks
// (the reason is on stderr). On an enforcing event any failure to read the
// input, the configuration or to write the log blocks the call; on the other
// events a failure is exit 1, which the CLI treats as non-blocking.
func RunHook(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	_ = stdout // the hook answers with its exit code only
	event := ""
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--event" {
			event = args[i+1]
		}
	}
	fail := func(reason string) int {
		_, _ = fmt.Fprintln(stderr, "Yggdrasil hook: "+reason)
		if enforcing(event) {
			return 2
		}
		return 1
	}
	cfg, err := readHookConfig(getenv(HookConfigEnv))
	if err != nil {
		return fail(err.Error())
	}
	unlock, err := lockFile(cfg.LogPath + ".lock")
	if err != nil {
		return fail(err.Error())
	}
	defer unlock()

	rec := decide(cfg, event, stdin, time.Now())
	if err := appendRecord(cfg.LogPath, rec); err != nil {
		if !enforcing(rec.Event) {
			return fail(err.Error())
		}
		rec.Decision, rec.Reason = decisionDeny, "Yggdrasil hook: "+err.Error()
	}
	if rec.Decision != decisionDeny {
		return 0
	}
	_, _ = fmt.Fprintln(stderr, rec.Reason)
	return 2
}

func readHookConfig(path string) (HookConfig, error) {
	if path == "" {
		return HookConfig{}, errors.New("its configuration is missing, so the call is blocked")
	}
	data, err := os.ReadFile(path) //nolint:gosec // the path is Yggdrasil's own hook configuration, set in the child's environment
	if err != nil {
		return HookConfig{}, errors.New("its configuration can't be read, so the call is blocked")
	}
	var cfg HookConfig
	if err := json.Unmarshal(data, &cfg); err != nil || cfg.LogPath == "" {
		return HookConfig{}, errors.New("its configuration is not valid, so the call is blocked")
	}
	return cfg, nil
}

func decide(cfg HookConfig, event string, stdin io.Reader, now time.Time) hookRecord {
	rec := hookRecord{Time: now.UTC().Format(time.RFC3339Nano), Event: event, Decision: decisionAllow}
	deny := func(format string, a ...any) hookRecord {
		rec.Decision, rec.Reason = decisionDeny, fmt.Sprintf(format, a...)
		if !enforcing(rec.Event) {
			rec.Decision = decisionLog
		}
		return rec
	}
	data, err := io.ReadAll(io.LimitReader(stdin, maxHookInput))
	if err != nil {
		return deny("Yggdrasil hook: the call could not be read, so it is blocked.")
	}
	var in hookInput
	if err := json.Unmarshal(data, &in); err != nil {
		return deny("Yggdrasil hook: the call could not be understood, so it is blocked.")
	}
	if rec.Event == "" {
		rec.Event = in.HookEventName
	}
	rec.Tool, rec.ToolUseID, rec.AgentID = in.ToolName, in.ToolUseID, in.AgentID

	if rec.Event == eventWorktreeCreate {
		rec.Decision = decisionDeny
		rec.Reason = "Agent-native worktrees are blocked inside Yggdrasil tasks; the task already runs in its own workspace."
		return rec
	}
	// An unnamed event counts as PreToolUse, so a renamed field cannot
	// silently switch enforcement off.
	pre := rec.Event == "" || rec.Event == eventPreToolUse
	if !pre {
		rec.Decision = decisionLog
	}
	if in.ToolName == "" {
		if pre {
			return deny("Yggdrasil hook: the call names no tool, so it is blocked.")
		}
		return rec
	}
	if !isSubagentTool(in.ToolName) {
		return rec
	}
	var sub struct {
		Isolation string `json:"isolation"`
	}
	_ = json.Unmarshal(in.ToolInput, &sub)
	rec.Subagent, rec.Isolation = true, sub.Isolation
	if !pre {
		rec.Async = rec.Event == eventPostToolUse && launchedAsync(in.ToolResponse)
		return rec
	}
	prior, err := readHookLog(cfg.LogPath)
	if err != nil {
		return deny("Yggdrasil hook: its log can't be read, so the sub-agent is blocked.")
	}
	rec.Depth, rec.Active = spawnDepth(in.AgentID), activeSubagents(prior)
	switch {
	case sub.Isolation != "":
		return deny("Sub-agents with isolation (%s) are blocked inside Yggdrasil tasks; run the sub-agent without isolation.", sub.Isolation)
	case cfg.MaxDepth > 0 && rec.Depth > cfg.MaxDepth:
		return deny("Sub-agent nesting is limited to %d levels in this task.", cfg.MaxDepth)
	case cfg.MaxConcurrent > 0 && rec.Active >= cfg.MaxConcurrent:
		return deny("At most %d sub-agents may run at once in this task; wait for one to finish.", cfg.MaxConcurrent)
	}
	return rec
}

// spawnDepth is the depth of a new sub-agent: 1 from the main conversation,
// at least 2 from inside a sub-agent. The hook input has no parent link, so
// deeper levels cannot be told apart here; the CLI's own depth variable is
// exact.
func spawnDepth(agentID string) int {
	if agentID == "" {
		return 1
	}
	return 2
}

func launchedAsync(resp json.RawMessage) bool {
	var r struct {
		Status string `json:"status"`
	}
	if len(resp) == 0 || json.Unmarshal(resp, &r) != nil {
		return false
	}
	return r.Status == "async_launched" || r.Status == "remote_launched"
}

func isSpawn(r hookRecord) bool {
	return r.Subagent && (r.Event == "" || r.Event == eventPreToolUse) && r.Decision == decisionAllow
}

// activeSubagents is allowed spawns minus finished ones. A background launch
// never counts as finished, so the count can only err high.
func activeSubagents(prior []hookRecord) int {
	n := 0
	for _, r := range prior {
		switch {
		case isSpawn(r):
			n++
		case r.Subagent && !r.Async && (r.Event == eventPostToolUse || r.Event == eventPostToolUseFailure):
			n--
		}
	}
	return max(n, 0)
}

func appendRecord(path string, rec hookRecord) error {
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encode its log record: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // the log path comes from the hook configuration
	if err != nil {
		return fmt.Errorf("open its log: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("write its log: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close its log: %w", err)
	}
	return nil
}

// readHookLog reads the log. A missing log is empty; unreadable lines are
// skipped.
func readHookLog(path string) ([]hookRecord, error) {
	f, err := os.Open(path) //nolint:gosec // the log path comes from the hook configuration
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var recs []hookRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var r hookRecord
		if json.Unmarshal(sc.Bytes(), &r) == nil {
			recs = append(recs, r)
		}
	}
	return recs, sc.Err()
}

// lockFile serializes hook calls (the CLI may run hooks for parallel tool
// calls at once) with an exclusive-create lock file. A lock older than
// staleLock is taken over.
func lockFile(path string) (func(), error) {
	deadline := time.Now().Add(lockTimeout)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // the lock path derives from the hook configuration
		if err == nil {
			_ = f.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		// Windows reports a lock file being deleted as access denied.
		if !errors.Is(err, fs.ErrExist) && !errors.Is(err, fs.ErrPermission) {
			return nil, errors.New("its log can't be locked, so the call is blocked")
		}
		if st, serr := os.Stat(path); serr == nil && time.Since(st.ModTime()) > staleLock {
			_ = os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, errors.New("its log stayed locked too long, so the call is blocked")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
