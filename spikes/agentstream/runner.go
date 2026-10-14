package agentstream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Config is one headless run of the agent CLI.
type Config struct {
	// Binary is the agent executable, already resolved by the caller.
	Binary string
	// Dir is the working directory: always the scratch repo, never a real one.
	Dir    string
	Prompt string
	Model  string
	// MaxBudgetUSD caps spend; 0 leaves the flag out.
	MaxBudgetUSD float64
	// SettingsPath is the generated settings file (hooks, env).
	SettingsPath string
	// ExtraArgs go after the known flags, as is (live-run experiments).
	ExtraArgs []string
	// Env are extras on top of BaseEnvAllowlist. Secrets are refused (CleanEnv).
	Env map[string]string
	// Stderr receives the agent's stderr. Use an *os.File: then os/exec starts
	// no copy goroutine. Nil discards it.
	Stderr io.Writer
	// Lookup reads our environment for the allowlist; nil means os.LookupEnv.
	Lookup func(string) (string, bool)
}

// Args builds the CLI arguments. UNVERIFIED, each one: -p (headless print
// mode, a boolean flag with the prompt as positional argument),
// --output-format stream-json, --verbose (needed for stream-json with -p),
// --forward-subagent-text (sub-agent messages in the stream, tagged with
// parent_tool_use_id), --settings <file>, --model, --max-budget-usd.
func (c Config) Args() []string {
	args := []string{"-p", "--output-format", "stream-json", "--verbose", "--forward-subagent-text"}
	if c.SettingsPath != "" {
		args = append(args, "--settings", c.SettingsPath)
	}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	if c.MaxBudgetUSD > 0 {
		args = append(args, "--max-budget-usd", strconv.FormatFloat(c.MaxBudgetUSD, 'f', -1, 64))
	}
	args = append(args, c.ExtraArgs...)
	return append(args, c.Prompt)
}

// Exit is how a run ended.
type Exit struct {
	// Code is the agent's exit code; -1 if it couldn't be read.
	Code int `json:"code"`
	// Cancelled: the context ended the run and the job was terminated.
	Cancelled bool   `json:"cancelled"`
	Err       string `json:"error,omitempty"`
}

// Run is a started agent.
//
// Goroutines, all ended by Wait's return: the waiter (cmd.Wait, then kills
// leftovers), the canceller (terminates the group on ctx.Done) and the reader
// (parses stdout, owns and closes Events).
type Run struct {
	// Events delivers parsed events. It is closed once the stream has ended
	// and the whole process tree is gone. Drain it or cancel the context;
	// otherwise the run blocks.
	Events <-chan Event
	// PID is the agent process.
	PID int

	g      group
	killed atomic.Bool
	done   chan struct{}
	exit   Exit
}

// Start runs the agent in its own job and streams its events. Cancelling ctx
// terminates the whole job; Events is closed afterwards.
func Start(ctx context.Context, cfg Config) (*Run, error) {
	if cfg.Binary == "" {
		return nil, errors.New("no agent binary given")
	}
	if strings.TrimSpace(cfg.Prompt) == "" || strings.HasPrefix(cfg.Prompt, "-") {
		return nil, errors.New("the prompt must not be empty or start with '-'")
	}
	lookup := cfg.Lookup
	if lookup == nil {
		lookup = os.LookupEnv
	}
	env, err := CleanEnv(lookup, cfg.Env)
	if err != nil {
		return nil, err
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	// Not CommandContext: cancel must take the whole job, not one process.
	cmd := exec.Command(cfg.Binary, cfg.Args()...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = cfg.Dir, env, pw, cfg.Stderr
	// Bounds Wait when Stderr isn't a file and a leftover process holds it.
	cmd.WaitDelay = 2 * time.Second
	g, err := startInGroup(cmd)
	// The children have their copies; EOF comes once every holder is gone.
	_ = pw.Close()
	if err != nil {
		_ = pr.Close()
		return nil, err
	}

	events := make(chan Event)
	r := &Run{Events: events, PID: cmd.Process.Pid, g: g, done: make(chan struct{})}
	exited := make(chan struct{})

	go func() { // waiter
		defer close(exited)
		werr := cmd.Wait()
		// Leftovers (background tool processes) die with the agent; this also
		// closes their copies of the stdout pipe.
		_ = g.terminate()
		r.exit = exitOf(cmd, werr)
	}()

	go func() { // canceller
		select {
		case <-ctx.Done():
			r.killed.Store(true)
			_ = g.terminate()
		case <-exited:
		}
	}()

	go func() { // reader, owns events
		defer close(r.done)
		defer close(events)
		perr := Parse(pr, func(ev Event) bool {
			select {
			case events <- ev:
				return true
			case <-ctx.Done():
				return false
			}
		})
		_ = pr.Close()
		<-exited
		_ = g.close()
		r.exit.Cancelled = r.killed.Load()
		if perr != nil && r.exit.Err == "" {
			r.exit.Err = perr.Error()
		}
	}()
	return r, nil
}

// Wait blocks until Events is closed and the tree is gone, and returns how the
// run ended.
func (r *Run) Wait() Exit {
	<-r.done
	return r.exit
}

func exitOf(cmd *exec.Cmd, err error) Exit {
	var ee *exec.ExitError
	switch {
	case err == nil:
		return Exit{}
	case errors.As(err, &ee):
		return Exit{Code: ee.ExitCode()}
	case errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil:
		return Exit{Code: cmd.ProcessState.ExitCode()}
	default:
		return Exit{Code: -1, Err: err.Error()}
	}
}
