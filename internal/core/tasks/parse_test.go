package tasks

import (
	"errors"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

func validRaw() RawMessage {
	return RawMessage{Prompt: "fix the bug", Mode: "code", Scope: "repo", Agent: "claude-code", Model: "sonnet-4.5", Repos: []string{"r_1"}, Title: "Fix"}
}

func TestParseMessageAccepts(t *testing.T) {
	cases := map[string]func(r *RawMessage){
		"code":           func(r *RawMessage) {},
		"ask":            func(r *RawMessage) { r.Mode = "ask" },
		"current branch": func(r *RawMessage) { r.Mode = "current_branch" },
		"project":        func(r *RawMessage) { r.Mode = "project"; r.Scope = "project"; r.Repos = nil },
		"no model":       func(r *RawMessage) { r.Model = "" },
		"no title":       func(r *RawMessage) { r.Title = "" },
		"max prompt":     func(r *RawMessage) { r.Prompt = strings.Repeat("a", MaxPromptBytes) },
	}
	for name, mut := range cases {
		r := validRaw()
		mut(&r)
		if _, err := ParseMessage(r); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// One test per rejection rule.
func TestParseMessageRejects(t *testing.T) {
	cases := map[string]func(r *RawMessage){
		"empty prompt":                 func(r *RawMessage) { r.Prompt = "" },
		"blank prompt":                 func(r *RawMessage) { r.Prompt = " \n\t " },
		"long prompt":                  func(r *RawMessage) { r.Prompt = strings.Repeat("a", MaxPromptBytes+1) },
		"nul in prompt":                func(r *RawMessage) { r.Prompt = "a\x00b" },
		"invalid utf8 prompt":          func(r *RawMessage) { r.Prompt = "a\xffb" },
		"unknown mode":                 func(r *RawMessage) { r.Mode = "yolo" },
		"empty mode":                   func(r *RawMessage) { r.Mode = "" },
		"unknown scope":                func(r *RawMessage) { r.Scope = "all" },
		"ask with no repo":             func(r *RawMessage) { r.Mode = "ask"; r.Repos = nil },
		"code with two repos":          func(r *RawMessage) { r.Repos = []string{"r_1", "r_2"} },
		"current branch with no repo":  func(r *RawMessage) { r.Mode = "current_branch"; r.Repos = nil },
		"project scope with repos":     func(r *RawMessage) { r.Mode = "project"; r.Scope = "project" },
		"project scope with code mode": func(r *RawMessage) { r.Scope = "project"; r.Repos = nil },
		"project mode with repo scope": func(r *RawMessage) { r.Mode = "project" },
		"duplicate repo":               func(r *RawMessage) { r.Mode = "project"; r.Scope = "project"; r.Repos = []string{"r_1", "r_1"} },
		"invalid repo id":              func(r *RawMessage) { r.Repos = []string{"has space"} },
		"empty agent":                  func(r *RawMessage) { r.Agent = "" },
		"agent with space":             func(r *RawMessage) { r.Agent = "claude code" },
		"model with space":             func(r *RawMessage) { r.Model = "a b" },
		"model starting with dash":     func(r *RawMessage) { r.Model = "-x" },
		"model with shell char":        func(r *RawMessage) { r.Model = "a;b" },
		"model too long":               func(r *RawMessage) { r.Model = strings.Repeat("a", 65) },
	}
	for name, mut := range cases {
		r := validRaw()
		mut(&r)
		_, err := ParseMessage(r)
		var ue *UserError
		if err == nil {
			t.Errorf("%s: accepted %+v", name, r)
		} else if !errors.As(err, &ue) || ue.Message == "" {
			t.Errorf("%s: error %v is not a plain-language UserError", name, err)
		}
	}
}

func TestParseMessageCleansTitle(t *testing.T) {
	r := validRaw()
	r.Title = "  fix\x00the\nbug\t! "
	p, err := ParseMessage(r)
	if err != nil || p.Title != "fix the bug !" {
		t.Fatalf("title = %q, %v", p.Title, err)
	}
	r.Title = strings.Repeat("é", 200)
	p, err = ParseMessage(r)
	if err != nil || len([]rune(p.Title)) != MaxTitleRunes {
		t.Fatalf("title has %d runes, %v", len([]rune(p.Title)), err)
	}
}

// acceptedValid checks that whatever ParseMessage accepts satisfies every
// rule, and that parse(format(x)) == x.
func acceptedValid(t *testing.T, r RawMessage) {
	t.Helper()
	p, err := ParseMessage(r)
	if err != nil {
		return
	}
	if strings.TrimSpace(p.Prompt) == "" || len(p.Prompt) > MaxPromptBytes || p.Mode.Validate() != nil || p.Scope.Validate() != nil {
		t.Fatalf("accepted an invalid message: %+v", p)
	}
	if p.Scope == core.ScopeProject && (len(p.Repos) != 0 || p.Mode != core.ModeProject) {
		t.Fatalf("project scope broken: %+v", p)
	}
	if p.Scope == core.ScopeRepo && (len(p.Repos) != 1 || p.Mode == core.ModeProject) {
		t.Fatalf("repo scope broken: %+v", p)
	}
	if !ValidModelName(p.Agent) || (p.Model != "" && !ValidModelName(p.Model)) {
		t.Fatalf("accepted bad names: %+v", p)
	}
	if p.Title != CleanTitle(p.Title) || strings.ContainsAny(p.Title, "\n\r\t\x00") {
		t.Fatalf("title not clean: %q", p.Title)
	}
	again, err := ParseMessage(FormatMessage(p))
	if err != nil || !reflect.DeepEqual(again, p) {
		t.Fatalf("round trip: %+v -> %+v, %v", p, again, err)
	}
}

func TestParseMessageAcceptedImpliesValidAndRoundTrips(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		pick := func(xs ...string) string { return xs[r.Intn(len(xs))] }
		raw := RawMessage{
			Prompt: pick("go", "  hi  ", "", " ", "a\x00", strings.Repeat("x", r.Intn(3)*MaxPromptBytes)),
			Mode:   pick("ask", "code", "current_branch", "project", "x"),
			Scope:  pick("repo", "project", "x"),
			Agent:  pick("claude-code", "", "a b"),
			Model:  pick("", "m1", "-m", "a:b.c_d"),
			Title:  pick("", "t\nx", "  y ", strings.Repeat("é", 100)),
		}
		for i := r.Intn(3); i > 0; i-- {
			raw.Repos = append(raw.Repos, pick("r_1", "r_2", "", "r 3"))
		}
		acceptedValid(t, raw)
	})
}

func FuzzParseMessage(f *testing.F) {
	f.Add("fix", "code", "repo", "claude-code", "m", "r_1", "title")
	f.Add("", "project", "project", "a", "", "", "")
	f.Add("a\xff", "ask", "repo", "a b", "-", "r 1", "\x00")
	f.Fuzz(func(t *testing.T, prompt, mode, scope, agent, model, repo, title string) {
		raw := RawMessage{Prompt: prompt, Mode: mode, Scope: scope, Agent: agent, Model: model, Title: title}
		if repo != "" {
			raw.Repos = []string{repo}
		}
		acceptedValid(t, raw)
	})
}

func TestParseMaxRoundTrip(t *testing.T) {
	for n := MinMaxAgents; n <= MaxMaxAgents; n++ {
		got, err := ParseMax(FormatMax(n))
		if err != nil || got != n {
			t.Errorf("ParseMax(Format(%d)) = %d, %v", n, got, err)
		}
	}
	for _, bad := range []string{"+2", "02", "2 ", "0x2", "1e1", "99999999999999999999"} {
		if _, err := ParseMax(bad); err == nil {
			t.Errorf("ParseMax(%q) accepted", bad)
		}
	}
}

// canonical returns states that hold only stored data.
func canonicalStates() []State {
	at := t0
	u := Usage{InputTokens: 5, OutputTokens: 2, CostKnown: true, CostMicroUSD: 7, DurationMS: 3}
	out := []State{
		Draft{},
		Waiting{Turn: "t1", BlockedBy: BlockedNone{}},
		Waiting{Turn: "t1", Attempt: 2, BlockedBy: BlockedNone{}},
		Starting{Turn: "t1", Attempt: 1, Since: at},
		Running{Turn: "t1", Attempt: 3, Since: at},
		Running{Turn: "t1", Attempt: 1, Since: at, Usage: u},
		Stopping{Turn: "t1", Attempt: 1, Since: at, Cause: CauseUserCancel, Usage: u},
	}
	for _, o := range []Outcome{
		Answered{}, ReadyForReview{}, Cancelled{}, Dismissed{},
		NeedsYou{Why: NeedsIntegrityViolated}, NeedsYou{Why: NeedsCapExceeded},
		Failed{Kind: FailPrepare}, Failed{Kind: FailStart}, Failed{Kind: FailRun}, Failed{Kind: FailInterrupted},
	} {
		out = append(out, Idle{Outcome: o, At: at}, Idle{Outcome: o})
	}
	return out
}

func TestStateRowsRoundTrip(t *testing.T) {
	for _, s := range canonicalStates() {
		tr, turn := RowsOf(s)
		got, err := StateFromRows(tr, turn)
		if err != nil || !reflect.DeepEqual(got, s) {
			t.Errorf("round trip %#v -> %#v, %v", s, got, err)
		}
		if got.Status() != tr.Status {
			t.Errorf("%#v: row status %q, state status %q", s, tr.Status, got.Status())
		}
	}
}

func TestStateFromRowsDropsWhatIsNotStored(t *testing.T) {
	s := Running{Turn: "t1", Attempt: 1, Since: t0, Subagents: []Subagent{{ID: "a", Depth: 1}}}
	tr, turn := RowsOf(s)
	got, err := StateFromRows(tr, turn)
	if err != nil || len(got.(Running).Subagents) != 0 {
		t.Fatalf("got %#v, %v", got, err)
	}
	w := Waiting{Turn: "t1", BlockedBy: BlockedMaxAgents{}}
	tr, turn = RowsOf(w)
	got, _ = StateFromRows(tr, turn)
	if _, ok := got.(Waiting).BlockedBy.(BlockedNone); !ok {
		t.Fatalf("blocker survived: %#v", got)
	}
}

func TestStateFromRowsLegacyIdle(t *testing.T) {
	want := map[core.TaskStatus]Outcome{
		core.TaskDone: Answered{}, core.TaskReview: ReadyForReview{},
		core.TaskFailed: Failed{Kind: FailRun}, core.TaskCancelled: Cancelled{},
	}
	for st, o := range want {
		got, err := StateFromRows(TaskRow{Status: st}, nil)
		if err != nil || !reflect.DeepEqual(got, Idle{Outcome: o}) {
			t.Errorf("legacy %s -> %#v, %v", st, got, err)
		}
	}
	if _, err := StateFromRows(TaskRow{Status: core.TaskNeedsYou}, nil); err == nil {
		t.Error("needs_you without an outcome accepted")
	}
}

// One test per rejection rule.
func TestStateFromRowsRejects(t *testing.T) {
	at := t0
	live := func(state string, attempt Attempt) (TaskRow, *TurnRow) {
		return TaskRow{Status: core.TaskRunning, CurrentTurn: "t1"}, &TurnRow{ID: "t1", Attempt: attempt, State: state, StartedAt: &at}
	}
	type rows struct {
		t    TaskRow
		turn *TurnRow
	}
	mk := func(f func() (TaskRow, *TurnRow)) rows { a, b := f(); return rows{a, b} }
	cases := map[string]rows{
		"unknown status":            {TaskRow{Status: "paused"}, nil},
		"empty status":              {TaskRow{}, nil},
		"draft with turn":           {TaskRow{Status: core.TaskDraft, CurrentTurn: "t1"}, nil},
		"draft with outcome":        {TaskRow{Status: core.TaskDraft, IdleOutcome: "answered"}, nil},
		"running without turn row":  {TaskRow{Status: core.TaskRunning, CurrentTurn: "t1"}, nil},
		"running without turn id":   {TaskRow{Status: core.TaskRunning}, &TurnRow{ID: "t1", Attempt: 1, State: TurnRunning, StartedAt: &at}},
		"turn id mismatch":          mk(func() (TaskRow, *TurnRow) { a, b := live(TurnRunning, 1); b.ID = "t2"; return a, b }),
		"running without start":     mk(func() (TaskRow, *TurnRow) { a, b := live(TurnRunning, 1); b.StartedAt = nil; return a, b }),
		"starting without start":    mk(func() (TaskRow, *TurnRow) { a, b := live(TurnStarting, 1); b.StartedAt = nil; return a, b }),
		"stopping attempt 0":        mk(func() (TaskRow, *TurnRow) { return live(TurnStopping, 0) }),
		"running attempt 0":         mk(func() (TaskRow, *TurnRow) { return live(TurnRunning, 0) }),
		"negative attempt":          mk(func() (TaskRow, *TurnRow) { return live(TurnRunning, -1) }),
		"live with idle outcome":    mk(func() (TaskRow, *TurnRow) { a, b := live(TurnRunning, 1); a.IdleOutcome = "answered"; return a, b }),
		"running status, waiting":   mk(func() (TaskRow, *TurnRow) { a, b := live(TurnWaiting, 0); return a, b }),
		"queued status, running":    mk(func() (TaskRow, *TurnRow) { a, b := live(TurnRunning, 1); a.Status = core.TaskQueued; return a, b }),
		"finished turn but running": mk(func() (TaskRow, *TurnRow) { return live(TurnFinished, 1) }),
		"unknown turn state":        mk(func() (TaskRow, *TurnRow) { return live("zombie", 1) }),
		"negative usage":            mk(func() (TaskRow, *TurnRow) { a, b := live(TurnRunning, 1); b.Usage.InputTokens = -1; return a, b }),
		"cost unknown but positive": mk(func() (TaskRow, *TurnRow) { a, b := live(TurnRunning, 1); b.Usage.CostMicroUSD = 3; return a, b }),
		"idle with turn in flight":  {TaskRow{Status: core.TaskReview, IdleOutcome: "ready_for_review"}, &TurnRow{ID: "t1", Attempt: 1, State: TurnRunning, StartedAt: &at}},
		"idle unknown outcome":      {TaskRow{Status: core.TaskDone, IdleOutcome: "finished"}, nil},
		"status outcome mismatch":   {TaskRow{Status: core.TaskDone, IdleOutcome: "cancelled"}, nil},
		"legacy with turn row":      {TaskRow{Status: core.TaskDone}, &TurnRow{ID: "t1", State: TurnFinished}},
		"legacy needs_you":          {TaskRow{Status: core.TaskNeedsYou}, nil},
	}
	for name, c := range cases {
		if s, err := StateFromRows(c.t, c.turn); err == nil {
			t.Errorf("%s: accepted as %#v", name, s)
		}
	}
}

func rowsFrom(r *rand.Rand) (TaskRow, *TurnRow) {
	statuses := []core.TaskStatus{core.TaskDraft, core.TaskQueued, core.TaskRunning, core.TaskNeedsYou, core.TaskReview, core.TaskDone, core.TaskFailed, core.TaskCancelled, "x"}
	codes := []string{"", "answered", "ready_for_review", "needs_cap_exceeded", "failed_run_failed", "cancelled", "dismissed", "zzz"}
	states := []string{TurnWaiting, TurnStarting, TurnRunning, TurnStopping, TurnFinished, "x"}
	var at *time.Time
	if r.Intn(2) == 0 {
		x := t0
		at = &x
	}
	tr := TaskRow{Status: statuses[r.Intn(len(statuses))], IdleOutcome: codes[r.Intn(len(codes))], IdleAt: at}
	if r.Intn(2) == 0 {
		tr.CurrentTurn = "t1"
	}
	var turn *TurnRow
	if r.Intn(3) > 0 {
		turn = &TurnRow{ID: []TurnID{"t1", "t2"}[r.Intn(2)], Attempt: Attempt(r.Intn(4) - 1), State: states[r.Intn(len(states))], StartedAt: at,
			Usage: Usage{InputTokens: int64(r.Intn(5) - 1), CostMicroUSD: int64(r.Intn(3)), CostKnown: r.Intn(2) == 0}}
	}
	return tr, turn
}

// accepted => valid: whatever StateFromRows accepts is a consistent state that
// writes back to the same rows.
func checkRows(t *testing.T, tr TaskRow, turn *TurnRow) {
	t.Helper()
	s, err := StateFromRows(tr, turn)
	if err != nil {
		return
	}
	if s.Status() != tr.Status {
		t.Fatalf("status %q read as %#v", tr.Status, s)
	}
	if liveState(s) && (turnOf(s) == "" || turn == nil) {
		t.Fatalf("live state without turn: %#v", s)
	}
	if activeState(s) && attemptOf(s) < 1 {
		t.Fatalf("live attempt below 1: %#v", s)
	}
	if i, ok := s.(Idle); ok && i.Outcome == nil {
		t.Fatalf("idle without outcome")
	}
	tr2, turn2 := RowsOf(s)
	s2, err := StateFromRows(tr2, turn2)
	if err != nil || !reflect.DeepEqual(s, s2) {
		t.Fatalf("rows of %#v do not read back: %#v, %v", s, s2, err)
	}
}

func TestStateFromRowsAcceptedImpliesValid(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		tr, turn := rowsFrom(r)
		checkRows(t, tr, turn)
	})
}

func FuzzStateFromRows(f *testing.F) {
	f.Add("running", "t1", "", "t1", 1, "running", true, int64(0), true)
	f.Add("done", "", "answered", "", 0, "", false, int64(0), false)
	f.Add("queued", "t1", "", "t1", 0, "waiting", false, int64(-1), false)
	f.Fuzz(func(t *testing.T, status, cur, outcome, turnID string, attempt int, state string, started bool, in int64, haveTurn bool) {
		tr := TaskRow{Status: core.TaskStatus(status), CurrentTurn: TurnID(cur), IdleOutcome: outcome}
		var turn *TurnRow
		if haveTurn {
			turn = &TurnRow{ID: TurnID(turnID), Attempt: Attempt(attempt), State: state, Usage: Usage{InputTokens: in}}
			if started {
				x := t0
				turn.StartedAt = &x
			}
		}
		checkRows(t, tr, turn)
	})
}
