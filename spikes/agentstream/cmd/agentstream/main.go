// Command agentstream is the B4 spike harness. See package agentstream.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/brendatamaar/yggdrasil/spikes/agentstream"
)

const usage = `usage:
  agentstream run --prompt "..." [--model haiku] [--max-budget-usd 0.10] [--keep]
                  [--claude <path>] [--deny-mode exit2|json|json-legacy]
                  [--max-depth 2] [--max-concurrent 4] [--timeout 10m] [--extra-arg <arg>]...
  agentstream hook --log <file> [--event PreToolUse] [--deny-mode exit2]
                   [--max-depth 0] [--max-concurrent 0]
  agentstream scrub --in <raw.jsonl> --out <fixture.jsonl> [--replace old=new]...`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	var code int
	switch os.Args[1] {
	case "run":
		code = cmdRun(os.Args[2:])
	case "hook":
		code = cmdHook(os.Args[2:])
	case "scrub":
		code = cmdScrub(os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, usage)
		code = 2
	}
	os.Exit(code)
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, " ") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	prompt := fs.String("prompt", "", "prompt for the agent (required)")
	model := fs.String("model", "", "model alias or name")
	budget := fs.Float64("max-budget-usd", 0.10, "spend cap passed to the CLI; 0 leaves it out")
	keep := fs.Bool("keep", false, "keep the run dir (scratch repo, raw capture, hook log)")
	claude := fs.String("claude", "", "agent binary (default: claude on PATH)")
	denyMode := fs.String("deny-mode", string(agentstream.DenyExit2), "hook deny variant: exit2, json or json-legacy")
	maxDepth := fs.Int("max-depth", 2, "sub-agent nesting cap (env var and hook); 0 = none")
	maxConc := fs.Int("max-concurrent", 4, "sub-agent concurrency cap (env var and hook); 0 = none")
	timeout := fs.Duration("timeout", 10*time.Minute, "cancel the run after this long")
	var extra multiFlag
	fs.Var(&extra, "extra-arg", "extra CLI argument, repeatable")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	mode, err := agentstream.ParseDenyMode(*denyMode)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if *prompt == "" {
		fmt.Fprintln(os.Stderr, "run: --prompt is required")
		return 2
	}
	ok, err := run(runOptions{
		prompt: *prompt, model: *model, budget: *budget, keep: *keep, claude: *claude, mode: mode,
		maxDepth: *maxDepth, maxConc: *maxConc, timeout: *timeout, extra: extra,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "run:", err)
		return 1
	}
	if !ok {
		return 1
	}
	return 0
}

type runOptions struct {
	prompt, model, claude string
	budget                float64
	keep                  bool
	mode                  agentstream.DenyMode
	maxDepth, maxConc     int
	timeout               time.Duration
	extra                 []string
}

func run(o runOptions) (bool, error) {
	bin := o.claude
	if bin == "" {
		p, err := exec.LookPath("claude")
		if err != nil {
			return false, fmt.Errorf("claude not found on PATH; pass --claude: %w", err)
		}
		bin = p
	}
	self, err := os.Executable()
	if err != nil {
		return false, fmt.Errorf("find own binary for the hook: %w", err)
	}
	dir, err := os.MkdirTemp("", "agentstream-")
	if err != nil {
		return false, err
	}
	if o.keep {
		fmt.Fprintln(os.Stderr, "run dir:", dir)
	} else {
		defer os.RemoveAll(dir)
	}
	repo, err := agentstream.NewScratchRepo(dir)
	if err != nil {
		return false, err
	}
	logPath := filepath.Join(dir, "hook.log")
	settingsPath := filepath.Join(dir, "settings.json")
	subEnv := agentstream.SubagentEnv(o.maxDepth, o.maxConc)
	hs := agentstream.HookSettings{Exe: self, LogPath: logPath, Mode: o.mode, MaxDepth: o.maxDepth, MaxConcurrent: o.maxConc}
	if err := agentstream.WriteSettings(settingsPath, hs, subEnv); err != nil {
		return false, err
	}
	stderr, err := os.Create(filepath.Join(dir, "stderr.log"))
	if err != nil {
		return false, err
	}
	defer stderr.Close()
	raw, err := os.Create(filepath.Join(dir, "raw.jsonl"))
	if err != nil {
		return false, err
	}
	defer raw.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()

	r, err := agentstream.Start(ctx, agentstream.Config{
		Binary: bin, Dir: repo, Prompt: o.prompt, Model: o.model, MaxBudgetUSD: o.budget,
		SettingsPath: settingsPath, ExtraArgs: o.extra, Env: subEnv, Stderr: stderr,
	})
	if err != nil {
		return false, err
	}
	sum := agentstream.NewSummary()
	enc := json.NewEncoder(os.Stdout)
	lastLine := 0
	for ev := range r.Events {
		sum.Add(ev)
		if ev.Raw != "" && ev.Line != lastLine {
			lastLine = ev.Line
			_, _ = raw.WriteString(ev.Raw + "\n")
		}
		ev.Raw = ""
		_ = enc.Encode(ev)
	}
	exit := r.Wait()
	sum.Exit = &exit
	recs, err := agentstream.ReadHookLog(logPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hook log:", err)
	}
	sum.AddHooks(recs)
	if o.keep {
		sum.RunDir = dir
	}
	out, err := json.MarshalIndent(sum, "", "  ")
	if err != nil {
		return false, err
	}
	fmt.Fprintf(os.Stderr, "summary:\n%s\n", out)
	return exit.Code == 0 && !exit.Cancelled, nil
}

func cmdHook(args []string) int {
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	logPath := fs.String("log", "", "hook log file (required)")
	event := fs.String("event", "", "hook event; overrides hook_event_name from the input")
	denyMode := fs.String("deny-mode", string(agentstream.DenyExit2), "exit2, json or json-legacy")
	maxDepth := fs.Int("max-depth", 0, "sub-agent nesting cap; 0 = none")
	maxConc := fs.Int("max-concurrent", 0, "sub-agent concurrency cap; 0 = none")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	mode, err := agentstream.ParseDenyMode(*denyMode)
	if err != nil || *logPath == "" {
		// Fail closed: a misconfigured hook blocks the call.
		fmt.Fprintln(os.Stderr, "agentstream hook: need --log and a valid --deny-mode")
		return 2
	}
	return agentstream.RunHook(agentstream.HookOptions{
		LogPath: *logPath, Mode: mode, Event: *event, MaxDepth: *maxDepth, MaxConcurrent: *maxConc,
	}, os.Stdin, os.Stdout, os.Stderr)
}

func cmdScrub(args []string) int {
	fs := flag.NewFlagSet("scrub", flag.ContinueOnError)
	in := fs.String("in", "", "raw capture (required)")
	out := fs.String("out", "", "scrubbed fixture (required, must not exist)")
	var repl multiFlag
	fs.Var(&repl, "replace", "extra old=new literal replacement, repeatable")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *in == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "scrub: --in and --out are required")
		return 2
	}
	if err := scrub(*in, *out, repl); err != nil {
		fmt.Fprintln(os.Stderr, "scrub:", err)
		return 1
	}
	return 0
}

// defaultScrub replaces this machine's identity with fictional values.
func defaultScrub() map[string]string {
	m := map[string]string{}
	for name, to := range map[string]string{
		"USERPROFILE":  `C:\Users\dev`,
		"LOCALAPPDATA": `C:\Users\dev\AppData\Local`,
		"APPDATA":      `C:\Users\dev\AppData\Roaming`,
		"TEMP":         `C:\Temp`,
		"USERNAME":     "dev",
		"COMPUTERNAME": "devbox",
		"USERDOMAIN":   "devbox",
	} {
		if v := os.Getenv(name); v != "" {
			m[v] = to
		}
	}
	return m
}

func scrub(in, out string, extra []string) error {
	repl := defaultScrub()
	for _, kv := range extra {
		from, to, ok := strings.Cut(kv, "=")
		if !ok || from == "" {
			return fmt.Errorf("--replace %q: want old=new", kv)
		}
		repl[from] = to
	}
	src, err := os.Open(in)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := agentstream.NewScrubber(repl).Copy(src, dst); err != nil {
		_ = dst.Close()
		return err
	}
	return dst.Close()
}
