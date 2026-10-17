package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// The fake "claude" is this test binary, started with YGG_FAKE_CLAUDE set. A
// fake never falls through to running the test suite: an unknown mode exits.
const (
	fakeEnv    = "YGG_FAKE_CLAUDE"
	fakeStream = "YGG_FAKE_STREAM"
	fakeOut    = "YGG_FAKE_OUT"
)

// fakeEnvNames are the variables the fake reports back: the ones the adapter
// sets, and tokens the test puts into the parent's environment.
var fakeEnvNames = []string{
	envMaxSubagentDepth, envMaxConcurrentSubagents, envDisableBackground, HookConfigEnv,
	"GITHUB_TOKEN", "GITLAB_TOKEN", "ANTHROPIC_API_KEY", "VIRTUAL_ENV", "PYTHONPATH",
}

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeEnv); mode != "" {
		os.Exit(runFake(mode))
	}
	os.Exit(m.Run())
}

func runFake(mode string) int {
	switch mode {
	case "version":
		fmt.Println("2.1.299 (Claude Code)")
		return 0
	case "exit3":
		fmt.Fprintln(os.Stderr, "boom: token sk-ant-api03-SECRETSECRETSECRET and C:\\Users\\someone\\x")
		return 3
	case "stream":
		data, err := os.ReadFile(os.Getenv(fakeStream))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		_, _ = os.Stdout.Write(data)
		return 0
	case "capture":
		out := os.Getenv(fakeOut)
		var settings string
		for i, a := range os.Args {
			if a == "--settings" && i+1 < len(os.Args) {
				settings = os.Args[i+1]
			}
		}
		_ = os.WriteFile(out+".args", []byte(strings.Join(os.Args[1:], "\x00")), 0o600)
		prompt, _ := io.ReadAll(os.Stdin)
		_ = os.WriteFile(out+".stdin", prompt, 0o600)
		_ = os.WriteFile(out+".settingspath", []byte(settings), 0o600)
		if data, err := os.ReadFile(settings); err == nil {
			_ = os.WriteFile(out+".settings", data, 0o600)
		}
		if data, err := os.ReadFile(os.Getenv(HookConfigEnv)); err == nil {
			_ = os.WriteFile(out+".hookcfg", data, 0o600)
		}
		var seen []string
		for _, name := range fakeEnvNames {
			seen = append(seen, name+"="+os.Getenv(name))
		}
		_ = os.WriteFile(out+".env", []byte(strings.Join(seen, "\x00")), 0o600)
		cwd, _ := os.Getwd()
		_ = os.WriteFile(out+".cwd", []byte(cwd), 0o600)
		fmt.Println(`{"type":"result","subtype":"success","is_error":false,"session_id":"sess-capture","total_cost_usd":0.5,"num_turns":1,"duration_ms":10}`)
		return 0
	case "sleep":
		fmt.Println(`{"type":"system","subtype":"init","session_id":"sess-sleep"}`)
		time.Sleep(60 * time.Second)
		return 0
	}
	fmt.Fprintln(os.Stderr, "unknown fake mode", mode)
	return 2
}

func testExe(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

func newAdapter(t *testing.T, mode string) *Adapter {
	t.Helper()
	exe := testExe(t)
	return New(Options{
		Binary: exe, HookExecutable: exe, TempDir: t.TempDir(),
		CheckEnv: map[string]string{fakeEnv: mode},
	})
}

// startOpts returns options for a fake run in mode, with the fake's own
// variables added to a clean environment.
func startOpts(t *testing.T, mode string, extra map[string]string) agents.StartOptions {
	t.Helper()
	vars := map[string]string{fakeEnv: mode}
	maps.Copy(vars, extra)
	env, err := safety.NewEnvBuilder().Build(safety.EnvOptions{Extra: vars})
	if err != nil {
		t.Fatal(err)
	}
	return agents.StartOptions{
		TaskID: "tsk_test", Prompt: "Find the order routes.", Mode: core.ModeAsk,
		Dir: t.TempDir(), Env: env, MaxSubagentDepth: 2, MaxConcurrentSubagents: 4,
	}
}

type collectSink struct {
	mu      sync.Mutex
	events  []agents.Event
	onEvent func(agents.Event)
}

func (c *collectSink) Append(_ context.Context, _ core.TaskID, e agents.Event) error {
	c.mu.Lock()
	c.events = append(c.events, e)
	fn := c.onEvent
	c.mu.Unlock()
	if fn != nil {
		fn(e)
	}
	return nil
}

func (c *collectSink) all() []agents.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.events)
}

func TestCapabilities(t *testing.T) {
	a := New(Options{})
	c := a.Capabilities()
	if a.Name() != "claude-code" || !c.Resume || !c.CostReporting || !c.PermissionHooks || !c.SubagentLimits || !c.BudgetCap ||
		c.Enforcement != agents.EnforcementHook || c.Subagents != agents.SubagentsNested {
		t.Errorf("capabilities = %+v", c)
	}
}

func TestCheck(t *testing.T) {
	if err := newAdapter(t, "version").Check(context.Background()); err != nil {
		t.Errorf("Check with a working CLI: %v", err)
	}

	err := newAdapter(t, "exit3").Check(context.Background())
	var ue *UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.Message, "exit code 3") || !strings.Contains(ue.Message, "claude") {
		t.Errorf("Check with a failing CLI = %v", err)
	}

	missing := New(Options{Binary: "no-such-claude-binary-xyz"})
	err = missing.Check(context.Background())
	if !errors.As(err, &ue) || !strings.Contains(ue.Message, "isn't installed") || !strings.Contains(ue.Message, "ygg doctor") {
		t.Errorf("Check with no CLI = %v", err)
	}
	if errors.Unwrap(err) == nil {
		t.Error("the internal cause was dropped")
	}
}

func TestStartRefusals(t *testing.T) {
	a := newAdapter(t, "stream")
	ok := startOpts(t, "stream", nil)
	cases := map[string]func(*agents.StartOptions){
		"nil env":      func(o *agents.StartOptions) { o.Env = nil },
		"code mode":    func(o *agents.StartOptions) { o.Mode = core.ModeCode },
		"project mode": func(o *agents.StartOptions) { o.Mode = core.ModeProject },
		"empty prompt": func(o *agents.StartOptions) { o.Prompt = "  \n" },
		"missing dir":  func(o *agents.StartOptions) { o.Dir = filepath.Join(o.Dir, "nope") },
	}
	for name, mutate := range cases {
		o := ok
		mutate(&o)
		if r, err := a.Start(context.Background(), o); err == nil {
			t.Errorf("%s: Start succeeded", name)
			for range r.Events() { //nolint:revive // drain so the run ends
			}
		}
	}
	// A repo that configures Claude Code itself could run its own commands.
	for _, name := range []string{".claude/settings.json", ".claude/settings.local.json", ".mcp.json", ".claude/agents"} {
		o := ok
		o.Dir = t.TempDir()
		path := filepath.Join(o.Dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		var err error
		if strings.HasSuffix(name, "agents") {
			err = os.Mkdir(path, 0o700)
		} else {
			err = os.WriteFile(path, []byte("{}"), 0o600)
		}
		if err != nil {
			t.Fatal(err)
		}
		r, err := a.Start(context.Background(), o)
		var ue *UserError
		if err == nil || !errors.As(err, &ue) || !strings.Contains(ue.Message, name) || !strings.Contains(ue.Message, "own Claude Code configuration") {
			t.Errorf("%s: err = %v", name, err)
			if err == nil {
				for range r.Events() { //nolint:revive // drain so the run ends
				}
			}
		}
	}
	// Model names go onto a command line.
	for _, model := range []string{"--dangerously-skip-permissions", "a b", "x;y", "-m", strings.Repeat("a", 65)} {
		o := ok
		o.Model = model
		if r, err := a.Start(context.Background(), o); err == nil {
			t.Errorf("model %q was accepted", model)
			for range r.Events() { //nolint:revive // drain so the run ends
			}
		}
	}
	// The hook executable must be absolute.
	bad := New(Options{Binary: testExe(t), HookExecutable: "ygg", TempDir: t.TempDir()})
	if r, err := bad.Start(context.Background(), ok); err == nil {
		t.Error("a relative hook executable was accepted")
		for range r.Events() { //nolint:revive // drain so the run ends
		}
	}
}

func TestRunStreamsARecordedSession(t *testing.T) {
	a := newAdapter(t, "stream")
	o := startOpts(t, "stream", map[string]string{fakeStream: mustAbs(t, filepath.Join("testdata", "live-subagent.jsonl"))})
	sink := &collectSink{}
	res, err := agents.Execute(context.Background(), a, o, sink)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != agents.RunCompleted || res.Err != nil {
		t.Fatalf("result = %+v", res)
	}
	if res.SessionID != "00000000-0000-4000-8000-000000000001" {
		t.Errorf("session = %q", res.SessionID)
	}
	u := res.Usage
	if u.InputTokens != 8 || u.OutputTokens != 29 || u.CacheWriteTokens != 21159 || u.CacheReadTokens != 64524 || u.Turns != 2 || u.CostUSD == nil {
		t.Errorf("usage = %+v", u)
	}
	events := sink.all()
	if first := events[0]; first.Kind != agents.EventStatus || first.Status != agents.RunStarted {
		t.Errorf("first event = %+v", first)
	}
	if last := events[len(events)-1]; last.Kind != agents.EventStatus || last.Status != agents.RunCompleted {
		t.Errorf("last event = %+v", last)
	}
	var starts, ends int
	for i, e := range events {
		if e.Seq != int64(i+1) {
			t.Fatalf("event %d has seq %d", i, e.Seq)
		}
		switch e.Kind {
		case agents.EventSubagentStart:
			starts++
		case agents.EventSubagentEnd:
			ends++
		}
	}
	if starts != 1 || ends != 1 {
		t.Errorf("sub-agent start/end = %d/%d", starts, ends)
	}
	for _, root := range agents.BuildTree(events) {
		if root.Orphan {
			t.Errorf("orphan root %+v", root.Event)
		}
	}
}

func TestRunCommandLineEnvironmentAndCleanup(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_abcdefghijklmnopqrstuvwxyz0123")
	t.Setenv("GITLAB_TOKEN", "glpat-abcdefghijklmnopqrst")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-api03-abcdefghijklmnop")
	t.Setenv("VIRTUAL_ENV", `C:\venv`)
	t.Setenv("PYTHONPATH", `C:\py`)
	a := newAdapter(t, "capture")
	out := filepath.Join(t.TempDir(), "capture")
	o := startOpts(t, "capture", map[string]string{fakeOut: out})
	o.Model, o.BudgetUSD = "haiku", 0.1
	sink := &collectSink{}
	res, err := agents.Execute(context.Background(), a, o, sink)
	if err != nil || res.Status != agents.RunCompleted || res.SessionID != "sess-capture" {
		t.Fatalf("result = %+v, %v", res, err)
	}

	argv := strings.Split(read(t, out+".args"), "\x00")
	// The prompt reaches the CLI on stdin, exactly as written, and not as an
	// argument.
	if got := read(t, out+".stdin"); got != o.Prompt {
		t.Errorf("stdin = %q, want the prompt %q", got, o.Prompt)
	}
	if slices.Contains(argv, o.Prompt) || slices.Contains(argv, "--") {
		t.Errorf("the prompt or a separator is on the command line: %q", argv)
	}
	for _, want := range []string{"--forward-subagent-text", "--setting-sources", "dontAsk", "--allowedTools", "--max-budget-usd", "0.1", "--model", "haiku"} {
		if !slices.Contains(argv, want) {
			t.Errorf("argument %q missing from %q", want, argv)
		}
	}

	env := map[string]string{}
	for kv := range strings.SplitSeq(read(t, out+".env"), "\x00") {
		k, v, _ := strings.Cut(kv, "=")
		env[strings.ToUpper(k)] = v
	}
	if env[envMaxSubagentDepth] != "2" || env[envMaxConcurrentSubagents] != "4" || env[envDisableBackground] != "1" || env[HookConfigEnv] == "" {
		t.Errorf("sub-agent environment = depth %q, concurrent %q, background %q, hook %q",
			env[envMaxSubagentDepth], env[envMaxConcurrentSubagents], env[envDisableBackground], env[HookConfigEnv])
	}
	// Tokens and Python environment of Yggdrasil's own process (set at the top
	// of the test) never reach the child (SF-04, invariant 3).
	for _, name := range []string{"GITHUB_TOKEN", "GITLAB_TOKEN", "ANTHROPIC_API_KEY", "VIRTUAL_ENV", "PYTHONPATH"} {
		if env[name] != "" {
			t.Errorf("%s reached the child: %q", name, env[name])
		}
	}
	// The working directory is in long form: an 8.3 short path breaks the CLI's
	// permission check (docs/spikes.md B4). TEMP is short on some machines.
	long, err := platform.LongPath(o.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if cwd := read(t, out+".cwd"); !strings.EqualFold(filepath.Clean(cwd), filepath.Clean(long)) {
		t.Errorf("working directory = %q, want %q", cwd, long)
	}

	// The hook was told to enforce Ask mode and the repo folder.
	var hookCfg HookConfig
	if err := json.Unmarshal([]byte(read(t, out+".hookcfg")), &hookCfg); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(hookCfg.AllowedTools, askTools) || !strings.EqualFold(filepath.Clean(hookCfg.AllowedDir), filepath.Clean(long)) {
		t.Errorf("hook configuration = %+v", hookCfg)
	}
	if !slices.Contains(argv, "--disallowedTools") {
		t.Errorf("--disallowedTools missing from %q", argv)
	}

	// The settings the CLI got hold only the hook command and the event name.
	settings := read(t, out+".settings")
	if !strings.Contains(settings, `"hook"`) || strings.Contains(settings, "hook.log") || strings.Contains(settings, o.Dir) {
		t.Errorf("settings = %s", settings)
	}

	// The run folder (settings, hook configuration, log) is gone afterwards.
	if _, err := os.Stat(read(t, out+".settingspath")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the settings file outlived the run: %v", err)
	}
}

// A prompt can hold anything a person types. It must arrive whole, which a
// command line could not promise (quotes, percent signs, &, line breaks, a
// leading dash, a long text).
func TestAnyPromptArrivesWholeOnStdin(t *testing.T) {
	prompts := []string{
		"-x --dangerously-skip-permissions",
		"say \"hi\" & echo %PATH% | more > out.txt\nsecond line\r\nthird",
		"Baca README.md dan jawab (satu kalimat) — émoji 😀 dan 世界",
		strings.Repeat("a long prompt ", 2000), // far past the 8191 characters cmd.exe allows
	}
	for i, prompt := range prompts {
		a := newAdapter(t, "capture")
		out := filepath.Join(t.TempDir(), "capture")
		o := startOpts(t, "capture", map[string]string{fakeOut: out})
		o.Prompt = prompt
		res, err := agents.Execute(context.Background(), a, o, &collectSink{})
		if err != nil || res.Status != agents.RunCompleted {
			t.Fatalf("prompt %d: %+v, %v", i, res, err)
		}
		if got := read(t, out+".stdin"); got != prompt {
			t.Errorf("prompt %d arrived as %d bytes, want %d", i, len(got), len(prompt))
		}
	}
}

// On Windows `claude` is often an npm shim, claude.cmd, in a folder with a
// space (C:\Program Files\nodejs). The whole chain has to work: adapter,
// platform.Start, cmd.exe, the program behind the shim, with the prompt on
// stdin. This is the shape of the failure seen with the real CLI
// ('C:\Program' is not recognized).
func TestRunThroughACmdShimInAFolderWithSpaces(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Program Files", "nodejs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(dir, "claude.cmd")
	body := "@echo off\r\n\"" + testExe(t) + "\" %*\r\n"
	if err := os.WriteFile(shim, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	a := New(Options{Binary: shim, HookExecutable: testExe(t), TempDir: t.TempDir()})
	out := filepath.Join(t.TempDir(), "capture")
	o := startOpts(t, "capture", map[string]string{fakeOut: out})
	o.Prompt = `Read "README.md" & tell me what it says (100%)`
	res, err := agents.Execute(context.Background(), a, o, &collectSink{})
	if err != nil || res.Status != agents.RunCompleted {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if got := read(t, out+".stdin"); got != o.Prompt {
		t.Errorf("stdin = %q, want %q", got, o.Prompt)
	}
	argv := strings.Split(read(t, out+".args"), "\x00")
	if !slices.Contains(argv, "--forward-subagent-text") || !slices.Contains(argv, "-p") {
		t.Errorf("arguments through the shim = %q", argv)
	}
	// The settings path (a folder with a hyphen and digits, under the temp
	// folder) arrived as one argument.
	if i := slices.Index(argv, "--settings"); i < 0 || !strings.HasSuffix(argv[i+1], "settings.json") {
		t.Errorf("--settings through the shim = %q", argv)
	}
}

func TestRunWithoutAResultFails(t *testing.T) {
	a := newAdapter(t, "exit3")
	o := startOpts(t, "exit3", nil)
	sink := &collectSink{}
	res, err := agents.Execute(context.Background(), a, o, sink)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != agents.RunFailed || res.Err == nil {
		t.Fatalf("result = %+v", res)
	}
	var ue *UserError
	if !errors.As(res.Err, &ue) || !strings.Contains(ue.Message, "exit code 3") {
		t.Errorf("error = %v", res.Err)
	}
	// The event shows a plain message; the stderr (redacted) stays internal.
	var shown []string
	for _, e := range sink.all() {
		if e.Kind == agents.EventError {
			shown = append(shown, e.Error)
		}
	}
	if len(shown) != 1 || strings.Contains(shown[0], "boom") {
		t.Errorf("error events = %q", shown)
	}
	if inner := errors.Unwrap(res.Err); inner == nil || strings.Contains(inner.Error(), "sk-ant-api03-SECRET") {
		t.Errorf("the internal cause leaks a token or is missing: %v", inner)
	}
}

func TestCancelEndsTheRun(t *testing.T) {
	a := newAdapter(t, "sleep")
	o := startOpts(t, "sleep", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &collectSink{}
	sink.onEvent = func(e agents.Event) {
		if e.Kind == agents.EventStatus && e.Status == agents.RunStarted {
			go func() { time.Sleep(500 * time.Millisecond); cancel() }()
		}
	}
	type outcome struct {
		res agents.Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := agents.Execute(ctx, a, o, sink)
		done <- outcome{res, err}
	}()
	select {
	case oc := <-done:
		if oc.err != nil || oc.res.Status != agents.RunCancelled {
			t.Fatalf("result = %+v, %v", oc.res, oc.err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the run did not end after cancel: the process tree is still running")
	}
	events := sink.all()
	if last := events[len(events)-1]; last.Kind != agents.EventStatus || last.Status != agents.RunCancelled {
		t.Errorf("last event = %+v, want a cancelled status", last)
	}
}

func TestReadLinesHandlesLongAndLastLines(t *testing.T) {
	var got []int
	readLines(io.MultiReader(strings.NewReader("ab\ncdef\n"), strings.NewReader("last")), func(l []byte) bool {
		got = append(got, len(l))
		return true
	})
	if !slices.Equal(got, []int{3, 5, 4}) { // lines keep their line ending
		t.Errorf("line lengths = %v", got)
	}
	var n int
	readLines(strings.NewReader("a\nb\nc\n"), func([]byte) bool { n++; return n < 2 })
	if n != 2 {
		t.Errorf("readLines kept going after fn returned false: %d calls", n)
	}
	huge := strings.Repeat("x", maxLineBytes+10) + "\nok\n"
	var lines []string
	readLines(strings.NewReader(huge), func(l []byte) bool {
		lines = append(lines, string(l))
		return true
	})
	if len(lines) != 2 || lines[0] != "{" || lines[1] != "ok\n" {
		t.Errorf("huge line handling: %d lines, first %q", len(lines), lines[0])
	}
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func read(t *testing.T, p string) string {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
