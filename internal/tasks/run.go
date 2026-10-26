package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/guard"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

func (s *Service) startRun(id core.TaskID, cmd coretasks.StartRun) {
	at := s.attemptOf(id, cmd.Turn, cmd.Attempt)
	if at == nil {
		at = s.newAttempt(id, cmd.Turn, cmd.Attempt)
	}
	if !s.spawn(func() { s.runTurn(id, at, cmd) }) {
		at.cancel()
	}
}

// report is what a run leaves for the core: the RunFinished to feed, whether
// the tree changed during a read-only turn, and the audit entries that belong
// with the state change.
type report struct {
	fin      coretasks.RunFinished
	violated bool
	audits   []auditSpec
}

// runTurn runs the agent of one attempt and feeds its end to the core. A panic
// anywhere in it becomes RunFinished{Failed} (the builds safeInterpret
// pattern), so the task never stays running with no one watching it.
func (s *Service) runTurn(id core.TaskID, at *attempt, cmd coretasks.StartRun) {
	rep := s.safeExecute(id, at, cmd)
	rep.fin.Turn, rep.fin.Attempt, rep.fin.Now = cmd.Turn, cmd.Attempt, s.c.Now().UTC()
	// RunFinished is held until the After snapshot was checked, and the
	// integrity verdict goes first (SF-07, tasks.md Gate 1 fixes).
	audits := rep.audits
	if rep.violated {
		if _, ok := s.feed(id, coretasks.IntegrityViolated{Turn: cmd.Turn, Now: s.c.Now().UTC()}, applyOpts{extra: audits}); ok {
			audits = nil
		} else {
			// The verdict could not be stored. The turn must never end as
			// answered after that: it fails, and the audit entries go with it.
			rep.fin.Status, rep.fin.StartFail = coretasks.RunFailed, false
			s.c.Log.Error("the integrity verdict of a read-only turn could not be stored; the turn is failed", "task", id)
		}
	}
	if _, ok := s.feed(id, rep.fin, applyOpts{extra: audits}); !ok {
		s.abandon(id, "run finished")
	}
}

func (s *Service) safeExecute(id core.TaskID, at *attempt, cmd coretasks.StartRun) (rep report) {
	defer func() {
		if v := recover(); v != nil {
			s.c.Log.Error("task run panic", "task", id, "panic", v, "stack", string(debug.Stack()))
			rep = report{fin: coretasks.RunFinished{Status: coretasks.RunFailed}}
			s.explain(s.bgCtx(), id, "The run stopped unexpectedly. The Yggdrasil log has the details.")
		}
	}()
	return s.execute(id, at, cmd)
}

// capture remembers the Run the adapter returned, so the reports (tamper, hook
// coverage, denies) can be read after the run ended.
type capture struct {
	agents.Adapter
	mu  sync.Mutex
	run agents.Run
}

func (c *capture) Start(ctx context.Context, opts agents.StartOptions) (agents.Run, error) {
	r, err := c.Adapter.Start(ctx, opts)
	if err == nil {
		c.mu.Lock()
		c.run = r
		c.mu.Unlock()
	}
	return r, err
}

func (c *capture) started() agents.Run {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.run
}

func (s *Service) execute(id core.TaskID, at *attempt, cmd coretasks.StartRun) report {
	bg := s.bgCtx()
	startFailed := func(msg string) report {
		s.explain(bg, id, msg)
		return report{fin: coretasks.RunFinished{Status: coretasks.RunFailed, StartFail: true}}
	}
	cancelled := func() report { return report{fin: coretasks.RunFinished{Status: coretasks.RunCancelled}} }
	if at.ctx.Err() != nil {
		return cancelled()
	}
	st, err := s.snapshot(bg, id)
	if err != nil {
		s.c.Log.Error("run: load task", "task", id, "err", err)
		return startFailed("The task could not be read. Try again.")
	}
	prompt, err := s.c.Store.GetTurnPrompt(bg, cmd.Turn)
	if err != nil {
		s.c.Log.Error("run: load prompt", "task", id, "err", err)
		return startFailed("The message could not be read. Try again.")
	}
	repos := make([]core.Repo, 0, len(st.Row.RepoIDs))
	for _, rid := range st.Row.RepoIDs {
		r, err := s.c.Store.GetRepo(bg, rid)
		if err != nil {
			return startFailed("A repo of this task isn't tracked any more. Track it again or start a new task.")
		}
		repos = append(repos, r)
	}
	adapter, err := s.adapterOf(st.Row.Agent)
	if err != nil {
		return startFailed("The agent isn't available. Pick another agent in Settings.")
	}

	// Admission again, with a fresh scan of the repos' agent config: the
	// verdict from the time of the message may be old (SF-09), and Admitted
	// comes from this result and nowhere else. The opt-in was needed to create
	// a turn in a main-checkout mode, so a stored turn in such a mode carries it.
	optIn := cmd.Mode == core.ModeCurrentBranch || cmd.Mode == core.ModeProject
	adm, err := s.admit(at.ctx, adapter, cmd.Mode, nil, optIn, repos)
	if err != nil {
		if at.ctx.Err() != nil {
			return cancelled()
		}
		s.c.Log.Error("run: admit", "task", id, "err", err)
		return startFailed("Yggdrasil could not check this repo's agent settings. Try again.")
	}
	if !adm.Admitted {
		return startFailed(adm.Refusal.Message())
	}
	caps := adapter.Capabilities()
	policy, err := s.buildPolicy(at.ctx, cmd, touchedRepos(cmd.Mode, repos), caps)
	if err != nil {
		s.c.Log.Error("run: build policy", "task", id, "err", err)
		return startFailed("The task's folders could not be checked, so it was not started.")
	}
	env, err := s.c.Env.Build(safety.EnvOptions{})
	if err != nil {
		s.c.Log.Error("run: clean environment", "task", id, "err", err)
		return startFailed("The agent's environment could not be prepared.")
	}

	// Ask turns: fingerprint every folder before the run (SF-07).
	var checks []*checker
	var rep report
	gturn := guard.TurnID(cmd.Turn)
	if cmd.Mode == core.ModeAsk {
		for _, d := range append([]string{cmd.Dir}, cmd.ExtraDirs...) {
			c := &checker{dir: d, state: guard.IntegrityIdle{}}
			checks = append(checks, c)
			v := s.integrityStep(at.ctx, c, guard.TurnStarts{Turn: gturn, Mode: cmd.Mode})
			rep.audits = append(rep.audits, v.audits...)
			if v.violated() {
				rep.violated = true
			}
			if v.aborted && !v.violated() {
				if at.ctx.Err() != nil {
					return cancelled()
				}
				return startFailed("Yggdrasil could not take a safety snapshot of the repo in time, so the question was not asked. Try again.")
			}
		}
		if rep.violated {
			s.explain(bg, id, "Yggdrasil could not check that this read-only question leaves the repo unchanged, so it was not run.")
			rep.fin = coretasks.RunFinished{Status: coretasks.RunCancelled}
			return rep
		}
	}

	var base int64
	if base, err = s.c.Store.GetMaxTaskEventSeq(bg, id); err != nil {
		s.c.Log.Error("run: event numbering", "task", id, "err", err)
		return startFailed("The transcript could not be opened. Try again.")
	}
	bt := newBatcher(s, id, cmd.Turn, cmd.Attempt)
	sk := &runSink{s: s, base: base, b: bt, depth: map[string]int{}}
	resume := ""
	if cmd.Resume && caps.Resume {
		resume = st.Task.SessionID
	}
	cap := &capture{Adapter: adapter}
	opts := agents.StartOptions{
		TaskID: id, Prompt: prompt, Mode: cmd.Mode, Model: st.Row.Model, Dir: cmd.Dir, ExtraDirs: cmd.ExtraDirs,
		ResumeSessionID: resume, Env: env, MaxSubagentDepth: cmd.Caps.Depth, MaxConcurrentSubagents: cmd.Caps.Concurrent,
		Policy: policy, ConfigTrusted: true, Admitted: adm.Admitted,
	}
	res, execErr := agents.Execute(at.ctx, cap, opts, sk)
	bt.flush()
	// Whatever the agent left running ends before the tree is judged and
	// before the task is stored as finished.
	s.killTask(id)

	run := cap.started()
	fin := coretasks.RunFinished{SessionID: res.SessionID, Usage: toUsage(res.Usage)}
	switch {
	case run == nil && execErr != nil:
		// The run never started.
		if at.ctx.Err() != nil {
			fin.Status = coretasks.RunCancelled
		} else {
			fin.Status, fin.StartFail = coretasks.RunFailed, true
			s.c.Log.Warn("run: start", "task", id, "err", execErr)
			s.explain(bg, id, userMessage(execErr, "The agent could not be started. The Yggdrasil log has the details."))
		}
	case execErr != nil:
		fin.Status = coretasks.RunFailed
		s.c.Log.Error("run: transcript", "task", id, "err", execErr)
	case res.Status == agents.RunCompleted:
		fin.Status = coretasks.RunCompleted
	case res.Status == agents.RunCancelled:
		fin.Status = coretasks.RunCancelled
	default:
		fin.Status = coretasks.RunFailed
		if res.Err != nil {
			s.c.Log.Warn("agent run failed", "task", id, "err", res.Err, "detail", errors.Unwrap(res.Err))
		}
	}

	// Ask turns: fingerprint again and compare. The agent has ended and its
	// processes were killed, so the tree no longer moves.
	if run != nil || cmd.Mode == core.ModeAsk {
		for _, c := range checks {
			v := s.integrityStep(bg, c, guard.TurnEnds{Turn: gturn})
			rep.audits = append(rep.audits, v.audits...)
			if v.violated() {
				rep.violated = true
			}
		}
		if rep.violated {
			s.explain(bg, id, "The tree changed during a read-only question, or Yggdrasil could not verify that it did not. The task needs your review; nothing was reverted.")
		}
	}

	if run != nil {
		s.checkReports(bg, id, run, &fin, &rep)
	}
	rep.fin = fin
	return rep
}

// checkReports reads what the adapter's run knows about enforcement: a tampered
// run, a tool call the hook never saw, and the hook's refusals. A tampered run
// or an unhooked tool use fails the task (hard requirement 1, SF-06).
func (s *Service) checkReports(ctx context.Context, id core.TaskID, run agents.Run, fin *coretasks.RunFinished, rep *report) {
	fail := func(msg string) {
		fin.Status, fin.StartFail = coretasks.RunFailed, false
		s.explain(ctx, id, msg)
	}
	if t, ok := run.(agents.TamperReporter); ok && t.Tampered() {
		rep.audits = append(rep.audits, auditSpec{Action: "guard.tamper", Detail: map[string]any{"outcome": "failed"}})
		fail("The agent changed Yggdrasil's own safety files, so the task was stopped.")
	}
	if h, ok := run.(agents.HookCoverageReporter); ok {
		if un := h.UnhookedToolUses(); len(un) > 0 {
			rep.audits = append(rep.audits, auditSpec{Action: "guard.unhooked", Detail: map[string]any{"outcome": "failed", "count": len(un)}})
			fail("Some of the agent's actions were not checked by Yggdrasil's safety hook, so the task was stopped.")
		}
	}
	if d, ok := run.(agents.DenyReporter); ok {
		denies := d.Denies()
		total := len(denies)
		if c, ok := run.(agents.DenyCountReporter); ok && c.DenyCount() > total {
			total = c.DenyCount()
		}
		for i, dn := range denies {
			if i == maxAuditedDenies {
				break
			}
			// Tool name and the guard's reason text only: no paths, no input.
			rep.audits = append(rep.audits, auditSpec{Action: "guard.deny", Detail: map[string]any{"tool": dn.Tool, "reason": dn.Reason}})
		}
		if total > maxAuditedDenies {
			rep.audits = append(rep.audits, auditSpec{Action: "guard.deny", Detail: map[string]any{"outcome": "capped", "count": total}})
		}
	}
}

const maxAuditedDenies = 50

// buildPolicy turns the folders of a run into the guard's policy: what may be
// read, what may be written, which commands may run (SF-05, SF-06). Roots are
// canonical (links and short names resolved). Ask has no write root.
func (s *Service) buildPolicy(ctx context.Context, cmd coretasks.StartRun, repos []core.Repo, caps agents.Capabilities) (guard.Policy, error) {
	dirs := append([]string{cmd.Dir}, cmd.ExtraDirs...)
	roots := make([]string, 0, len(dirs))
	for _, d := range dirs {
		r, err := platform.ResolveRoot(d)
		if err != nil {
			return guard.Policy{}, fmt.Errorf("resolve %s: %w", "a task folder", err)
		}
		roots = append(roots, r)
	}
	p := guard.Policy{Mode: cmd.Mode, ReadRoots: roots, FoldCase: platform.FoldCase(), Enforcement: guard.Enforcement(caps.Enforcement)}
	if cmd.Mode != core.ModeAsk {
		p.WriteRoots = append([]string(nil), roots...)
		if s.c.Allowlist != nil {
			seen := map[string]bool{}
			for _, r := range repos {
				rules, err := s.c.Allowlist(ctx, r)
				if err != nil {
					return guard.Policy{}, fmt.Errorf("allowlist of repo %s: %w", r.ID, err)
				}
				for _, rule := range rules {
					key := fmt.Sprintf("%s|%v|%s", rule.Program, rule.Args, rule.Tail)
					if !seen[key] {
						seen[key] = true
						p.Allow = append(p.Allow, rule)
					}
				}
			}
		}
	}
	if err := p.Validate(); err != nil {
		return guard.Policy{}, err
	}
	return p, nil
}

// explain puts a plain-language line into the transcript, so the person sees it
// on the page and not only in the log. The text is redacted like any event.
func (s *Service) explain(ctx context.Context, id core.TaskID, msg string) {
	base, err := s.c.Store.GetMaxTaskEventSeq(ctx, id)
	if err != nil {
		s.c.Log.Error("explain: event numbering", "task", id, "err", err)
		return
	}
	now := s.c.Now().UTC()
	sink := &runSink{s: s, base: base}
	for _, e := range []agents.Event{
		{Seq: 1, Kind: agents.EventError, At: now, IsError: true, Error: msg},
		{Seq: 2, Kind: agents.EventStatus, At: now, Status: agents.RunFailed},
	} {
		if err := sink.Append(ctx, id, e); err != nil {
			s.c.Log.Error("store explanation", "task", id, "err", err)
			return
		}
	}
}

// runSink persists every event and then publishes it, in that order, so a
// client that backfills over REST never misses an event it was not sent live.
// Sequence numbers continue across the turns of a task. It also feeds the
// usage and sub-agent events to the batcher.
type runSink struct {
	s     *Service
	base  int64
	b     *batcher
	depth map[string]int // sub-agent start ID -> depth
}

func (k *runSink) Append(ctx context.Context, id core.TaskID, e agents.Event) error {
	e = k.s.redactEvent(e)
	e.Seq += k.base
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encode event: %w", err)
	}
	if err := k.s.c.Store.AppendTaskEvent(ctx, db.TaskEvent{TaskID: id, Seq: e.Seq, Kind: string(e.Kind), At: e.At, Data: data}); err != nil {
		return err
	}
	k.s.c.Hub.Event(id, e.Seq, e)
	if k.b == nil {
		return nil
	}
	switch e.Kind {
	case agents.EventUsage:
		if e.Usage != nil {
			k.b.usage(toUsage(*e.Usage), e.Usage.Cumulative)
		}
	case agents.EventSubagentStart:
		d := 1
		if e.ParentID != "" {
			d = k.depth[e.ParentID] + 1
		}
		if e.ID != "" {
			k.depth[e.ID] = d
		}
		k.b.subagentStarted(e.ID, d)
	case agents.EventSubagentEnd:
		who := e.ParentID
		if who == "" {
			who = e.ID
		}
		k.b.subagentStopped(who)
	}
	return nil
}

// redactEvent masks tokens in every free-text field of an event. What an agent
// reads can hold secrets (an .env file in the repo): known token shapes are
// masked before the event is stored or sent anywhere (invariant 4).
func (s *Service) redactEvent(e agents.Event) agents.Event {
	r := s.c.Redactor
	e.Text, e.Error = r.Redact(e.Text), r.Redact(e.Error)
	if e.Tool != nil {
		t := *e.Tool
		if len(t.Input) > 0 {
			red := r.Redact(string(t.Input))
			if json.Valid([]byte(red)) {
				t.Input = json.RawMessage(red)
			} else {
				t.Input = json.RawMessage(`{"redacted":true}`)
			}
		}
		e.Tool = &t
	}
	if e.File != nil {
		f := *e.File
		f.Path, f.Diff = r.Redact(f.Path), r.Redact(f.Diff)
		e.File = &f
	}
	if e.Command != nil {
		c := *e.Command
		c.Line, c.Output = r.Redact(c.Line), r.Redact(c.Output)
		e.Command = &c
	}
	return e
}
