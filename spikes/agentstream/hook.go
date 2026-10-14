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

// HookInput is what the CLI sends a command hook on stdin. UNVERIFIED: every
// field name. ToolUseID and ParentToolUseID may not exist at all; then every
// spawn counts as depth 1 and concurrency is counted from the log alone.
type HookInput struct {
	SessionID       string          `json:"session_id"`
	TranscriptPath  string          `json:"transcript_path"`
	Cwd             string          `json:"cwd"`
	HookEventName   string          `json:"hook_event_name"`
	ToolName        string          `json:"tool_name"`
	ToolInput       json.RawMessage `json:"tool_input"`
	ToolUseID       string          `json:"tool_use_id"`
	ParentToolUseID string          `json:"parent_tool_use_id"`
}

// DenyMode is how the hook tells the CLI to block a tool call.
type DenyMode string

const (
	// DenyExit2: reason on stderr, exit code 2. UNVERIFIED: exit 2 blocks the
	// call and the CLI shows stderr to the model.
	DenyExit2 DenyMode = "exit2"
	// DenyJSON: exit 0, stdout {"hookSpecificOutput": {"hookEventName":
	// "PreToolUse", "permissionDecision": "deny", ...}}. UNVERIFIED.
	DenyJSON DenyMode = "json"
	// DenyJSONLegacy: exit 0, stdout {"decision": "block", "reason": ...}.
	// UNVERIFIED, older form.
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

// Hook event names. UNVERIFIED.
const (
	EventPreToolUse  = "PreToolUse"
	EventPostToolUse = "PostToolUse"
)

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
	Time         string `json:"time"`
	Event        string `json:"event"`
	SessionID    string `json:"sessionId,omitempty"`
	Tool         string `json:"tool,omitempty"`
	ToolUseID    string `json:"toolUseId,omitempty"`
	ParentID     string `json:"parentId,omitempty"`
	Subagent     bool   `json:"subagent,omitempty"`
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
// log and returns the exit code. It fails closed: if it can't read its input
// or write its log, the call is denied.
func RunHook(opts HookOptions, stdin io.Reader, stdout, stderr io.Writer) int {
	unlock, err := lockFile(opts.LogPath + ".lock")
	if err != nil {
		return deny(opts.Mode, "agentstream hook: "+err.Error(), stdout, stderr)
	}
	defer unlock()
	rec := decide(opts, stdin, time.Now())
	if err := appendRecord(opts.LogPath, rec); err != nil {
		rec.Decision, rec.Reason = DecisionDeny, "agentstream hook: "+err.Error()
	}
	if rec.Decision != DecisionDeny {
		return 0
	}
	return deny(opts.Mode, rec.Reason, stdout, stderr)
}

func decide(opts HookOptions, stdin io.Reader, now time.Time) HookRecord {
	rec := HookRecord{Time: now.UTC().Format(time.RFC3339Nano), Event: opts.Event, Decision: DecisionAllow}
	fail := func(format string, a ...any) HookRecord {
		rec.Decision, rec.Reason = DecisionDeny, "agentstream hook: "+fmt.Sprintf(format, a...)
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
	rec.SessionID, rec.Tool, rec.ToolUseID, rec.ParentID = in.SessionID, in.ToolName, in.ToolUseID, in.ParentToolUseID
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
		return rec
	}
	prior, err := ReadHookLog(opts.LogPath)
	if err != nil {
		return fail("%v", err)
	}
	rec.Depth, rec.Active = spawnDepth(prior, in.ParentToolUseID), activeSubagents(prior)
	switch {
	case sub.WantsWorktree():
		rec.Decision, rec.Reason = DecisionDeny, "Sub-agents with isolation: worktree are blocked inside Yggdrasil tasks; run the sub-agent without isolation."
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

// spawnDepth is 1 + the depth of the allowed spawn whose tool_use id is
// parent, or 1 if there is none.
func spawnDepth(prior []HookRecord, parent string) int {
	if parent == "" {
		return 1
	}
	for _, r := range prior {
		if isSpawn(r) && r.ToolUseID == parent {
			return r.Depth + 1
		}
	}
	return 1
}

// activeSubagents is allowed spawns minus finished ones (PostToolUse on a
// sub-agent tool; UNVERIFIED that it fires when the sub-agent ends).
func activeSubagents(prior []HookRecord) int {
	n := 0
	for _, r := range prior {
		switch {
		case isSpawn(r):
			n++
		case r.Subagent && r.Event == EventPostToolUse:
			n--
		}
	}
	return max(n, 0)
}

func deny(mode DenyMode, reason string, stdout, stderr io.Writer) int {
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
