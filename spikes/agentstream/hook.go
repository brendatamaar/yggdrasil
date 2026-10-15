package agentstream

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"
)

// HookInput is what the CLI sends a command hook on stdin (hooks docs, common
// input fields; Agent SDK PreToolUseHookInput). There is no parent tool_use
// id: a call made inside a sub-agent carries that sub-agent's agent_id (the
// same id as agent_id on its stream messages and task_id on task_started).
type HookInput struct {
	SessionID      string          `json:"session_id"`
	TranscriptPath string          `json:"transcript_path"`
	Cwd            string          `json:"cwd"`
	HookEventName  string          `json:"hook_event_name"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	ToolUseID      string          `json:"tool_use_id"`
	// ToolResponse is on PostToolUse; for the Agent tool it has a status.
	ToolResponse json.RawMessage `json:"tool_response"`
	// AgentID and AgentType are set when the hook fires inside a sub-agent,
	// and on SubagentStart/SubagentStop.
	AgentID   string `json:"agent_id"`
	AgentType string `json:"agent_type"`
}

// DenyMode is how the hook tells the CLI to block a tool call.
type DenyMode string

const (
	// DenyExit2: reason on stderr, exit code 2. Documented: blocks the call
	// and stderr is the reason the model sees.
	DenyExit2 DenyMode = "exit2"
	// DenyJSON: exit 0, stdout {"hookSpecificOutput": {"hookEventName":
	// "PreToolUse", "permissionDecision": "deny", ...}}. Documented.
	DenyJSON DenyMode = "json"
	// DenyJSONLegacy: exit 0, stdout {"decision": "block", "reason": ...}.
	// Documented as deprecated for PreToolUse ("block" maps to "deny").
	DenyJSONLegacy DenyMode = "json-legacy"
)

// ParseDenyMode checks a --deny-mode value.
func ParseDenyMode(s string) (DenyMode, error) {
	switch m := DenyMode(s); m {
	case DenyExit2, DenyJSON, DenyJSONLegacy:
		return m, nil
	}
	return "", fmt.Errorf("deny mode %q: want exit2, json or json-legacy", s)
}

// Hook decisions as logged.
const (
	DecisionAllow = "allow"
	DecisionDeny  = "deny"
	// DecisionLog: not a PreToolUse call, only recorded.
	DecisionLog = "log"
)

// Hook event names, from the hooks docs.
const (
	EventPreToolUse         = "PreToolUse"
	EventPostToolUse        = "PostToolUse"
	EventPostToolUseFailure = "PostToolUseFailure"
	EventSubagentStart      = "SubagentStart"
	EventSubagentStop       = "SubagentStop"
	// EventWorktreeCreate fires for every agent-native worktree, including a
	// sub-agent with isolation: worktree in its definition's frontmatter,
	// which PreToolUse can't see. A failing hook makes creation fail.
	EventWorktreeCreate = "WorktreeCreate"
)

// blocking reports whether the hook enforces on an event. On the others it
// only logs: exit 2 on SubagentStop would keep the sub-agent running.
func blocking(event string) bool {
	return event == "" || event == EventPreToolUse || event == EventWorktreeCreate
}

// HookOptions configure hook mode.
type HookOptions struct {
	LogPath string
	Mode    DenyMode
	// Event overrides hook_event_name from the input (the settings file passes
	// it per hook, in case the input field is named differently).
	Event string
	// MaxDepth and MaxConcurrent cap sub-agent spawns; 0 means no cap. They
	// back up the CLI's own env var limits.
	MaxDepth      int
	MaxConcurrent int
}

// HookRecord is one line of the hook log.
type HookRecord struct {
	Time      string `json:"time"`
	Event     string `json:"event"`
	SessionID string `json:"sessionId,omitempty"`
	Tool      string `json:"tool,omitempty"`
	ToolUseID string `json:"toolUseId,omitempty"`
	AgentID   string `json:"agentId,omitempty"`
	AgentType string `json:"agentType,omitempty"`
	Subagent  bool   `json:"subagent,omitempty"`
	// Async: PostToolUse of a sub-agent that went to the background (it is
	// still running, so it doesn't count as finished).
	Async        bool   `json:"async,omitempty"`
	SubagentType string `json:"subagentType,omitempty"`
	Isolation    string `json:"isolation,omitempty"`
	// Depth and Active are what the caps saw, for sub-agent PreToolUse calls.
	Depth    int    `json:"depth,omitempty"`
	Active   int    `json:"active,omitempty"`
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
	// Input is the hook's stdin when it was valid JSON, for the live run to
	// see which fields really arrive.
	Input json.RawMessage `json:"input,omitempty"`
}

const (
	maxHookInput = 4 << 20
	staleLock    = 30 * time.Second
	lockTimeout  = 10 * time.Second
)

// RunHook is hook mode: it reads one hook call from stdin, appends it to the
// log and returns the exit code. On an enforcing event (PreToolUse,
// WorktreeCreate) it fails closed: if it can't read its input or write its
// log, the call is denied. On the log-only events a failure is exit 1, which
// the CLI treats as a non-blocking error.
func RunHook(opts HookOptions, stdin io.Reader, stdout, stderr io.Writer) int {
	unlock, err := lockFile(opts.LogPath + ".lock")
	if err != nil {
		if !blocking(opts.Event) {
			_, _ = fmt.Fprintln(stderr, "agentstream hook:", err)
			return 1
		}
		return deny(opts.Mode, opts.Event, "agentstream hook: "+err.Error(), stdout, stderr)
	}
	defer unlock()
	rec := decide(opts, stdin, time.Now())
	if err := appendRecord(opts.LogPath, rec); err != nil {
		if !blocking(rec.Event) {
			_, _ = fmt.Fprintln(stderr, "agentstream hook:", err)
			return 1
		}
		rec.Decision, rec.Reason = DecisionDeny, "agentstream hook: "+err.Error()
	}
	if rec.Decision != DecisionDeny {
		return 0
	}
	return deny(opts.Mode, rec.Event, rec.Reason, stdout, stderr)
}

func decide(opts HookOptions, stdin io.Reader, now time.Time) HookRecord {
	rec := HookRecord{Time: now.UTC().Format(time.RFC3339Nano), Event: opts.Event, Decision: DecisionAllow}
	fail := func(format string, a ...any) HookRecord {
		rec.Decision, rec.Reason = DecisionDeny, "agentstream hook: "+fmt.Sprintf(format, a...)
		if !blocking(rec.Event) {
			rec.Decision = DecisionLog
		}
		return rec
	}
	data, err := io.ReadAll(io.LimitReader(stdin, maxHookInput))
	if err != nil {
		return fail("read input: %v", err)
	}
	var in HookInput
	if err := json.Unmarshal(data, &in); err != nil {
		return fail("unreadable input: %v", err)
	}
	rec.Input = json.RawMessage(bytes.TrimSpace(data))
	if rec.Event == "" {
		rec.Event = in.HookEventName
	}
	rec.SessionID, rec.Tool, rec.ToolUseID = in.SessionID, in.ToolName, in.ToolUseID
	rec.AgentID, rec.AgentType = in.AgentID, in.AgentType
	if rec.Event == EventWorktreeCreate {
		rec.Decision, rec.Reason = DecisionDeny, "Agent-native worktrees are blocked inside Yggdrasil tasks; the task already runs in its own worktree."
		return rec
	}
	// An unnamed event is treated as PreToolUse, so a renamed field can't
	// silently turn the block off.
	pre := rec.Event == "" || rec.Event == EventPreToolUse
	if !pre {
		rec.Decision = DecisionLog
	}
	if in.ToolName == "" {
		if pre {
			return fail("no tool_name in input")
		}
		return rec
	}
	if !IsSubagentTool(in.ToolName) {
		return rec
	}
	sub := ParseSubagentInput(in.ToolInput)
	rec.Subagent, rec.SubagentType, rec.Isolation = true, sub.SubagentType, sub.Isolation
	if !pre {
		rec.Async = rec.Event == EventPostToolUse && launchedAsync(in.ToolResponse)
		return rec
	}
	prior, err := ReadHookLog(opts.LogPath)
	if err != nil {
		return fail("%v", err)
	}
	rec.Depth, rec.Active = spawnDepth(in.AgentID), activeSubagents(prior)
	switch {
	case sub.Isolation != "":
		rec.Decision, rec.Reason = DecisionDeny, fmt.Sprintf("Sub-agents with isolation: %s are blocked inside Yggdrasil tasks; run the sub-agent without isolation.", sub.Isolation)
	case opts.MaxDepth > 0 && rec.Depth > opts.MaxDepth:
		rec.Decision, rec.Reason = DecisionDeny, fmt.Sprintf("Sub-agent nesting is limited to %d levels in this task.", opts.MaxDepth)
	case opts.MaxConcurrent > 0 && rec.Active >= opts.MaxConcurrent:
		rec.Decision, rec.Reason = DecisionDeny, fmt.Sprintf("At most %d sub-agents may run at once in this task; wait for one to finish.", opts.MaxConcurrent)
	}
	return rec
}

func isSpawn(r HookRecord) bool {
	return r.Subagent && (r.Event == "" || r.Event == EventPreToolUse) && r.Decision == DecisionAllow
}

// spawnDepth is the depth of a new sub-agent: 1 from the main conversation,
// at least 2 from inside a sub-agent. The hook input has no parent link, so
// deeper levels can't be told apart here; the CLI's own depth env var is
// exact. (Phase 1: the adapter can map agent_id to spawn_depth from the
// stream's task_started messages.)
func spawnDepth(agentID string) int {
	if agentID == "" {
		return 1
	}
	return 2
}

// launchedAsync reports whether an Agent tool_response says the sub-agent
// went on in the background ("async_launched", or "remote_launched").
func launchedAsync(resp json.RawMessage) bool {
	var r struct {
		Status string `json:"status"`
	}
	if len(resp) == 0 || json.Unmarshal(resp, &r) != nil {
		return false
	}
	return r.Status == "async_launched" || r.Status == "remote_launched"
}

// activeSubagents is allowed spawns minus finished ones: PostToolUse (which
// fires when a foreground sub-agent completes) or PostToolUseFailure on a
// sub-agent tool. A background launch never counts as finished, so the count
// can only err high.
func activeSubagents(prior []HookRecord) int {
	n := 0
	for _, r := range prior {
		switch {
		case isSpawn(r):
			n++
		case r.Subagent && !r.Async && (r.Event == EventPostToolUse || r.Event == EventPostToolUseFailure):
			n--
		}
	}
	return max(n, 0)
}

// deny blocks the call. WorktreeCreate always gets exit 2 and no stdout: it
// reads the last stdout line as the worktree path, so JSON there would be
// taken as one.
func deny(mode DenyMode, event, reason string, stdout, stderr io.Writer) int {
	if event == EventWorktreeCreate {
		mode = DenyExit2
	}
	var out any
	switch mode {
	case DenyJSON:
		out = map[string]any{"hookSpecificOutput": map[string]string{
			"hookEventName":            EventPreToolUse,
			"permissionDecision":       "deny",
			"permissionDecisionReason": reason,
		}}
	case DenyJSONLegacy:
		out = map[string]string{"decision": "block", "reason": reason}
	default:
		_, _ = fmt.Fprintln(stderr, reason)
		return 2
	}
	if err := json.NewEncoder(stdout).Encode(out); err != nil {
		// Can't answer in JSON: fall back to exit 2 so the call is still blocked.
		_, _ = fmt.Fprintln(stderr, reason)
		return 2
	}
	return 0
}

func appendRecord(path string, rec HookRecord) error {
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encode log record: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open hook log: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("write hook log: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close hook log: %w", err)
	}
	return nil
}

// ReadHookLog reads the hook log. A missing log is empty; unreadable lines
// are skipped.
func ReadHookLog(path string) ([]HookRecord, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open hook log: %w", err)
	}
	defer f.Close()
	var recs []HookRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), maxHookInput+64<<10)
	for sc.Scan() {
		var r HookRecord
		if json.Unmarshal(sc.Bytes(), &r) == nil {
			recs = append(recs, r)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read hook log: %w", err)
	}
	return recs, nil
}

// lockFile serializes hook calls (the CLI may run hooks for parallel tool
// calls at once) with an exclusive-create lock file. A lock older than
// staleLock is taken over.
func lockFile(path string) (func(), error) {
	deadline := time.Now().Add(lockTimeout)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_ = f.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		// Windows reports a lock file being deleted as access denied.
		if !errors.Is(err, fs.ErrExist) && !errors.Is(err, fs.ErrPermission) {
			return nil, fmt.Errorf("lock hook log: %w", err)
		}
		if st, serr := os.Stat(path); serr == nil && time.Since(st.ModTime()) > staleLock {
			_ = os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("lock hook log: %s still held after %v", path, lockTimeout)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
