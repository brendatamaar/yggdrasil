package composer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/agents/mock"
	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/safety"
	"github.com/brendatamaar/yggdrasil/internal/safety/safetytest"
	"github.com/brendatamaar/yggdrasil/internal/tasks"
	"github.com/brendatamaar/yggdrasil/internal/worktrees"
	"github.com/brendatamaar/yggdrasil/internal/worktrees/worktreestest"
)

// ---- fakes for the unit tests ----

type fakeDiff struct {
	staged, work string
	err          error
}

func (f fakeDiff) Diff(_ context.Context, _ string, o git.DiffOptions) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	switch o.Mode {
	case git.DiffStaged:
		return f.staged, nil
	case git.DiffWorktree:
		return f.work, nil
	}
	return f.staged, nil
}

// fakeRunner is a task service that records calls and plays a script.
type fakeRunner struct {
	mu        sync.Mutex
	msgErr    error
	prompt    string
	req       tasks.MessageRequest
	status    core.TaskStatus // what the reader reports
	events    []db.TaskEvent
	cancelled bool
	dismissed bool
}

func (r *fakeRunner) Message(_ context.Context, req tasks.MessageRequest) (tasks.MessageResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.msgErr != nil {
		return tasks.MessageResult{}, r.msgErr
	}
	r.req, r.prompt = req, req.Prompt
	return tasks.MessageResult{Task: core.Task{ID: "tsk_0000000000000001"}}, nil
}

func (r *fakeRunner) Cancel(context.Context, core.TaskID) (core.Task, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancelled, r.status = true, core.TaskCancelled
	return core.Task{}, nil
}

func (r *fakeRunner) Dismiss(context.Context, core.TaskID) (core.Task, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dismissed = true
	return core.Task{}, nil
}

func (r *fakeRunner) Status(context.Context, core.TaskID) (core.TaskStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status, nil
}

func (r *fakeRunner) Events(context.Context, core.TaskID) ([]db.TaskEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.events, nil
}

func (r *fakeRunner) wasDismissed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dismissed
}

// unitSvc builds a service over a real store with one tracked repo, and fakes
// for git and the task service.
func unitSvc(t *testing.T, d fakeDiff, r *fakeRunner, mod func(*Config)) (*Service, api.DraftRequest) {
	t.Helper()
	st := newStore(t)
	proj := newProject(t, st)
	s := newService(t, st, func(c *Config) {
		c.Tasks, c.Reader, c.Git = r, r, d
		c.DraftPoll, c.DraftTimeout = time.Millisecond, 2*time.Second
		if mod != nil {
			mod(c)
		}
	})
	return s, api.DraftRequest{Kind: KindCommitMessage, ProjectID: proj.proj.ID, RepoID: proj.repo.ID, Agent: "mock"}
}

func asstEvent(t *testing.T, text string) []db.TaskEvent {
	return []db.TaskEvent{ev(t, 1, agents.Event{Kind: agents.EventMessage, Role: agents.RoleAssistant, Text: text})}
}

func TestDraftCommitMessageUsesAnAskTaskAndDismissesIt(t *testing.T) {
	r := &fakeRunner{status: core.TaskReview}
	s, req := unitSvc(t, fakeDiff{work: "diff --git a/x b/x\n+hello"}, r, nil)
	r.events = asstEvent(t, "```\nfeat: say hello\n```")
	res, err := s.Draft(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "feat: say hello" {
		t.Fatalf("text %q", res.Text)
	}
	if r.req.Mode != "ask" || r.req.Scope != "repo" || r.req.Title != "Draft commit message" || len(r.req.Repos) != 1 {
		t.Fatalf("not an ask task on one repo: %+v", r.req)
	}
	if !strings.Contains(r.prompt, "+hello") || !strings.Contains(r.prompt, "Output only the message") {
		t.Fatalf("prompt %q", r.prompt)
	}
	if !r.wasDismissed() {
		t.Fatal("the draft task must be dismissed")
	}
}

func TestDraftMRSplitsTitleAndPushLine(t *testing.T) {
	r := &fakeRunner{status: core.TaskReview}
	s, req := unitSvc(t, fakeDiff{staged: "d"}, r, nil)
	req.Kind = KindMRDescription
	r.events = asstEvent(t, "Add orders API\n\nLine one\nLine two")
	res, err := s.Draft(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Title != "Add orders API" || res.PushDescription != "Line one Line two" || !strings.Contains(res.Description, "\n") {
		t.Fatalf("%+v", res)
	}
	if r.req.Title != "Draft MR description" {
		t.Fatalf("title %q", r.req.Title)
	}
}

func TestDraftTimeoutCancelsTheTaskAndDismisses(t *testing.T) {
	r := &fakeRunner{status: core.TaskRunning}
	s, req := unitSvc(t, fakeDiff{work: "d"}, r, func(c *Config) { c.DraftTimeout = 30 * time.Millisecond })
	_, err := s.Draft(context.Background(), req)
	if c, _ := apperr.ClassOf(err); c != apperr.Unavailable {
		t.Fatalf("timeout: %v", err)
	}
	if !r.cancelled || !r.wasDismissed() {
		t.Fatalf("cancelled=%v dismissed=%v", r.cancelled, r.wasDismissed())
	}
}

func TestDraftCallerGoingAwayCancelsTheTask(t *testing.T) {
	r := &fakeRunner{status: core.TaskRunning}
	s, req := unitSvc(t, fakeDiff{work: "d"}, r, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := s.Draft(ctx, req); err == nil {
		t.Fatal("want an error")
	}
	if !r.cancelled {
		t.Fatal("task left running")
	}
}

func TestDraftErrors(t *testing.T) {
	big := strings.Repeat("x", MaxDiffBytes+1)
	cases := []struct {
		name  string
		diff  fakeDiff
		run   *fakeRunner
		edit  func(*api.DraftRequest)
		class apperr.Class
	}{
		{"diff over the cap", fakeDiff{work: big}, &fakeRunner{status: core.TaskReview}, nil, apperr.Invalid},
		{"no changes", fakeDiff{}, &fakeRunner{status: core.TaskReview}, nil, apperr.Invalid},
		{"git fails", fakeDiff{err: errors.New("boom")}, &fakeRunner{status: core.TaskReview}, nil, apperr.Unavailable},
		{"bad kind", fakeDiff{work: "d"}, &fakeRunner{}, func(r *api.DraftRequest) { r.Kind = "poem" }, apperr.Invalid},
		{"no agent", fakeDiff{work: "d"}, &fakeRunner{}, func(r *api.DraftRequest) { r.Agent = "" }, apperr.Invalid},
		{"bad model", fakeDiff{work: "d"}, &fakeRunner{}, func(r *api.DraftRequest) { r.Model = "a b" }, apperr.Invalid},
		{"agent unavailable", fakeDiff{work: "d"}, &fakeRunner{msgErr: &tasks.RejectedError{Reason: "agent_unavailable", Message: "no agent"}}, nil, apperr.Unavailable},
		{"task failed", fakeDiff{work: "d"}, &fakeRunner{status: core.TaskFailed}, nil, apperr.Unavailable},
		{"empty answer", fakeDiff{work: "d"}, &fakeRunner{status: core.TaskReview}, nil, apperr.Unavailable},
		{"unknown repo", fakeDiff{work: "d"}, &fakeRunner{}, func(r *api.DraftRequest) { r.RepoID = "rep_0000000000000000" }, apperr.NotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, req := unitSvc(t, c.diff, c.run, nil)
			if c.edit != nil {
				c.edit(&req)
			}
			_, err := s.Draft(context.Background(), req)
			if got, _ := apperr.ClassOf(err); got != c.class {
				t.Fatalf("class %v, err %v", got, err)
			}
			if c.run.prompt != "" && c.name == "diff over the cap" {
				t.Fatal("an over-cap diff must not reach the agent")
			}
		})
	}
}

// ---- integration: the real task service with the mock adapter ----

type recAdapter struct {
	*mock.Adapter
	mu      sync.Mutex
	started []agents.StartOptions
}

func (a *recAdapter) Start(ctx context.Context, o agents.StartOptions) (agents.Run, error) {
	a.mu.Lock()
	a.started = append(a.started, o)
	a.mu.Unlock()
	return a.Adapter.Start(ctx, o)
}

type reg map[string]agents.Adapter

func (r reg) Get(n string) (agents.Adapter, bool) { a, ok := r[n]; return a, ok }

type nopHub struct{}

func (nopHub) Status(tasks.StatusMessage)             {}
func (nopHub) Event(core.TaskID, int64, agents.Event) {}

type machine struct{}

func (machine) CPUs() int      { return 8 }
func (machine) FreeMemMB() int { return 16384 }

type openLock struct{}

func (openLock) TryAcquire([]core.RepoID, string) (tasks.Release, string, bool) {
	return func() {}, "", true
}

func TestDraftThroughTheRealTaskServiceWithTheMockAgent(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	env, err := w.env.Build(safety.EnvOptions{})
	if err != nil {
		t.Fatal(err)
	}
	g, err := git.New(env)
	if err != nil {
		t.Skip("git is not installed")
	}
	// An unstaged change in the main checkout.
	mainFile := filepath.Join(w.api.Path, "src", "main.go")
	if err := os.WriteFile(mainFile, []byte("package main\n\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := g.Diff(ctx, w.api.Path, git.DiffOptions{Mode: git.DiffWorktree})

	wtRoot := filepath.Join(w.root, "ygg")
	if err := os.MkdirAll(filepath.Join(wtRoot, "wt"), 0o750); err != nil {
		t.Fatal(err)
	}
	ens, err := worktrees.New(worktrees.Config{
		Git: worktreestest.NewGit(), Store: worktrees.DBStore{S: w.st}, Rand: &worktreestest.Rand{},
		Now: func() time.Time { return time.Now().UTC() }, NewID: db.NewID, DataDir: wtRoot,
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter := &recAdapter{Adapter: mock.New(mock.Options{Fixture: "plain"})}
	tsvc, err := tasks.New(tasks.Config{
		Store: w.st, Adapters: reg{"mock": adapter}, Worktrees: ens, MainLock: openLock{},
		Scanner: safetytest.FakeScanner{}, Snapshotter: g, Env: w.env, Redactor: safety.NewRedactor(),
		Hub: nopHub{}, Machine: machine{}, Killer: func(string) error { return nil },
		Now: func() time.Time { return time.Now().UTC() }, NewID: db.NewID,
		WriteModes: true, UsageEvery: time.Millisecond, SnapshotTimeout: 20 * time.Second, SnapshotRetry: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tsvc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tsvc.Close() })

	s := w.svc(t, func(c *Config) { c.Tasks, c.Git = tsvc, g })
	res, err := s.Draft(ctx, api.DraftRequest{Kind: KindCommitMessage, ProjectID: w.project.ID, RepoID: w.api.ID, Agent: "mock"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "shop-web calls it for every page." {
		t.Fatalf("text %q (the last assistant message of the run)", res.Text)
	}
	if len(adapter.started) != 1 || adapter.started[0].Mode != core.ModeAsk {
		t.Fatalf("starts %+v", adapter.started)
	}
	if !strings.Contains(adapter.started[0].Prompt, "func main() {}") {
		t.Fatal("the diff did not reach the agent")
	}
	// The task left the board (dismissed -> done) and the repo is untouched.
	deadline := time.Now().Add(10 * time.Second)
	for {
		list, err := w.st.ListTasks(ctx, w.project.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(list) == 1 && list[0].Status == core.TaskDone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("draft task not dismissed: %+v", list)
		}
		time.Sleep(20 * time.Millisecond)
	}
	after, _ := g.Diff(ctx, w.api.Path, git.DiffOptions{Mode: git.DiffWorktree})
	if before != after {
		t.Fatal("drafting changed the repo")
	}
}
