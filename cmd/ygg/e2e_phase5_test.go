package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/api"
)

// Phase 5 exit criteria (docs/phases/phase-5.md):
//
//   - side_by_side: two worktrees of the frontend+backend fixture pair run at
//     the same time with no port conflict;
//   - no_leftovers: killing ygg leaves no dev server process behind
//     (e2e_phase5_leftovers_test.go);
//   - uv_and_dotnet_verify: a uv repo and the .NET repo verify to green in
//     fresh worktrees (e2e_phase5_verify_test.go).
//
// The services are the real ones: devservers, worktreeenv and platform.Start
// run the fixture servers of fixtures/demo-gen as child processes.
func TestPhase5EndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the demo generator and real dev servers")
	}
	t.Run("side_by_side", testP5SideBySide)
	t.Run("no_leftovers", testP5NoLeftovers)
	t.Run("uv_and_dotnet_verify", testP5UvAndDotnetVerify)
}

// Two tasks, each with a worktree of shop-api-dev (Go) and of shop-web-dev
// (Node). Each worktree has its own port block; the four servers run at once.
func testP5SideBySide(t *testing.T) {
	p5NeedTool(t, "go", "node", "npm")
	cfg := config{addr: "127.0.0.1:0", dataDir: t.TempDir(), writeModes: true}
	p := newP5Project(t, []string{"alpha", "beta"}, []string{"shop-api-dev", "shop-web-dev"}, cfg)
	_, base := startFullApp(t, cfg)
	c := &p5Client{t: t, base: base, client: appClient}

	type running struct {
		repo, task string
		srv        api.DevServerDTO
	}
	var all []running
	t.Cleanup(func() {
		for _, r := range all {
			c.stop(r.srv)
		}
	})
	for _, task := range []string{"alpha", "beta"} {
		for _, repo := range []string{"shop-api-dev", "shop-web-dev"} {
			w := p.worktrees[task][repo]
			srv := c.start(repo, w.ID)
			all = append(all, running{repo: repo, task: task, srv: srv})
		}
	}

	// Distinct detected ports, each inside its own worktree's 20-port block.
	seen := map[int]string{}
	for _, r := range all {
		who := r.task + "/" + r.repo
		if r.srv.BlockSize != 20 || r.srv.BlockBase == 0 {
			t.Fatalf("%s: port block = %d+%d, want a block of 20 ports", who, r.srv.BlockBase, r.srv.BlockSize)
		}
		for _, pt := range r.srv.Ports {
			if pt.Port < r.srv.BlockBase || pt.Port >= r.srv.BlockBase+r.srv.BlockSize || pt.OutsideBlock {
				t.Errorf("%s: port %+v is outside its block %d+%d", who, pt, r.srv.BlockBase, r.srv.BlockSize)
			}
			if other, dup := seen[pt.Port]; dup {
				t.Errorf("port %d is used by both %s and %s", pt.Port, other, who)
			}
			seen[pt.Port] = who
		}
	}
	// Blocks of different worktrees never overlap.
	for i, a := range all {
		for _, b := range all[i+1:] {
			if a.srv.WorktreeID != b.srv.WorktreeID && a.srv.BlockBase < b.srv.BlockBase+b.srv.BlockSize && b.srv.BlockBase < a.srv.BlockBase+a.srv.BlockSize {
				t.Errorf("port blocks of %s/%s and %s/%s overlap", a.task, a.repo, b.task, b.repo)
			}
		}
	}

	// All four answer over HTTP, and each says which worktree it is.
	for _, r := range all {
		url := "http://127.0.0.1:" + strconv.Itoa(r.srv.Ports[0].Port)
		wtName := r.task + "-" + r.repo
		switch r.repo {
		case "shop-api-dev":
			body := httpGetEventually(t, url+"/api/info")
			var info struct{ Service, Port, Worktree string }
			if err := json.Unmarshal([]byte(body), &info); err != nil {
				t.Fatalf("%s: %q: %v", wtName, body, err)
			}
			if info.Service != "shop-api-dev" || info.Port != strconv.Itoa(r.srv.Ports[0].Port) {
				t.Errorf("%s answered %+v on port %d", wtName, info, r.srv.Ports[0].Port)
			}
		case "shop-web-dev":
			body := httpGetEventually(t, url+"/")
			if !strings.Contains(body, "shop-web-dev") || !strings.Contains(body, ">"+strconv.Itoa(r.srv.Ports[0].Port)+"<") {
				t.Errorf("%s answered %q on port %d", wtName, body, r.srv.Ports[0].Port)
			}
		}
	}

	// Logs are separate: each server's log names its own port, never another's.
	for _, r := range all {
		var text string
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if text = c.logText(r.srv); strings.Contains(text, "Listening on") {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		own := fmt.Sprintf("127.0.0.1:%d", r.srv.Ports[0].Port)
		if !strings.Contains(text, own) {
			t.Errorf("%s/%s: log has no line about its own port %d:\n%s", r.task, r.repo, r.srv.Ports[0].Port, text)
		}
		for _, o := range all {
			if o.srv.ID == r.srv.ID {
				continue
			}
			if strings.Contains(text, fmt.Sprintf("127.0.0.1:%d", o.srv.Ports[0].Port)) {
				t.Errorf("%s/%s: log holds a line of %s/%s:\n%s", r.task, r.repo, o.task, o.repo, text)
			}
		}
	}

	// Stopping one leaves the others up.
	c.stop(all[0].srv)
	deadline := time.Now().Add(20 * time.Second)
	for {
		s, _ := c.server(all[0].repo, all[0].srv.WorktreeID)
		if s.Status == api.DevServerStopped {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s/%s did not stop: %+v", all[0].task, all[0].repo, s)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, err := httpGet("http://127.0.0.1:" + strconv.Itoa(all[0].srv.Ports[0].Port) + "/api/info"); err == nil {
		t.Errorf("the stopped server still answers")
	}
	for _, r := range all[1:] {
		if s, _ := c.server(r.repo, r.srv.WorktreeID); s.Status != api.DevServerRunning {
			t.Errorf("%s/%s should still run: %+v", r.task, r.repo, s)
		}
	}
}
