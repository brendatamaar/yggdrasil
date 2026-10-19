package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/repos"
)

type fakeActions struct {
	err      error
	started  []repos.BatchRequest
	opened   []repos.OpenRequest
	revealed []repos.RelPath
	cancels  int
	editor   repos.Editor
	setTo    []repos.Editor
}

func (f *fakeActions) Start(_ context.Context, req repos.BatchRequest, _ corerepos.Actor) (corerepos.BatchID, error) {
	if f.err != nil {
		return "", f.err
	}
	f.started = append(f.started, req)
	return "gab_1", nil
}

func (f *fakeActions) view() repos.BatchView {
	at := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	return repos.BatchView{ID: "gab_1", Kind: corerepos.KindPull, Started: at, Finished: true, Items: []repos.ItemView{
		{Repo: "r1", State: "done", Outcome: "pulled", From: "aaa", To: "bbb", At: at},
		{Repo: "r2", State: "skipped", Reason: "build_running", Message: "Skipped: a build is running in this repo."},
	}}
}

func (f *fakeActions) Get(id corerepos.BatchID) (repos.BatchView, bool) {
	return f.view(), id == "gab_1"
}

func (f *fakeActions) Cancel(id corerepos.BatchID, _ corerepos.Actor) (repos.BatchView, error) {
	f.cancels++
	if id != "gab_1" {
		return repos.BatchView{}, apperr.New(apperr.NotFound, "gone", nil)
	}
	return f.view(), f.err
}

func (f *fakeActions) Open(_ context.Context, _ core.RepoID, req repos.OpenRequest) error {
	f.opened = append(f.opened, req)
	return f.err
}

func (f *fakeActions) Reveal(_ context.Context, _ core.RepoID, file repos.RelPath) error {
	f.revealed = append(f.revealed, file)
	return f.err
}

func (f *fakeActions) Editor(context.Context) (repos.EditorSetting, error) {
	return repos.EditorSetting{Chosen: f.editor, Detected: []repos.Editor{repos.EditorVSCode}}, f.err
}

func (f *fakeActions) SetEditor(_ context.Context, e repos.Editor, _ corerepos.Actor) error {
	f.setTo = append(f.setTo, e)
	f.editor = e
	return f.err
}

func newActionsEnv(t *testing.T, f *fakeActions) *env {
	t.Helper()
	return newEnv(t, "plain", 0, func(d *Deps) { d.GitActions = f })
}

func TestGitBatchStartGetCancel(t *testing.T) {
	f := &fakeActions{}
	e := newActionsEnv(t, f)
	resp, data := e.do(t, http.MethodPost, "/api/git/batches", StartGitBatchRequest{Kind: "switch", Repos: []string{"r1", "r2"}, Branch: "dev"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("start: %d %s", resp.StatusCode, data)
	}
	var sr StartGitBatchResponse
	if err := json.Unmarshal(data, &sr); err != nil || sr.BatchID != "gab_1" {
		t.Fatalf("start body: %s", data)
	}
	if len(f.started) != 1 || f.started[0].Kind != corerepos.KindSwitch || f.started[0].Branch != "dev" || len(f.started[0].Repos) != 2 {
		t.Fatalf("service got %+v", f.started)
	}
	resp, data = e.do(t, http.MethodGet, "/api/git/batches/gab_1", nil)
	var b GitBatchDTO
	if resp.StatusCode != http.StatusOK || json.Unmarshal(data, &b) != nil {
		t.Fatalf("get: %d %s", resp.StatusCode, data)
	}
	if !b.Finished || len(b.Items) != 2 || b.Items[0].Outcome != "pulled" || b.Items[0].At == nil || b.Items[1].Reason != "build_running" || b.Items[1].At != nil {
		t.Fatalf("batch = %+v", b)
	}
	if resp, _ = e.do(t, http.MethodGet, "/api/git/batches/gab_gone", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown batch: %d", resp.StatusCode)
	}
	if resp, _ = e.do(t, http.MethodPost, "/api/git/batches/gab_1/cancel", nil); resp.StatusCode != http.StatusOK || f.cancels != 1 {
		t.Errorf("cancel: %d", resp.StatusCode)
	}
	if resp, _ = e.do(t, http.MethodPost, "/api/git/batches/gab_x/cancel", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("cancel unknown: %d", resp.StatusCode)
	}
}

func TestGitBatchRejectsBadInputBeforeTheService(t *testing.T) {
	f := &fakeActions{}
	e := newActionsEnv(t, f)
	for name, body := range map[string]any{
		"force kind":     StartGitBatchRequest{Kind: "force-push", Repos: []string{"r1"}},
		"no repos":       StartGitBatchRequest{Kind: "fetch"},
		"branch on pull": StartGitBatchRequest{Kind: "pull", Repos: []string{"r1"}, Branch: "main"},
		"option branch":  StartGitBatchRequest{Kind: "switch", Repos: []string{"r1"}, Branch: "--force"},
		"two checkouts":  StartGitBatchRequest{Kind: "checkout", Repos: []string{"r1", "r2"}, Branch: "x"},
		"duplicate":      StartGitBatchRequest{Kind: "fetch", Repos: []string{"r1", "r1"}},
		"unknown field":  map[string]any{"kind": "fetch", "repos": []string{"r1"}, "force": true},
	} {
		if resp, data := e.do(t, http.MethodPost, "/api/git/batches", body); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, resp.StatusCode, data)
		}
	}
	if len(f.started) != 0 {
		t.Fatalf("service started %d batches for bad input", len(f.started))
	}
}

func TestGitBatchMapsServiceErrors(t *testing.T) {
	for cls, want := range map[apperr.Class]int{
		apperr.NotFound: http.StatusNotFound, apperr.Unavailable: http.StatusServiceUnavailable,
	} {
		e := newActionsEnv(t, &fakeActions{err: apperr.New(cls, "msg", nil)})
		resp, data := e.do(t, http.MethodPost, "/api/git/batches", StartGitBatchRequest{Kind: "fetch", Repos: []string{"r1"}})
		if resp.StatusCode != want {
			t.Errorf("%v: %d %s, want %d", cls, resp.StatusCode, data, want)
		}
	}
}

func TestOpenAndReveal(t *testing.T) {
	f := &fakeActions{}
	e := newActionsEnv(t, f)
	if resp, data := e.do(t, http.MethodPost, "/api/repos/r1/open", OpenRequestBody{Editor: "cursor", File: `src\a.go`}); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("open: %d %s", resp.StatusCode, data)
	}
	if resp, data := e.do(t, http.MethodPost, "/api/repos/r1/open", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("open without a body: %d %s", resp.StatusCode, data)
	}
	if len(f.opened) != 2 || f.opened[0].Editor != repos.EditorCursor || f.opened[0].File != "src/a.go" || f.opened[1].Editor != "" {
		t.Fatalf("opened = %+v", f.opened)
	}
	if resp, _ := e.do(t, http.MethodPost, "/api/repos/r1/reveal", RevealRequestBody{File: "docs/x.md"}); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("reveal: %d", resp.StatusCode)
	}
	if len(f.revealed) != 1 || f.revealed[0] != "docs/x.md" {
		t.Fatalf("revealed = %+v", f.revealed)
	}
	for name, body := range map[string]any{
		"unknown editor": OpenRequestBody{Editor: "notepad"},
		"absolute file":  OpenRequestBody{File: "/etc/passwd"},
		"escaping file":  OpenRequestBody{File: "../../x"},
	} {
		if resp, data := e.do(t, http.MethodPost, "/api/repos/r1/open", body); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, resp.StatusCode, data)
		}
	}
	if resp, _ := e.do(t, http.MethodPost, "/api/repos/r1/reveal", RevealRequestBody{File: "../x"}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("reveal escape: %d", resp.StatusCode)
	}
	if len(f.opened) != 2 || len(f.revealed) != 1 {
		t.Fatalf("service called for bad input: %+v %+v", f.opened, f.revealed)
	}
	if resp, _ := e.do(t, http.MethodPost, "/api/repos/bad id/open", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("malformed repo ID: %d", resp.StatusCode)
	}
}

func TestEditorSetting(t *testing.T) {
	f := &fakeActions{}
	e := newActionsEnv(t, f)
	resp, data := e.do(t, http.MethodPut, "/api/settings/editor", SetEditorRequest{Editor: "idea"})
	var dto EditorSettingDTO
	if resp.StatusCode != http.StatusOK || json.Unmarshal(data, &dto) != nil || dto.Editor != "idea" || len(dto.Detected) != 1 {
		t.Fatalf("put: %d %s", resp.StatusCode, data)
	}
	resp, data = e.do(t, http.MethodGet, "/api/settings/editor", nil)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(data, &dto) != nil || dto.Editor != "idea" {
		t.Fatalf("get: %d %s", resp.StatusCode, data)
	}
	if resp, _ := e.do(t, http.MethodPut, "/api/settings/editor", SetEditorRequest{Editor: "emacs"}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown editor: %d", resp.StatusCode)
	}
	if len(f.setTo) != 1 {
		t.Fatalf("service saw %v", f.setTo)
	}
}

func TestGitActionsNotRoutedWithoutService(t *testing.T) {
	e := newEnv(t, "plain", 0)
	for _, p := range []string{"/api/git/batches/x", "/api/settings/editor"} {
		if resp, _ := e.do(t, http.MethodGet, p, nil); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d", p, resp.StatusCode)
		}
	}
}
