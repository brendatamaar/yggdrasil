package agentstest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Kind names a scripted scenario an adapter replays.
type Kind string

const (
	// KindPlain: a short answer with usage events. Required.
	KindPlain Kind = "plain"
	// KindEdits: tool calls, file edits and commands, one failing.
	KindEdits Kind = "edits"
	// KindError: the run reports an error and ends failed.
	KindError Kind = "error"
	// KindNested: nested sub-agents, ParentID filled in (AG-21).
	KindNested Kind = "nested"
	// KindCancel: a run that is still going when the context is cancelled.
	KindCancel Kind = "cancel"
	// KindMissingPrereq: an adapter whose Check fails because the CLI is missing.
	KindMissingPrereq Kind = "missing-prereq"
)

// ErrUnsupported is returned by a factory for a Kind it cannot replay.
var ErrUnsupported = errors.New("scenario not supported by this adapter")

// Subject is an adapter plus the scenario it will replay.
type Subject struct {
	Adapter agents.Adapter
	// Options is the base for the run. The suite sets TaskID and Env.
	Options agents.StartOptions
	// WantStatus overrides the expected final status (default: completed,
	// failed for KindError, cancelled for KindCancel).
	WantStatus agents.RunStatus
	// RawSentinel is a string that is in the raw stream but must reach no event
	// and no log line. Empty skips that part of the raw_lines clause.
	RawSentinel string
	// Cleanup runs after the scenario.
	Cleanup func()
}

// Factory builds the Subject for one scenario. It is called once per scenario
// run, so each gets a fresh adapter.
type Factory func(k Kind) (Subject, error)

// T is the part of *testing.T the suite uses; the self-tests pass a recorder.
type T interface {
	Helper()
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
	Logf(format string, args ...any)
	Skipf(format string, args ...any)
}

// secret is the sentinel placed in the run's Env.
const secret = "ygg-sentinel-7f3a9c" //nolint:gosec // fictional sentinel, not a credential

// Clause is one named rule of the contract.
type Clause struct {
	Name string
	run  func(s *suite, t T)
}

// Clauses lists the clause names, in run order.
func Clauses() []string {
	names := make([]string, 0, len(clauses))
	for _, c := range clauses {
		names = append(names, c.Name)
	}
	return names
}

var clauses = []Clause{
	{"event_order", (*suite).eventOrder},
	{"event_payloads", (*suite).eventPayloads},
	{"tree", (*suite).tree},
	{"final_status", (*suite).finalStatus},
	{"usage", (*suite).usage},
	{"cancel", (*suite).cancel},
	{"refusals", (*suite).refusals},
	{"capabilities", (*suite).capabilities},
	{"check", (*suite).check},
	{"secrets", (*suite).secrets},
	{"raw_lines", (*suite).rawLines},
}

// AdapterContract runs every clause as a subtest. See the package doc for how
// an adapter package calls it.
func AdapterContract(t *testing.T, f Factory) {
	t.Helper()
	for _, c := range clauses {
		t.Run(c.Name, func(t *testing.T) {
			c.run(newSuite(f), t)
		})
	}
}

type execFunc func(ctx context.Context, a agents.Adapter, o agents.StartOptions, s agents.EventSink) (agents.Result, error)

type suite struct {
	factory Factory
	exec    execFunc
	timeout time.Duration
}

func newSuite(f Factory) *suite {
	return &suite{factory: f, exec: agents.Execute, timeout: 5 * time.Second}
}

// subject builds a scenario; ok is false when it is unsupported (the caller
// skips). KindPlain is mandatory.
func (s *suite) subject(t T, k Kind) (Subject, bool) {
	t.Helper()
	sub, err := s.factory(k)
	if errors.Is(err, ErrUnsupported) {
		if k == KindPlain {
			t.Fatalf("scenario %q is required but the factory does not support it", k)
		}
		return Subject{}, false
	}
	if err != nil {
		t.Fatalf("factory(%s): %v", k, err)
	}
	if sub.Adapter == nil {
		t.Fatalf("factory(%s) returned no adapter", k)
	}
	return sub, true
}

func (sub Subject) want(k Kind) agents.RunStatus {
	switch {
	case sub.WantStatus != "":
		return sub.WantStatus
	case k == KindError:
		return agents.RunFailed
	case k == KindCancel:
		return agents.RunCancelled
	}
	return agents.RunCompleted
}

func (sub Subject) opts() agents.StartOptions {
	o := sub.Options
	o.TaskID = "t_contract"
	o.Env = append(append([]string{}, o.Env...), "YGG_CONTRACT_TOKEN="+secret)
	return o
}

type sink struct {
	mu     sync.Mutex
	events []agents.Event
}

func (k *sink) Append(_ context.Context, _ core.TaskID, e agents.Event) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.events = append(k.events, e)
	return nil
}

func (k *sink) all() []agents.Event {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]agents.Event(nil), k.events...)
}

// outcome is one finished run through Execute.
type outcome struct {
	events  []agents.Event
	res     agents.Result
	err     error
	logs    string
	started int32
	timeout bool
}

// countingAdapter counts Start calls.
type countingAdapter struct {
	agents.Adapter
	starts *int32
}

func (c countingAdapter) Start(ctx context.Context, o agents.StartOptions) (agents.Run, error) {
	atomic.AddInt32(c.starts, 1)
	return c.Adapter.Start(ctx, o)
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

var logMu sync.Mutex // slog.SetDefault is global

// capture runs fn with the default slog logger writing to a buffer.
func capture(fn func()) string {
	logMu.Lock()
	defer logMu.Unlock()
	prev := slog.Default()
	buf := &lockedBuf{}
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)
	fn()
	return buf.String()
}

// run executes a scenario to the end through s.exec. If cancelAfter > 0 the
// context is cancelled after that delay.
func (s *suite) run(sub Subject, o agents.StartOptions, cancelAfter time.Duration) outcome {
	var out outcome
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if cancelAfter > 0 {
		timer := time.AfterFunc(cancelAfter, cancel)
		defer timer.Stop()
	}
	sk := &sink{}
	a := countingAdapter{Adapter: sub.Adapter, starts: &out.started}
	done := make(chan struct{})
	out.logs = capture(func() {
		go func() {
			defer close(done)
			out.res, out.err = s.exec(ctx, a, o, sk)
		}()
		select {
		case <-done:
		case <-time.After(s.timeout):
			out.timeout = true
			cancel()
		}
	})
	out.events = sk.all()
	return out
}

// play runs scenario k and returns the outcome, or ok=false when unsupported.
func (s *suite) play(t T, k Kind) (Subject, outcome, bool) {
	t.Helper()
	sub, ok := s.subject(t, k)
	if !ok {
		return Subject{}, outcome{}, false
	}
	if sub.Cleanup != nil {
		defer sub.Cleanup()
	}
	out := s.run(sub, sub.opts(), 0)
	if out.timeout {
		t.Fatalf("%s: run did not end within %s", k, s.timeout)
	}
	if out.err != nil {
		t.Fatalf("%s: Execute returned %v", k, out.err)
	}
	return sub, out, true
}

// ---- clauses ----

func (s *suite) eventOrder(t T) {
	ran := false
	for _, k := range []Kind{KindPlain, KindEdits, KindError, KindNested} {
		_, out, ok := s.play(t, k)
		if !ok {
			continue
		}
		ran = true
		if len(out.events) == 0 {
			t.Errorf("%s: no events", k)
			continue
		}
		if first := out.events[0]; first.Kind != agents.EventStatus || first.Status != agents.RunStarted {
			t.Errorf("%s: first event is %s/%s, want status started", k, first.Kind, first.Status)
		}
		calls := map[string]bool{}
		for i, e := range out.events {
			if e.Seq != int64(i+1) {
				t.Errorf("%s: event %d has Seq %d, want %d", k, i, e.Seq, i+1)
			}
			if e.At.IsZero() {
				t.Errorf("%s: event %d (%s) has no time", k, i, e.Kind)
			}
			switch e.Kind {
			// A tool use may be reported as a file edit, a command or a
			// sub-agent start (all carry the call's ID); a result refers to it.
			case agents.EventToolCall, agents.EventFileEdit, agents.EventCommand, agents.EventSubagentStart:
				if e.ID != "" {
					calls[e.ID] = true
				}
			case agents.EventToolResult:
				if e.ResultFor != "" && !calls[e.ResultFor] {
					t.Errorf("%s: event %d is a result for %q before its tool call", k, i, e.ResultFor)
				}
			}
		}
	}
	if !ran {
		t.Fatalf("no scenario ran")
	}
}

var (
	knownKinds = map[agents.EventKind]bool{
		agents.EventStatus: true, agents.EventMessage: true, agents.EventToolCall: true,
		agents.EventToolResult: true, agents.EventFileEdit: true, agents.EventCommand: true,
		agents.EventSubagentStart: true, agents.EventSubagentEnd: true, agents.EventUsage: true,
		agents.EventError: true,
	}
	pathLike = regexp.MustCompile(`(?i)((^|[^a-z])[a-z]:[\\/])|(\\)|((^|\s)/(usr|home|tmp|var|etc|users|opt|mnt|private)/)`)
	stackish = regexp.MustCompile(`goroutine \d+|\.go:\d+|\b0x[0-9a-f]{6,}\b|\bpanic:|runtime\.|Traceback`)
)

// plainProblem says why msg is not fit to show a person; "" when it is.
func plainProblem(msg string) string {
	switch {
	case strings.TrimSpace(msg) == "":
		return "empty"
	case pathLike.MatchString(msg):
		return "contains a file path"
	case stackish.MatchString(msg):
		return "contains a stack trace or code location"
	}
	return ""
}

func (s *suite) eventPayloads(t T) {
	ran := false
	for _, k := range []Kind{KindPlain, KindEdits, KindError, KindNested} {
		_, out, ok := s.play(t, k)
		if !ok {
			continue
		}
		ran = true
		for i, e := range out.events {
			bad := func(format string, args ...any) {
				t.Errorf("%s: event %d (%s): %s", k, i, e.Kind, fmt.Sprintf(format, args...))
			}
			if !knownKinds[e.Kind] {
				bad("unknown kind")
				continue
			}
			switch e.Kind {
			case agents.EventStatus:
				if e.Status == "" {
					bad("status event without Status")
				}
			case agents.EventMessage:
				if e.Role != agents.RoleAssistant && e.Role != agents.RoleUser {
					bad("message with role %q", e.Role)
				}
			case agents.EventToolCall:
				if e.Tool == nil || e.Tool.Name == "" {
					bad("tool call without a tool name")
				}
			case agents.EventToolResult:
				if e.ResultFor == "" {
					bad("tool result without ResultFor")
				}
			case agents.EventFileEdit:
				if e.File == nil {
					bad("file edit without File")
					break
				}
				p := e.File.Path
				if p == "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) ||
					regexp.MustCompile(`^[A-Za-z]:`).MatchString(p) || strings.Contains(p, "..") {
					bad("file path %q is not relative to the worktree", p)
				}
				switch e.File.Op {
				case "create", "modify", "delete":
				default:
					bad("file op %q", e.File.Op)
				}
			case agents.EventCommand:
				if e.Command == nil || strings.TrimSpace(e.Command.Line) == "" {
					bad("command without a line")
				}
			case agents.EventSubagentStart:
				if e.ID == "" {
					bad("sub-agent start without ID")
				}
			case agents.EventUsage:
				if e.Usage == nil {
					bad("usage event without Usage")
				}
			case agents.EventError:
				if msg := e.Error; plainProblem(msg) != "" {
					bad("error text %q: %s", msg, plainProblem(msg))
				}
			}
		}
		if k == KindError {
			found := false
			for _, e := range out.events {
				found = found || e.Kind == agents.EventError
			}
			if !found {
				t.Errorf("error scenario has no error event")
			}
		}
		if k == KindEdits {
			var edit, cmd bool
			for _, e := range out.events {
				edit = edit || e.Kind == agents.EventFileEdit
				cmd = cmd || e.Kind == agents.EventCommand
			}
			if !edit || !cmd {
				t.Errorf("edits scenario has file edit=%v command=%v, want both", edit, cmd)
			}
		}
	}
	if !ran {
		t.Fatalf("no scenario ran")
	}
}

func (s *suite) tree(t T) {
	sub, out, ok := s.play(t, KindNested)
	if !ok {
		t.Skipf("nested scenario not supported")
		return
	}
	starts := 0
	for _, e := range out.events {
		if e.Kind == agents.EventSubagentStart {
			starts++
		}
	}
	if starts == 0 {
		t.Fatalf("nested scenario has no sub-agent start")
	}
	if depth(agents.BuildTree(out.events)) < 2 {
		t.Errorf("nested scenario builds a flat tree")
	}
	var walk func(ns []*agents.Node)
	walk = func(ns []*agents.Node) {
		for _, n := range ns {
			if n.Orphan {
				t.Errorf("event %d (%s) names parent %q that no earlier event has", n.Event.Seq, n.Event.Kind, n.Event.ParentID)
			}
			walk(n.Children)
		}
	}
	walk(agents.BuildTree(out.events))
	// Spans: every sub-agent start has its end, and only the end of a
	// started sub-agent appears.
	open := map[string]bool{}
	for _, e := range out.events {
		switch e.Kind {
		case agents.EventSubagentStart:
			open[e.ID] = true
		case agents.EventSubagentEnd:
			if !open[e.ParentID] {
				t.Errorf("sub-agent end for %q without a start", e.ParentID)
			}
			delete(open, e.ParentID)
		}
	}
	if len(open) > 0 && sub.want(KindNested) == agents.RunCompleted {
		t.Errorf("sub-agents never ended: %v", open)
	}
}

func depth(ns []*agents.Node) int {
	d := 0
	for _, n := range ns {
		if c := 1 + depth(n.Children); c > d {
			d = c
		}
	}
	return d
}

func (s *suite) finalStatus(t T) {
	ran := false
	for _, k := range []Kind{KindPlain, KindEdits, KindError, KindNested} {
		sub, out, ok := s.play(t, k)
		if !ok {
			continue
		}
		ran = true
		want := sub.want(k)
		var finals []agents.Event
		for _, e := range out.events {
			if e.Kind == agents.EventStatus && e.Status != agents.RunStarted {
				finals = append(finals, e)
			}
		}
		if len(finals) != 1 {
			t.Errorf("%s: %d final status events, want exactly 1", k, len(finals))
			continue
		}
		if last := out.events[len(out.events)-1]; last.Seq != finals[0].Seq {
			t.Errorf("%s: events follow the final status (last is %s)", k, last.Kind)
		}
		if finals[0].Status != want {
			t.Errorf("%s: final status %q, want %q", k, finals[0].Status, want)
		}
		if out.res.Status != want {
			t.Errorf("%s: Result.Status %q, want %q", k, out.res.Status, want)
		}
		if want == agents.RunFailed && out.res.Err == nil {
			t.Errorf("%s: a failed run reports no Result.Err", k)
		}
		if want == agents.RunCompleted && out.res.Err != nil {
			t.Errorf("%s: completed run has Result.Err %v", k, out.res.Err)
		}
	}
	if !ran {
		t.Fatalf("no scenario ran")
	}
}

// sumUsage is the contract's own reading of the usage rules (AG-21): a
// Cumulative event replaces the total, others add up.
func sumUsage(events []agents.Event) agents.Usage {
	var total agents.Usage
	for _, e := range events {
		if e.Kind != agents.EventUsage || e.Usage == nil {
			continue
		}
		u := *e.Usage
		if u.Cumulative {
			total = agents.Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens,
				CacheWriteTokens: u.CacheWriteTokens, CacheReadTokens: u.CacheReadTokens,
				DurationMS: u.DurationMS, Turns: u.Turns, CostUSD: u.CostUSD}
			continue
		}
		total.InputTokens += u.InputTokens
		total.OutputTokens += u.OutputTokens
		total.CacheWriteTokens += u.CacheWriteTokens
		total.CacheReadTokens += u.CacheReadTokens
		total.DurationMS += u.DurationMS
		total.Turns += u.Turns
		if u.CostUSD != nil {
			c := *u.CostUSD
			if total.CostUSD != nil {
				c += *total.CostUSD
			}
			total.CostUSD = &c
		}
	}
	return total
}

func costOf(u agents.Usage) float64 {
	if u.CostUSD == nil {
		return 0
	}
	return *u.CostUSD
}

func (s *suite) usage(t T) {
	ran := false
	for _, k := range []Kind{KindPlain, KindEdits, KindNested} {
		sub, out, ok := s.play(t, k)
		if !ok {
			continue
		}
		ran = true
		want := sumUsage(out.events)
		got := out.res.Usage
		if got.InputTokens != want.InputTokens || got.OutputTokens != want.OutputTokens ||
			got.CacheWriteTokens != want.CacheWriteTokens || got.CacheReadTokens != want.CacheReadTokens ||
			got.DurationMS != want.DurationMS || got.Turns != want.Turns ||
			math.Abs(costOf(got)-costOf(want)) > 1e-9 {
			t.Errorf("%s: Result.Usage = %+v (cost %v), events sum to %+v (cost %v)", k, got, costOf(got), want, costOf(want))
		}
		var prev agents.Usage
		sawCost, sawUsage := false, false
		for _, e := range out.events {
			if e.Kind != agents.EventUsage || e.Usage == nil {
				continue
			}
			sawUsage = true
			u := *e.Usage
			if u.InputTokens < 0 || u.OutputTokens < 0 || u.CacheReadTokens < 0 || u.CacheWriteTokens < 0 {
				t.Errorf("%s: negative token count in %+v", k, u)
			}
			if u.CostUSD != nil {
				sawCost = true
				if *u.CostUSD < 0 {
					t.Errorf("%s: negative cost %v", k, *u.CostUSD)
				}
			}
			if u.Cumulative {
				if u.InputTokens < prev.InputTokens || u.OutputTokens < prev.OutputTokens {
					t.Errorf("%s: cumulative usage shrank from %+v to %+v", k, prev, u)
				}
				prev = u
			}
		}
		caps := sub.Adapter.Capabilities()
		if sawCost && !caps.CostReporting {
			t.Errorf("%s: usage carries a cost but Capabilities.CostReporting is false", k)
		}
		if k == KindPlain && caps.CostReporting && !sawCost {
			t.Errorf("%s: CostReporting is true but no usage event has a cost", k)
		}
		if k == KindPlain && !sawUsage {
			t.Errorf("%s: no usage event", k)
		}
	}
	if !ran {
		t.Fatalf("no scenario ran")
	}
}

func (s *suite) cancel(t T) {
	sub, ok := s.subject(t, KindCancel)
	if !ok {
		t.Skipf("cancel scenario not supported")
		return
	}
	if sub.Cleanup != nil {
		defer sub.Cleanup()
	}
	o := sub.opts()

	// Directly: cancel ends the run, closes Events and Wait returns.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	run, err := sub.Adapter.Start(ctx, o)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	cancel()
	var got []agents.Event
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		for e := range run.Events() {
			got = append(got, e)
		}
	}()
	select {
	case <-closed:
	case <-time.After(s.timeout):
		t.Fatalf("Events was not closed within %s of cancelling", s.timeout)
	}
	waited := make(chan struct{})
	go func() { _ = run.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(s.timeout):
		t.Fatalf("Wait did not return within %s of cancelling", s.timeout)
	}
	for _, e := range got {
		if e.Kind == agents.EventStatus && e.Status == agents.RunCompleted {
			t.Errorf("a cancelled run reported completed")
		}
	}

	// Through Execute: a cancelled result, not a failure.
	sub2, ok := s.subject(t, KindCancel)
	if !ok {
		return
	}
	if sub2.Cleanup != nil {
		defer sub2.Cleanup()
	}
	out := s.run(sub2, sub2.opts(), 30*time.Millisecond)
	switch {
	case out.timeout:
		t.Errorf("Execute did not return within %s of cancelling", s.timeout)
	case out.err != nil:
		t.Errorf("Execute returned %v for a cancelled run", out.err)
	case out.res.Status != agents.RunCancelled:
		t.Errorf("Result.Status = %q after cancel, want %q", out.res.Status, agents.RunCancelled)
	case out.res.Err != nil:
		t.Errorf("Result.Err = %v after cancel, want nil", out.res.Err)
	}
}

func (s *suite) refusals(t T) {
	sub, ok := s.subject(t, KindPlain)
	if !ok {
		return
	}
	if sub.Cleanup != nil {
		defer sub.Cleanup()
	}

	o := sub.opts()
	o.Env = nil
	out := s.run(sub, o, 0)
	if out.err == nil {
		t.Errorf("Execute accepted a nil Env")
	}
	if out.started != 0 || len(out.events) != 0 {
		t.Errorf("Execute started the adapter (%d starts, %d events) despite a nil Env", out.started, len(out.events))
	}

	o = sub.opts()
	o.ResumeSessionID = "sess_acme_1"
	out = s.run(sub, o, 0)
	if sub.Adapter.Capabilities().Resume {
		if out.err != nil || out.res.Status != agents.RunCompleted {
			t.Errorf("resume on an adapter with Capabilities.Resume failed: %v / %s", out.err, out.res.Status)
		}
		return
	}
	if out.err == nil {
		t.Errorf("Execute accepted a resume on an adapter without Capabilities.Resume")
	}
	if out.started != 0 || len(out.events) != 0 {
		t.Errorf("Execute started the adapter (%d starts) for a refused resume", out.started)
	}
}

func (s *suite) capabilities(t T) {
	sub, out, ok := s.play(t, KindPlain)
	if !ok {
		return
	}
	_ = out
	c := sub.Adapter.Capabilities()
	if sub.Adapter.Name() == "" {
		t.Errorf("adapter has no Name")
	}
	switch c.Enforcement {
	case agents.EnforcementHook, agents.EnforcementLimited:
	default:
		t.Errorf("Enforcement = %q, want %q or %q", c.Enforcement, agents.EnforcementHook, agents.EnforcementLimited)
	}
	switch c.Subagents {
	case agents.SubagentsNone, agents.SubagentsFlat, agents.SubagentsNested:
	default:
		t.Errorf("Subagents = %q, want none, flat or nested", c.Subagents)
	}
	if c.Enforcement == agents.EnforcementHook && !c.PermissionHooks {
		t.Errorf("Enforcement is hook but PermissionHooks is false")
	}
	if c.SubagentLimits && c.Subagents == agents.SubagentsNone {
		t.Errorf("SubagentLimits is set but the adapter has no sub-agents")
	}

	// What the events show must agree with the claim, in every scenario.
	for _, k := range []Kind{KindPlain, KindEdits, KindNested} {
		_, o, ok := s.play(t, k)
		if !ok {
			continue
		}
		for _, e := range o.events {
			sa := e.Kind == agents.EventSubagentStart || e.Kind == agents.EventSubagentEnd
			switch c.Subagents {
			case agents.SubagentsNone:
				if sa || e.ParentID != "" || e.AgentID != "" {
					t.Errorf("%s: Subagents is none but event %d (%s) shows sub-agent data", k, e.Seq, e.Kind)
				}
			case agents.SubagentsFlat:
				if e.ParentID != "" {
					t.Errorf("%s: Subagents is flat but event %d carries ParentID %q", k, e.Seq, e.ParentID)
				}
			case agents.SubagentsNested:
				if e.AgentID != "" && e.Kind != agents.EventSubagentStart && e.ParentID == "" {
					t.Errorf("%s: Subagents is nested but event %d (%s) from agent %q has no ParentID", k, e.Seq, e.Kind, e.AgentID)
				}
			}
		}
		if c.Subagents == agents.SubagentsNested && k == KindNested {
			seen := false
			for _, e := range o.events {
				seen = seen || e.ParentID != ""
			}
			if !seen {
				t.Errorf("nested scenario has no event with a ParentID")
			}
		}
	}
}

func (s *suite) check(t T) {
	sub, ok := s.subject(t, KindMissingPrereq)
	if !ok {
		t.Skipf("missing-prerequisite scenario not supported")
		return
	}
	if sub.Cleanup != nil {
		defer sub.Cleanup()
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	err := sub.Adapter.Check(ctx)
	if err == nil {
		t.Fatalf("Check passed although the prerequisite is missing")
	}
	var um interface{ UserMessage() string }
	if !errors.As(err, &um) {
		t.Fatalf("Check error %T has no UserMessage(); callers could only show %q", err, err.Error())
	}
	msg := um.UserMessage()
	if p := plainProblem(msg); p != "" {
		t.Errorf("UserMessage %q: %s", msg, p)
	}
	if len(strings.Fields(msg)) < 3 {
		t.Errorf("UserMessage %q is too short to say what to do next", msg)
	}
}

func (s *suite) secrets(t T) {
	ran := false
	for _, k := range []Kind{KindPlain, KindEdits, KindError, KindNested} {
		_, out, ok := s.play(t, k)
		if !ok {
			continue
		}
		ran = true
		for _, e := range out.events {
			b, _ := json.Marshal(e)
			if strings.Contains(string(b), secret) {
				t.Errorf("%s: event %d (%s) contains the secret from Env", k, e.Seq, e.Kind)
			}
		}
		if out.res.Err != nil && strings.Contains(out.res.Err.Error(), secret) {
			t.Errorf("%s: Result.Err contains the secret", k)
		}
		if strings.Contains(out.logs, secret) {
			t.Errorf("%s: a log line contains the secret", k)
		}
	}
	if !ran {
		t.Fatalf("no scenario ran")
	}
}

var jsonObj = regexp.MustCompile(`^\s*\{.*\}\s*$`)

func (s *suite) rawLines(t T) {
	ran := false
	for _, k := range []Kind{KindPlain, KindEdits, KindError, KindNested} {
		sub, out, ok := s.play(t, k)
		if !ok {
			continue
		}
		ran = true
		for _, e := range out.events {
			for _, text := range []string{e.Text, e.Error} {
				if !jsonObj.MatchString(text) {
					continue
				}
				var m map[string]json.RawMessage
				if json.Unmarshal([]byte(text), &m) == nil {
					_, hasType := m["type"]
					_, hasKind := m["kind"]
					if hasType || hasKind {
						t.Errorf("%s: event %d (%s) forwards what looks like a raw stream line", k, e.Seq, e.Kind)
					}
				}
			}
			if sub.RawSentinel != "" {
				b, _ := json.Marshal(e)
				if strings.Contains(string(b), sub.RawSentinel) {
					t.Errorf("%s: event %d (%s) carries raw stream data (%q)", k, e.Seq, e.Kind, sub.RawSentinel)
				}
			}
		}
		if sub.RawSentinel != "" && strings.Contains(out.logs, sub.RawSentinel) {
			t.Errorf("%s: a log line carries raw stream data (%q)", k, sub.RawSentinel)
		}
	}
	if !ran {
		t.Fatalf("no scenario ran")
	}
}
