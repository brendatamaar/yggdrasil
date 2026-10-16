package claudecode

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/agents"
)

// Limits that keep one runaway line or tool result from filling memory or the
// database.
const (
	maxLineBytes = 16 << 20
	maxTextBytes = 64 << 10
)

// The shapes of the stream-json lines the parser reads (docs/spikes.md B4).
// Everything else in a line, in particular the init message, the command
// list and rate-limit events (account data), is never decoded or kept.
type rawLine struct {
	Type            string          `json:"type"`
	Subtype         string          `json:"subtype"`
	SessionID       string          `json:"session_id"`
	ParentToolUseID *string         `json:"parent_tool_use_id"`
	AgentID         string          `json:"agent_id"`
	Message         json.RawMessage `json:"message"` // an object, or a string on permission_denied
	ToolName        string          `json:"tool_name"`
	ToolUseID       string          `json:"tool_use_id"`
	TaskID          string          `json:"task_id"`
	IsError         bool            `json:"is_error"`
	TotalCostUSD    *float64        `json:"total_cost_usd"`
	DurationMS      int64           `json:"duration_ms"`
	NumTurns        int             `json:"num_turns"`
}

type rawMessage struct {
	ID      string          `json:"id"`
	Content json.RawMessage `json:"content"` // a string or a list of blocks
	Usage   *rawUsage       `json:"usage"`
}

type rawBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

type rawUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
}

// parser turns stream lines into normalized events. Use one per run; it is
// not safe for concurrent use.
type parser struct {
	dir string           // the worktree, to show file paths relative to it
	now func() time.Time // set by tests

	session   string
	seenMsg   map[string]bool   // message IDs whose usage was already counted
	subagents map[string]bool   // tool_use IDs of sub-agent calls
	agentOf   map[string]string // sub-agent tool_use ID -> agent ID
	startOf   map[string]string // agent ID -> its sub-agent tool_use ID
	bad       int               // malformed lines reported so far

	sawResult bool
	resultOK  bool
}

func newParser(dir string) *parser {
	return &parser{
		dir: dir, now: time.Now,
		seenMsg: map[string]bool{}, subagents: map[string]bool{}, agentOf: map[string]string{}, startOf: map[string]string{},
	}
}

// maxBadLines caps the error events for unreadable lines.
const maxBadLines = 20

// parseLine converts one physical line. Unknown line types yield no events;
// an unreadable line yields one plain error event.
func (p *parser) parseLine(line []byte) []agents.Event {
	line = trimEOL(line)
	if len(strings.TrimSpace(string(line))) == 0 {
		return nil
	}
	var rl rawLine
	if err := json.Unmarshal(line, &rl); err != nil {
		return p.unreadable("The agent sent a line that couldn't be read.")
	}
	if rl.SessionID != "" {
		p.session = rl.SessionID
	}
	base := agents.Event{At: p.now().UTC(), AgentID: rl.AgentID}
	if rl.ParentToolUseID != nil {
		base.ParentID = *rl.ParentToolUseID
	}
	// System lines of a sub-agent carry its agent ID but no parent link.
	if base.ParentID == "" && base.AgentID != "" {
		base.ParentID = p.startOf[base.AgentID]
	}

	switch rl.Type {
	case "system":
		switch rl.Subtype {
		case "task_started":
			// The only use of this line: which agent runs a sub-agent call.
			if rl.ToolUseID != "" && rl.TaskID != "" {
				p.agentOf[rl.ToolUseID] = rl.TaskID
				p.startOf[rl.TaskID] = rl.ToolUseID
			}
		case "permission_denied":
			ev := base
			ev.Kind, ev.ResultFor, ev.IsError = agents.EventError, rl.ToolUseID, true
			ev.Error = fmt.Sprintf("The agent wasn't allowed to use %s here.", safeName(rl.ToolName))
			return []agents.Event{ev}
		}
		return nil // init, commands_changed, task_*, thinking_tokens...: not kept
	case "assistant", "user":
		if len(rl.Message) == 0 || string(rl.Message) == "null" {
			return nil
		}
		var m rawMessage
		if err := json.Unmarshal(rl.Message, &m); err != nil {
			return p.unreadable("The agent sent a message that couldn't be read.")
		}
		return p.message(base, rl.Type, &m)
	case "result":
		return p.result(base, &rl)
	}
	return nil // rate_limit_event, tool_progress and anything new
}

func (p *parser) unreadable(msg string) []agents.Event {
	p.bad++
	if p.bad > maxBadLines {
		return nil
	}
	return []agents.Event{{Kind: agents.EventError, At: p.now().UTC(), Error: msg}}
}

func (p *parser) message(base agents.Event, role string, m *rawMessage) []agents.Event {
	var events []agents.Event
	var text string
	var blocks []rawBlock
	switch {
	case len(m.Content) == 0 || string(m.Content) == "null":
	case json.Unmarshal(m.Content, &text) == nil:
		blocks = []rawBlock{{Type: "text", Text: text}}
	case json.Unmarshal(m.Content, &blocks) != nil:
		return p.unreadable("The agent sent a message that couldn't be read.")
	}
	for _, b := range blocks {
		events = append(events, p.block(base, role, m.ID, b)...)
	}
	// One usage event per message: the stream repeats a message (same ID, same
	// usage) once per content block.
	if u := m.Usage; u != nil && (m.ID == "" || !p.seenMsg[m.ID]) {
		if m.ID != "" {
			p.seenMsg[m.ID] = true
		}
		if u.InputTokens+u.OutputTokens+u.CacheCreationInputTokens+u.CacheReadInputTokens > 0 {
			ev := base
			ev.Kind, ev.ID = agents.EventUsage, m.ID
			ev.Usage = &agents.Usage{
				InputTokens: u.InputTokens, OutputTokens: u.OutputTokens,
				CacheWriteTokens: u.CacheCreationInputTokens, CacheReadTokens: u.CacheReadInputTokens,
			}
			events = append(events, ev)
		}
	}
	return events
}

func (p *parser) block(base agents.Event, role, msgID string, b rawBlock) []agents.Event {
	ev := base
	switch b.Type {
	case "text":
		if strings.TrimSpace(b.Text) == "" {
			return nil
		}
		ev.Kind, ev.ID, ev.Role, ev.Text = agents.EventMessage, msgID, agents.Role(role), capText(b.Text)
		return []agents.Event{ev}
	case "tool_use":
		return []agents.Event{p.toolUse(ev, b)}
	case "tool_result":
		var out []agents.Event
		if p.subagents[b.ToolUseID] {
			end := base
			end.Kind, end.ParentID, end.AgentID = agents.EventSubagentEnd, b.ToolUseID, p.agentOf[b.ToolUseID]
			out = append(out, end)
		}
		ev.Kind, ev.ResultFor, ev.IsError, ev.Text = agents.EventToolResult, b.ToolUseID, b.IsError, capText(blockText(b.Content))
		return append(out, ev)
	}
	return nil // thinking and unknown block types are not kept
}

// editTools change a file; commandTools run a command.
var (
	editTools    = map[string]string{"Edit": "modify", "MultiEdit": "modify", "NotebookEdit": "modify", "Write": "create"}
	commandTools = map[string]bool{"Bash": true, "PowerShell": true}
)

func (p *parser) toolUse(ev agents.Event, b rawBlock) agents.Event {
	ev.ID = b.ID
	var in struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
		OldString    string `json:"old_string"`
		NewString    string `json:"new_string"`
		Command      string `json:"command"`
		Description  string `json:"description"`
	}
	_ = json.Unmarshal(b.Input, &in)
	switch {
	case isSubagentTool(b.Name):
		p.subagents[b.ID] = true
		ev.Kind, ev.Text = agents.EventSubagentStart, capText(in.Description)
		ev.Tool = &agents.ToolCall{Name: b.Name, Input: capInput(b.Input)}
	case editTools[b.Name] != "":
		path := in.FilePath
		if path == "" {
			path = in.NotebookPath
		}
		ev.Kind = agents.EventFileEdit
		ev.File = &agents.FileEdit{Path: p.relPath(path), Op: editTools[b.Name], Diff: editDiff(in.OldString, in.NewString)}
	case commandTools[b.Name]:
		ev.Kind = agents.EventCommand
		ev.Command = &agents.CommandRun{Line: capText(in.Command)}
	default:
		ev.Kind = agents.EventToolCall
		ev.Tool = &agents.ToolCall{Name: safeName(b.Name), Input: capInput(b.Input)}
	}
	return ev
}

// result turns the final line into a usage event (cost, time and turns; the
// tokens come from the messages, sub-agents included) and, on failure, a plain
// error.
func (p *parser) result(base agents.Event, rl *rawLine) []agents.Event {
	p.sawResult = true
	p.resultOK = !rl.IsError && (rl.Subtype == "" || rl.Subtype == "success")
	var events []agents.Event
	if rl.TotalCostUSD != nil || rl.DurationMS > 0 || rl.NumTurns > 0 {
		ev := base
		ev.Kind = agents.EventUsage
		ev.Usage = &agents.Usage{CostUSD: rl.TotalCostUSD, DurationMS: rl.DurationMS, Turns: rl.NumTurns}
		events = append(events, ev)
	}
	if !p.resultOK {
		ev := base
		ev.Kind, ev.IsError, ev.Error = agents.EventError, true, resultError(rl.Subtype)
		events = append(events, ev)
	}
	return events
}

func resultError(subtype string) string {
	switch subtype {
	case "error_max_turns":
		return "The agent stopped because it reached its turn limit."
	case "error_max_budget_usd":
		return "The agent stopped because it reached its budget."
	case "error_during_execution":
		return "The agent stopped because of an error while it was working."
	}
	return "The agent reported an error and stopped."
}

// isSubagentTool: the sub-agent tool is "Agent" (renamed from "Task" in CLI
// 2.1.63; "Task" stays an alias and older streams still say it).
func isSubagentTool(name string) bool {
	return strings.EqualFold(name, "Agent") || strings.EqualFold(name, "Task")
}

// relPath shows a path relative to the worktree when it lies inside it.
func (p *parser) relPath(path string) string {
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) || p.dir == "" {
		return filepath.ToSlash(path)
	}
	if rel, err := filepath.Rel(p.dir, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(path)
}

// editDiff is a small diff of an Edit call: the replaced text and its
// replacement. Write calls carry whole files, which are not shown.
func editDiff(oldS, newS string) string {
	if oldS == "" && newS == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("@@ edit @@\n")
	for l := range strings.SplitSeq(capText(oldS), "\n") {
		b.WriteString("-" + l + "\n")
	}
	for l := range strings.SplitSeq(capText(newS), "\n") {
		b.WriteString("+" + l + "\n")
	}
	return b.String()
}

// blockText flattens tool_result content: a string, or text blocks joined.
func blockText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(content, &s) == nil {
		return s
	}
	var blocks []rawBlock
	if json.Unmarshal(content, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// capText cuts s to maxTextBytes at a rune boundary.
func capText(s string) string {
	if len(s) <= maxTextBytes {
		return s
	}
	cut := maxTextBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n… [truncated]"
}

// capInput replaces a tool input that is too large with a marker.
func capInput(in json.RawMessage) json.RawMessage {
	if len(in) <= maxTextBytes {
		return in
	}
	return json.RawMessage(`{"truncated":true}`)
}

// safeName makes a tool name safe to show: printable, short.
func safeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
		if b.Len() >= 64 {
			break
		}
	}
	if b.Len() == 0 {
		return "a tool"
	}
	return b.String()
}

func trimEOL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}
