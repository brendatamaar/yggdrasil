package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
	"github.com/brendatamaar/yggdrasil/internal/devservers"
	"github.com/brendatamaar/yggdrasil/internal/platform/platformtest"
)

// A freshly tracked repo with a dev command appears as a stopped server (one
// per target), the client starts it with the request body, and stop and log
// then resolve by ID alone.
func TestPhase5FreshRepoOffersAStoppedDevServerThatStarts(t *testing.T) {
	r := newPhase5Rig(t)
	r.fake.Push(platformtest.Script{Stdout: "ready on http://localhost:5173\n", Block: true})

	var list api.DevServersResponse
	if code := getJSON(t, r.base+"/api/devservers", &list); code != 200 {
		t.Fatalf("list = %d", code)
	}
	want := map[core.DevServerID]api.DevServerDTO{}
	for _, d := range list.Servers {
		want[d.ID] = d
	}
	repoKey := devservers.Target{Repo: r.repo}.Key("dev")
	wtKey := devservers.Target{Repo: r.repo, Worktree: r.wt}.Key("dev")
	for _, k := range []struct {
		id         string
		repo, wt   string
		wantTarget string
	}{{devservers.IDOf(repoKey), string(r.repo), "", "repo"}, {devservers.IDOf(wtKey), string(r.repo), string(r.wt), "worktree"}} {
		d, ok := want[core.DevServerID(k.id)]
		if !ok || d.Status != api.DevServerStopped || string(d.RepoID) != k.repo || string(d.WorktreeID) != k.wt || d.Command != "dev" || d.RepoName == "" {
			t.Fatalf("no stopped placeholder for the %s: %+v", k.wantTarget, list.Servers)
		}
	}

	id := devservers.IDOf(repoKey)
	body := api.DevServerActionRequest{RepoID: r.repo, Command: "dev"}
	if code, data := apiDo(t, http.MethodPost, r.base+"/api/devservers/"+id+"/start", body, appClient); code != 200 {
		t.Fatalf("first start = %d %s", code, data)
	}
	for deadline := time.Now().Add(5 * time.Second); len(r.fake.Started()) == 0 && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	// The log, by ID alone, carries the numbers the live lines carry.
	var logs api.DevServerLogResponse
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if code := getJSON(t, r.base+"/api/devservers/"+id+"/log", &logs); code == 200 && hasLine(logs, "ready on http://localhost:5173") {
			break
		}
	}
	if !hasLine(logs, "ready on http://localhost:5173") || logs.Lines[0].Seq != 1 {
		t.Fatalf("log = %+v", logs)
	}
	// Stop by ID alone (no body).
	if code, data := apiDo(t, http.MethodPost, r.base+"/api/devservers/"+id+"/stop", nil, appClient); code != 200 {
		t.Fatalf("stop without a body = %d %s", code, data)
	}
	// A placeholder that was never started has no log and cannot be stopped by ID.
	other := devservers.IDOf(wtKey)
	if code, _ := apiDo(t, http.MethodPost, r.base+"/api/devservers/"+other+"/stop", nil, appClient); code != 404 {
		t.Fatalf("stop of a never started server = %d, want 404", code)
	}
}

func hasLine(l api.DevServerLogResponse, text string) bool {
	for _, m := range l.Lines {
		if m.Line == text {
			return true
		}
	}
	return false
}

// A tracked folder that is a symlink is not a place to start a program.
func TestPhase5LinkedFolderIsRefusedForDevServersAndTerminals(t *testing.T) {
	r := newPhase5Rig(t)
	ctx := context.Background()
	real := filepath.Join(t.TempDir(), "real")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "package.json"), []byte(`{"scripts":{"dev":"vite"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}
	repos, err := r.a.store.ListProjects(ctx)
	if err != nil || len(repos) == 0 {
		t.Fatal(err)
	}
	task := dbtest.Task(t, r.a.store, core.Task{ProjectID: repos[0].ID, Scope: core.ScopeRepo, RepoIDs: []core.RepoID{r.repo}, Mode: core.ModeCode, Status: core.TaskReview})
	const wt = core.WorktreeID("wt_link")
	if err := r.a.store.Commit(ctx, func(tx *db.Tx) error {
		_, err := tx.RecordWorktree(ctx, core.Worktree{
			ID: wt, RepoID: r.repo, TaskID: task.ID, Name: "link", Path: link, Branch: "ygg/link", CreatedAt: time.Now().UTC(),
		}, "0000000000000000000000000000000000000000", core.AuditEntry{ID: core.AuditID(db.NewID("aud")), At: time.Now().UTC(), Actor: "test", Action: "worktree.create", Subject: "worktree:wt_link", Detail: []byte(`{}`)})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	key := devservers.Target{Repo: r.repo, Worktree: wt}.Key("dev")
	body := api.DevServerActionRequest{RepoID: r.repo, WorktreeID: wt, Command: "dev"}
	code, data := apiDo(t, http.MethodPost, r.base+"/api/devservers/"+devservers.IDOf(key)+"/start", body, appClient)
	if code != 400 || !strings.Contains(string(data), "link") {
		t.Fatalf("start in a linked folder = %d %s, want 400 naming the link", code, data)
	}
	if len(r.fake.Started()) != 0 {
		t.Fatal("a process started in a linked folder")
	}
	if code, _ = apiDo(t, http.MethodPost, r.base+"/api/terminal", api.TerminalTokenRequest{WorktreeID: wt}, appClient); code != 404 {
		t.Fatalf("terminal in a linked folder = %d, want 404", code)
	}
}

// Compose settings (WE-03) are stored with the request, not refused or
// dropped; a file that is not a compose file, or compose on without a file, is
// a 400 that stores nothing, not even the profile of the same request.
func TestPhase5ComposeSettingsAreStored(t *testing.T) {
	r := newPhase5Rig(t)
	url := r.base + "/api/repos/" + string(r.repo) + "/env"
	for name, put := range map[string]api.RepoEnvDTO{
		"enabled only": {RepoID: r.repo, ProfilePaths: []string{".env"}, ComposeEnabled: true},
		"not yaml":     {RepoID: r.repo, ProfilePaths: []string{".env"}, ComposeFile: "docker-compose.txt"},
		"outside":      {RepoID: r.repo, ProfilePaths: []string{".env"}, ComposeFile: "../docker-compose.yml"},
	} {
		code, data := apiDo(t, http.MethodPut, url, put, appClient)
		if code != 400 {
			t.Errorf("%s: PUT = %d %s, want 400", name, code, data)
		}
		var e api.RepoEnvDTO
		if getJSON(t, url, &e); len(e.ProfilePaths) != 0 || e.ComposeEnabled {
			t.Errorf("%s: the request stored %+v", name, e)
		}
	}
	want := api.RepoEnvDTO{RepoID: r.repo, ProfilePaths: []string{".env"}, ComposeEnabled: true, ComposeFile: "deploy/docker-compose.yml"}
	if code, data := apiDo(t, http.MethodPut, url, want, appClient); code != 200 {
		t.Fatalf("PUT = %d %s", code, data)
	}
	var got api.RepoEnvDTO
	if getJSON(t, url, &got); !got.ComposeEnabled || got.ComposeFile != "deploy/docker-compose.yml" || len(got.ProfilePaths) != 1 {
		t.Fatalf("stored %+v", got)
	}
}
