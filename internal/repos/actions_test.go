package repos_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/repos"
	"github.com/brendatamaar/yggdrasil/internal/repos/repostest"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// Contract: GitRunner against the fake and against real git.

func TestGitRunnerContractFake(t *testing.T) {
	repostest.GitRunnerContract(t, func(t *testing.T) (repos.GitRunner, repostest.GitSetup) {
		f := repostest.NewFakeGit()
		return f, f
	})
}

// actFx builds temp git repos with a local bare remote, through platform.Start.
type actFx struct {
	t   *testing.T
	g   *git.Git
	env []string
	exe string
}

func newActFx(t *testing.T) *actFx {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(cfg, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	env, err := safety.NewEnvBuilder().Build(safety.EnvOptions{Extra: map[string]string{
		"GIT_CONFIG_GLOBAL": cfg, "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "Ada Test", "GIT_AUTHOR_EMAIL": "ada@example.test",
		"GIT_COMMITTER_NAME": "Ada Test", "GIT_COMMITTER_EMAIL": "ada@example.test",
	}})
	if err != nil {
		t.Fatal(err)
	}
	g, err := git.New(env)
	if errors.Is(err, git.ErrNotInstalled) {
		t.Skip("git is not installed")
	}
	if err != nil {
		t.Fatal(err)
	}
	exe, err := platform.Resolve("git", "", env)
	if err != nil {
		t.Fatal(err)
	}
	return &actFx{t: t, g: g, env: env, exe: exe}
}

func (f *actFx) run(dir string, args ...string) {
	f.t.Helper()
	var out, errb bytes.Buffer
	argv := append([]string{f.exe, "-c", "core.longpaths=true"}, args...)
	p, err := platform.Start(context.Background(), platform.Spec{Argv: argv, Dir: dir, Env: f.env, Stdout: &out, Stderr: &errb})
	if err != nil {
		f.t.Fatal(err)
	}
	if code, err := p.Wait(); err != nil || code != 0 {
		f.t.Fatalf("git %v: exit %d, err %v: %s", args, code, err, errb.String())
	}
}

func (f *actFx) write(dir, name, content string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *actFx) commit(dir, name, content, msg string) {
	f.t.Helper()
	f.write(dir, name, content)
	f.run(dir, "add", "-A")
	f.run(dir, "commit", "-q", "-m", msg)
}

// pair returns a work repo a and a clone b of one bare remote, in sync.
func (f *actFx) pair() (a, b string) {
	f.t.Helper()
	base := f.t.TempDir()
	bare := filepath.Join(base, "origin.git")
	f.run(base, "init", "-q", "--bare", "-b", "main", bare)
	a = filepath.Join(base, "a")
	if err := os.MkdirAll(a, 0o750); err != nil {
		f.t.Fatal(err)
	}
	f.run(a, "init", "-q", "-b", "main")
	f.commit(a, "README.md", "hello\n", "init")
	f.run(a, "remote", "add", "origin", bare)
	f.run(a, "push", "-q", "-u", "origin", "main")
	b = filepath.Join(base, "b")
	f.run(base, "clone", "-q", bare, b)
	return a, b
}

// Prepare implements repostest.GitSetup with real repos.
func (f *actFx) Prepare(t *testing.T, s repostest.GitScenario) (string, string) {
	t.Helper()
	a, b := f.pair()
	branch := ""
	switch s {
	case repostest.GitPullAdvance:
		f.commit(a, "new.txt", "n\n", "next")
		f.run(a, "push", "-q")
	case repostest.GitPullDiverged:
		f.commit(a, "remote.txt", "r\n", "remote work")
		f.run(a, "push", "-q")
		f.commit(b, "local.txt", "l\n", "local work")
		f.run(b, "fetch", "-q")
	case repostest.GitPullDirty:
		f.commit(a, "README.md", "remote change\n", "remote edit")
		f.run(a, "push", "-q")
		f.write(b, "README.md", "local change\n")
	case repostest.GitPullDetached:
		f.run(b, "checkout", "-q", "--detach")
	case repostest.GitCheckoutClean:
		f.run(b, "branch", "feature")
		branch = "feature"
	case repostest.GitCheckoutRemoteOnly:
		f.run(a, "branch", "feature")
		f.run(a, "push", "-q", "origin", "feature")
		f.run(b, "fetch", "-q")
		branch = "feature"
	case repostest.GitCheckoutDirty:
		f.run(b, "branch", "feature")
		f.write(b, "README.md", "dirty\n")
		branch = "feature"
	case repostest.GitBranches:
		f.run(a, "branch", "remote-only")
		f.run(a, "push", "-q", "origin", "remote-only")
		f.run(b, "branch", "local-only")
		f.run(b, "fetch", "-q")
	}
	return b, branch
}

func TestGitRunnerContractRealGit(t *testing.T) {
	repostest.GitRunnerContract(t, func(t *testing.T) (repos.GitRunner, repostest.GitSetup) {
		f := newActFx(t)
		return f.g, f
	})
}

// Service tests: a scripted runner, a real SQLite store.

type scriptGit struct {
	mu        sync.Mutex
	calls     []string
	active    map[string]int
	maxActive int
	totalRun  int
	gate      chan struct{} // when set, fetch, pull and checkout wait on it
	started   chan string
	dirty     map[string]bool
	fail      map[string]error
}

func newScriptGit() *scriptGit {
	return &scriptGit{active: map[string]int{}, started: make(chan string, 1000), dirty: map[string]bool{}, fail: map[string]error{}}
}

func (g *scriptGit) enter(ctx context.Context, op, dir string) error {
	g.mu.Lock()
	g.calls = append(g.calls, op+":"+filepath.Base(dir))
	g.active[dir]++
	if g.active[dir] > 1 {
		g.mu.Unlock()
		panic("two git commands at once in " + dir)
	}
	g.totalRun++
	if g.totalRun > g.maxActive {
		g.maxActive = g.totalRun
	}
	gate := g.gate
	g.mu.Unlock()
	g.started <- filepath.Base(dir)
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
		}
	}
	return g.fail[filepath.Base(dir)]
}

func (g *scriptGit) leave(dir string) {
	g.mu.Lock()
	g.active[dir]--
	g.totalRun--
	g.mu.Unlock()
}

func (g *scriptGit) Fetch(ctx context.Context, dir, _ string) error {
	defer g.leave(dir)
	return g.enter(ctx, "fetch", dir)
}

func (g *scriptGit) PullFFOnly(ctx context.Context, dir string) error {
	defer g.leave(dir)
	return g.enter(ctx, "pull", dir)
}

func (g *scriptGit) Checkout(ctx context.Context, dir, _ string) error {
	defer g.leave(dir)
	return g.enter(ctx, "checkout", dir)
}

func (g *scriptGit) Status(_ context.Context, dir string) (git.Status, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := git.Status{Branch: "main", OID: "aaaa", HasUpstream: true}
	if g.dirty[filepath.Base(dir)] {
		st.Unstaged = 1
	}
	return st, nil
}

func (g *scriptGit) BranchExists(_ context.Context, _, name string) (bool, bool, error) {
	return name != "missing", false, nil
}

func (g *scriptGit) called() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	c := append([]string(nil), g.calls...)
	sort.Strings(c)
	return c
}

type actRefresh struct {
	mu  sync.Mutex
	ids []core.RepoID
	rs  []corerepos.RefreshReason
}

func (r *actRefresh) ReloadRepos(context.Context) error { return nil }
func (r *actRefresh) Refresh(_ context.Context, ids []core.RepoID, reason corerepos.RefreshReason) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ids = append(r.ids, ids...)
	r.rs = append(r.rs, reason)
	return nil
}

type actPub struct {
	mu sync.Mutex
	n  int
}

func (p *actPub) PublishGitBatch(corerepos.BatchID, []core.RepoID, bool) {
	p.mu.Lock()
	p.n++
	p.mu.Unlock()
}

type fakeBuilds struct {
	mu    sync.Mutex
	busy  map[core.RepoID]string
	subs  []func(builds.BuildStarted)
	asked int
}

func (b *fakeBuilds) BusyMainCheckouts(_ context.Context, ids []core.RepoID) (map[core.RepoID]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.asked++
	out := map[core.RepoID]string{}
	for _, id := range ids {
		if r, ok := b.busy[id]; ok {
			out[id] = r
		}
	}
	return out, nil
}

func (b *fakeBuilds) SubscribeStarted(fn func(builds.BuildStarted)) { b.subs = append(b.subs, fn) }

type actEnv struct {
	t     *testing.T
	svc   *repos.ActionsService
	git   *scriptGit
	store *db.Store
	rec   *actRefresh
	pub   *actPub
	bld   *fakeBuilds
	stop  context.CancelFunc
	done  chan struct{} // closed when Run returned
	ids   []core.RepoID
}

type actOpts struct {
	repos int
	max   int
	ids   func(prefix string) string
	// wrap, if set, puts a runner around the script runner.
	wrap func(*scriptGit) repos.GitRunner
}

func newActEnv(t *testing.T, o actOpts) *actEnv {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	red := safety.NewRedactor()
	store := db.NewStore(d, red.Redact)
	tr, err := repos.NewTrackingService(repos.Deps{Store: store, Redactor: red})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	var picks []corerepos.Pick
	for i := 0; i < o.repos; i++ {
		name := fmt.Sprintf("repo%d", i)
		if err := os.MkdirAll(filepath.Join(root, name, ".git"), 0o750); err != nil {
			t.Fatal(err)
		}
		picks = append(picks, corerepos.Pick{RelPath: corerepos.RelPath(name)})
	}
	out, err := tr.Track(ctx, root, "acme-shop", picks, "user")
	if err != nil {
		t.Fatal(err)
	}
	tracked, ok := out.(corerepos.Tracked)
	if !ok {
		t.Fatalf("track = %#v", out)
	}
	list, err := store.ListRepos(ctx, tracked.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Path < list[j].Path })
	e := &actEnv{t: t, git: newScriptGit(), store: store, rec: &actRefresh{}, pub: &actPub{}, bld: &fakeBuilds{busy: map[core.RepoID]string{}}}
	for _, r := range list {
		e.ids = append(e.ids, r.ID)
	}
	var runner repos.GitRunner = e.git
	if o.wrap != nil {
		runner = o.wrap(e.git)
	}
	e.svc, err = repos.NewActionsService(repos.ActionsDeps{
		Store: store, Redactor: red, Git: runner, Builds: e.bld, Status: e.rec, Publisher: e.pub,
		Max: o.max, IDs: o.ids,
	})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	e.stop = cancel
	done := make(chan struct{})
	e.done = done
	go func() { _ = e.svc.Run(runCtx); close(done) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	// Run sets its context on entry: wait until Start is accepted.
	return e
}

func (e *actEnv) start(kind, branch string, ids ...core.RepoID) (corerepos.BatchID, error) {
	e.t.Helper()
	strs := make([]string, len(ids))
	for i, id := range ids {
		strs[i] = string(id)
	}
	req, err := repos.ParseBatchRequest(kind, strs, branch)
	if err != nil {
		e.t.Fatal(err)
	}
	var id corerepos.BatchID
	for i := 0; i < 200; i++ { // Run may not have set its context yet
		id, err = e.svc.Start(context.Background(), req, "user")
		if c, _ := apperr.ClassOf(err); err == nil || c != apperr.Unavailable || !strings.Contains(err.Error(), "not running") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return id, err
}

func (e *actEnv) wait(id corerepos.BatchID) repos.BatchView {
	e.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if v, ok := e.svc.Get(id); ok && v.Finished {
			// per-repo audit entries and the refresh follow the state change
			time.Sleep(50 * time.Millisecond)
			return v
		}
		time.Sleep(5 * time.Millisecond)
	}
	e.t.Fatalf("batch %s did not finish", id)
	return repos.BatchView{}
}

func (e *actEnv) audits(subject string) []string {
	e.t.Helper()
	list, err := e.store.ListAudit(context.Background(), subject, 100)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, a := range list {
		out = append(out, a.Action)
	}
	sort.Strings(out)
	return out
}

func states(v repos.BatchView) []string {
	var s []string
	for _, it := range v.Items {
		s = append(s, it.State+"/"+it.Outcome+it.Reason)
	}
	return s
}

func TestServiceFetchBatchAuditsAndRefreshes(t *testing.T) {
	e := newActEnv(t, actOpts{repos: 3})
	id, err := e.start("fetch", "", e.ids...)
	if err != nil {
		t.Fatal(err)
	}
	v := e.wait(id)
	for _, it := range v.Items {
		if it.State != "done" || it.Outcome != "fetched" {
			t.Fatalf("items = %v", states(v))
		}
	}
	if got := e.audits("batch:" + string(id)); len(got) != 1 || got[0] != "git.fetch.start" {
		t.Errorf("batch audit = %v", got)
	}
	for _, r := range e.ids {
		if got := e.audits("repo:" + string(r)); !contains(got, "git.fetch") {
			t.Errorf("repo %s audit = %v", r, got)
		}
	}
	e.rec.mu.Lock()
	refreshed, reasons := append([]core.RepoID(nil), e.rec.ids...), append([]corerepos.RefreshReason(nil), e.rec.rs...)
	e.rec.mu.Unlock()
	if len(refreshed) != 3 || reasons[0] != corerepos.ReasonGitAction {
		t.Errorf("refresh = %v %v", refreshed, reasons)
	}
	e.pub.mu.Lock()
	published := e.pub.n
	e.pub.mu.Unlock()
	if published == 0 {
		t.Error("nothing was published")
	}
	if len(e.git.called()) != 3 {
		t.Errorf("git calls = %v", e.git.called())
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func TestServiceBoundsConcurrencyAndSerialisesARepo(t *testing.T) {
	e := newActEnv(t, actOpts{repos: 6, max: 4})
	e.git.gate = make(chan struct{})
	id, err := e.start("fetch", "", e.ids...)
	if err != nil {
		t.Fatal(err)
	}
	// a second batch on the same repos must wait for the first (the script
	// panics when two commands run in one repo)
	id2, err := e.start("fetch", "", e.ids[0], e.ids[1])
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		select {
		case <-e.git.started:
		case <-time.After(10 * time.Second):
			t.Fatal("four commands did not start")
		}
	}
	select {
	case n := <-e.git.started:
		t.Fatalf("a fifth command started (%s) while 4 run", n)
	case <-time.After(200 * time.Millisecond):
	}
	close(e.git.gate)
	e.wait(id)
	e.wait(id2)
	if e.git.maxActive > 4 {
		t.Errorf("max active = %d", e.git.maxActive)
	}
	if n := len(e.git.called()); n != 8 {
		t.Errorf("git calls = %d, want 8", n)
	}
}

func TestServiceBuildRunningSkipsPullButNotFetch(t *testing.T) {
	e := newActEnv(t, actOpts{repos: 2})
	e.bld.busy[e.ids[0]] = "a build is running in this repo"
	id, err := e.start("pull", "", e.ids...)
	if err != nil {
		t.Fatal(err)
	}
	v := e.wait(id)
	if v.Items[0].State != "skipped" || v.Items[0].Reason != "build_running" || v.Items[1].State != "done" {
		t.Fatalf("items = %v", states(v))
	}
	if got := e.git.called(); len(got) != 1 || got[0] != "pull:repo1" {
		t.Errorf("git calls = %v", got)
	}
	e.git.mu.Lock()
	e.git.calls = nil
	e.git.mu.Unlock()
	id, err = e.start("fetch", "", e.ids...)
	if err != nil {
		t.Fatal(err)
	}
	v = e.wait(id)
	if v.Items[0].State != "done" || len(e.git.called()) != 2 {
		t.Fatalf("fetch while a build runs: %v, calls %v", states(v), e.git.called())
	}
}

func TestServiceBuildStartedSkipsPendingItem(t *testing.T) {
	e := newActEnv(t, actOpts{repos: 2, max: 1})
	e.git.gate = make(chan struct{})
	id, err := e.start("pull", "", e.ids...)
	if err != nil {
		t.Fatal(err)
	}
	<-e.git.started // the first runs, the second waits for the slot
	e.bld.mu.Lock()
	e.bld.busy[e.ids[1]] = "a build is running in this repo"
	subs := e.bld.subs
	e.bld.mu.Unlock()
	if len(subs) != 1 {
		t.Fatalf("subscriptions = %d", len(subs))
	}
	subs[0](builds.BuildStarted{ID: "bld_1", Repos: []core.RepoID{e.ids[1]}})
	close(e.git.gate)
	v := e.wait(id)
	if v.Items[1].State != "skipped" || v.Items[1].Reason != "build_running" {
		t.Fatalf("items = %v", states(v))
	}
	if got := e.git.called(); len(got) != 1 {
		t.Errorf("git calls = %v", got)
	}
}

func TestServiceSwitchSkipsDirtyAndMissingBranch(t *testing.T) {
	e := newActEnv(t, actOpts{repos: 3})
	e.git.dirty["repo1"] = true
	id, err := e.start("switch", "feature", e.ids...)
	if err != nil {
		t.Fatal(err)
	}
	v := e.wait(id)
	if v.Items[0].Outcome != "checked_out" || v.Items[1].Reason != "dirty" || v.Items[2].Outcome != "checked_out" {
		t.Fatalf("items = %v", states(v))
	}
	if got := e.git.called(); len(got) != 2 || strings.Contains(strings.Join(got, ","), "repo1") {
		t.Errorf("git calls = %v", got)
	}
	id, err = e.start("switch", "missing", e.ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if v := e.wait(id); v.Items[0].Reason != "no_such_branch" {
		t.Fatalf("items = %v", states(v))
	}
}

func TestServiceGitFailureIsAnOutcome(t *testing.T) {
	e := newActEnv(t, actOpts{repos: 2})
	e.git.fail["repo0"] = &git.Error{Op: "fetch", Code: 128, Stderr: "fatal: unable to access 'https://x.invalid/': Could not resolve host: x.invalid", Err: errors.New("exit status 128")}
	e.git.fail["repo1"] = &git.Error{Op: "fetch", Code: 128, Stderr: "fatal: Authentication failed for 'https://x.invalid/'", Err: errors.New("exit status 128")}
	id, err := e.start("fetch", "", e.ids...)
	if err != nil {
		t.Fatal(err)
	}
	v := e.wait(id)
	if v.Items[0].Reason != "network" || v.Items[1].Reason != "auth" || v.Items[1].Message == "" {
		t.Fatalf("items = %v", states(v))
	}
}

func TestServiceCancelSkipsWhatHasNotStarted(t *testing.T) {
	e := newActEnv(t, actOpts{repos: 3, max: 1})
	e.git.gate = make(chan struct{})
	id, err := e.start("fetch", "", e.ids...)
	if err != nil {
		t.Fatal(err)
	}
	<-e.git.started
	if _, err := e.svc.Cancel(id, "user"); err != nil {
		t.Fatal(err)
	}
	close(e.git.gate)
	v := e.wait(id)
	if v.Items[0].State != "done" || v.Items[1].Reason != "cancelled" || v.Items[2].Reason != "cancelled" {
		t.Fatalf("items = %v", states(v))
	}
	if _, err := e.svc.Cancel("gab_nope", "user"); err == nil {
		t.Error("cancel of an unknown batch worked")
	}
}

func TestServiceUnknownRepoIsNotFound(t *testing.T) {
	e := newActEnv(t, actOpts{repos: 1})
	_, err := e.start("fetch", "", "rep_unknown")
	if c, _ := apperr.ClassOf(err); c != apperr.NotFound {
		t.Fatalf("err = %v", err)
	}
	if len(e.git.called()) != 0 {
		t.Error("git ran for an untracked repo")
	}
}

// The start entry is committed before any git command: when it cannot be
// written, nothing runs and the batch does not exist.
func TestServiceStartAuditFailureStartsNothing(t *testing.T) {
	used := false
	e := newActEnv(t, actOpts{repos: 1, ids: func(prefix string) string {
		if prefix == "aud" {
			if used {
				return "aud_dup"
			}
			used = true
			return "aud_dup"
		}
		return db.NewID(prefix)
	}})
	id, err := e.start("fetch", "", e.ids...)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(id) // writes aud_dup (start) and then fails to write its repo entry: logged, batch goes on
	e.git.mu.Lock()
	e.git.calls = nil
	e.git.mu.Unlock()
	_, err = e.start("fetch", "", e.ids...)
	if c, _ := apperr.ClassOf(err); c != apperr.Unavailable {
		t.Fatalf("err = %v", err)
	}
	if len(e.git.called()) != 0 {
		t.Errorf("git ran although the start audit failed: %v", e.git.called())
	}
}

func TestServiceRunsOnlyFetchPullCheckout(t *testing.T) {
	e := newActEnv(t, actOpts{repos: 1})
	for _, k := range []struct{ kind, branch string }{{"fetch", ""}, {"pull", ""}, {"checkout", "x"}, {"switch", "x"}} {
		id, err := e.start(k.kind, k.branch, e.ids...)
		if err != nil {
			t.Fatal(err)
		}
		e.wait(id)
	}
	for _, c := range e.git.called() {
		if !strings.HasPrefix(c, "fetch:") && !strings.HasPrefix(c, "pull:") && !strings.HasPrefix(c, "checkout:") {
			t.Errorf("unexpected git call %q", c)
		}
	}
}

// A command that already ran must still be audited when the server stops
// while it is running (invariant 8).
func TestServiceAuditsCommandThatEndsAfterShutdownStarted(t *testing.T) {
	e := newActEnv(t, actOpts{repos: 1})
	e.git.gate = make(chan struct{})
	id, err := e.start("fetch", "", e.ids...)
	if err != nil {
		t.Fatal(err)
	}
	<-e.git.started
	e.stop() // the command now ends because its context is cancelled
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !contains(e.audits("repo:"+string(e.ids[0])), "git.fetch") {
		time.Sleep(10 * time.Millisecond)
	}
	if got := e.audits("repo:" + string(e.ids[0])); !contains(got, "git.fetch") {
		t.Fatalf("repo audit after shutdown = %v (batch %s)", got, id)
	}
}
