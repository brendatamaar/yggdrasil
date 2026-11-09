package worktreeenv_test

import (
	"context"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	env "github.com/brendatamaar/yggdrasil/internal/core/worktreeenv"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/platform/platformtest"
	"github.com/brendatamaar/yggdrasil/internal/safety"
	"github.com/brendatamaar/yggdrasil/internal/worktreeenv"
	"github.com/brendatamaar/yggdrasil/internal/worktreeenv/worktreeenvtest"
)

// ---- runner contract ------------------------------------------------------

// scriptedFake is the fake runner driven by the contract's Outcome.
func scriptedFake() (*worktreeenvtest.ComposeRunner, func(worktreeenvtest.Outcome)) {
	r := &worktreeenvtest.ComposeRunner{}
	script := func(o worktreeenvtest.Outcome) {
		r.Gate = nil
		if o.Block {
			r.Gate = make(chan struct{}) // never opened: ends only with the context
		}
		r.Result = func(c worktreeenv.ComposeCall) worktreeenv.ComposeResult {
			switch {
			case o.Exit == 0:
				if c.Op == env.OpPs {
					return worktreeenv.ComposeResult{Output: []byte(o.Stdout)}
				}
				return worktreeenv.ComposeResult{}
			case strings.Contains(o.Stderr, "Cannot connect to the Docker daemon"):
				return worktreeenv.ComposeResult{Failure: env.FailDaemonDown, Summary: worktreeenv.MsgDaemonDown}
			}
			return worktreeenv.ComposeResult{Failure: env.FailComposeError, Summary: "Docker compose failed."}
		}
	}
	return r, script
}

func TestFakeComposeRunnerContract(t *testing.T) {
	worktreeenvtest.ComposeRunnerContract(t, func(t *testing.T) worktreeenvtest.ComposeRunnerEnv {
		missing := &worktreeenvtest.ComposeRunner{Result: func(worktreeenv.ComposeCall) worktreeenv.ComposeResult {
			return worktreeenv.ComposeResult{Failure: env.FailDockerMissing, Summary: worktreeenv.MsgDockerMissing}
		}}
		r, script := scriptedFake()
		return worktreeenvtest.ComposeRunnerEnv{Runner: r, NoDocker: missing, Dir: t.TempDir(), Script: script}
	})
}

// emptyPath is an env builder whose PATH holds nothing, so docker is missing.
type emptyPath struct{ path string }

func (e emptyPath) Build(o safety.EnvOptions) ([]string, error) {
	out := []string{"PATH=" + e.path}
	for k, v := range o.Extra {
		out = append(out, k+"="+v)
	}
	return out, nil
}

// TestScriptedPlatformComposeRunnerContract runs the real runner over a
// scripted platform.StartFunc, so every clause runs without Docker.
func TestScriptedPlatformComposeRunnerContract(t *testing.T) {
	worktreeenvtest.ComposeRunnerContract(t, func(t *testing.T) worktreeenvtest.ComposeRunnerEnv {
		dir := t.TempDir()
		f := &platformtest.Fake{}
		return worktreeenvtest.ComposeRunnerEnv{
			Dir:      dir,
			NoDocker: worktreeenv.PlatformComposeRunner{Env: emptyPath{path: dir}},
			Runner:   fakeRunner(f),
			Script: func(o worktreeenvtest.Outcome) {
				f.Push(platformtest.Script{Exit: o.Exit, Stdout: o.Stdout, Stderr: o.Stderr, Block: o.Block})
			},
		}
	})
}

// TestPlatformComposeRunnerContract runs the unscripted clauses against the
// docker on this machine, when there is one.
func TestPlatformComposeRunnerContract(t *testing.T) {
	worktreeenvtest.ComposeRunnerContract(t, func(t *testing.T) worktreeenvtest.ComposeRunnerEnv {
		dir := t.TempDir()
		e := worktreeenvtest.ComposeRunnerEnv{
			Dir:      dir,
			NoDocker: worktreeenv.PlatformComposeRunner{Env: emptyPath{path: dir}},
		}
		if _, err := platform.Resolve("docker", dir, safetyEnv(t)); err == nil {
			e.Runner = worktreeenv.PlatformComposeRunner{Env: safety.NewEnvBuilder(), Timeout: 30 * time.Second}
		}
		return e
	})
}

func safetyEnv(t *testing.T) []string {
	t.Helper()
	e, err := safety.NewEnvBuilder().Build(safety.EnvOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// ---- runner with a fake process -------------------------------------------

func fakeRunner(f *platformtest.Fake) worktreeenv.PlatformComposeRunner {
	return worktreeenv.PlatformComposeRunner{
		Env: safety.NewEnvBuilder(), Start: f.Start,
		Resolve: func(name, _ string, _ []string) (string, error) { return "/usr/bin/" + name, nil },
	}
}

func TestComposeRunnerArgvEnvAndStdin(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "glpat-secret")
	t.Setenv("VIRTUAL_ENV", "/x")
	body := "services: {db: {image: postgres}}\n"
	for op, want := range map[env.ComposeOp]string{
		env.OpUp:   "compose -p ygg-a --file - --project-directory deploy --env-file deploy/.env up -d",
		env.OpStop: "compose -p ygg-a --file - --project-directory deploy --env-file deploy/.env stop",
		env.OpDown: "compose -p ygg-a --file - --project-directory deploy --env-file deploy/.env down",
		env.OpPs:   "compose -p ygg-a --file - --project-directory deploy --env-file deploy/.env ps --format json",
	} {
		f := &platformtest.Fake{}
		f.Push(platformtest.Script{Stdout: `{"State":"running"}`})
		res := fakeRunner(f).Run(context.Background(), worktreeenv.ComposeCall{
			Op: op, Project: "ygg-a", File: "deploy/c.yml", Compose: []byte(body), HasEnvFile: true, Dir: "/wt",
			Env: map[string]string{"YGG_PORT_BASE": "20000", "YGG_PORT_3": "20003"},
		})
		if res.Failure != "" {
			t.Fatalf("%s: %+v", op, res)
		}
		spec := f.Started()[0]
		if got := strings.Join(spec.Argv[1:], " "); got != want || spec.Argv[0] != "/usr/bin/docker" {
			t.Errorf("%s: argv %v", op, spec.Argv)
		}
		if spec.Stdin == nil {
			t.Fatalf("%s: the hashed compose file is not on stdin", op)
		}
		if b, _ := io.ReadAll(spec.Stdin); string(b) != body {
			t.Errorf("%s: stdin %q", op, b)
		}
		joined := strings.Join(spec.Env, "\n")
		for _, bad := range []string{"GITLAB_TOKEN", "glpat-secret", "VIRTUAL_ENV"} {
			if strings.Contains(joined, bad) {
				t.Errorf("%s leaked into the child environment", bad)
			}
		}
		if !slices.Contains(spec.Env, "YGG_PORT_BASE=20000") || !slices.Contains(spec.Env, "YGG_PORT_3=20003") {
			t.Errorf("port variables missing: %v", spec.Env)
		}
		if spec.Dir != "/wt" {
			t.Errorf("dir %q", spec.Dir)
		}
		if (op == env.OpPs) != (len(res.Output) > 0) {
			t.Errorf("%s: output kept only for ps: %q", op, res.Output)
		}
	}
	// no .env: no --env-file; an empty compose file runs nothing
	f := &platformtest.Fake{}
	f.Push(platformtest.Script{})
	r := fakeRunner(f)
	r.Run(context.Background(), worktreeenv.ComposeCall{Op: env.OpUp, Project: "ygg-a", File: "c.yml", Compose: []byte("x"), Dir: "/wt"})
	if got := strings.Join(f.Started()[0].Argv[1:], " "); got != "compose -p ygg-a --file - up -d" {
		t.Errorf("argv %q", got)
	}
	if res := r.Run(context.Background(), worktreeenv.ComposeCall{Op: env.OpUp, Project: "ygg-a", File: "c.yml", Dir: "/wt"}); res.Failure == "" || len(f.Started()) != 1 {
		t.Errorf("an empty compose file ran: %+v", res)
	}
}

func TestComposeRunnerFailures(t *testing.T) {
	call := worktreeenv.ComposeCall{Op: env.OpUp, Project: "ygg-a", File: "c.yml", Compose: []byte("x"), Dir: "/wt"}
	cases := []struct {
		name   string
		script platformtest.Script
		kind   env.FailKind
		has    string
	}{
		{"daemon down", platformtest.Script{Exit: 1, Stderr: "Cannot connect to the Docker daemon at unix:///x. Is the docker daemon running?"}, env.FailDaemonDown, "not running"},
		{"windows daemon down", platformtest.Script{Exit: 1, Stderr: "error during connect: open //./pipe/docker_engine"}, env.FailDaemonDown, ""},
		{"compose error", platformtest.Script{Exit: 1, Stderr: "yaml: line 3: mapping values are not allowed\n"}, env.FailComposeError, "mapping values"},
		{"token is redacted", platformtest.Script{Exit: 1, Stderr: "pull failed: Authorization: Bearer abcdefghijklmnop1234567890"}, env.FailComposeError, ""},
	}
	for _, c := range cases {
		f := &platformtest.Fake{}
		f.Push(c.script)
		res := fakeRunner(f).Run(context.Background(), call)
		if res.Failure != c.kind || !strings.Contains(res.Summary, c.has) {
			t.Errorf("%s: %+v", c.name, res)
		}
		if strings.Contains(res.Summary, "abcdefghijklmnop1234567890") {
			t.Errorf("%s: token in summary", c.name)
		}
	}
	// docker not found by Resolve
	r := fakeRunner(&platformtest.Fake{})
	r.Resolve = func(string, string, []string) (string, error) { return "", platform.ErrNotFound }
	if res := r.Run(context.Background(), call); res.Failure != env.FailDockerMissing {
		t.Errorf("%+v", res)
	}
}

// ---- service --------------------------------------------------------------

type composeRig struct {
	svc     *worktreeenv.ComposeService
	store   *worktreeenvtest.Store
	runner  *worktreeenvtest.ComposeRunner
	files   *worktreeenvtest.ComposeFiles
	wt      core.Worktree
	wts     map[core.WorktreeID]core.Worktree
	cancel  context.CancelFunc
	changes atomic.Int32
}

func newComposeRig(t *testing.T) *composeRig { return newComposeRigWith(t, nil) }

// newComposeRigWith lets a test change the wiring before the service is built.
func newComposeRigWith(t *testing.T, mod func(*composeRig, *worktreeenv.ComposeConfig)) *composeRig {
	t.Helper()
	r := &composeRig{
		store: worktreeenvtest.NewStore("rep_1"), runner: &worktreeenvtest.ComposeRunner{}, files: &worktreeenvtest.ComposeFiles{},
		wt: core.Worktree{ID: "Wt_Amber Fox", RepoID: "rep_1", Path: "/wt/amber"},
	}
	r.wts = map[core.WorktreeID]core.Worktree{r.wt.ID: r.wt}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	var n atomic.Int32
	cfg := worktreeenv.ComposeConfig{
		Store: r.store, Audit: r.store, Runner: r.runner, Files: r.files,
		Worktree: func(_ context.Context, id core.WorktreeID) (core.Worktree, error) {
			w, ok := r.wts[id]
			if !ok {
				return core.Worktree{}, worktreeenv.ErrNotFound
			}
			return w, nil
		},
		Block: func(context.Context, core.WorktreeID) (core.PortBlock, bool, error) {
			return core.PortBlock{Base: 20040, Size: 20}, true, nil
		},
		Now:      func() time.Time { return time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC) },
		NewID:    func(p string) string { return p + "_" + strconv.Itoa(int(n.Add(1))) },
		OnChange: func(core.WorktreeID) { r.changes.Add(1) },
	}
	if mod != nil {
		mod(r, &cfg)
	}
	svc, err := worktreeenv.NewComposeService(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	r.svc = svc
	t.Cleanup(func() { cancel(); svc.Wait() })
	r.files.Put(r.wt.Path, "compose.yml", "services: {db: {ports: ['${YGG_PORT_0}:5432']}}")
	return r
}

func (r *composeRig) enable(t *testing.T) {
	t.Helper()
	if err := r.svc.Configure(context.Background(), "rep_1", true, "compose.yml"); err != nil {
		t.Fatal(err)
	}
}

func (r *composeRig) waitFor(t *testing.T, pred func(env.Service) bool) worktreeenv.ComposeStatus {
	t.Helper()
	for range 400 {
		st, err := r.svc.Status(context.Background(), r.wt.ID)
		if err != nil {
			t.Fatal(err)
		}
		if pred(st.State) {
			return st
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("state never reached")
	return worktreeenv.ComposeStatus{}
}

// ops are the calls other than the startup `ps`.
func (r *composeRig) ops() []worktreeenv.ComposeCall {
	var out []worktreeenv.ComposeCall
	for _, c := range r.runner.Calls() {
		if c.Op != env.OpPs {
			out = append(out, c)
		}
	}
	return out
}

func (r *composeRig) actions() []string {
	var out []string
	for _, a := range r.store.Audits() {
		out = append(out, a.Action)
	}
	return out
}

func TestComposeOffByDefault(t *testing.T) {
	r := newComposeRig(t)
	st, err := r.svc.Status(context.Background(), r.wt.ID)
	if err != nil || st.State != (env.Off{}) {
		t.Fatalf("%+v %v", st, err)
	}
	if _, err := r.svc.Up(context.Background(), r.wt.ID); !errors.Is(err, worktreeenv.ErrComposeOff) {
		t.Fatalf("got %v", err)
	}
	if len(r.ops()) != 0 {
		t.Fatal("Off ran docker")
	}
}

func TestComposeConfirmUpStopDownAudited(t *testing.T) {
	ctx := context.Background()
	r := newComposeRig(t)
	r.enable(t)
	st, _ := r.svc.Status(ctx, r.wt.ID)
	if _, ok := st.State.(env.NeedsConfirm); !ok || st.Hash == "" || st.Note != "" {
		t.Fatalf("%+v", st)
	}
	// up before confirm: refused, nothing runs
	if _, err := r.svc.Up(ctx, r.wt.ID); !errors.Is(err, worktreeenv.ErrComposeConfirm) {
		t.Fatalf("got %v", err)
	}
	if len(r.ops()) != 0 {
		t.Fatal("ran before confirmation")
	}
	// a stale hash does not confirm
	if _, err := r.svc.Confirm(ctx, r.wt.ID, "deadbeef"); !errors.Is(err, worktreeenv.ErrComposeStale) {
		t.Fatalf("got %v", err)
	}
	if _, err := r.svc.Confirm(ctx, r.wt.ID, st.Hash); err != nil {
		t.Fatal(err)
	}
	set, _ := r.svc.Settings(ctx, "rep_1")
	if set.ConfirmedHash != st.Hash {
		t.Fatal("confirmed hash not stored")
	}
	if _, err := r.svc.Up(ctx, r.wt.ID); err != nil {
		t.Fatal(err)
	}
	r.waitFor(t, func(s env.Service) bool { _, ok := s.(env.Up); return ok })
	if _, err := r.svc.Stop(ctx, r.wt.ID); err != nil {
		t.Fatal(err)
	}
	r.waitFor(t, func(s env.Service) bool { _, ok := s.(env.Down); return ok })
	if _, err := r.svc.Down(ctx, r.wt.ID); err != nil {
		t.Fatal(err)
	}
	r.waitFor(t, func(s env.Service) bool { _, ok := s.(env.Down); return ok && len(r.ops()) == 3 })

	var ops []env.ComposeOp
	for _, c := range r.ops() {
		ops = append(ops, c.Op)
		if !strings.HasPrefix(string(c.Project), "ygg-") || len(c.Project) > 44 || c.File != "compose.yml" || c.Dir != r.wt.Path {
			t.Errorf("call %+v", c)
		}
		if c.Env["YGG_PORT_BASE"] != "20040" || c.Env["YGG_PORT_19"] != "20059" || len(c.Env) != 21 {
			t.Errorf("env %v", c.Env)
		}
	}
	if !slices.Equal(ops, []env.ComposeOp{env.OpUp, env.OpStop, env.OpDown}) {
		t.Fatalf("ops %v", ops)
	}
	got := r.actions()
	for _, want := range []string{"worktreeenv.compose_settings", env.ActionComposeEnabled, env.ActionComposeConfirmed, env.ActionComposeUp,
		env.ActionComposeStarted, env.ActionComposeStop, env.ActionComposeStopped, env.ActionComposeDown} {
		if !slices.Contains(got, want) {
			t.Errorf("audit lacks %s: %v", want, got)
		}
	}
	if r.changes.Load() == 0 {
		t.Error("nothing published")
	}
}

func TestComposeChangedFileNeedsReconfirm(t *testing.T) {
	ctx := context.Background()
	r := newComposeRig(t)
	r.enable(t)
	st, _ := r.svc.Status(ctx, r.wt.ID)
	_, _ = r.svc.Confirm(ctx, r.wt.ID, st.Hash)
	r.waitFor(t, func(s env.Service) bool { _, ok := s.(env.Down); return ok })
	// an agent edits the file
	r.files.Put(r.wt.Path, "compose.yml", "services: {db: {volumes: ['/:/host']}}")
	if _, err := r.svc.Up(ctx, r.wt.ID); !errors.Is(err, worktreeenv.ErrComposeConfirm) {
		t.Fatalf("got %v", err)
	}
	if len(r.ops()) != 0 {
		t.Fatal("ran a changed file")
	}
	st, _ = r.svc.Status(ctx, r.wt.ID)
	if _, ok := st.State.(env.NeedsConfirm); !ok {
		t.Fatalf("%+v", st.State)
	}
}

func TestComposeUnreadableOrEscapingFile(t *testing.T) {
	ctx := context.Background()
	r := newComposeRig(t)
	if err := r.svc.Configure(ctx, "rep_1", true, "../x.yml"); err == nil {
		t.Fatal("a file outside the repo was accepted")
	}
	if err := r.svc.Configure(ctx, "rep_1", true, "compose.txt"); err == nil {
		t.Fatal("a non-yaml file was accepted")
	}
	if err := r.svc.Configure(ctx, "rep_1", true, "missing.yml"); err != nil {
		t.Fatal(err)
	}
	st, err := r.svc.Status(ctx, r.wt.ID)
	if err != nil || st.State != (env.Off{}) || st.Note == "" {
		t.Fatalf("%+v %v", st, err)
	}
	if _, err := r.svc.Up(ctx, r.wt.ID); err == nil || len(r.ops()) != 0 {
		t.Fatalf("up on an unreadable file: %v", err)
	}
}

func TestComposeFailureAndDockerMissing(t *testing.T) {
	ctx := context.Background()
	r := newComposeRig(t)
	r.runner.Result = func(c worktreeenv.ComposeCall) worktreeenv.ComposeResult {
		return worktreeenv.ComposeResult{Failure: env.FailDockerMissing, Summary: worktreeenv.MsgDockerMissing}
	}
	r.enable(t)
	st, _ := r.svc.Status(ctx, r.wt.ID)
	_, _ = r.svc.Confirm(ctx, r.wt.ID, st.Hash)
	// startup recovery found docker missing at the first look; the state is Failed or NeedsConfirm
	_, _ = r.svc.Up(ctx, r.wt.ID)
	st = r.waitFor(t, func(s env.Service) bool { _, ok := s.(env.ComposeFailed); return ok })
	if st.Note != worktreeenv.MsgDockerMissing {
		t.Fatalf("note %q", st.Note)
	}
	if !slices.Contains(r.actions(), env.ActionComposeFail) {
		t.Fatal("failure not audited")
	}
}

func TestComposeOneOperationAtATime(t *testing.T) {
	ctx := context.Background()
	r := newComposeRig(t)
	r.runner.Gate = make(chan struct{})
	r.enable(t)
	st, _ := r.svc.Status(ctx, r.wt.ID)
	_, _ = r.svc.Confirm(ctx, r.wt.ID, st.Hash)
	if _, err := r.svc.Up(ctx, r.wt.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Down(ctx, r.wt.ID); !errors.Is(err, worktreeenv.ErrComposeBusy) {
		t.Fatalf("got %v", err)
	}
	r.runner.Gate <- struct{}{}
	r.waitFor(t, func(s env.Service) bool { _, ok := s.(env.Up); return ok })
	if n := len(r.ops()); n != 1 {
		t.Fatalf("calls %d", n)
	}
}

func TestComposeRecoversRunningProject(t *testing.T) {
	ctx := context.Background()
	r := newComposeRig(t)
	r.runner.Result = func(c worktreeenv.ComposeCall) worktreeenv.ComposeResult {
		return worktreeenv.ComposeResult{Output: env.FormatPs(env.PsRunning)}
	}
	r.enable(t)
	st, _ := r.svc.Status(ctx, r.wt.ID)
	if _, ok := st.State.(env.Up); !ok {
		t.Fatalf("a running project must show as up after a restart: %+v", st.State)
	}
	for _, c := range r.runner.Calls() {
		if c.Op != env.OpPs {
			t.Fatalf("startup ran %s", c.Op)
		}
	}
}

func TestComposeShutdownRunsNothing(t *testing.T) {
	ctx := context.Background()
	r := newComposeRig(t)
	r.enable(t)
	st, _ := r.svc.Status(ctx, r.wt.ID)
	_, _ = r.svc.Confirm(ctx, r.wt.ID, st.Hash)
	_, _ = r.svc.Up(ctx, r.wt.ID)
	r.waitFor(t, func(s env.Service) bool { _, ok := s.(env.Up); return ok })
	before := len(r.ops())
	r.cancel()
	r.svc.Wait()
	if len(r.ops()) != before {
		t.Fatal("something ran at shutdown")
	}
}

func TestComposeUnknownWorktreeAndNoDeletion(t *testing.T) {
	r := newComposeRig(t)
	if _, err := r.svc.Status(context.Background(), "wt_other"); !errors.Is(err, worktreeenv.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}
