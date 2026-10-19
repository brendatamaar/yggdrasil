package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/repos"
)

// fakeTracking returns canned answers and records what the handlers pass.
type fakeTracking struct {
	out  corerepos.Outcome
	err  error
	path string
	name string
	pick []corerepos.Pick
	id   string
}

func (f *fakeTracking) BrowseDirs(_ context.Context, path string) (corerepos.DirListing, error) {
	f.path = path
	return corerepos.DirListing{Path: path, Entries: []corerepos.DirEntry{{Name: "shop", Path: path + "/shop", IsRepo: true}}}, f.err
}

func (f *fakeTracking) Preview(_ context.Context, path string) (corerepos.Preview, error) {
	f.path = path
	known := core.RepoID("rep_1")
	existing := core.ProjectID("prj_1")
	return corerepos.Preview{
		Path: corerepos.CleanPath(path), Truncated: true, Skipped: 2, Existing: &existing,
		Candidates: []corerepos.Candidate{{RelPath: "api", Name: "api", Known: &known, Active: true}},
	}, f.err
}

func (f *fakeTracking) Projects(context.Context) ([]corerepos.Project, error) { return nil, f.err }

func (f *fakeTracking) Track(_ context.Context, path, name string, picks []corerepos.Pick, _ corerepos.Actor) (corerepos.Outcome, error) {
	f.path, f.name, f.pick = path, name, picks
	return f.out, f.err
}

func (f *fakeTracking) Rescan(_ context.Context, project core.ProjectID) (corerepos.RescanReport, error) {
	f.id = string(project)
	return corerepos.RescanReport{New: []corerepos.Candidate{{RelPath: "web", Name: "web"}}, Missing: []core.RepoID{"rep_9"}}, f.err
}

func (f *fakeTracking) TrackRepos(_ context.Context, project core.ProjectID, picks []corerepos.Pick, _ corerepos.Actor) (corerepos.Outcome, error) {
	f.id, f.pick = string(project), picks
	return f.out, f.err
}

func (f *fakeTracking) UntrackRepo(_ context.Context, repo core.RepoID, _ corerepos.Actor) (corerepos.Outcome, error) {
	f.id = string(repo)
	return f.out, f.err
}

func (f *fakeTracking) UntrackProject(_ context.Context, project core.ProjectID, _ corerepos.Actor) (corerepos.Outcome, error) {
	f.id = string(project)
	return f.out, f.err
}

func (f *fakeTracking) RenameProject(_ context.Context, project core.ProjectID, name string, _ corerepos.Actor) (corerepos.Outcome, error) {
	f.id, f.name = string(project), name
	return f.out, f.err
}

func trackingEnv(t *testing.T) (*env, *fakeTracking) {
	t.Helper()
	f := &fakeTracking{}
	return newEnv(t, "plain", 0, func(d *Deps) { d.Tracking = f }), f
}

func errCode(t *testing.T, data []byte) ErrorCode {
	t.Helper()
	var b ErrorBody
	if err := json.Unmarshal(data, &b); err != nil || b.Message == "" {
		t.Fatalf("not an error body: %s (%v)", data, err)
	}
	return b.Code
}

func TestTrackingOutcomeToStatus(t *testing.T) {
	cases := []struct {
		name   string
		out    corerepos.Outcome
		status int
		code   ErrorCode
	}{
		{"new project", corerepos.Tracked{ProjectID: "prj_1", Added: []core.RepoID{"rep_1"}, NewProject: true}, http.StatusCreated, ""},
		{"more repos", corerepos.Tracked{ProjectID: "prj_1", Restored: []core.RepoID{"rep_1"}}, http.StatusOK, ""},
		{"already tracked", corerepos.AlreadyTracked{ProjectID: "prj_1"}, http.StatusOK, ""},
		{"name taken", corerepos.NameTaken{Name: "shop"}, http.StatusConflict, ErrConflict},
		{"overlaps", corerepos.Overlaps{Other: "prj_2"}, http.StatusConflict, ErrConflict},
		{"busy", corerepos.RepoBusy{RepoID: "rep_1", Reason: corerepos.BusyBuildRunning}, http.StatusConflict, ErrConflict},
		{"not found", corerepos.NotFound{}, http.StatusNotFound, ErrNotFound},
		{"nothing picked", corerepos.NothingPicked{}, http.StatusBadRequest, ErrBadRequest},
		{"invalid", corerepos.InvalidInput{Reason: "x"}, http.StatusBadRequest, ErrBadRequest},
	}
	for _, c := range cases {
		e, f := trackingEnv(t)
		f.out = c.out
		resp, data := e.do(t, http.MethodPost, "/api/projects", TrackProjectRequest{Path: "/w/shop", Name: "shop", Picks: []PickDTO{{RelPath: "api"}}})
		if resp.StatusCode != c.status {
			t.Errorf("%s: status %d, want %d (%s)", c.name, resp.StatusCode, c.status, data)
			continue
		}
		if c.code != "" {
			if got := errCode(t, data); got != c.code {
				t.Errorf("%s: code %q, want %q", c.name, got, c.code)
			}
			continue
		}
		var body TrackResponse
		if err := json.Unmarshal(data, &body); err != nil || body.ProjectID != "prj_1" || body.Added == nil || body.Restored == nil {
			t.Errorf("%s: body %s", c.name, data)
		}
		if _, isAlready := c.out.(corerepos.AlreadyTracked); isAlready != body.AlreadyTracked {
			t.Errorf("%s: alreadyTracked = %v", c.name, body.AlreadyTracked)
		}
	}
}

func TestTrackingPassesTheRequestThrough(t *testing.T) {
	e, f := trackingEnv(t)
	f.out = corerepos.Tracked{ProjectID: "prj_1"}
	e.do(t, http.MethodPost, "/api/projects", TrackProjectRequest{Path: "/w/shop", Name: "shop", Picks: []PickDTO{{RelPath: "api", Name: "a"}, {RelPath: "web"}}})
	if f.path != "/w/shop" || f.name != "shop" || len(f.pick) != 2 || f.pick[0].RelPath != "api" || f.pick[0].Name != "a" || f.pick[0].NewRepoID != "" {
		t.Errorf("service got %q %q %+v", f.path, f.name, f.pick)
	}
	e.do(t, http.MethodPost, "/api/projects/prj_7/repos", TrackReposRequest{Picks: []PickDTO{{RelPath: "web"}}})
	if f.id != "prj_7" || len(f.pick) != 1 {
		t.Errorf("TrackRepos got %q %+v", f.id, f.pick)
	}
}

func TestTrackingErrorsUseTheirClass(t *testing.T) {
	cases := map[error]int{
		apperr.New(apperr.Invalid, "Choose a folder.", nil):         http.StatusBadRequest,
		apperr.New(apperr.NotFound, "Gone.", nil):                   http.StatusNotFound,
		apperr.New(apperr.Unavailable, "Try again.", nil):           http.StatusServiceUnavailable,
		apperr.New(apperr.Conflict, "Changed.", nil):                http.StatusConflict,
		errors.New("disk exploded: C:\\secret\\place may be shown"): http.StatusInternalServerError,
	}
	for err, want := range cases {
		e, f := trackingEnv(t)
		f.err = err
		resp, data := e.do(t, http.MethodPost, "/api/projects/preview", PreviewRequest{Path: "/w"})
		if resp.StatusCode != want {
			t.Errorf("%v: status %d, want %d", err, resp.StatusCode, want)
		}
		if want == http.StatusInternalServerError && json.Valid(data) && containsAny(string(data), "secret", "exploded") {
			t.Errorf("an internal error leaked to the client: %s", data)
		}
	}
}

func containsAny(s string, subs ...string) bool {
	for _, x := range subs {
		for i := 0; i+len(x) <= len(s); i++ {
			if s[i:i+len(x)] == x {
				return true
			}
		}
	}
	return false
}

func TestTrackingEndpoints(t *testing.T) {
	e, f := trackingEnv(t)

	resp, data := e.do(t, http.MethodGet, "/api/fs/dirs?path=%2Fw", nil)
	var dirs DirListingResponse
	if resp.StatusCode != 200 || json.Unmarshal(data, &dirs) != nil || f.path != "/w" || len(dirs.Entries) != 1 || !dirs.Entries[0].IsRepo {
		t.Errorf("browse: %d %s (path %q)", resp.StatusCode, data, f.path)
	}
	if resp, _ := e.do(t, http.MethodGet, "/api/fs/dirs", nil); resp.StatusCode != 200 || f.path != "" {
		t.Errorf("browse roots: %d, path %q", resp.StatusCode, f.path)
	}

	resp, data = e.do(t, http.MethodPost, "/api/projects/preview", PreviewRequest{Path: "/w/shop"})
	var pv PreviewResponse
	if resp.StatusCode != 200 || json.Unmarshal(data, &pv) != nil || !pv.Truncated || pv.Skipped != 2 ||
		pv.ExistingProjectID == nil || len(pv.Candidates) != 1 || pv.Candidates[0].KnownRepoID == nil || !pv.Candidates[0].Active {
		t.Errorf("preview: %d %s", resp.StatusCode, data)
	}

	resp, data = e.do(t, http.MethodPost, "/api/projects/prj_1/rescan", nil)
	var rs RescanResponse
	if resp.StatusCode != 200 || json.Unmarshal(data, &rs) != nil || len(rs.New) != 1 || len(rs.Missing) != 1 || f.id != "prj_1" {
		t.Errorf("rescan: %d %s", resp.StatusCode, data)
	}

	f.out = corerepos.UntrackDone{Project: "prj_1", Repos: []core.RepoID{"rep_1"}}
	resp, data = e.do(t, http.MethodPost, "/api/repos/rep_1/untrack", nil)
	var un UntrackResponse
	if resp.StatusCode != 200 || json.Unmarshal(data, &un) != nil || len(un.Repos) != 1 || f.id != "rep_1" {
		t.Errorf("untrack repo: %d %s", resp.StatusCode, data)
	}
	f.out = corerepos.UntrackDone{Project: "prj_1"}
	resp, data = e.do(t, http.MethodPost, "/api/projects/prj_1/untrack", nil)
	if resp.StatusCode != 200 || json.Unmarshal(data, &un) != nil || un.Repos == nil {
		t.Errorf("untrack project: %d %s", resp.StatusCode, data)
	}
	f.out = corerepos.RepoBusy{RepoID: "rep_1", Reason: corerepos.BusyBuildRunning}
	if resp, data := e.do(t, http.MethodPost, "/api/projects/prj_1/untrack", nil); resp.StatusCode != http.StatusConflict || errCode(t, data) != ErrConflict {
		t.Errorf("untrack of a busy project: %d %s", resp.StatusCode, data)
	}

	f.out = corerepos.Renamed{ProjectID: "prj_1"}
	if resp, _ := e.do(t, http.MethodPatch, "/api/projects/prj_1", RenameProjectRequest{Name: "new"}); resp.StatusCode != http.StatusNoContent || f.name != "new" {
		t.Errorf("rename: %d", resp.StatusCode)
	}
	f.out = corerepos.NameTaken{Name: "new"}
	if resp, _ := e.do(t, http.MethodPatch, "/api/projects/prj_1", RenameProjectRequest{Name: "new"}); resp.StatusCode != http.StatusConflict {
		t.Errorf("rename to a taken name: %d", resp.StatusCode)
	}
}

func TestTrackingRejectsBadRequests(t *testing.T) {
	e, _ := trackingEnv(t)
	for name, c := range map[string]struct {
		method, path string
		body         any
	}{
		"bad project id": {http.MethodPost, "/api/projects/bad%20id/untrack", nil},
		"bad repo id":    {http.MethodPost, "/api/repos/bad%20id/untrack", nil},
		"unknown field":  {http.MethodPost, "/api/projects", map[string]any{"path": "/w", "evil": 1}},
		"bad rename id":  {http.MethodPatch, "/api/projects/bad%20id", RenameProjectRequest{Name: "x"}},
	} {
		if resp, data := e.do(t, c.method, c.path, c.body); resp.StatusCode != http.StatusBadRequest || errCode(t, data) != ErrBadRequest {
			t.Errorf("%s: %d %s", name, resp.StatusCode, data)
		}
	}
}

func TestTrackingPublishesOnTheReposTopic(t *testing.T) {
	e, f := trackingEnv(t)
	ws := e.dial(t)
	subscribe(t, ws, TopicRepos)
	time.Sleep(150 * time.Millisecond)

	// Handlers publish nothing themselves: the tracking service does, through
	// Server.PublishReposChanged (covered in internal/repos).
	f.out = corerepos.Tracked{ProjectID: "prj_1", Added: []core.RepoID{"rep_1"}, NewProject: true}
	e.do(t, http.MethodPost, "/api/projects", TrackProjectRequest{Path: "/w", Name: "n", Picks: []PickDTO{{RelPath: "a"}}})
	e.srv.PublishReposChanged()
	m, err := readMsg(t, ws, 5*time.Second)
	if err != nil || m.Type != MsgEvent || m.Topic != TopicRepos || m.Seq != 1 {
		t.Fatalf("message = %+v, %v; want the first event on the repos topic", m, err)
	}
	if m, err := readMsg(t, ws, 300*time.Millisecond); err == nil {
		t.Fatalf("a handler published too: %+v", m)
	}
}

// Untracking a repo while an agent task runs in it is refused (invariant 6:
// the task's worktree must not lose its tracked repo underneath it).
func TestUntrackRefusedWhileATaskRuns(t *testing.T) {
	e := newEnv(t, "subagents", 300*time.Millisecond, func(d *Deps) {
		svc, err := repos.NewTrackingService(repos.Deps{Store: d.Store, Redactor: d.Redactor})
		if err != nil {
			t.Fatal(err)
		}
		d.Tracking = svc
	})
	task := e.create(t)
	e.waitStatus(t, task.ID, core.TaskRunning)

	resp, data := e.do(t, http.MethodPost, "/api/repos/"+string(e.repo.ID)+"/untrack", nil)
	if resp.StatusCode != http.StatusConflict || errCode(t, data) != ErrConflict {
		t.Fatalf("untrack repo during a running task = %d %s, want 409", resp.StatusCode, data)
	}
	if !containsAny(string(data), "task") {
		t.Errorf("the message doesn't name the task as the reason: %s", data)
	}
	resp, data = e.do(t, http.MethodPost, "/api/projects/"+string(e.project.ID)+"/untrack", nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("untrack project during a running task = %d %s, want 409", resp.StatusCode, data)
	}

	// Once the task is over the repo can go.
	e.do(t, http.MethodPost, "/api/tasks/"+string(task.ID)+"/cancel", nil)
	e.waitStatus(t, task.ID, core.TaskCancelled)
	resp, data = e.do(t, http.MethodPost, "/api/repos/"+string(e.repo.ID)+"/untrack", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("untrack after the task ended = %d %s", resp.StatusCode, data)
	}
}

// Untracked repos and projects are invisible to tasks (invariant 6).
func TestTasksRefuseUntrackedRepoAndProject(t *testing.T) {
	e := newEnv(t, "plain", 0, func(d *Deps) {
		svc, err := repos.NewTrackingService(repos.Deps{Store: d.Store, Redactor: d.Redactor})
		if err != nil {
			t.Fatal(err)
		}
		d.Tracking = svc
	})
	if resp, data := e.do(t, http.MethodPost, "/api/repos/"+string(e.repo.ID)+"/untrack", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("untrack repo = %d %s", resp.StatusCode, data)
	}
	if resp, data := e.do(t, http.MethodPost, "/api/tasks", e.createReq()); resp.StatusCode != http.StatusNotFound {
		t.Errorf("task in an untracked repo = %d %s, want 404", resp.StatusCode, data)
	}
	if resp, data := e.do(t, http.MethodPost, "/api/projects/"+string(e.project.ID)+"/untrack", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("untrack project = %d %s", resp.StatusCode, data)
	}
	if resp, data := e.do(t, http.MethodPost, "/api/tasks", e.createReq()); resp.StatusCode != http.StatusNotFound {
		t.Errorf("task in an untracked project = %d %s, want 404", resp.StatusCode, data)
	}
	if resp, data := e.do(t, http.MethodGet, "/api/tasks?project="+string(e.project.ID), nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("task list of an untracked project = %d %s, want 404", resp.StatusCode, data)
	}
}
