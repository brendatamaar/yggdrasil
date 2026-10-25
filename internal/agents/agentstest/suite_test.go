package agentstest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/core"
)

// recorder is a T that records failures instead of failing the real test, so
// a test can assert that a clause catches a broken adapter.
type recorder struct {
	mu      sync.Mutex
	errs    []string
	skipped bool
}

func (r *recorder) Helper() {}
func (r *recorder) Errorf(f string, a ...any) {
	r.mu.Lock()
	r.errs = append(r.errs, fmt.Sprintf(f, a...))
	r.mu.Unlock()
}
func (r *recorder) Fatalf(f string, a ...any) { r.Errorf(f, a...); runtime.Goexit() }
func (r *recorder) Logf(string, ...any)       {}
func (r *recorder) Skipf(string, ...any)      { r.skipped = true; runtime.Goexit() }

// runClause runs one clause against f on a recorder and returns its failures.
func runClause(name string, s *suite) (errs []string, skipped bool) {
	var c Clause
	for _, x := range clauses {
		if x.Name == name {
			c = x
		}
	}
	r := &recorder{}
	done := make(chan struct{})
	go func() { defer close(done); c.run(s, r) }()
	<-done
	return r.errs, r.skipped
}

// ---- a scripted adapter that is good by default and breakable per test ----

func fp(f float64) *float64 { return &f }

func script(k Kind) []agents.Event {
	st := func(s agents.RunStatus) agents.Event { return agents.Event{Kind: agents.EventStatus, Status: s} }
	msg := func(id string, r agents.Role, txt string) agents.Event {
		return agents.Event{Kind: agents.EventMessage, ID: id, Role: r, Text: txt}
	}
	use := func(in, out int64, c float64) agents.Event {
		return agents.Event{Kind: agents.EventUsage, Usage: &agents.Usage{InputTokens: in, OutputTokens: out, CostUSD: fp(c)}}
	}
	switch k {
	case KindEdits:
		code := 1
		return []agents.Event{
			st(agents.RunStarted), msg("m1", agents.RoleAssistant, "Adding a health check to shop-api."),
			{Kind: agents.EventToolCall, ID: "tc1", Tool: &agents.ToolCall{Name: "Edit"}},
			{Kind: agents.EventFileEdit, File: &agents.FileEdit{Path: "cmd/shop-api/health.go", Op: "create"}},
			{Kind: agents.EventToolResult, ResultFor: "tc1", Text: "created"},
			{Kind: agents.EventCommand, Command: &agents.CommandRun{Line: "go vet ./...", ExitCode: &code}},
			use(10, 5, 0.001), st(agents.RunCompleted),
		}
	case KindError:
		return []agents.Event{
			st(agents.RunStarted), msg("m1", agents.RoleAssistant, "Looking at shop-web."),
			{Kind: agents.EventError, Error: "The agent lost its connection. Try again."},
			st(agents.RunFailed),
		}
	case KindNested:
		return []agents.Event{
			st(agents.RunStarted), msg("m1", agents.RoleAssistant, "Asking a helper."),
			{Kind: agents.EventSubagentStart, ID: "sa_1", AgentID: "explorer"},
			{Kind: agents.EventToolCall, ID: "tc1", ParentID: "sa_1", AgentID: "explorer", Tool: &agents.ToolCall{Name: "Grep"}},
			{Kind: agents.EventToolResult, ParentID: "sa_1", AgentID: "explorer", ResultFor: "tc1"},
			{Kind: agents.EventUsage, ParentID: "sa_1", AgentID: "explorer", Usage: &agents.Usage{InputTokens: 100, OutputTokens: 20, CostUSD: fp(0.001)}},
			{Kind: agents.EventSubagentEnd, ParentID: "sa_1", AgentID: "explorer"},
			{Kind: agents.EventUsage, Usage: &agents.Usage{InputTokens: 300, OutputTokens: 60, CostUSD: fp(0.004), Cumulative: true}},
			st(agents.RunCompleted),
		}
	}
	return []agents.Event{
		st(agents.RunStarted), msg("m1", agents.RoleUser, "Explain shop-api."),
		msg("m2", agents.RoleAssistant, "It serves the acme-shop catalog."),
		use(120, 40, 0.002), use(30, 15, 0.001), st(agents.RunCompleted),
	}
}

type userErr string

func (e userErr) Error() string       { return "wrapped: " + string(e) }
func (e userErr) UserMessage() string { return string(e) }

// fake replays script(kind). Every knob below breaks one clause.
type fake struct {
	kind    Kind
	caps    agents.Capabilities
	mutate  func(Kind, []agents.Event) []agents.Event
	onStart func()
	// stuck: ignore cancel; Events stays open until release is closed.
	stuck   bool
	release chan struct{}
	// block: wait for cancel (a healthy cancel scenario).
	block    bool
	checkErr error
}

func goodCaps() agents.Capabilities {
	return agents.Capabilities{Resume: true, CostReporting: true, Enforcement: agents.EnforcementLimited, Subagents: agents.SubagentsNested}
}

func (f *fake) Name() string                      { return "fake" }
func (f *fake) Capabilities() agents.Capabilities { return f.caps }
func (f *fake) Check(context.Context) error       { return f.checkErr }

func (f *fake) Start(ctx context.Context, o agents.StartOptions) (agents.Run, error) {
	if f.onStart != nil {
		f.onStart()
	}
	evs := script(f.kind)
	if f.mutate != nil {
		evs = f.mutate(f.kind, evs)
	}
	r := &frun{events: make(chan agents.Event, len(evs)+2), done: make(chan struct{})}
	go func() {
		defer close(r.done)
		defer close(r.events)
		if f.block || f.stuck {
			if f.stuck {
				<-f.release
				return
			}
			<-ctx.Done()
			r.events <- agents.Event{Kind: agents.EventStatus, Status: agents.RunCancelled}
			return
		}
		for _, e := range evs {
			e.At = time.Now()
			r.events <- e
			if e.Kind == agents.EventStatus && e.Status == agents.RunFailed {
				r.err = errors.New("failed")
			}
		}
	}()
	return r, nil
}

type frun struct {
	events chan agents.Event
	done   chan struct{}
	err    error
}

func (r *frun) Events() <-chan agents.Event { return r.events }
func (r *frun) SessionID() string           { return "s1" }
func (r *frun) Wait() error                 { <-r.done; return r.err }

// factoryOf builds a Factory from a template; mk may adjust the fake per kind.
func factoryOf(mk func(f *fake)) Factory {
	return func(k Kind) (Subject, error) {
		f := &fake{kind: k, caps: goodCaps(), release: make(chan struct{})}
		switch k {
		case KindCancel:
			f.block = true
		case KindMissingPrereq:
			f.checkErr = userErr("Claude Code is not installed. Install it, then try again.")
		}
		if mk != nil {
			mk(f)
		}
		return Subject{Adapter: f, Cleanup: func() {
			select {
			case <-f.release:
			default:
				close(f.release)
			}
		}}, nil
	}
}

func fastSuite(f Factory) *suite {
	s := newSuite(f)
	s.timeout = 300 * time.Millisecond
	return s
}

func TestGoodAdapterPasses(t *testing.T) {
	for _, name := range Clauses() {
		errs, skipped := runClause(name, fastSuite(factoryOf(nil)))
		if skipped || len(errs) > 0 {
			t.Errorf("clause %s: skipped=%v errs=%v", name, skipped, errs)
		}
	}
}

// brokenExec returns an exec that calls the real Execute through fix.
func execWith(fix func(ctx context.Context, a agents.Adapter, o agents.StartOptions, s agents.EventSink) (agents.Result, error)) func(*suite) {
	return func(s *suite) { s.exec = fix }
}

type seqSink struct{ agents.EventSink }

func (s seqSink) Append(ctx context.Context, id core.TaskID, e agents.Event) error {
	e.Seq = 1
	return s.EventSink.Append(ctx, id, e)
}

func TestClausesCatchBrokenAdapters(t *testing.T) {
	cases := []struct {
		name   string
		clause string
		mk     func(f *fake)
		tweak  func(*suite)
		want   string // substring of a failure
	}{
		{name: "tool result before its call", clause: "event_order", want: "before its tool call",
			mk: func(f *fake) {
				f.mutate = func(k Kind, e []agents.Event) []agents.Event {
					if k == KindEdits {
						e[2], e[4] = e[4], e[2]
					}
					return e
				}
			}},
		{name: "no started status", clause: "event_order", want: "want status started",
			mk: func(f *fake) {
				f.mutate = func(k Kind, e []agents.Event) []agents.Event { return e[1:] }
			}},
		{name: "duplicate Seq", clause: "event_order", want: "Seq",
			tweak: execWith(func(ctx context.Context, a agents.Adapter, o agents.StartOptions, s agents.EventSink) (agents.Result, error) {
				return agents.Execute(ctx, a, o, seqSink{s})
			})},
		{name: "tool call without a name", clause: "event_payloads", want: "without a tool name",
			mk: func(f *fake) {
				f.mutate = func(k Kind, e []agents.Event) []agents.Event {
					for i := range e {
						if e[i].Kind == agents.EventToolCall {
							e[i].Tool = nil
						}
					}
					return e
				}
			}},
		{name: "absolute file path", clause: "event_payloads", want: "not relative",
			mk: func(f *fake) {
				f.mutate = func(k Kind, e []agents.Event) []agents.Event {
					for i := range e {
						if e[i].File != nil {
							e[i].File = &agents.FileEdit{Path: `C:\work\shop-api\health.go`, Op: "create"}
						}
					}
					return e
				}
			}},
		{name: "error text with a stack", clause: "event_payloads", want: "stack trace",
			mk: func(f *fake) {
				f.mutate = func(k Kind, e []agents.Event) []agents.Event {
					for i := range e {
						if e[i].Kind == agents.EventError {
							e[i].Error = "boom at main.go:42"
						}
					}
					return e
				}
			}},
		{name: "orphan parent", clause: "tree", want: "names parent",
			mk: func(f *fake) {
				f.mutate = func(k Kind, e []agents.Event) []agents.Event {
					for i := range e {
						if e[i].Kind == agents.EventToolCall {
							e[i].ParentID = "sa_ghost"
						}
					}
					return e
				}
			}},
		{name: "sub-agent never ends", clause: "tree", want: "never ended",
			mk: func(f *fake) {
				f.mutate = func(k Kind, e []agents.Event) []agents.Event {
					var out []agents.Event
					for _, x := range e {
						if x.Kind != agents.EventSubagentEnd {
							out = append(out, x)
						}
					}
					return out
				}
			}},
		{name: "flat nested transcript", clause: "tree", want: "flat tree",
			mk: func(f *fake) {
				f.mutate = func(k Kind, e []agents.Event) []agents.Event {
					for i := range e {
						e[i].ParentID = ""
					}
					return e
				}
			}},
		{name: "no final status", clause: "final_status", want: "final status events",
			mk: func(f *fake) {
				f.mutate = func(k Kind, e []agents.Event) []agents.Event { return e[:len(e)-1] }
			}},
		{name: "events after the final status", clause: "final_status", want: "follow the final status",
			mk: func(f *fake) {
				f.mutate = func(k Kind, e []agents.Event) []agents.Event {
					return append(e, agents.Event{Kind: agents.EventMessage, Role: agents.RoleAssistant, Text: "late"})
				}
			}},
		{name: "wrong final status", clause: "final_status", want: "want \"failed\"",
			mk: func(f *fake) {
				f.mutate = func(k Kind, e []agents.Event) []agents.Event {
					if k == KindError {
						e[len(e)-1].Status = agents.RunCompleted
					}
					return e
				}
			}},
		{name: "usage not summed", clause: "usage", want: "events sum to",
			tweak: execWith(func(ctx context.Context, a agents.Adapter, o agents.StartOptions, s agents.EventSink) (agents.Result, error) {
				r, err := agents.Execute(ctx, a, o, s)
				r.Usage.InputTokens++
				return r, err
			})},
		{name: "cumulative usage shrinks", clause: "usage", want: "shrank",
			mk: func(f *fake) {
				f.mutate = func(k Kind, e []agents.Event) []agents.Event {
					if k == KindNested {
						e = append(e[:len(e)-1:len(e)-1],
							agents.Event{Kind: agents.EventUsage, Usage: &agents.Usage{InputTokens: 10, OutputTokens: 1, Cumulative: true}},
							e[len(e)-1])
					}
					return e
				}
			}},
		{name: "cost without CostReporting", clause: "usage", want: "CostReporting is false",
			mk: func(f *fake) { f.caps.CostReporting = false }},
		{name: "run ignores cancel", clause: "cancel", want: "not closed",
			mk: func(f *fake) {
				if f.kind == KindCancel {
					f.block, f.stuck = false, true
				}
			}},
		{name: "cancelled run reports completed", clause: "cancel", want: "reported completed",
			mk: func(f *fake) {
				f.mutate = nil
				if f.kind == KindCancel {
					f.block = false
					f.mutate = func(Kind, []agents.Event) []agents.Event {
						return []agents.Event{{Kind: agents.EventStatus, Status: agents.RunCompleted}}
					}
				}
			}},
		{name: "execute accepts nil Env", clause: "refusals", want: "nil Env",
			tweak: execWith(func(ctx context.Context, a agents.Adapter, o agents.StartOptions, s agents.EventSink) (agents.Result, error) {
				o.Env = []string{}
				return agents.Execute(ctx, a, o, s)
			})},
		{name: "execute accepts resume without capability", clause: "refusals", want: "without Capabilities.Resume",
			mk: func(f *fake) { f.caps.Resume = false },
			tweak: execWith(func(ctx context.Context, a agents.Adapter, o agents.StartOptions, s agents.EventSink) (agents.Result, error) {
				o.ResumeSessionID = ""
				return agents.Execute(ctx, a, o, s)
			})},
		{name: "unknown enforcement", clause: "capabilities", want: "Enforcement",
			mk: func(f *fake) { f.caps.Enforcement = "strict" }},
		{name: "hook enforcement without hooks", clause: "capabilities", want: "PermissionHooks",
			mk: func(f *fake) { f.caps.Enforcement = agents.EnforcementHook }},
		{name: "nested claim without ParentID", clause: "capabilities", want: "no ParentID",
			mk: func(f *fake) {
				f.mutate = func(k Kind, e []agents.Event) []agents.Event {
					for i := range e {
						e[i].ParentID = ""
					}
					return e
				}
			}},
		{name: "flat claim with ParentID", clause: "capabilities", want: "flat",
			mk: func(f *fake) { f.caps.Subagents = agents.SubagentsFlat }},
		{name: "none claim with sub-agents", clause: "capabilities", want: "none",
			mk: func(f *fake) { f.caps.Subagents = agents.SubagentsNone }},
		{name: "check passes without prerequisite", clause: "check", want: "Check passed",
			mk: func(f *fake) { f.checkErr = nil }},
		{name: "check error without UserMessage", clause: "check", want: "no UserMessage",
			mk: func(f *fake) { f.checkErr = errors.New("exec: claude: not found") }},
		{name: "check error with a path", clause: "check", want: "file path",
			mk: func(f *fake) { f.checkErr = userErr(`Could not find C:\Users\ada\bin\claude.exe, install it.`) }},
		{name: "check error with a stack", clause: "check", want: "stack",
			mk: func(f *fake) { f.checkErr = userErr("failed in check.go:55 please install it") }},
		{name: "secret echoed in an event", clause: "secrets", want: "secret from Env",
			mk: func(f *fake) {
				f.mutate = func(k Kind, e []agents.Event) []agents.Event {
					e[1].Text = "env: " + secret
					return e
				}
			}},
		{name: "secret in a log line", clause: "secrets", want: "log line",
			mk: func(f *fake) { f.onStart = func() { slog.Info("starting", "env", secret) } }},
		{name: "raw stream line forwarded", clause: "raw_lines", want: "raw stream line",
			mk: func(f *fake) {
				f.mutate = func(k Kind, e []agents.Event) []agents.Event {
					e[1].Text = `{"type":"assistant","account":"ada"}`
					return e
				}
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := fastSuite(factoryOf(c.mk))
			if c.tweak != nil {
				c.tweak(s)
			}
			errs, _ := runClause(c.clause, s)
			if len(errs) == 0 {
				t.Fatalf("clause %s did not fail for a broken adapter (%s)", c.clause, c.name)
			}
			if !strings.Contains(strings.Join(errs, "\n"), c.want) {
				t.Errorf("clause %s failed, but not with %q:\n%s", c.clause, c.want, strings.Join(errs, "\n"))
			}
		})
	}
}

// RawSentinel: the clause also catches raw account data in events and logs.
func TestRawSentinel(t *testing.T) {
	const raw = "ada@acme-shop.example"
	mk := func(mut func(f *fake)) Factory {
		inner := factoryOf(mut)
		return func(k Kind) (Subject, error) {
			sub, err := inner(k)
			sub.RawSentinel = raw
			return sub, err
		}
	}
	if errs, _ := runClause("raw_lines", fastSuite(mk(nil))); len(errs) > 0 {
		t.Fatalf("good adapter failed: %v", errs)
	}
	errs, _ := runClause("raw_lines", fastSuite(mk(func(f *fake) {
		f.mutate = func(k Kind, e []agents.Event) []agents.Event { e[1].Text = "hi " + raw; return e }
	})))
	if !strings.Contains(strings.Join(errs, ""), "raw stream data") {
		t.Errorf("sentinel in an event not caught: %v", errs)
	}
	errs, _ = runClause("raw_lines", fastSuite(mk(func(f *fake) {
		f.onStart = func() { slog.Debug("raw line", "line", raw) }
	})))
	if !strings.Contains(strings.Join(errs, ""), "log line") {
		t.Errorf("sentinel in a log not caught: %v", errs)
	}
}

func TestOptionalScenariosSkip(t *testing.T) {
	f := func(k Kind) (Subject, error) {
		if k != KindPlain {
			return Subject{}, ErrUnsupported
		}
		return factoryOf(nil)(k)
	}
	for _, name := range []string{"tree", "cancel", "check"} {
		if _, skipped := runClause(name, fastSuite(f)); !skipped {
			t.Errorf("clause %s did not skip an unsupported scenario", name)
		}
	}
	// KindPlain is required.
	errs, _ := runClause("event_order", fastSuite(func(Kind) (Subject, error) { return Subject{}, ErrUnsupported }))
	if len(errs) == 0 {
		t.Error("missing plain scenario not reported")
	}
}

func TestPlainProblem(t *testing.T) {
	bad := []string{"", "  ", `see C:\Users\ada\x`, "see /home/ada/x now", `a\b`, "at server.go:12", "goroutine 5 [running]", "panic: nil"}
	for _, m := range bad {
		if plainProblem(m) == "" {
			t.Errorf("%q accepted", m)
		}
	}
	for _, m := range []string{"Install Claude Code, then try again.", "Open https://claude.ai/install to install it."} {
		if p := plainProblem(m); p != "" {
			t.Errorf("%q rejected: %s", m, p)
		}
	}
}
