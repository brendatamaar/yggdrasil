package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/mainlock"
	"github.com/brendatamaar/yggdrasil/internal/search/searchtest"
)

// trackRepo tracks a fresh temp git repo and returns the project and repo IDs.
func trackRepo(t *testing.T, cfg config) (project, repo string) {
	t.Helper()
	env := searchtest.GitEnv(t)
	dir := searchtest.InitRepo(t, env, map[string]string{"README.md": "# shop-api\n"})
	var out bytes.Buffer
	if err := seed(context.Background(), &out, cfg, dir, ""); err != nil {
		t.Fatal(err)
	}
	ids := regexp.MustCompile(`(prj|rep)_[0-9a-f]+`).FindAllString(out.String(), -1)
	if len(ids) < 2 {
		t.Fatalf("seed output has no IDs: %s", out.String())
	}
	return ids[0], ids[1]
}

func createTask(t *testing.T, base, project, repo, mode string) (int, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"projectId": project, "repoIds": []string{repo}, "prompt": "Find the order routes.", "mode": mode, "agent": "mock",
	})
	resp, err := appClient.Post(base+"/api/tasks", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var created struct{ Task struct{ ID string } }
	_ = json.NewDecoder(resp.Body).Decode(&created)
	return resp.StatusCode, created.Task.ID
}

func TestWriteModesAreOffByDefault(t *testing.T) {
	cfg, err := loadConfig(func(string) string { return "" }, func() (string, error) { return t.TempDir(), nil })
	if err != nil || cfg.writeModes {
		t.Fatalf("default config = %+v, %v", cfg, err)
	}
	for in, want := range map[string]bool{"1": true, "true": true, "TRUE": true, "0": false, "false": false} {
		c, err := loadConfig(func(k string) string {
			if k == "YGG_WRITE_MODES" {
				return in
			}
			return ""
		}, func() (string, error) { return t.TempDir(), nil })
		if err != nil || c.writeModes != want {
			t.Errorf("YGG_WRITE_MODES=%q gave %v, %v", in, c.writeModes, err)
		}
	}
	if _, err := loadConfig(func(k string) string {
		if k == "YGG_WRITE_MODES" {
			return "maybe"
		}
		return ""
	}, func() (string, error) { return t.TempDir(), nil }); err == nil {
		t.Error("YGG_WRITE_MODES=maybe was accepted")
	}
}

func TestCodeModeIsRefusedWhileWriteModesAreOff(t *testing.T) {
	base, cfg, _ := startAppWith(t, config{addr: "127.0.0.1:0", dataDir: t.TempDir()})
	project, repo := trackRepo(t, cfg)
	for _, mode := range []string{"code", "current_branch", "project"} {
		if code, id := createTask(t, base, project, repo, mode); code != http.StatusBadRequest || id != "" {
			t.Errorf("%s create = %d %q, want 400", mode, code, id)
		}
	}
}

func TestShutdownCancelsARunningTaskBeforeTheStoreCloses(t *testing.T) {
	// A slow demo agent keeps the task running while the app stops.
	base, cfg, stop := startAppWith(t, config{addr: "127.0.0.1:0", dataDir: t.TempDir(), mockDelay: 2 * time.Second})
	project, repo := trackRepo(t, cfg)
	code, id := createTask(t, base, project, repo, "ask")
	if code != http.StatusCreated || id == "" {
		t.Fatalf("create = %d", code)
	}
	var task struct{ Task struct{ Status string } }
	deadline := time.Now().Add(20 * time.Second)
	for task.Task.Status != "running" {
		getJSON(t, base+"/api/tasks/"+id, &task)
		if time.Now().After(deadline) {
			t.Fatalf("task is %q, never running", task.Task.Status)
		}
		time.Sleep(25 * time.Millisecond)
	}

	done := make(chan error, 1)
	go func() { done <- stop() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("shutdown did not finish with a task running")
	}

	// The run was cancelled and its final state was stored before the store
	// closed: reopen the database and look.
	d, err := db.Open(context.Background(), cfg.dataDir+"/ygg.db")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	st, err := db.NewStore(d, nil).GetTaskState(context.Background(), core.TaskID(id))
	if err != nil {
		t.Fatal(err)
	}
	if s := st.Row.Status; s == core.TaskRunning || s == core.TaskQueued {
		t.Fatalf("task is still %q after shutdown", s)
	}
}

func TestTaskLockReportsTheHolder(t *testing.T) {
	l := taskLock{reg: &mainlock.Registry{}}
	rel, _, ok := l.TryAcquire([]core.RepoID{"rep_a"}, "task:tsk_1")
	if !ok {
		t.Fatal("first acquire refused")
	}
	if h, held := l.Held([]core.RepoID{"rep_a"}); !held || h != "task:tsk_1" {
		t.Fatalf("Held = %q %v", h, held)
	}
	if _, h, ok := l.TryAcquire([]core.RepoID{"rep_a"}, "task:tsk_2"); ok || h != "task:tsk_1" {
		t.Fatalf("second acquire = %q %v", h, ok)
	}
	rel()
	rel()
	if _, held := l.Held([]core.RepoID{"rep_a"}); held {
		t.Fatal("lock still held after release")
	}
	if _, held := l.Held(nil); held {
		t.Fatal("empty repo list reported as held")
	}
}
