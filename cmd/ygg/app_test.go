package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

// startApp wires the real application on a loopback port with the demo agent
// and no pause between events.
func startApp(t *testing.T) (base string, cfg config, stop func() error) {
	t.Helper()
	cfg = config{addr: "127.0.0.1:0", dataDir: t.TempDir(), mockDelay: 0}
	assets := fstest.MapFS{"index.html": {Data: []byte("<html>ygg</html>")}}
	var logs bytes.Buffer
	a, err := newApp(context.Background(), cfg, assets, &logs)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.run(ctx, ln) }()
	// stop may be called by the test and again by Cleanup: the result is read
	// from the channel only once.
	var once sync.Once
	var result error
	stop = func() error {
		once.Do(func() { cancel(); result = <-done })
		return result
	}
	t.Cleanup(func() { _ = stop() })
	return "http://" + ln.Addr().String(), cfg, stop
}

func getJSON(t *testing.T, url string, v any) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if v != nil {
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			t.Fatalf("decode %s: %v", url, err)
		}
	}
	return resp.StatusCode
}

func TestAppRunsADemoTaskEndToEnd(t *testing.T) {
	base, cfg, stop := startApp(t)

	var health struct{ Status, Version string }
	if code := getJSON(t, base+"/api/health", &health); code != 200 || health.Status != "ok" || health.Version != "dev" {
		t.Fatalf("health = %d %+v", code, health)
	}
	resp, err := http.Get(base + "/some/client/route")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(page), "ygg") {
		t.Errorf("UI = %d %q", resp.StatusCode, page)
	}

	// Track a repo the way a person does today, then start a task on it.
	repo := gitRepo(t, filepath.Join(t.TempDir(), "acme-shop"), "shop-api")
	var out bytes.Buffer
	if err := seed(context.Background(), &out, cfg, repo, ""); err != nil {
		t.Fatal(err)
	}
	ids := regexp.MustCompile(`(prj|rep)_[0-9a-f]+`).FindAllString(out.String(), -1)
	body, _ := json.Marshal(map[string]any{
		"projectId": ids[0], "repoIds": []string{ids[1]}, "prompt": "Find the order routes.", "mode": "ask", "agent": "mock",
	})
	resp, err = http.Post(base+"/api/tasks", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var created struct{ Task struct{ ID, Title string } }
	err = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if err != nil || resp.StatusCode != 201 || created.Task.ID == "" {
		t.Fatalf("create = %d %+v %v", resp.StatusCode, created, err)
	}

	var task struct{ Task struct{ Status string } }
	deadline := time.Now().Add(15 * time.Second)
	for {
		getJSON(t, base+"/api/tasks/"+created.Task.ID, &task)
		if task.Task.Status == "done" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("task is %q, want done", task.Task.Status)
		}
		time.Sleep(25 * time.Millisecond)
	}
	var events struct {
		Events []struct {
			Seq      int64
			Kind     string
			ParentID string
		}
	}
	if code := getJSON(t, base+"/api/tasks/"+created.Task.ID+"/events", &events); code != 200 || len(events.Events) < 8 {
		t.Fatalf("events = %d, %d of them", code, len(events.Events))
	}
	nested := 0
	for i, e := range events.Events {
		if e.Seq != int64(i+1) {
			t.Fatalf("event %d has seq %d", i, e.Seq)
		}
		if e.ParentID != "" {
			nested++
		}
	}
	if nested == 0 {
		t.Error("the demo session's sub-agent events lost their parent")
	}

	// Stopping the application returns cleanly and releases the database.
	if err := stop(); err != nil {
		t.Errorf("shutdown: %v", err)
	}
}

func TestAppRefusesToStartOnAnUnusableDataFolder(t *testing.T) {
	file := filepath.Join(t.TempDir(), "afile")
	if err := writeFile(file); err != nil {
		t.Fatal(err)
	}
	_, err := newApp(context.Background(), config{dataDir: filepath.Join(file, "ygg")}, fstest.MapFS{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "data folder") {
		t.Errorf("newApp on a bad folder: %v", err)
	}
}

func TestAppLogsNeverHoldATokenShape(t *testing.T) {
	var logs bytes.Buffer
	log := newLogger(&logs, 0, redactorForTest())
	log.Info("clone", "url", "https://user:ghp_abcdefghijklmnopqrstuvwxyz0123@github.com/a/b.git", "token", "plain")
	if s := logs.String(); strings.Contains(s, "ghp_") || strings.Contains(s, "user:") || strings.Contains(s, "plain") {
		t.Errorf("log leaks: %s", s)
	}
}
