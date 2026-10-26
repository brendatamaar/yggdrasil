package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/guard"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/tasks"
)

// fakeTasks is a TaskService that records what it is asked and answers with
// what the test set.
type fakeTasks struct {
	mu sync.Mutex

	msgs      []tasks.MessageRequest
	msgRes    tasks.MessageResult
	cancelled []core.TaskID
	dismissed []core.TaskID
	board     tasks.BoardView
	max       int
	setMax    []string
	trust     guard.Trust
	trustArgs []string
	revokes   int

	// err is returned by every method when set.
	err error
}

func (f *fakeTasks) Message(_ context.Context, r tasks.MessageRequest) (tasks.MessageResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, r)
	return f.msgRes, f.err
}

func (f *fakeTasks) Cancel(_ context.Context, id core.TaskID) (core.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled = append(f.cancelled, id)
	return core.Task{ID: id, Status: core.TaskCancelled}, f.err
}

func (f *fakeTasks) Dismiss(_ context.Context, id core.TaskID) (core.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dismissed = append(f.dismissed, id)
	return core.Task{ID: id, Status: core.TaskDone}, f.err
}

func (f *fakeTasks) Board(context.Context, core.ProjectID) (tasks.BoardView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.board, f.err
}

func (f *fakeTasks) MaxAgents(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.max, f.err
}

func (f *fakeTasks) SetMaxAgents(_ context.Context, v string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setMax = append(f.setMax, v)
	return f.err
}

func (f *fakeTasks) TrustStatus(context.Context, core.RepoID) (guard.Trust, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.trust, f.err
}

func (f *fakeTasks) Trust(_ context.Context, r core.RepoID, hash string) (guard.Trust, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.trustArgs = append(f.trustArgs, string(r)+":"+hash)
	return f.trust, f.err
}

func (f *fakeTasks) Revoke(context.Context, core.RepoID) (guard.Trust, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revokes++
	return guard.Untrusted{Why: guard.Why{Kind: guard.WhyRevoked}}, f.err
}

func (f *fakeTasks) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.msgs) + len(f.cancelled) + len(f.dismissed) + len(f.setMax) + len(f.trustArgs) + f.revokes
}

var _ TaskService = (*fakeTasks)(nil)

// The real service fits the interface for the methods it has today (the trust
// methods arrive with internal/tasks/trust.go).
type serviceSubset interface {
	Message(ctx context.Context, req tasks.MessageRequest) (tasks.MessageResult, error)
	Cancel(ctx context.Context, id core.TaskID) (core.Task, error)
	Dismiss(ctx context.Context, id core.TaskID) (core.Task, error)
	Board(ctx context.Context, project core.ProjectID) (tasks.BoardView, error)
	MaxAgents(ctx context.Context) (int, error)
	SetMaxAgents(ctx context.Context, value string) error
}

var _ serviceSubset = (*tasks.Service)(nil)

func taskEnv(t *testing.T, writeModes bool) (*env, *fakeTasks) {
	t.Helper()
	f := &fakeTasks{max: 2}
	e := newEnv(t, func(d *Deps) { d.Tasks = f; d.WriteModes = writeModes })
	return e, f
}

const goodHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestCreateTaskPassesTheParsedRequestToTheService(t *testing.T) {
	e, f := taskEnv(t, false)
	f.msgRes = tasks.MessageResult{Task: core.Task{ID: "tsk_1", Title: "Cart bug", Status: core.TaskQueued}, Warning: "edits_main_checkout"}
	req := e.createReq()
	req.Title = "Cart bug"
	req.Model = "haiku"
	resp, data := e.do(t, http.MethodPost, "/api/tasks", req)
	var tr TaskResponse
	if resp.StatusCode != http.StatusCreated || json.Unmarshal(data, &tr) != nil || tr.Task.ID != "tsk_1" || tr.Warning != "edits_main_checkout" {
		t.Fatalf("create = %d %s", resp.StatusCode, data)
	}
	got := f.msgs[0]
	want := tasks.MessageRequest{
		ProjectID: e.project.ID, Prompt: req.Prompt, Mode: "ask", Scope: "repo", Repos: []string{string(e.repo.ID)},
		Agent: "mock", Model: "haiku", Title: "Cart bug",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("service got %+v, want %+v", got, want)
	}
}

func TestCreateTaskRejectsWhatParseMessageRejects(t *testing.T) {
	e, f := taskEnv(t, true)
	mutate := map[string]func(*CreateTaskRequest){
		"empty prompt":    func(r *CreateTaskRequest) { r.Prompt = "  " },
		"unknown mode":    func(r *CreateTaskRequest) { r.Mode = "wild" },
		"two repos":       func(r *CreateTaskRequest) { r.RepoIDs = append(r.RepoIDs, "repo_2") },
		"no repos":        func(r *CreateTaskRequest) { r.RepoIDs = nil },
		"bad agent":       func(r *CreateTaskRequest) { r.Agent = "a b" },
		"bad model":       func(r *CreateTaskRequest) { r.Model = "x;rm" },
		"project, repos":  func(r *CreateTaskRequest) { r.Mode, r.Scope = core.ModeProject, "project" },
		"unknown scope":   func(r *CreateTaskRequest) { r.Scope = "galaxy" },
		"no project":      func(r *CreateTaskRequest) { r.ProjectID = "" },
		"mode vs scope":   func(r *CreateTaskRequest) { r.Scope = "project" },
		"project in repo": func(r *CreateTaskRequest) { r.Mode = core.ModeProject },
	}
	for name, m := range mutate {
		req := e.createReq()
		req.Prompt = "Look at this " + secretDetail
		m(&req)
		resp, data := e.do(t, http.MethodPost, "/api/tasks", req)
		if resp.StatusCode != http.StatusBadRequest || errCode(t, data) != ErrBadRequest {
			t.Errorf("%s: %d %s, want 400", name, resp.StatusCode, data)
		}
		if strings.Contains(string(data), secretDetail) {
			t.Errorf("%s: the error echoes the user's text: %s", name, data)
		}
	}
	for _, body := range []string{`{"bogusField":1}`, `not json`, ``} {
		resp, _ := e.doRaw(t, http.MethodPost, "/api/tasks", body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body %q = %d, want 400", body, resp.StatusCode)
		}
	}
	if f.calls() != 0 {
		t.Errorf("the service was called %d times for refused requests", f.calls())
	}
}

func TestProjectModeDefaultsToTheProjectScope(t *testing.T) {
	e, f := taskEnv(t, true)
	req := e.createReq()
	req.Mode, req.RepoIDs = core.ModeProject, nil
	f.msgRes = tasks.MessageResult{Task: core.Task{ID: "tsk_1"}}
	if resp, data := e.do(t, http.MethodPost, "/api/tasks", req); resp.StatusCode != http.StatusCreated {
		t.Fatalf("project task = %d %s", resp.StatusCode, data)
	}
	if got := f.msgs[0]; got.Scope != "project" || got.Mode != "project" {
		t.Errorf("service got %+v", got)
	}
}

// The flag: until it is on, only Ask passes (the lead flips the default after
// review gate 3).
func TestWriteModesFlag(t *testing.T) {
	for _, on := range []bool{false, true} {
		e, f := taskEnv(t, on)
		f.msgRes = tasks.MessageResult{Task: core.Task{ID: "tsk_1"}}
		task := e.seedTask(t, core.TaskReview)
		for _, mode := range []core.TaskMode{core.ModeCode, core.ModeCurrentBranch} {
			req := e.createReq()
			req.Mode = mode
			create, cdata := e.do(t, http.MethodPost, "/api/tasks", req)
			msg, mdata := e.do(t, http.MethodPost, "/api/tasks/"+string(task.ID)+"/messages", MessageRequest{Mode: mode, Prompt: "go"})
			if on {
				if create.StatusCode != http.StatusCreated || msg.StatusCode != http.StatusAccepted {
					t.Errorf("flag on, %s: create %d %s, message %d %s", mode, create.StatusCode, cdata, msg.StatusCode, mdata)
				}
				continue
			}
			for _, d := range [][]byte{cdata, mdata} {
				if errCode(t, d) != ErrBadRequest || !strings.Contains(string(d), "Code mode is not enabled in this version") {
					t.Errorf("flag off, %s: %s", mode, d)
				}
			}
		}
		if !on && f.calls() != 0 {
			t.Errorf("flag off: the service was called %d times", f.calls())
		}
		// Ask always passes.
		req := e.createReq()
		if resp, data := e.do(t, http.MethodPost, "/api/tasks", req); resp.StatusCode != http.StatusCreated {
			t.Errorf("ask with flag %v = %d %s", on, resp.StatusCode, data)
		}
	}
}

func TestServiceErrorsMapToPlainResponses(t *testing.T) {
	e, f := taskEnv(t, false)
	secret := errors.New(`open C:\Users\someone\secret.db: ` + secretDetail)
	cases := []struct {
		name   string
		err    error
		status int
		code   ErrorCode
		shown  string
	}{
		{"user error", &coretasks.UserError{Message: "Choose one repo."}, 400, ErrBadRequest, "Choose one repo."},
		{"wrapped user error", fmt.Errorf("x: %w", &coretasks.UserError{Message: "Choose one repo."}), 400, ErrBadRequest, "Choose one repo."},
		{"rejected", &tasks.RejectedError{Reason: "task_busy", Message: "This task is still working."}, 409, ErrConflict, "This task is still working."},
		{"not found", fmt.Errorf("get: %w", db.ErrNotFound), 404, ErrNotFound, ""},
		{"conflict", fmt.Errorf("save: %w", db.ErrConflict), 409, ErrConflict, ""},
		{"other", secret, 500, ErrInternal, ""},
	}
	task := e.seedTask(t, core.TaskReview)
	routes := []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/tasks", e.createReq()},
		{"POST", "/api/tasks/" + string(task.ID) + "/messages", MessageRequest{Mode: core.ModeAsk, Prompt: "again"}},
		{"POST", "/api/tasks/" + string(task.ID) + "/cancel", nil},
		{"POST", "/api/tasks/" + string(task.ID) + "/dismiss", nil},
		{"GET", "/api/board", nil},
		{"PUT", "/api/board/max-agents", SetMaxAgentsRequest{MaxAgents: 3}},
		{"POST", "/api/repos/" + string(e.repo.ID) + "/trust", TrustRequest{Hash: goodHash}},
		{"DELETE", "/api/repos/" + string(e.repo.ID) + "/trust", nil},
	}
	for _, c := range cases {
		f.err = c.err
		for _, r := range routes {
			resp, data := e.do(t, r.method, r.path, r.body)
			if resp.StatusCode != c.status || errCode(t, data) != c.code {
				t.Errorf("%s %s %s = %d %s, want %d %s", c.name, r.method, r.path, resp.StatusCode, data, c.status, c.code)
			}
			if c.shown != "" && !strings.Contains(string(data), c.shown) {
				t.Errorf("%s %s: message %s lacks %q", c.name, r.path, data, c.shown)
			}
			if strings.Contains(string(data), "secret.db") || strings.Contains(string(data), secretDetail) {
				t.Errorf("%s %s leaks internal detail: %s", c.name, r.path, data)
			}
		}
	}
}

func TestMessageCancelAndDismiss(t *testing.T) {
	e, f := taskEnv(t, false)
	id := "tsk_abc123"
	f.msgRes = tasks.MessageResult{Task: core.Task{ID: core.TaskID(id), Status: core.TaskQueued}}
	resp, data := e.do(t, http.MethodPost, "/api/tasks/"+id+"/messages", MessageRequest{Mode: core.ModeAsk, Prompt: "and the tests?", Model: "haiku", OptIn: true})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("message = %d %s", resp.StatusCode, data)
	}
	got := f.msgs[0]
	if got.TaskID != core.TaskID(id) || got.Prompt != "and the tests?" || got.Mode != "ask" || got.Model != "haiku" || !got.OptIn {
		t.Errorf("service got %+v", got)
	}
	if resp, data = e.do(t, http.MethodPost, "/api/tasks/"+id+"/cancel", nil); resp.StatusCode != http.StatusAccepted {
		t.Errorf("cancel = %d %s", resp.StatusCode, data)
	}
	var tr TaskResponse
	if resp, data = e.do(t, http.MethodPost, "/api/tasks/"+id+"/dismiss", nil); resp.StatusCode != http.StatusOK || json.Unmarshal(data, &tr) != nil || tr.Task.Status != core.TaskDone {
		t.Errorf("dismiss = %d %s", resp.StatusCode, data)
	}
	if len(f.cancelled) != 1 || len(f.dismissed) != 1 || f.cancelled[0] != core.TaskID(id) {
		t.Errorf("calls: cancelled %v, dismissed %v", f.cancelled, f.dismissed)
	}
	for _, path := range []string{"/api/tasks/a%20b/messages", "/api/tasks/a%20b/cancel", "/api/tasks/a%20b/dismiss"} {
		if resp, _ := e.do(t, http.MethodPost, path, MessageRequest{Mode: core.ModeAsk, Prompt: "x"}); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", path, resp.StatusCode)
		}
	}
	// Unknown fields are refused.
	if resp, _ := e.doRaw(t, http.MethodPost, "/api/tasks/"+id+"/messages", `{"prompt":"x","mode":"ask","agent":"other"}`); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("message with an agent = %d, want 400", resp.StatusCode)
	}
}

func TestMutatingTaskRoutesKeepHostAndOriginChecks(t *testing.T) {
	e, f := taskEnv(t, true)
	task := e.seedTask(t, core.TaskReview)
	routes := []struct{ method, path string }{
		{"POST", "/api/tasks"},
		{"POST", "/api/tasks/" + string(task.ID) + "/messages"},
		{"POST", "/api/tasks/" + string(task.ID) + "/cancel"},
		{"POST", "/api/tasks/" + string(task.ID) + "/dismiss"},
		{"PUT", "/api/board/max-agents"},
		{"POST", "/api/repos/" + string(e.repo.ID) + "/trust"},
		{"DELETE", "/api/repos/" + string(e.repo.ID) + "/trust"},
	}
	for _, r := range routes {
		for name, mod := range map[string]func(*http.Request){
			"foreign origin": func(q *http.Request) { q.Header.Set("Origin", "https://evil.example") },
			"foreign host":   func(q *http.Request) { q.Host = "evil.example" },
		} {
			resp, data := e.do(t, r.method, r.path, e.createReq(), mod)
			if resp.StatusCode != http.StatusForbidden || errCode(t, data) != ErrHostRejected {
				t.Errorf("%s %s with %s = %d %s, want 403", r.method, r.path, name, resp.StatusCode, data)
			}
		}
	}
	if f.calls() != 0 {
		t.Errorf("the service was reached %d times through rejected requests", f.calls())
	}
}

func TestTaskRoutesAnswer501WithoutAService(t *testing.T) {
	e := newEnv(t)
	for _, r := range []struct{ method, path string }{
		{"POST", "/api/tasks"}, {"POST", "/api/tasks/tsk_1/messages"}, {"POST", "/api/tasks/tsk_1/cancel"},
		{"POST", "/api/tasks/tsk_1/dismiss"}, {"GET", "/api/board"}, {"GET", "/api/board/max-agents"},
		{"PUT", "/api/board/max-agents"}, {"GET", "/api/repos/repo_1/trust"},
	} {
		if resp, data := e.do(t, r.method, r.path, nil); resp.StatusCode != 501 || errCode(t, data) != ErrUnavailable {
			t.Errorf("%s %s = %d %s, want 501", r.method, r.path, resp.StatusCode, data)
		}
	}
}

func TestTaskEventsAndListFromTheStore(t *testing.T) {
	e, _ := taskEnv(t, false)
	task := e.seedTask(t, core.TaskReview)
	e.seedEvents(t, task.ID, 6)
	var er TaskEventsResponse
	_, data := e.do(t, http.MethodGet, "/api/tasks/"+string(task.ID)+"/events?after=0", nil)
	if err := json.Unmarshal(data, &er); err != nil || len(er.Events) != 6 || er.Events[5].Seq != 6 {
		t.Fatalf("events = %s", data)
	}
	_, data = e.do(t, http.MethodGet, "/api/tasks/"+string(task.ID)+"/events?after=4", nil)
	if err := json.Unmarshal(data, &er); err != nil || len(er.Events) != 2 || er.Events[0].Seq != 5 {
		t.Errorf("events after 4 = %s", data)
	}
	if resp, _ := e.do(t, http.MethodGet, "/api/tasks/"+string(task.ID)+"/events?after=-1", nil); resp.StatusCode != 400 {
		t.Errorf("after=-1 gave %d, want 400", resp.StatusCode)
	}
	var list TaskListResponse
	_, data = e.do(t, http.MethodGet, "/api/tasks?status=review", nil)
	if err := json.Unmarshal(data, &list); err != nil || len(list.Tasks) != 1 || list.Tasks[0].ID != task.ID {
		t.Errorf("review list = %s", data)
	}
	_, data = e.do(t, http.MethodGet, "/api/tasks?status=running", nil)
	if err := json.Unmarshal(data, &list); err != nil || len(list.Tasks) != 0 {
		t.Errorf("running list = %s", data)
	}
}

func TestBoardResponse(t *testing.T) {
	e, f := taskEnv(t, false)
	now := time.Now().UTC()
	f.board = tasks.BoardView{
		Max: 3, Active: 3, Hint: coretasks.Hint{RaiseTo: 4},
		Cards: []tasks.BoardCard{
			{Task: core.Task{ID: "tsk_1", Title: "Cart bug", Mode: core.ModeAsk, Agent: "mock", Status: core.TaskRunning, RepoIDs: []core.RepoID{"repo_1"}, UpdatedAt: now, Prompt: secretDetail}, Column: coretasks.ColumnWorking, BlockedBy: "Waiting for shop-api"},
			{Task: core.Task{ID: "tsk_2", Title: "Draft", Status: core.TaskDraft}, Column: coretasks.ColumnDone},
			{Task: core.Task{ID: "tsk_3", Title: "Docs", Status: core.TaskReview}, Column: coretasks.ColumnNeedsYou, PendingProposals: 2},
		},
	}
	resp, data := e.do(t, http.MethodGet, "/api/board?project="+string(e.project.ID), nil)
	var b BoardResponse
	if resp.StatusCode != 200 || json.Unmarshal(data, &b) != nil {
		t.Fatalf("board = %d %s", resp.StatusCode, data)
	}
	if b.MaxAgents != 3 || b.Active != 3 || b.Hint.RaiseTo != 4 || len(b.Cards) != 2 {
		t.Fatalf("board = %+v", b)
	}
	if c := b.Cards[0]; c.TaskID != "tsk_1" || c.Column != "working" || c.Title != "Cart bug" || len(c.RepoIDs) != 1 || c.BlockedBy != "Waiting for shop-api" {
		t.Errorf("card 0 = %+v", c)
	}
	if c := b.Cards[1]; c.TaskID != "tsk_3" || c.Column != "needs_you" || c.PendingProposals != 2 {
		t.Errorf("card 1 = %+v", c)
	}
	if strings.Contains(string(data), secretDetail) || strings.Contains(string(data), "tsk_2") {
		t.Errorf("the board leaks a prompt or a draft: %s", data)
	}
	if resp, _ := e.do(t, http.MethodGet, "/api/board?project=a%20b", nil); resp.StatusCode != 400 {
		t.Errorf("bad project = %d, want 400", resp.StatusCode)
	}
}

func TestMaxAgentsBounds(t *testing.T) {
	e, f := taskEnv(t, false)
	f.max = 2
	f.board = tasks.BoardView{Hint: coretasks.Hint{RaiseTo: 4}}
	var m MaxAgentsResponse
	resp, data := e.do(t, http.MethodGet, "/api/board/max-agents", nil)
	if resp.StatusCode != 200 || json.Unmarshal(data, &m) != nil || m.MaxAgents != 2 || m.Min != 1 || m.Max != 16 || m.Hint.RaiseTo != 4 {
		t.Fatalf("get = %d %s", resp.StatusCode, data)
	}
	for _, n := range []int{-1, 0, 17, 1000} {
		resp, data := e.do(t, http.MethodPut, "/api/board/max-agents", SetMaxAgentsRequest{MaxAgents: n})
		if resp.StatusCode != 400 || errCode(t, data) != ErrBadRequest {
			t.Errorf("max %d = %d %s, want 400", n, resp.StatusCode, data)
		}
	}
	if len(f.setMax) != 0 {
		t.Fatalf("out-of-range values reached the service: %v", f.setMax)
	}
	for _, n := range []int{1, 8, 16} {
		f.max = n
		resp, data := e.do(t, http.MethodPut, "/api/board/max-agents", SetMaxAgentsRequest{MaxAgents: n})
		if resp.StatusCode != 200 || json.Unmarshal(data, &m) != nil || m.MaxAgents != n {
			t.Errorf("max %d = %d %s", n, resp.StatusCode, data)
		}
	}
	if fmt.Sprint(f.setMax) != "[1 8 16]" {
		t.Errorf("service got %v", f.setMax)
	}
	if resp, _ := e.doRaw(t, http.MethodPut, "/api/board/max-agents", `{"maxAgents":"many"}`); resp.StatusCode != 400 {
		t.Errorf("text value = %d, want 400", resp.StatusCode)
	}
}

type fakeWorktrees struct{ views []WorktreeView }

func (f fakeWorktrees) ForTask(context.Context, core.TaskID) ([]WorktreeView, error) {
	return f.views, nil
}

func TestTaskWorktreesDoNotExposePaths(t *testing.T) {
	const secretPath = `C:\Users\someone\worktrees\wt-1`
	e := newEnv(t, func(d *Deps) {
		d.Worktrees = fakeWorktrees{views: []WorktreeView{{
			Worktree: core.Worktree{ID: "wt_1", RepoID: "repo_1", TaskID: "tsk_1", Name: "calm-otter", Branch: "ygg/calm-otter", Path: secretPath, Status: core.WorktreeActive},
			RepoName: "shop-api", BaseCommit: "abc123",
		}}}
	})
	resp, data := e.do(t, http.MethodGet, "/api/tasks/tsk_1/worktrees", nil)
	var w WorktreesResponse
	if resp.StatusCode != 200 || json.Unmarshal(data, &w) != nil || len(w.Worktrees) != 1 {
		t.Fatalf("worktrees = %d %s", resp.StatusCode, data)
	}
	if g := w.Worktrees[0]; g.Name != "calm-otter" || g.Branch != "ygg/calm-otter" || g.RepoName != "shop-api" || g.Status != core.WorktreeActive {
		t.Errorf("worktree = %+v", g)
	}
	if strings.Contains(string(data), "someone") || strings.Contains(string(data), "wt-1") {
		t.Errorf("the response exposes a path: %s", data)
	}
	if resp, _ := e.do(t, http.MethodGet, "/api/tasks/a%20b/worktrees", nil); resp.StatusCode != 400 {
		t.Errorf("bad id = %d, want 400", resp.StatusCode)
	}
}

func TestTrustEndpoints(t *testing.T) {
	e, f := taskEnv(t, false)
	base := "/api/repos/" + string(e.repo.ID) + "/trust"
	scan := guard.Scan{Hash: goodHash, Files: []guard.ScanFile{{Path: ".mcp.json", WouldRun: []string{"Start the MCP server 'x'"}}}}

	f.trust = guard.Untrusted{Repo: e.repo.ID, Found: scan, Why: guard.Why{Kind: guard.WhyNeverTrusted}}
	var d TrustDTO
	resp, data := e.do(t, http.MethodGet, base, nil)
	if resp.StatusCode != 200 || json.Unmarshal(data, &d) != nil || d.State != "untrusted" || d.Why != "never_trusted" || d.Hash != goodHash ||
		len(d.Files) != 1 || d.Files[0].WouldRun[0] != "Start the MCP server 'x'" {
		t.Fatalf("status = %d %s", resp.StatusCode, data)
	}

	// A repo with no config, and one whose scan was incomplete (no hash to trust).
	f.trust = guard.NoConfig{Repo: e.repo.ID}
	_, data = e.do(t, http.MethodGet, base, nil)
	if json.Unmarshal(data, &d) != nil || d.State != "none" || d.Files == nil {
		t.Errorf("no config = %s", data)
	}
	f.trust = guard.Untrusted{Repo: e.repo.ID, Found: scan, Why: guard.Why{Kind: guard.WhyScanIncomplete}}
	_, data = e.do(t, http.MethodGet, base, nil)
	d = TrustDTO{}
	if json.Unmarshal(data, &d) != nil || d.Hash != "" || d.Why != "scan_incomplete" {
		t.Errorf("incomplete = %s", data)
	}

	// Trusting: the hash must be well formed before the service hears of it.
	for _, h := range []string{"", "abc", strings.ToUpper(goodHash), goodHash + "0", strings.Repeat("g", 64)} {
		if resp, data := e.do(t, http.MethodPost, base, TrustRequest{Hash: h}); resp.StatusCode != 400 {
			t.Errorf("hash %q = %d %s, want 400", h, resp.StatusCode, data)
		}
	}
	if len(f.trustArgs) != 0 {
		t.Fatalf("malformed hashes reached the service: %v", f.trustArgs)
	}

	// The config changed since the user looked: the service leaves the state alone.
	f.trust = guard.Untrusted{Repo: e.repo.ID, Found: scan, Why: guard.Why{Kind: guard.WhyConfigChanged}}
	resp, data = e.do(t, http.MethodPost, base, TrustRequest{Hash: goodHash})
	if resp.StatusCode != 409 || errCode(t, data) != ErrConflict || !strings.Contains(string(data), "not what you were shown") {
		t.Errorf("stale hash = %d %s, want 409", resp.StatusCode, data)
	}

	f.trust = guard.Trusted{Repo: e.repo.ID, Hash: goodHash, Scan: scan}
	resp, data = e.do(t, http.MethodPost, base, TrustRequest{Hash: goodHash})
	if resp.StatusCode != 200 || json.Unmarshal(data, &d) != nil || d.State != "trusted" || d.Hash != goodHash {
		t.Errorf("trust = %d %s", resp.StatusCode, data)
	}
	if got := f.trustArgs[len(f.trustArgs)-1]; got != string(e.repo.ID)+":"+goodHash {
		t.Errorf("service got %q", got)
	}

	resp, data = e.do(t, http.MethodDelete, base, nil)
	if resp.StatusCode != 200 || json.Unmarshal(data, &d) != nil || d.State != "untrusted" || d.Why != "revoked" || f.revokes != 1 {
		t.Errorf("revoke = %d %s", resp.StatusCode, data)
	}
	if resp, _ := e.do(t, http.MethodGet, "/api/repos/a%20b/trust", nil); resp.StatusCode != 400 {
		t.Errorf("bad repo id = %d, want 400", resp.StatusCode)
	}
}

func TestTrustAlreadyTrustedOtherHashIs409(t *testing.T) {
	e, f := taskEnv(t, false)
	base := "/api/repos/" + string(e.repo.ID) + "/trust"
	other := strings.Repeat("a", 64)
	f.trust = guard.Trusted{Repo: e.repo.ID, Hash: other}
	resp, data := e.do(t, http.MethodPost, base, TrustRequest{Hash: goodHash})
	if resp.StatusCode != 409 || errCode(t, data) != ErrConflict {
		t.Errorf("other hash on a trusted repo = %d %s, want 409", resp.StatusCode, data)
	}
	resp, _ = e.do(t, http.MethodPost, base, TrustRequest{Hash: other})
	if resp.StatusCode != 200 {
		t.Errorf("same hash = %d, want 200", resp.StatusCode)
	}
}

func TestDecodeBodyRejectsTrailingJSON(t *testing.T) {
	for body, want := range map[string]bool{
		`{"hash":"x"}`:         true,
		`{"hash":"x"} {"a":1}`: false,
		`{"hash":"x"} garbage`: false,
		"{\"hash\":\"x\"}\n\n": true,
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		var v TrustRequest
		if got := decodeBody(rec, req, &v); got != want {
			t.Errorf("decodeBody(%q) = %v, want %v", body, got, want)
		}
	}
}

func TestHubAdapterPublishesOnTheTopics(t *testing.T) {
	e := newEnv(t)
	h := &HubAdapter{}
	h.Status(tasks.StatusMessage{TaskID: "tsk_0", Title: "before bind", Status: core.TaskRunning}) // dropped, no panic
	h.Event("tsk_0", 1, agents.Event{})
	h.Bind(e.srv)

	ws := e.dial(t)
	subscribe(t, ws, TopicTasks)
	subscribe(t, ws, TopicBoard)
	subscribe(t, ws, TopicTaskPrefix+"tsk_1")
	time.Sleep(100 * time.Millisecond)

	h.Status(tasks.StatusMessage{TaskID: "tsk_1", Title: "Cart bug", Status: core.TaskRunning})
	h.Event("tsk_1", 7, agents.Event{Seq: 7, Kind: agents.EventMessage, Text: "hello"})
	h.Event("tsk_1", 8, agents.Event{Seq: 8, Kind: agents.EventMessage, Text: "again"})

	seen := map[string][]WSMessage{}
	for range 4 {
		m, err := readMsg(t, ws, 5*time.Second)
		if err != nil {
			t.Fatalf("read: %v (seen %v)", err, seen)
		}
		seen[m.Topic] = append(seen[m.Topic], m)
	}
	var sm TaskStatusMessage
	if len(seen[TopicTasks]) != 1 || json.Unmarshal(seen[TopicTasks][0].Data, &sm) != nil || sm.TaskID != "tsk_1" || sm.Status != core.TaskRunning || sm.Title != "Cart bug" {
		t.Errorf("tasks topic = %v", seen[TopicTasks])
	}
	var bm BoardChangedMessage
	if len(seen[TopicBoard]) != 1 || json.Unmarshal(seen[TopicBoard][0].Data, &bm) != nil || bm.TaskID != "tsk_1" {
		t.Errorf("board topic = %v", seen[TopicBoard])
	}
	ev := seen[TopicTaskPrefix+"tsk_1"]
	if len(ev) != 2 || ev[0].Seq != 7 || ev[1].Seq != 8 {
		t.Errorf("task topic = %v, want the stored sequence numbers 7 and 8", ev)
	}
}

func TestHubAdapterNeverBlocks(t *testing.T) {
	e := newEnv(t)
	h := &HubAdapter{}
	h.Bind(e.srv)
	ws := e.dial(t)
	subscribe(t, ws, TopicTasks)
	time.Sleep(100 * time.Millisecond)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 20000 { // the client never reads
			h.Status(tasks.StatusMessage{TaskID: "tsk_1", Title: strings.Repeat("x", 1024), Status: core.TaskRunning})
			h.Event("tsk_1", int64(i), agents.Event{})
		}
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the adapter blocked on a slow client")
	}
}

func FuzzParseCreate(f *testing.F) {
	f.Add([]byte(`{"projectId":"prj_1","repoIds":["repo_1"],"prompt":"hi","mode":"ask","agent":"mock"}`))
	f.Add([]byte(`{"projectId":"prj_1","repoIds":[],"prompt":"hi","mode":"project","agent":"mock","scope":"project"}`))
	f.Add([]byte(`{"prompt":"\u0000"}`))
	f.Add([]byte(`[]`))
	f.Fuzz(func(t *testing.T, body []byte) {
		req, err := parseCreate(body)
		if err != nil {
			var ue *coretasks.UserError
			if !errors.As(err, &ue) || ue.Message == "" {
				t.Fatalf("a rejection must be a UserError with a message, got %T %v", err, err)
			}
			return
		}
		// Accepted implies the core accepts it too, and the request is what the
		// core parsed.
		pm, err := coretasks.ParseMessage(coretasks.RawMessage{
			Prompt: req.Prompt, Mode: req.Mode, Scope: req.Scope, Agent: req.Agent, Model: req.Model, Repos: req.Repos, Title: req.Title,
		})
		if err != nil {
			t.Fatalf("accepted by parseCreate but rejected by ParseMessage: %v (%+v)", err, req)
		}
		if string(pm.Mode) != req.Mode || string(pm.Scope) != req.Scope || pm.Agent != req.Agent {
			t.Fatalf("request %+v differs from the parsed message %+v", req, pm)
		}
		if req.ProjectID.Validate() != nil {
			t.Fatalf("accepted an invalid project %q", req.ProjectID)
		}
	})
}
