package agentstream

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// MaxLineBytes caps one stream line. A longer line becomes an error event and
// is skipped, so a runaway tool result can't exhaust memory.
const MaxLineBytes = 16 << 20

// Stream line shapes. UNVERIFIED: written from memory of Claude Code's
// stream-json output; the live run checks every field name here.
type rawLine struct {
	Type            string      `json:"type"`
	Subtype         string      `json:"subtype"`
	SessionID       string      `json:"session_id"`
	ParentToolUseID *string     `json:"parent_tool_use_id"`
	Model           string      `json:"model"`
	Message         *rawMessage `json:"message"`
	// type=result
	IsError      bool      `json:"is_error"`
	Result       string    `json:"result"`
	TotalCostUSD *float64  `json:"total_cost_usd"`
	Usage        *rawUsage `json:"usage"`
	DurationMS   int64     `json:"duration_ms"`
	NumTurns     int       `json:"num_turns"`
}

type rawMessage struct {
	ID      string          `json:"id"`
	Role    string          `json:"role"`
	Model   string          `json:"model"`
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
	Content   json.RawMessage `json:"content"` // tool_result: a string or a list of blocks
	IsError   bool            `json:"is_error"`
}

type rawUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
}

func (u *rawUsage) normalize() *Usage {
	if u == nil {
		return nil
	}
	return &Usage{
		InputTokens:              u.InputTokens,
		OutputTokens:             u.OutputTokens,
		CacheCreationInputTokens: u.CacheCreationInputTokens,
		CacheReadInputTokens:     u.CacheReadInputTokens,
	}
}

// Parser turns stream-json lines into events. It counts lines; use one per
// stream.
type Parser struct {
	line int
}

// Parse reads stream-json from r line by line and calls emit for each event,
// in order. It stops early when emit returns false. Unknown and malformed
// lines become events too; only a read error is returned.
func Parse(r io.Reader, emit func(Event) bool) error {
	var p Parser
	br := bufio.NewReaderSize(r, 64<<10)
	var buf []byte
	tooLong := false
	for {
		chunk, err := br.ReadSlice('\n')
		switch {
		case tooLong:
		case len(buf)+len(chunk) > MaxLineBytes:
			tooLong, buf = true, buf[:0]
		default:
			buf = append(buf, chunk...)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		var events []Event
		if tooLong {
			p.line++
			events = []Event{{Line: p.line, Kind: KindError, Err: fmt.Sprintf("line longer than %d bytes, skipped", MaxLineBytes)}}
		} else if len(buf) > 0 {
			events = p.ParseLine(buf)
		}
		for _, ev := range events {
			if !emit(ev) {
				return nil
			}
		}
		buf, tooLong = buf[:0], false
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read stream: %w", err)
		}
	}
}

// ParseLine parses one physical line (with or without its line ending). A
// blank line yields no events but still counts.
func (p *Parser) ParseLine(line []byte) []Event {
	p.line++
	line = bytes.TrimRight(line, "\r\n")
	if len(bytes.TrimSpace(line)) == 0 {
		return nil
	}
	base := Event{Line: p.line, Raw: string(line)}
	var rl rawLine
	if err := json.Unmarshal(line, &rl); err != nil {
		base.Kind, base.Err = KindError, fmt.Sprintf("malformed line: %v", err)
		return []Event{base}
	}
	base.SessionID = rl.SessionID
	if rl.ParentToolUseID != nil {
		base.ParentID = *rl.ParentToolUseID
	}
	switch {
	case rl.Type == "system" && rl.Subtype == "init":
		base.Kind, base.ID, base.Model = KindInit, rl.SessionID, rl.Model
		return []Event{base}
	case (rl.Type == "assistant" || rl.Type == "user") && rl.Message != nil:
		return messageEvents(base, rl.Type, rl.Message)
	case rl.Type == "result":
		base.Kind, base.ID, base.Text, base.IsError = KindResult, rl.SessionID, rl.Result, rl.IsError
		base.Usage = rl.Usage.normalize()
		if base.Usage == nil {
			base.Usage = &Usage{}
		}
		base.Usage.CostUSD, base.Usage.DurationMS, base.Usage.NumTurns = rl.TotalCostUSD, rl.DurationMS, rl.NumTurns
		return []Event{base}
	default:
		base.Kind = KindUnknown
		return []Event{base}
	}
}

func messageEvents(base Event, role string, m *rawMessage) []Event {
	if m.Model != "" {
		base.Model = m.Model
	}
	var events []Event
	add := func(ev Event) { events = append(events, ev) }

	var text string
	var blocks []rawBlock
	switch {
	case len(m.Content) == 0 || string(m.Content) == "null":
	case json.Unmarshal(m.Content, &text) == nil:
		blocks = []rawBlock{{Type: "text", Text: text}}
	case json.Unmarshal(m.Content, &blocks) != nil:
		ev := base
		ev.Kind, ev.Err = KindError, "message content is neither a string nor a list of blocks"
		add(ev)
	}
	for _, b := range blocks {
		ev := base
		switch b.Type {
		case "text":
			ev.Kind, ev.ID, ev.Role, ev.Text = KindText, m.ID, role, b.Text
		case "tool_use":
			ev.Kind, ev.ID = KindToolCall, b.ID
			if IsSubagentTool(b.Name) {
				ev.Kind = KindSubagentStart
			}
			ev.Tool = &Tool{Name: b.Name, Input: b.Input}
		case "tool_result":
			ev.Kind, ev.ResultFor, ev.IsError, ev.Text = KindToolResult, b.ToolUseID, b.IsError, blockText(b.Content)
		default:
			ev.Kind, ev.ID, ev.Err = KindUnknown, m.ID, fmt.Sprintf("unhandled content block %q", b.Type)
		}
		add(ev)
	}
	if u := m.Usage.normalize(); u != nil {
		ev := base
		ev.Kind, ev.ID, ev.Usage = KindUsage, m.ID, u
		add(ev)
	}
	if len(events) == 0 {
		base.Kind = KindUnknown
		add(base)
	}
	return events
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
