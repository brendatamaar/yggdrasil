package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

var _ BuildService = (*builds.Service)(nil)

// fakeBuilds returns canned answers and records what the handlers pass.
type fakeBuilds struct {
	err    error
	state  corebuilds.State
	states []corebuilds.State
	log    []byte
	next   int64

	raw    string
	actor  corebuilds.Actor
	filter db.BuildFilter
	step   int
	after  int64
	id     string
	preset corebuilds.Preset
}

func (f *fakeBuilds) EnqueueRequest(_ context.Context, raw []byte, a corebuilds.Actor) (core.BuildID, error) {
	f.raw, f.actor = string(raw), a
	return "bld_1", f.err
}

func (f *fakeBuilds) Cancel(_ context.Context, id core.BuildID, a corebuilds.Actor) error {
	f.id, f.actor = string(id), a
	return f.err
}

func (f *fakeBuilds) Get(_ context.Context, id core.BuildID) (corebuilds.State, error) {
	f.id = string(id)
	return f.state, f.err
}

func (f *fakeBuilds) List(_ context.Context, fl db.BuildFilter) ([]corebuilds.State, error) {
	f.filter = fl
	return f.states, f.err
}

func (f *fakeBuilds) StepLog(_ context.Context, id core.BuildID, step int, after int64) ([]byte, int64, error) {
	f.id, f.step, f.after = string(id), step, after
	return f.log, f.next, f.err
}

func (f *fakeBuilds) ListPresets(_ context.Context, p core.ProjectID) ([]corebuilds.Preset, error) {
	f.id = string(p)
	return []corebuilds.Preset{f.preset}, f.err
}

func (f *fakeBuilds) CreatePreset(_ context.Context, raw []byte, a corebuilds.Actor) (corebuilds.Preset, error) {
	f.raw, f.actor = string(raw), a
	return f.preset, f.err
}

func (f *fakeBuilds) UpdatePreset(_ context.Context, id corebuilds.PresetID, raw []byte, a corebuilds.Actor) (corebuilds.Preset, error) {
	f.id, f.raw, f.actor = string(id), string(raw), a
	return f.preset, f.err
}

func (f *fakeBuilds) RunPreset(_ context.Context, id corebuilds.PresetID, a corebuilds.Actor) (core.BuildID, error) {
	f.id, f.actor = string(id), a
	return "bld_2", f.err
}

func buildsEnv(t *testing.T) (*env, *fakeBuilds) {
	t.Helper()
	f := &fakeBuilds{preset: corebuilds.Preset{ID: "pre_1", ProjectID: "prj_1", Name: "verify",
		Steps: []corebuilds.StepSpec{{RepoID: "rep_a", Name: "t", Argv: []string{"npm", "test"}}}}}
	return newEnv(t, "plain", 0, func(d *Deps) { d.Builds = f }), f
}

var bt0 = time.Date(2025, 3, 1, 10, 0, 0, 0, time.UTC)

func testSpec() corebuilds.Spec {
	return corebuilds.Spec{Target: corebuilds.InWorktree{WorktreeID: "wt_1"}, PresetID: "pre_1", Steps: []corebuilds.StepSpec{
		{RepoID: "rep_a", Name: "install", Argv: []string{"npm", "ci"}},
		{RepoID: "rep_a", Name: "test", Argv: []string{"npm", "test"}},
		{RepoID: "rep_a", Name: "lint", Argv: []string{"npm", "run", "lint"}},
	}}
}

func TestEnqueueBuildPassesTheBodyAndAnswers202(t *testing.T) {
	e, f := buildsEnv(t)
	body := EnqueueBuildRequest{Target: "main", Steps: []StepSpecDTO{{RepoID: "rep_a", Name: "t", Argv: []string{"npm", "test"}}}}
	resp, data := e.do(t, http.MethodPost, "/api/builds", body)
	var out EnqueueBuildResponse
	if resp.StatusCode != http.StatusAccepted || json.Unmarshal(data, &out) != nil || out.BuildID != "bld_1" {
		t.Fatalf("%d %s", resp.StatusCode, data)
	}
	if f.actor != "user" || !strings.Contains(f.raw, `"target":"main"`) || !strings.Contains(f.raw, `"argv":["npm","test"]`) {
		t.Errorf("service got %q as %q", f.raw, f.actor)
	}
}

func TestBuildErrorsUseTheirClass(t *testing.T) {
	cases := map[error]int{
		apperr.New(apperr.Invalid, "A step has no command.", nil): http.StatusBadRequest,
		apperr.New(apperr.NotFound, "Gone.", nil):                 http.StatusNotFound,
		apperr.New(apperr.Conflict, "Taken.", nil):                http.StatusConflict,
		apperr.New(apperr.Unavailable, "Try again.", nil):         http.StatusServiceUnavailable,
		errors.New("disk exploded at C:\\secret"):                 http.StatusInternalServerError,
	}
	for err, want := range cases {
		e, f := buildsEnv(t)
		f.err = err
		for _, c := range []struct{ method, path string }{
			{http.MethodPost, "/api/builds"}, {http.MethodGet, "/api/builds"}, {http.MethodGet, "/api/builds/bld_1"},
			{http.MethodPost, "/api/builds/bld_1/cancel"}, {http.MethodGet, "/api/builds/bld_1/steps/0/log"},
			{http.MethodGet, "/api/build-presets?project=prj_1"}, {http.MethodPost, "/api/build-presets"},
			{http.MethodPatch, "/api/build-presets/pre_1"}, {http.MethodPost, "/api/build-presets/pre_1/run"},
		} {
			var body any
			if c.method != http.MethodGet {
				body = map[string]any{}
			}
			resp, data := e.do(t, c.method, c.path, body)
			if resp.StatusCode != want {
				t.Errorf("%v %s %s = %d, want %d (%s)", err, c.method, c.path, resp.StatusCode, want, data)
			}
			if strings.Contains(string(data), "secret") {
				t.Errorf("%s leaks internal error text: %s", c.path, data)
			}
		}
	}
}

func TestGetBuildDTOForEachState(t *testing.T) {
	spec := testSpec()
	t1, t2, t3 := bt0.Add(time.Second), bt0.Add(2*time.Second), bt0.Add(3*time.Second)
	running := corebuilds.Running{ID: "bld_1", Spec: spec, QueuedAt: bt0, StartedAt: t1, Actor: "user", Current: 1,
		Steps: []corebuilds.StepState{
			corebuilds.StepDone{Attempt: 1, StartedAt: t1, FinishedAt: t2, Outcome: corebuilds.Succeeded{}},
			corebuilds.StepRunning{Attempt: 1, StartedAt: t2}, corebuilds.StepPending{}}}
	cases := []struct {
		name  string
		state corebuilds.State
		check func(t *testing.T, d BuildDTO)
	}{
		{"queued", corebuilds.Queued{ID: "bld_1", Spec: spec, QueuedAt: bt0, Actor: "agent:x"}, func(t *testing.T, d BuildDTO) {
			if d.Status != core.BuildQueued || d.Actor != "agent:x" || d.StartedAt != nil || d.Steps[0].Status != core.StepQueued ||
				d.Target != BuildTargetWorktree || d.WorktreeID == nil || *d.WorktreeID != "wt_1" || d.PresetID != "pre_1" || d.Artifacts == nil {
				t.Errorf("%+v", d)
			}
		}},
		{"running", running, func(t *testing.T, d BuildDTO) {
			if d.Status != core.BuildRunning || d.Steps[0].Outcome != core.OutcomeSucceeded || d.Steps[1].Status != core.StepRunning ||
				d.Steps[2].Status != core.StepQueued || d.Steps[1].Attempt != 1 {
				t.Errorf("%+v", d)
			}
		}},
		{"cancelling", corebuilds.Cancelling{Running: running, CancelledAt: t3}, func(t *testing.T, d BuildDTO) {
			if d.Status != core.BuildRunning || d.CancelledAt == nil || !d.CancelledAt.Equal(t3) {
				t.Errorf("%+v", d)
			}
		}},
		{"failed", corebuilds.Finished{ID: "bld_1", Spec: spec, QueuedAt: bt0, Actor: "user", StartedAt: &t1, FinishedAt: t3,
			Result: corebuilds.ResultFailed{Step: 1},
			Steps: []corebuilds.StepState{
				corebuilds.StepDone{Attempt: 1, StartedAt: t1, FinishedAt: t2, Outcome: corebuilds.Succeeded{}},
				corebuilds.StepDone{Attempt: 1, StartedAt: t2, FinishedAt: t3, Outcome: corebuilds.Failed{ExitCode: 7}, LogTruncated: true},
				corebuilds.StepSkipped{}}}, func(t *testing.T, d BuildDTO) {
			s := d.Steps[1]
			if d.Status != core.BuildFailed || d.FinishedAt == nil || s.ExitCode == nil || *s.ExitCode != 7 || !s.LogTruncated ||
				s.Status != core.StepFailed || d.Steps[2].Status != core.StepSkipped || d.ArtifactsCollected {
				t.Errorf("%+v", d)
			}
		}},
		{"start failed", corebuilds.Finished{ID: "bld_1", Spec: spec, QueuedAt: bt0, Actor: "user", StartedAt: &t1, FinishedAt: t2,
			Result: corebuilds.ResultFailed{Step: 0},
			Steps: []corebuilds.StepState{
				corebuilds.StepDone{Attempt: 1, StartedAt: t1, FinishedAt: t2, Outcome: corebuilds.StartFailed{Reason: corebuilds.StartNotFound}},
				corebuilds.StepSkipped{}, corebuilds.StepSkipped{}}}, func(t *testing.T, d BuildDTO) {
			s := d.Steps[0]
			if s.StartFailure != "not_found" || !strings.Contains(s.StartMessage, "npm") || s.Outcome != core.OutcomeStartFailed || s.ExitCode != nil {
				t.Errorf("%+v", s)
			}
		}},
		{"succeeded with artifacts", corebuilds.Finished{ID: "bld_1", Spec: spec, QueuedAt: bt0, Actor: "user", StartedAt: &t1, FinishedAt: t3,
			Result: corebuilds.ResultSucceeded{}, Artifacts: []corebuilds.Artifact{{RepoID: "rep_a", Path: "dist/a.js", Size: 5}},
			Steps: []corebuilds.StepState{
				corebuilds.StepDone{Attempt: 1, StartedAt: t1, FinishedAt: t2, Outcome: corebuilds.Succeeded{}},
				corebuilds.StepDone{Attempt: 1, StartedAt: t1, FinishedAt: t2, Outcome: corebuilds.Succeeded{}},
				corebuilds.StepDone{Attempt: 1, StartedAt: t1, FinishedAt: t2, Outcome: corebuilds.Succeeded{}}}}, func(t *testing.T, d BuildDTO) {
			if !d.ArtifactsCollected || len(d.Artifacts) != 1 || d.Artifacts[0].Path != "dist/a.js" {
				t.Errorf("%+v", d)
			}
		}},
		{"interrupted", corebuilds.Finished{ID: "bld_1", Spec: spec, QueuedAt: bt0, Actor: "system", StartedAt: &t1, FinishedAt: t3,
			Result: corebuilds.ResultInterrupted{Step: 0},
			Steps: []corebuilds.StepState{corebuilds.StepDone{Attempt: 1, StartedAt: t1, FinishedAt: t3, Outcome: corebuilds.Interrupted{}},
				corebuilds.StepSkipped{}, corebuilds.StepSkipped{}}}, func(t *testing.T, d BuildDTO) {
			if d.Status != core.BuildInterrupted || d.Steps[0].Status != core.StepInterrupted {
				t.Errorf("%+v", d)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, f := buildsEnv(t)
			f.state = c.state
			resp, data := e.do(t, http.MethodGet, "/api/builds/bld_1", nil)
			var d BuildDTO
			if resp.StatusCode != http.StatusOK || json.Unmarshal(data, &d) != nil {
				t.Fatalf("%d %s", resp.StatusCode, data)
			}
			if d.ID != "bld_1" || len(d.Steps) != 3 {
				t.Fatalf("%+v", d)
			}
			c.check(t, d)
		})
	}
}

func TestListBuildsFiltersAndRejectsBadQueries(t *testing.T) {
	e, f := buildsEnv(t)
	f.states = []corebuilds.State{corebuilds.Queued{ID: "bld_1", Spec: testSpec(), QueuedAt: bt0, Actor: "user"}, corebuilds.None{}}
	resp, data := e.do(t, http.MethodGet, "/api/builds?project=prj_1&repo=rep_a&status=failed&limit=5", nil)
	var out BuildListResponse
	if resp.StatusCode != http.StatusOK || json.Unmarshal(data, &out) != nil || len(out.Builds) != 1 {
		t.Fatalf("%d %s", resp.StatusCode, data)
	}
	if f.filter != (db.BuildFilter{Project: "prj_1", Repo: "rep_a", Status: core.BuildFailed, Limit: 5}) {
		t.Errorf("filter %+v", f.filter)
	}
	for _, q := range []string{"status=bogus", "limit=0", "limit=1001", "limit=x", "project=a%20b", "repo=a%2Fb"} {
		resp, data := e.do(t, http.MethodGet, "/api/builds?"+q, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("?%s = %d %s", q, resp.StatusCode, data)
		}
	}
}

func TestCancelBuild(t *testing.T) {
	e, f := buildsEnv(t)
	f.state = corebuilds.Queued{ID: "bld_1", Spec: testSpec(), QueuedAt: bt0, Actor: "user"}
	resp, data := e.do(t, http.MethodPost, "/api/builds/bld_1/cancel", nil)
	if resp.StatusCode != http.StatusAccepted || f.id != "bld_1" || f.actor != "user" {
		t.Errorf("%d %s %+v", resp.StatusCode, data, f)
	}
	if resp, _ := e.do(t, http.MethodPost, "/api/builds/a%20b/cancel", nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad id = %d", resp.StatusCode)
	}
}

func TestStepLogEndpoint(t *testing.T) {
	e, f := buildsEnv(t)
	f.log, f.next = []byte("hello\n"), 106
	resp, data := e.do(t, http.MethodGet, "/api/builds/bld_1/steps/2/log?after=100", nil)
	var out StepLogResponse
	if resp.StatusCode != http.StatusOK || json.Unmarshal(data, &out) != nil || out.Data != "hello\n" || out.Next != 106 {
		t.Fatalf("%d %s", resp.StatusCode, data)
	}
	if f.step != 2 || f.after != 100 || f.id != "bld_1" {
		t.Errorf("service got %d %d %s", f.step, f.after, f.id)
	}
	for _, p := range []string{"after=-1", "after=abc", "after=1.5", "after=%2B1", "after=99999999999999999999"} {
		if resp, data := e.do(t, http.MethodGet, "/api/builds/bld_1/steps/0/log?"+p, nil); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("?%s = %d %s", p, resp.StatusCode, data)
		}
	}
	if resp, _ := e.do(t, http.MethodGet, "/api/builds/bld_1/steps/-1/log", nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("negative step = %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, http.MethodGet, "/api/builds/bld_1/steps/x/log", nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("non-numeric step = %d", resp.StatusCode)
	}
}

func TestStepLogNeverSplitsARune(t *testing.T) {
	e, f := buildsEnv(t)
	whole := "ab\u00e9" // e-acute is two bytes
	f.log, f.next = []byte(whole)[:3], 3
	_, data := e.do(t, http.MethodGet, "/api/builds/bld_1/steps/0/log", nil)
	var out StepLogResponse
	if err := json.Unmarshal(data, &out); err != nil || out.Data != "ab" || out.Next != 2 {
		t.Errorf("cut rune: %s", data)
	}
	f.log, f.next = []byte(whole), 4
	_, data = e.do(t, http.MethodGet, "/api/builds/bld_1/steps/0/log", nil)
	if err := json.Unmarshal(data, &out); err != nil || out.Data != whole || out.Next != 4 {
		t.Errorf("whole rune: %s", data)
	}
}

func TestPresetEndpoints(t *testing.T) {
	e, f := buildsEnv(t)
	resp, data := e.do(t, http.MethodGet, "/api/build-presets?project=prj_1", nil)
	var list BuildPresetListResponse
	if resp.StatusCode != http.StatusOK || json.Unmarshal(data, &list) != nil || len(list.Presets) != 1 || list.Presets[0].Name != "verify" || f.id != "prj_1" {
		t.Fatalf("list %d %s", resp.StatusCode, data)
	}
	if resp, _ := e.do(t, http.MethodGet, "/api/build-presets", nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing project = %d", resp.StatusCode)
	}
	resp, data = e.do(t, http.MethodPost, "/api/build-presets", SaveBuildPresetRequest{ProjectID: "prj_1", Name: "verify", Steps: []StepSpecDTO{{RepoID: "rep_a", Name: "t", Argv: []string{"npm", "test"}}}})
	var p BuildPresetDTO
	if resp.StatusCode != http.StatusCreated || json.Unmarshal(data, &p) != nil || p.ID != "pre_1" || !strings.Contains(f.raw, `"projectId":"prj_1"`) {
		t.Fatalf("create %d %s (%s)", resp.StatusCode, data, f.raw)
	}
	name := "renamed"
	resp, _ = e.do(t, http.MethodPatch, "/api/build-presets/pre_1", UpdateBuildPresetRequest{Name: &name})
	if resp.StatusCode != http.StatusOK || f.id != "pre_1" || !strings.Contains(f.raw, `"name":"renamed"`) || strings.Contains(f.raw, "steps") {
		t.Errorf("patch %d %q", resp.StatusCode, f.raw)
	}
	resp, data = e.do(t, http.MethodPost, "/api/build-presets/pre_1/run", nil)
	var run EnqueueBuildResponse
	if resp.StatusCode != http.StatusAccepted || json.Unmarshal(data, &run) != nil || run.BuildID != "bld_2" {
		t.Errorf("run %d %s", resp.StatusCode, data)
	}
	if resp, _ := e.do(t, http.MethodPatch, "/api/build-presets/a%20b", map[string]string{}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad preset id = %d", resp.StatusCode)
	}
}

func TestBuildPublishersReachWebSocketClients(t *testing.T) {
	e, _ := buildsEnv(t)
	ws := e.dial(t)
	subscribe(t, ws, "builds")
	subscribe(t, ws, "build:bld_1")
	time.Sleep(150 * time.Millisecond)
	e.srv.PublishBuildChanged("bld_1", core.BuildRunning)
	e.srv.PublishBuildLog("bld_1", 0, 1, "compiling")
	seen := map[string]int{}
	for i := 0; i < 3; i++ {
		m, err := readMsg(t, ws, 5*time.Second)
		if err != nil || m.Type != MsgEvent {
			t.Fatalf("%+v %v", m, err)
		}
		seen[m.Topic]++
		if m.Topic == "build:bld_1" && strings.Contains(string(m.Data), "compiling") {
			var l BuildLogMessage
			if json.Unmarshal(m.Data, &l) != nil || l.Line != "compiling" || l.Step != 0 || l.Attempt != 1 {
				t.Errorf("log message %s", m.Data)
			}
		}
	}
	if seen["builds"] != 1 || seen["build:bld_1"] != 2 {
		t.Errorf("messages by topic: %v", seen)
	}
}

func TestParseOffset(t *testing.T) {
	ok := map[string]int64{"": 0, "0": 0, "7": 7, "00012": 12, "9223372036854775807": 1<<63 - 1}
	for in, want := range ok {
		if got, err := parseOffset(in); err != nil || got != want {
			t.Errorf("parseOffset(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"-1", "+1", "1.5", "abc", " 1", "1 ", "0x10", "9223372036854775808", "1e3", "\u0661"} {
		if _, err := parseOffset(in); err == nil {
			t.Errorf("parseOffset(%q) accepted", in)
		}
	}
	for _, n := range []int64{0, 1, 999, 1 << 40, 1<<63 - 1} {
		if got, err := parseOffset(strconv.FormatInt(n, 10)); err != nil || got != n {
			t.Errorf("round trip %d: %d %v", n, got, err)
		}
	}
}

func FuzzParseOffset(f *testing.F) {
	for _, s := range []string{"", "0", "12", "-3", "abc", "9223372036854775808", "\x00"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		n, err := parseOffset(s)
		if err != nil {
			return
		}
		if n < 0 {
			t.Fatalf("accepted %q as negative %d", s, n)
		}
		if again, err := parseOffset(strconv.FormatInt(n, 10)); err != nil || again != n {
			t.Fatalf("round trip of %q: %d %v", s, again, err)
		}
	})
}
