package tasks

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// MaxTitleRunes bounds a task title.
const MaxTitleRunes = 80

// maxNameLen bounds an agent or model name.
const maxNameLen = 64

// RawMessage is the outside shape of POST /api/tasks and /messages after the
// handler decoded it. ParseMessage turns it into a Message-ready value.
type RawMessage struct {
	Prompt string
	Mode   string
	Scope  string
	Agent  string
	Model  string
	Repos  []string
	Title  string
}

// ParsedMessage is a RawMessage that passed every rule that needs no lookup.
// The shell adds the lookups (repo tracked, agent available) and the turn ID
// and clock.
type ParsedMessage struct {
	Prompt string
	Mode   core.TaskMode
	Scope  core.TaskScope
	Agent  string
	Model  string
	Repos  []core.RepoID
	Title  string
}

// ValidModelName is the model-name rule: a name ends up on a command line, so
// only a letter or digit followed by letters, digits, dot, dash, colon or
// underscore, up to 64 characters. An empty model is allowed by ParseMessage,
// not by this rule.
func ValidModelName(s string) bool {
	if s == "" || len(s) > maxNameLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if i == 0 && !alnum {
			return false
		}
		if !alnum && c != '.' && c != '_' && c != ':' && c != '-' {
			return false
		}
	}
	return true
}

// CleanTitle makes a title one line: control characters become spaces, the
// ends are trimmed and it is cut to MaxTitleRunes. It is idempotent.
func CleanTitle(s string) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > MaxTitleRunes {
		s = string([]rune(s)[:MaxTitleRunes])
	}
	return strings.TrimSpace(s)
}

// ParseMessage rejects: empty or over-long prompt; unknown mode or scope;
// ask/code/current_branch with other than one repo; project scope with repos
// listed (and project scope with any other mode, or the project mode with a
// repo scope); an empty agent or a model failing the model-name rule. A title
// with control characters is cleaned, not rejected. Errors are plain
// language.
func ParseMessage(r RawMessage) (ParsedMessage, error) {
	if len(r.Prompt) > MaxPromptBytes {
		return ParsedMessage{}, reject("The message is too long. Keep it under " + strconv.Itoa(MaxPromptBytes/1024) + " KiB.")
	}
	prompt := strings.TrimSpace(r.Prompt)
	switch {
	case prompt == "":
		return ParsedMessage{}, reject("The message is empty. Write what the agent should do.")
	case !utf8.ValidString(prompt) || strings.ContainsRune(prompt, 0):
		return ParsedMessage{}, reject("The message has characters that can't be sent. Remove them and try again.")
	}
	mode := core.TaskMode(r.Mode)
	if err := mode.Validate(); err != nil {
		return ParsedMessage{}, reject("That mode isn't known. Use ask, code, current_branch or project.")
	}
	scope := core.TaskScope(r.Scope)
	if err := scope.Validate(); err != nil {
		return ParsedMessage{}, reject("That scope isn't known. Use repo or project.")
	}
	repos := make([]core.RepoID, 0, len(r.Repos))
	seen := map[string]bool{}
	for _, s := range r.Repos {
		id := core.RepoID(s)
		if err := id.Validate(); err != nil {
			return ParsedMessage{}, reject("A repo in the list isn't valid.")
		}
		if seen[s] {
			return ParsedMessage{}, reject("A repo is listed twice.")
		}
		seen[s] = true
		repos = append(repos, id)
	}
	switch scope {
	case core.ScopeProject:
		if len(repos) != 0 {
			return ParsedMessage{}, reject("A whole-project task covers all repos; don't list repos.")
		}
		if mode != core.ModeProject {
			return ParsedMessage{}, reject("A whole-project scope needs the project mode.")
		}
	default:
		if mode == core.ModeProject {
			return ParsedMessage{}, reject("The project mode needs the project scope.")
		}
		if len(repos) != 1 {
			return ParsedMessage{}, reject("Choose exactly one repo for this mode.")
		}
	}
	if !ValidModelName(r.Agent) {
		return ParsedMessage{}, reject("That agent name isn't valid.")
	}
	if r.Model != "" && !ValidModelName(r.Model) {
		return ParsedMessage{}, reject("That model name isn't valid. Use letters, digits, dots, dashes and colons, up to 64 characters.")
	}
	return ParsedMessage{
		Prompt: prompt, Mode: mode, Scope: scope, Agent: r.Agent, Model: r.Model,
		Repos: repos, Title: CleanTitle(r.Title),
	}, nil
}

// UserError is a rejection whose text is meant for people: plain language that
// says what to do next. The API shows Message as it is.
type UserError struct{ Message string }

func (e *UserError) Error() string { return e.Message }

func reject(msg string) error { return &UserError{Message: msg} }

// FormatMessage is the inverse of ParseMessage on valid values.
func FormatMessage(p ParsedMessage) RawMessage {
	repos := make([]string, len(p.Repos))
	for i, r := range p.Repos {
		repos[i] = string(r)
	}
	return RawMessage{
		Prompt: p.Prompt, Mode: string(p.Mode), Scope: string(p.Scope), Agent: p.Agent,
		Model: p.Model, Repos: repos, Title: p.Title,
	}
}

// ParseMax parses the maxAgents setting: a whole number from MinMaxAgents to
// MaxMaxAgents.
func ParseMax(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || FormatMax(n) != s {
		return 0, fmt.Errorf("maxAgents must be a whole number from %d to %d", MinMaxAgents, MaxMaxAgents)
	}
	if n < MinMaxAgents || n > MaxMaxAgents {
		return 0, fmt.Errorf("maxAgents must be from %d to %d", MinMaxAgents, MaxMaxAgents)
	}
	return n, nil
}

// FormatMax is the inverse of ParseMax.
func FormatMax(n int) string { return strconv.Itoa(n) }

// TaskRow is the part of the tasks row that carries the state.
type TaskRow struct {
	Status      core.TaskStatus
	CurrentTurn TurnID
	// IdleOutcome is the Idle outcome as stored (tasks.idle_outcome); empty
	// unless the task is idle. Rows written before turns existed have it
	// empty for an idle task; StateFromRows maps those from Status.
	IdleOutcome string
	// IdleAt is when the task went idle (the shell stores it in
	// tasks.finished_at); nil when unknown.
	IdleAt *time.Time
}

// Stored states of a task_turns row.
const (
	TurnWaiting  = "waiting"
	TurnStarting = "starting"
	TurnRunning  = "running"
	TurnStopping = "stopping"
	TurnFinished = "finished"
)

// TurnRow is a task_turns row.
type TurnRow struct {
	ID         TurnID
	Seq        int
	Mode       core.TaskMode
	Prompt     string
	Attempt    Attempt
	State      string
	StartedAt  *time.Time
	FinishedAt *time.Time
	Outcome    string
	Usage      Usage
}

func (u Usage) valid() bool {
	if u.InputTokens < 0 || u.OutputTokens < 0 || u.CacheWriteTokens < 0 || u.CacheReadTokens < 0 ||
		u.CostMicroUSD < 0 || u.DurationMS < 0 {
		return false
	}
	return u.CostKnown || u.CostMicroUSD == 0
}

func since(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// legacyOutcome maps an idle row written before turns existed (no stored
// outcome) onto an outcome. Phase 2 only had Ask turns.
func legacyOutcome(s core.TaskStatus) (Outcome, bool) {
	switch s {
	case core.TaskDone:
		return Answered{}, true
	case core.TaskReview:
		return ReadyForReview{}, true
	case core.TaskFailed:
		return Failed{Kind: FailRun}, true
	case core.TaskCancelled:
		return Cancelled{}, true
	}
	return nil, false
}

// StateFromRows rebuilds a State from the stored rows and rejects rows that
// are inconsistent: unknown status, a live state without its turn or start
// time, an idle task with a turn in flight, a live Attempt below 1, an outcome
// that disagrees with the status, negative usage.
//
// What is not stored is not rebuilt: a Waiting task is BlockedNone (the
// scheduler recomputes why), a Running task has no sub-agents, and a Stopping
// task reads back with CauseUserCancel (a stopping task is always recovered
// to Idle{Failed(Interrupted)} at the next start).
func StateFromRows(t TaskRow, turn *TurnRow) (State, error) {
	if err := t.Status.Validate(); err != nil {
		return nil, fmt.Errorf("task has an unknown status %q", string(t.Status))
	}
	switch t.Status {
	case core.TaskDraft:
		if t.CurrentTurn != "" || turn != nil || t.IdleOutcome != "" {
			return nil, errors.New("a draft task cannot have a turn or an outcome")
		}
		return Draft{}, nil
	case core.TaskQueued, core.TaskRunning:
		return liveFromRows(t, turn)
	}
	// Idle statuses.
	if turn != nil && turn.State != TurnFinished {
		return nil, errors.New("an idle task cannot have a turn in flight")
	}
	var o Outcome
	if t.IdleOutcome == "" {
		var ok bool
		if o, ok = legacyOutcome(t.Status); !ok || turn != nil {
			return nil, fmt.Errorf("task is %q but has no stored outcome", string(t.Status))
		}
	} else {
		var err error
		if o, err = ParseOutcomeCode(t.IdleOutcome); err != nil {
			return nil, err
		}
	}
	if o.status() != t.Status {
		return nil, fmt.Errorf("task status %q does not match outcome %q", string(t.Status), t.IdleOutcome)
	}
	return Idle{Outcome: o, At: since(t.IdleAt)}, nil
}

func liveFromRows(t TaskRow, turn *TurnRow) (State, error) {
	if t.IdleOutcome != "" {
		return nil, errors.New("a task with a turn in flight cannot have an idle outcome")
	}
	if turn == nil || t.CurrentTurn == "" {
		return nil, fmt.Errorf("task is %q but has no turn", string(t.Status))
	}
	if err := t.CurrentTurn.Validate(); err != nil || turn.ID != t.CurrentTurn {
		return nil, errors.New("the task's current turn does not match its turn row")
	}
	if !turn.Usage.valid() {
		return nil, errors.New("the turn has invalid usage")
	}
	if turn.Attempt < 0 {
		return nil, errors.New("the turn has a negative attempt")
	}
	wantStatus := core.TaskRunning
	if turn.State == TurnWaiting {
		wantStatus = core.TaskQueued
	}
	if t.Status != wantStatus {
		return nil, fmt.Errorf("task status %q does not match turn state %q", string(t.Status), turn.State)
	}
	if turn.State == TurnWaiting {
		return Waiting{Turn: turn.ID, Attempt: turn.Attempt, BlockedBy: BlockedNone{}}, nil
	}
	switch turn.State {
	case TurnStarting, TurnRunning, TurnStopping:
	default:
		return nil, fmt.Errorf("turn state %q is not a live state", turn.State)
	}
	if turn.Attempt < 1 {
		return nil, errors.New("a live turn must have an attempt of 1 or more")
	}
	if turn.StartedAt == nil {
		return nil, errors.New("a live turn must have a start time")
	}
	switch turn.State {
	case TurnStarting:
		return Starting{Turn: turn.ID, Attempt: turn.Attempt, Since: *turn.StartedAt}, nil
	case TurnRunning:
		return Running{Turn: turn.ID, Attempt: turn.Attempt, Since: *turn.StartedAt, Usage: turn.Usage}, nil
	}
	return Stopping{Turn: turn.ID, Attempt: turn.Attempt, Since: *turn.StartedAt, Cause: CauseUserCancel, Usage: turn.Usage}, nil
}

// RowsOf is the inverse of StateFromRows: StateFromRows(RowsOf(s)) equals s
// for every state that holds only stored data (see StateFromRows). It fills
// the state-bearing fields only; the shell adds sequence, mode and prompt.
func RowsOf(s State) (TaskRow, *TurnRow) {
	switch st := s.(type) {
	case nil:
		return TaskRow{}, nil
	case Draft:
		return TaskRow{Status: core.TaskDraft}, nil
	case Waiting:
		return TaskRow{Status: core.TaskQueued, CurrentTurn: st.Turn}, &TurnRow{ID: st.Turn, Attempt: st.Attempt, State: TurnWaiting}
	case Starting:
		at := st.Since
		return TaskRow{Status: core.TaskRunning, CurrentTurn: st.Turn}, &TurnRow{ID: st.Turn, Attempt: st.Attempt, State: TurnStarting, StartedAt: &at}
	case Running:
		at := st.Since
		return TaskRow{Status: core.TaskRunning, CurrentTurn: st.Turn}, &TurnRow{ID: st.Turn, Attempt: st.Attempt, State: TurnRunning, StartedAt: &at, Usage: st.Usage}
	case Stopping:
		at := st.Since
		return TaskRow{Status: core.TaskRunning, CurrentTurn: st.Turn}, &TurnRow{ID: st.Turn, Attempt: st.Attempt, State: TurnStopping, StartedAt: &at, Usage: st.Usage}
	case Idle:
		row := TaskRow{Status: st.Status(), IdleOutcome: OutcomeCode(st.Outcome)}
		if !st.At.IsZero() {
			at := st.At
			row.IdleAt = &at
		}
		return row, nil
	}
	return TaskRow{Status: s.Status()}, nil
}
