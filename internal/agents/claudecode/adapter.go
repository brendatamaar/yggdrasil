// Package claudecode is the reference adapter for the Claude Code CLI. It
// runs `claude -p` headless in a task's job, converts the stream-json output
// into normalized agents.Event values and enforces the sub-agent rules with a
// hook (SF-06, AG-22). Nothing outside this package depends on the CLI's
// output format (docs/AGENTS.md 5.5).
package claudecode

import (
	"bufio"
	"bytes"
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
var sessionID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,63}$`)

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

// Capabilities implements agents.Adapter.
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
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var out, errOut bytes.Buffer
	p, err := platform.Start(ctx, platform.Spec{Argv: []string{bin, "--version"}, Env: env, Stdout: &out, Stderr: &errOut})
	if err != nil {
		return &UserError{Message: "Claude Code couldn't be started. Run `claude --version` in a terminal to see why.", Err: err}
	}
	code, err := p.Wait()
	if err != nil || code != 0 {
		return &UserError{
			Message: fmt.Sprintf("Claude Code didn't start (exit code %d). Run `claude --version` in a terminal to see why, and sign in with `claude` if it asks.", code),
			Err:     err,
		}
	}
	return nil
}

// Start implements agents.Adapter. Only Ask mode is wired: Code mode needs
// worktrees and the path guard, which arrive in a later phase.
func (a *Adapter) Start(ctx context.Context, o agents.StartOptions) (agents.Run, error) {
	switch {
	case o.Env == nil:
		return nil, errors.New("claudecode: no clean environment given")
	case o.Mode != core.ModeAsk:
		return nil, &UserError{Message: "Claude Code can only run in Ask mode (read-only) in this version."}
	case strings.TrimSpace(o.Prompt) == "":
		return nil, &UserError{Message: "The prompt is empty. Write what the agent should look into."}
	case o.Model != "" && !modelName.MatchString(o.Model):
		return nil, &UserError{Message: "That model name isn't valid. Use letters, digits, dots, dashes and colons."}
	case o.ResumeSessionID != "" && !sessionID.MatchString(o.ResumeSessionID):
		return nil, &UserError{Message: "That session ID isn't valid."}
	}
	dir, err := platform.LongPath(o.Dir)
	if err != nil {
		return nil, &UserError{Message: "The repo folder can't be used. Check that it still exists.", Err: err}
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil, &UserError{Message: "The repo folder doesn't exist any more. Check the project's folder.", Err: err}
	}
	// A repo that configures Claude Code itself (settings with hooks, MCP
	// servers, agent definitions) could run its own commands even in Ask mode.
	// No agent runs there until the user can trust a repo (SF-09, invariant 10).
	if f := ownAgentConfig(dir, exists); f != "" {
		return nil, &UserError{Message: fmt.Sprintf(
			"This repo has its own Claude Code configuration (%s), which could run commands. "+
				"Yggdrasil doesn't run agents in such a repo until you can trust it once, which arrives in a later version.", f)}
	}
	bin, err := platform.Resolve(a.opts.Binary, "", o.Env)
	if err != nil {
		return nil, &UserError{Message: "Claude Code isn't installed, or its folder isn't on the PATH. Run `ygg doctor`.", Err: err}
	}
	work, err := os.MkdirTemp(a.opts.TempDir, "ygg-claude-")
	if err != nil {
		return nil, fmt.Errorf("create run folder: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(work) } // our own temp folder only
	settings, hookConfig, err := writeConfigFiles(work, a.opts.HookExecutable, HookConfig{
		LogPath: filepath.Join(work, "hook.log"), MaxDepth: o.MaxSubagentDepth, MaxConcurrent: o.MaxConcurrentSubagents,
		AllowedTools: askTools, AllowedDir: dir,
	})
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("prepare run: %w", err)
	}

	pr, pw := io.Pipe()
	stderr := &tailBuffer{max: 8 << 10}
	proc, err := platform.Start(ctx, platform.Spec{
		Argv:   append([]string{bin}, buildArgs(settings, o)...),
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
		return nil, &UserError{Message: "Claude Code couldn't be started. Run `ygg doctor` to see what is missing.", Err: err}
	}

	r := &run{events: make(chan agents.Event), done: make(chan struct{})}
	go r.loop(ctx, proc, pr, pw, newParser(dir), stderr, cleanup)
	return r, nil
}

// exists reports whether a file or folder exists.
func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// run is one Claude Code session. Two goroutines belong to it, and both end
// before Events is closed: the waiter (process exit) and the loop (stream).
type run struct {
	events  chan agents.Event
	session atomic.Value // string
	done    chan struct{}
	err     error
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

func (r *run) loop(ctx context.Context, proc platform.Process, pr *io.PipeReader, pw *io.PipeWriter, p *parser, stderr *tailBuffer, cleanup func()) {
	defer close(r.done)
	defer close(r.events)
	defer cleanup()

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
