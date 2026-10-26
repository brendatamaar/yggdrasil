// Package claudecode is the reference adapter for the Claude Code CLI. It
// runs `claude -p` headless in a task's job, converts the stream-json output
// into normalized agents.Event values and enforces the sub-agent rules with a
// hook (SF-06, AG-22). Nothing outside this package depends on the CLI's
// output format (docs/AGENTS.md 5.5).
package claudecode

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/guard"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// UserError carries a plain-language message that says what to do next. The
// wrapped error, if any, stays internal (docs/AGENTS.md 7).
type UserError struct {
	Message string
	Err     error
}

func (e *UserError) Error() string { return e.Message }
func (e *UserError) Unwrap() error { return e.Err }

// UserMessage is the text that is safe to show a person; callers that only
// know errors in general look for this method.
func (e *UserError) UserMessage() string { return e.Message }

// modelName is what a model name may look like: it goes into a command line.
var modelName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)

// sessionID is what a resumable session ID may look like (a UUID); it goes
// into a command line too.
var sessionID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// batchArgMessage is the user message when a folder name can't be passed to a
// .cmd shim of the CLI. It names the kind of folder, never the path.
const batchArgMessage = "Claude Code couldn't be started because a folder name holds a character a Windows .cmd file can't take safely (such as & % ^ or a quote). Move the project folder, or your user profile folder or temp folder, to a name without those characters."

// Options configure the adapter.
type Options struct {
	// Binary is the claude executable name or path. Default "claude".
	Binary string
	// HookExecutable is the absolute path of the ygg binary, which serves the
	// hidden `hook` subcommand (see RunHook). Required to start a run.
	HookExecutable string
	// TempDir is where each run keeps its settings and hook log. Default: the
	// system temp folder.
	TempDir string
	// EnvBuilder builds the environment for Check. Default: the real one.
	EnvBuilder safety.EnvBuilder
	// CheckEnv adds variables to the environment of Check (used by tests).
	CheckEnv map[string]string
}

// Adapter runs the Claude Code CLI.
type Adapter struct{ opts Options }

// New returns the adapter.
func New(opts Options) *Adapter {
	if opts.Binary == "" {
		opts.Binary = "claude"
	}
	if opts.EnvBuilder == nil {
		opts.EnvBuilder = safety.NewEnvBuilder()
	}
	return &Adapter{opts: opts}
}

// Name implements agents.Adapter.
func (*Adapter) Name() string { return "claude-code" }

// Capabilities implements agents.Adapter. SubagentLimits: the hook enforces the
// sub-agent depth only for a cap of 1 or 2 (its input has no parent link, see
// spawnDepth); deeper caps rest on the CLI's own depth variable. The
// concurrency cap is enforced by both.
func (*Adapter) Capabilities() agents.Capabilities {
	return agents.Capabilities{
		Resume:          true,
		CostReporting:   true,
		PermissionHooks: true,
		Enforcement:     agents.EnforcementHook,
		Subagents:       agents.SubagentsNested,
		SubagentLimits:  true,
		BudgetCap:       true,
	}
}

// Check implements agents.Adapter: the CLI must be installed and start.
func (a *Adapter) Check(ctx context.Context) error {
	env, err := a.opts.EnvBuilder.Build(safety.EnvOptions{Extra: a.opts.CheckEnv})
	if err != nil {
		return fmt.Errorf("build environment: %w", err)
	}
	bin, err := platform.Resolve(a.opts.Binary, "", env)
	if err != nil {
		return &UserError{
			Message: "Claude Code isn't installed, or its folder isn't on the PATH. Install it, open a new terminal, then run `ygg doctor` again.",
			Err:     err,
		}
	}
	return checkCLI(ctx, bin, env)
}

// Start implements agents.Adapter for Ask, Code, Project and Current-branch
// runs. Every tool call of the run is judged by the hook against o.Policy
// (core/guard); the CLI flags are a second layer.
func (a *Adapter) Start(ctx context.Context, o agents.StartOptions) (agents.Run, error) {
	switch {
	case o.Env == nil:
		return nil, errors.New("claudecode: no clean environment given")
	case o.Mode.Validate() != nil:
		return nil, &UserError{Message: "That task mode isn't known."}
	case len(o.ExtraDirs) > 0 && o.Mode != core.ModeProject:
		return nil, &UserError{Message: "Only a task over the whole project can use more than one folder."}
	case o.MCPConfigPath != "" && (!filepath.IsAbs(o.MCPConfigPath) || strings.ContainsAny(o.MCPConfigPath, "\r\n\x00") || !exists(o.MCPConfigPath)):
		return nil, errors.New("claudecode: the MCP config file is missing")
	case !o.Admitted:
		return nil, &UserError{Message: "The task was not cleared by Yggdrasil's safety check, so the agent was not started."}
	case !filepath.IsAbs(a.opts.HookExecutable) || !exists(a.opts.HookExecutable):
		return nil, &UserError{Message: "Yggdrasil's own guard program is missing, so the agent was not started. Reinstall Yggdrasil.",
			Err: fmt.Errorf("hook executable %q not found", a.opts.HookExecutable)}
	case strings.TrimSpace(o.Prompt) == "":
		return nil, &UserError{Message: "The prompt is empty. Write what the agent should look into."}
	case o.Model != "" && !modelName.MatchString(o.Model):
		return nil, &UserError{Message: "That model name isn't valid. Use letters, digits, dots, dashes and colons."}
	case o.ResumeSessionID != "" && !sessionID.MatchString(o.ResumeSessionID):
		return nil, &UserError{Message: "That session ID isn't valid."}
	}
	// Without a valid policy for this very mode the hook would have nothing to
	// enforce, so no run starts (SF-06, invariant 5).
	if err := o.Policy.Validate(); err != nil || o.Policy.Mode != o.Mode || o.Policy.Enforcement != guard.EnforcementHook {
		return nil, &UserError{Message: "The task's safety rules are missing or don't match its mode, so the agent was not started.", Err: err}
	}
	dir, err := platform.LongPath(o.Dir)
	if err != nil {
		return nil, &UserError{Message: "The repo folder can't be used. Check that it still exists.", Err: err}
	}
	dirs := []string{dir}
	extra := make([]string, len(o.ExtraDirs))
	for i, d := range o.ExtraDirs {
		long, err := platform.LongPath(d)
		if err != nil {
			return nil, &UserError{Message: "A folder of the project can't be used. Check that it still exists.", Err: err}
		}
		extra[i] = long
		dirs = append(dirs, long)
	}
	o.ExtraDirs = extra
	for _, d := range dirs {
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			return nil, &UserError{Message: "A repo folder doesn't exist any more. Check the project's folder.", Err: err}
		}
		if err := checkFolderInPolicy(o.Policy, d); err != nil {
			return nil, &UserError{Message: "A folder of this task isn't covered by its safety rules, so the agent was not started.", Err: err}
		}
		// A repo's own Claude Code config (settings, hooks, MCP servers,
		// agents) is inert: --setting-sources "" and a strict MCP config keep
		// it from loading (docs/spikes.md B5). The SF-09 trust policy stays in
		// guard.Admit, so nothing is refused here.
	}
	if err := checkPolicyNotWider(o, dir); err != nil {
		return nil, &UserError{Message: "The task's safety rules reach beyond its folders, so the agent was not started.", Err: err}
	}
	// The program the agent's tool calls trigger must not sit where the agent
	// can overwrite it (or read its way to it).
	if err := checkOutsidePolicy(o.Policy, a.opts.HookExecutable, "the guard program"); err != nil {
		return nil, &UserError{Message: "Yggdrasil's guard program sits inside a folder the agent can reach, so the agent was not started. Install Yggdrasil elsewhere.", Err: err}
	}
	if o.MCPConfigPath != "" {
		if err := checkMCPFileOutsidePolicy(o.Policy, o.MCPConfigPath); err != nil {
			return nil, &UserError{Message: "The agent tool server's config sits where the agent can read it, so the agent was not started.", Err: err}
		}
	}
	bin, err := platform.Resolve(a.opts.Binary, "", o.Env)
	if err != nil {
		return nil, &UserError{Message: "Claude Code isn't installed, or its folder isn't on the PATH. Run `ygg doctor`.", Err: err}
	}
	if err := checkOutsidePolicy(o.Policy, bin, "the claude program"); err != nil {
		return nil, &UserError{Message: "The Claude Code program sits inside a folder the agent can reach, so the agent was not started. Install it elsewhere.", Err: err}
	}
	if err := checkCLI(ctx, bin, o.Env); err != nil {
		if ctx.Err() != nil {
			return cancelledRun(), nil //nolint:nilerr // a start cut short by cancellation is a cancelled run, not an error
		}
		return nil, err
	}
	work, err := os.MkdirTemp(a.opts.TempDir, "ygg-claude-")
	if err != nil {
		return nil, fmt.Errorf("create run folder: %w", err)
	}
	cleanup := func() { removeRunFolder(work, os.RemoveAll, 200*time.Millisecond) } // our own temp folder only
	// The run folder holds the policy, hook config and token: no folder the
	// agent can reach may contain it.
	if err := checkOutsidePolicy(o.Policy, work, "the run folder"); err != nil {
		cleanup()
		return nil, &UserError{Message: "The folder for temporary files sits inside a folder the agent can reach, so the agent was not started. Change the temporary folder.", Err: err}
	}
	// The CLI reads this copy, in a folder no policy root covers; it holds the
	// bearer token and goes away with the run folder.
	mcpPath, err := prepareMCPFile(work, o.MCPConfigPath)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("prepare MCP config: %w", err)
	}
	policy := guard.FormatPolicy(o.Policy)
	if policy == nil {
		cleanup()
		return nil, errors.New("claudecode: encode the policy")
	}
	policyPath := filepath.Join(work, "policy.json")
	if err := os.WriteFile(policyPath, append(policy, '\n'), 0o600); err != nil {
		cleanup()
		return nil, fmt.Errorf("write policy: %w", err)
	}
	settings, hookConfig, err := writeConfigFiles(work, a.opts.HookExecutable, HookConfig{
		LogPath: filepath.Join(work, "hook.log"), MaxDepth: o.MaxSubagentDepth, MaxConcurrent: o.MaxConcurrentSubagents,
		PolicyPath: policyPath,
	})
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("prepare run: %w", err)
	}

	// Remember the files the guard depends on; the loop compares them when the
	// process has ended.
	hashes, err := hashFiles(policyPath, settings, hookConfig, mcpPath)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("prepare run: %w", err)
	}

	pr, pw := io.Pipe()
	stderr := &tailBuffer{max: 8 << 10}
	proc, err := platform.Start(ctx, platform.Spec{
		Argv:   append([]string{bin}, buildArgs(settings, mcpPath, o)...),
		Dir:    dir,
		Env:    buildEnv(o.Env, o, hookConfig),
		TaskID: string(o.TaskID),
		Stdin:  strings.NewReader(o.Prompt), // see buildArgs: the prompt is never an argument
		Stdout: pw,
		Stderr: stderr,
	})
	if err != nil {
		_ = pw.Close()
		_ = pr.Close()
		cleanup()
		if ctx.Err() != nil {
			// Cancelled while starting: the run ends as cancelled, not as a failure.
			return cancelledRun(), nil //nolint:nilerr // a start cut short by cancellation is a cancelled run, not an error
		}
		if errors.Is(err, platform.ErrBatchArg) {
			return nil, &UserError{Message: batchArgMessage, Err: err}
		}
		return nil, &UserError{Message: "Claude Code couldn't be started. Run `ygg doctor` to see what is missing.", Err: err}
	}

	r := &run{events: make(chan agents.Event), done: make(chan struct{}), hashes: hashes}
	go r.loop(ctx, proc, pr, pw, newParser(dir), stderr, filepath.Join(work, "hook.log"), cleanup)
	return r, nil
}

// checkFolderInPolicy confirms that the policy lets the run read the folder
// and, in a writing mode, change it. A mismatch between the folders handed to
// Start and the policy is a bug upstream: it must not start a run whose hook
// would deny everything, or judge another folder than the one the CLI works in.
func checkFolderInPolicy(p guard.Policy, dir string) error {
	canon, err := platform.ResolveRoot(dir)
	if err != nil {
		return err
	}
	ref := []guard.PathRef{{Canon: canon, OK: true}}
	if v := guard.Decide(p, guard.Call{Tool: guard.ToolRead, Paths: ref}); !v.Allow {
		return errors.New("folder is outside the readable folders of the policy")
	}
	if p.Mode == core.ModeAsk {
		return nil
	}
	probe := []guard.PathRef{{Canon: filepath.Join(canon, "probe"), OK: true}}
	if v := guard.Decide(p, guard.Call{Tool: guard.ToolWrite, Paths: probe}); !v.Allow {
		return errors.New("folder is outside the writable folders of the policy")
	}
	return nil
}

// exists reports whether a file or folder exists.
func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// cancelledRun is a run that never started because its context ended first.
func cancelledRun() *run {
	r := &run{events: make(chan agents.Event, 2), done: make(chan struct{})}
	now := time.Now().UTC()
	r.events <- agents.Event{Kind: agents.EventStatus, Status: agents.RunStarted, At: now}
	r.events <- agents.Event{Kind: agents.EventStatus, Status: agents.RunCancelled, At: now}
	close(r.events)
	close(r.done)
	return r
}

// run is one Claude Code session. Two goroutines belong to it, and both end
// before Events is closed: the waiter (process exit) and the loop (stream).
type run struct {
	events  chan agents.Event
	session atomic.Value // string
	done    chan struct{}
	err     error
	// denies and toolUses are set before done is closed; read them after Wait.
	denies    []agents.Deny
	denyCount int
	toolUses  []string
	hooked    []string
	unhooked  []string
	// hashes are the run files as written at Start; tampered is set by the
	// loop when the process has ended and any of them changed.
	hashes   fileHashes
	tampered bool
}

func (r *run) Events() <-chan agents.Event { return r.events }

func (r *run) SessionID() string {
	s, _ := r.session.Load().(string)
	return s
}

func (r *run) Wait() error {
	<-r.done
	return r.err
}

type waitResult struct {
	code int
	err  error
}

func (r *run) loop(ctx context.Context, proc platform.Process, pr *io.PipeReader, pw *io.PipeWriter, p *parser, stderr *tailBuffer, hookLog string, cleanup func()) {
	defer close(r.done)
	defer close(r.events)
	defer func() {
		// The hook log goes with the run folder; keep what the tasks service audits.
		sum := summarizeHookLog(hookLog)
		r.denies, r.denyCount, r.hooked = sum.denies, sum.denyCount, sum.hooked
		r.toolUses = p.toolUses
		r.unhooked = unhooked(p.toolUses, sum.hooked)
		cleanup()
	}()

	waitc := make(chan waitResult, 1)
	go func() { // waiter: ends when the process tree is gone
		code, err := proc.Wait()
		_ = pw.Close() // end of stream for the reader
		waitc <- waitResult{code, err}
	}()

	send := func(e agents.Event) bool {
		select {
		case r.events <- e:
			return true
		case <-ctx.Done():
			return false
		}
	}
	if !send(agents.Event{Kind: agents.EventStatus, Status: agents.RunStarted, At: time.Now().UTC()}) {
		_ = pr.Close()
		r.finish(ctx, p, <-waitc, stderr)
		return
	}
	readLines(pr, func(line []byte) bool {
		for _, ev := range p.parseLine(line) {
			if !send(ev) {
				return false
			}
		}
		if p.session != "" {
			r.session.Store(p.session)
		}
		return true
	})
	_ = pr.Close() // a writer blocked on a full pipe must not hold the process
	r.finish(ctx, p, <-waitc, stderr)
}

// finish emits the final events. The sends do not watch the context, so a
// cancelled run still reports that it was cancelled; the consumer drains
// Events until it is closed (agents.Execute does).
func (r *run) finish(ctx context.Context, p *parser, res waitResult, stderr *tailBuffer) {
	status := agents.RunFailed
	// The process is gone, so the files can't change any more. A run that
	// altered the guard's files can't be trusted, whatever it reported.
	if r.hashes.changed() {
		r.tampered = true
		msg := tamperMessage
		r.events <- agents.Event{Kind: agents.EventError, At: time.Now().UTC(), IsError: true, Error: msg}
		r.err = &UserError{Message: msg}
		r.events <- agents.Event{Kind: agents.EventStatus, Status: agents.RunFailed, At: time.Now().UTC()}
		return
	}
	switch {
	case ctx.Err() != nil:
		status = agents.RunCancelled
	case p.sawResult && p.resultOK:
		status = agents.RunCompleted
	}
	if status == agents.RunFailed && !p.sawResult {
		msg := fmt.Sprintf("Claude Code stopped before it finished (exit code %d). The Yggdrasil log has the details.", res.code)
		r.events <- agents.Event{Kind: agents.EventError, At: time.Now().UTC(), IsError: true, Error: msg}
		r.err = &UserError{Message: msg, Err: fmt.Errorf("claude stderr: %s", safety.NewRedactor().Redact(stderr.String()))}
	}
	r.events <- agents.Event{Kind: agents.EventStatus, Status: status, At: time.Now().UTC()}
}

// readLines calls fn for each line of r until fn returns false or the stream
// ends. A line longer than maxLineBytes is skipped (one error event is made
// by the parser for the unreadable remainder).
func readLines(r io.Reader, fn func(line []byte) bool) {
	br := bufio.NewReaderSize(r, 64<<10)
	var buf []byte
	tooLong := false
	for {
		chunk, err := br.ReadSlice('\n')
		switch {
		case tooLong:
		case len(buf)+len(chunk) > maxLineBytes:
			tooLong, buf = true, buf[:0]
		default:
			buf = append(buf, chunk...)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		line := buf
		if tooLong {
			line = []byte("{")
		}
		if len(line) > 0 && !fn(line) {
			return
		}
		buf, tooLong = buf[:0], false
		if err != nil {
			return
		}
	}
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// Denies implements agents.DenyReporter: the tool calls the hook refused. Call
// it after Wait.
func (r *run) Denies() []agents.Deny { return r.denies }

// SeenToolUses implements agents.ToolUseReporter: the IDs of the tool calls
// the stream showed. Call it after Wait.
func (r *run) SeenToolUses() []string { return r.toolUses }

// DenyCount implements agents.DenyCountReporter: the true number of refusals,
// of which Denies keeps the first 100.
func (r *run) DenyCount() int { return r.denyCount }

// HookedToolUses implements agents.HookCoverageReporter.
func (r *run) HookedToolUses() []string { return r.hooked }

// UnhookedToolUses implements agents.HookCoverageReporter: stream tool_use IDs
// with no hook record. Computed before the hook log is deleted.
func (r *run) UnhookedToolUses() []string { return r.unhooked }

// Tampered implements agents.TamperReporter.
func (r *run) Tampered() bool { return r.tampered }
