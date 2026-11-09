package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// lockedBuffer is an io.Writer a test can read while a process writes to it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// buildYgg compiles the real binary (CGO_ENABLED=0, like a release) into dir.
func buildYgg(t *testing.T, dir string) string {
	t.Helper()
	env, err := safety.NewEnvBuilder().Build(safety.EnvOptions{Extra: map[string]string{"CGO_ENABLED": "0"}})
	if err != nil {
		t.Fatal(err)
	}
	goExe, err := platform.Resolve("go", "", env)
	if err != nil {
		t.Skipf("go is not installed: %v", err)
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, yggExe)
	var out bytes.Buffer
	p, err := platform.Start(context.Background(), platform.Spec{
		Argv: []string{goExe, "build", "-o", exe, "./cmd/ygg"}, Dir: root, Env: env, Stdout: &out, Stderr: &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if code, err := p.Wait(); err != nil || code != 0 {
		t.Fatalf("go build exit %d, %v:\n%s", code, err, out.String())
	}
	return exe
}

// yggProc is a real ygg subprocess serving a data folder.
type yggProc struct {
	proc   platform.Process
	pid    int
	client *p5Client
	exited chan struct{}
}

// startYgg runs the binary on the data folder and opens the UI session with
// the launch link it prints, like the browser does.
func startYgg(t *testing.T, exe, dataDir string) *yggProc {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	env, err := safety.NewEnvBuilder().Build(safety.EnvOptions{Extra: map[string]string{
		"YGG_ADDR": addr, "YGG_DATA_DIR": dataDir, "YGG_NO_BROWSER": "1", "YGG_MOCK_DELAY_MS": "0",
	}})
	if err != nil {
		t.Fatal(err)
	}
	out := &lockedBuffer{}
	p, err := platform.Start(context.Background(), platform.Spec{Argv: []string{exe, "serve"}, Dir: dataDir, Env: env, Stdout: out, Stderr: out})
	if err != nil {
		t.Fatal(err)
	}
	y := &yggProc{proc: p, pid: p.Pid(), exited: make(chan struct{})}
	go func() { _, _ = p.Wait(); close(y.exited) }()
	t.Cleanup(func() {
		select {
		case <-y.exited:
		default:
			killPid(y.pid)
			<-y.exited
		}
	})

	link := regexp.MustCompile(`http://\S+/\?k=\S+`)
	var url string
	for deadline := time.Now().Add(30 * time.Second); url == ""; time.Sleep(50 * time.Millisecond) {
		url = link.FindString(out.String())
		if time.Now().After(deadline) {
			t.Fatalf("ygg printed no launch link:\n%s", out.String())
		}
		select {
		case <-y.exited:
			t.Fatalf("ygg exited early:\n%s", out.String())
		default:
		}
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	cl := &http.Client{Jar: jar, Timeout: 30 * time.Second}
	resp, err := cl.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	y.client = &p5Client{t: t, base: "http://" + addr, client: cl}
	return y
}

// hardKill ends ygg the way a crash or Task Manager does: no shutdown code
// runs, so only the operating system can clean up. It ends that one process,
// not its tree.
func (y *yggProc) hardKill(t *testing.T) {
	t.Helper()
	p, err := os.FindProcess(y.pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Kill(); err != nil {
		t.Fatalf("kill ygg: %v", err)
	}
	select {
	case <-y.exited:
	case <-time.After(15 * time.Second):
		t.Fatal("ygg did not exit after the kill")
	}
}

func portClosed(port int) bool {
	c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 300*time.Millisecond)
	if err != nil {
		return true
	}
	_ = c.Close()
	return false
}

// eventually polls cond until it holds or the time is up, and fails with what.
func eventually(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(d); !cond(); time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for: %s", what)
		}
	}
}

// Real ygg binary, real dev servers, ygg killed hard.
//
//   - Windows: the job object closes with ygg, so every server, and everything
//     it started, is gone at once.
//   - Linux/macOS have no job object. The servers outlive the killed ygg; the
//     next start finds them (the orphan records of internal/platform) and lists
//     them as "orphaned", and stopping them through the API leaves nothing
//     behind. That is the Unix contract in docs/design/devservers.md.
func testP5NoLeftovers(t *testing.T) {
	p5NeedTool(t, "go", "node", "npm")
	dataDir := t.TempDir()
	cfg := config{addr: "127.0.0.1:0", dataDir: dataDir}
	p := newP5Project(t, []string{"gamma"}, []string{"shop-api-dev", "shop-web-dev"}, cfg)
	exe := buildYgg(t, t.TempDir())

	y := startYgg(t, exe, dataDir)
	var started []api.DevServerDTO
	for _, repo := range []string{"shop-api-dev", "shop-web-dev"} {
		started = append(started, y.client.start(repo, p.worktrees["gamma"][repo].ID))
	}
	var ports []int
	for _, s := range started {
		page := "/" // the Node storefront
		if s.RepoName == "shop-api-dev" {
			page = "/api/info" // the Go backend has no root page
		}
		for _, pt := range s.Ports {
			ports = append(ports, pt.Port)
			httpGetEventually(t, "http://127.0.0.1:"+strconv.Itoa(pt.Port)+page)
		}
	}
	tree := descendants(y.pid)
	t.Logf("ygg pid %d, process tree below it: %v, server ports %v", y.pid, tree, ports)
	t.Cleanup(func() { // whatever happens below, no process outlives the test
		for _, pid := range tree {
			killPid(pid)
		}
	})
	if hasProcessList && len(tree) == 0 {
		t.Fatal("ygg has no child processes although two dev servers run")
	}

	y.hardKill(t)

	if !hostKillLeavesServers {
		eventually(t, 30*time.Second, "every server process and port to be gone after ygg was killed", func() bool {
			for _, pid := range tree {
				if pidAlive(pid) {
					return false
				}
			}
			for _, port := range ports {
				if !portClosed(port) {
					return false
				}
			}
			return true
		})
		return
	}

	// Unix: the servers survive the kill, and a second ygg finds them.
	for _, port := range ports {
		if portClosed(port) {
			t.Fatalf("port %d closed after the kill: this system's servers were expected to survive ygg (no job object)", port)
		}
	}
	y2 := startYgg(t, exe, dataDir)
	for _, repo := range []string{"shop-api-dev", "shop-web-dev"} {
		wt := p.worktrees["gamma"][repo].ID
		eventually(t, 30*time.Second, repo+" to be listed as orphaned by the next ygg", func() bool {
			s, ok := y2.client.server(repo, wt)
			return ok && s.Status == api.DevServerOrphaned
		})
		s, _ := y2.client.server(repo, wt)
		y2.client.stop(s)
		eventually(t, 30*time.Second, repo+" to be stopped", func() bool {
			s, _ := y2.client.server(repo, wt)
			return s.Status == api.DevServerStopped
		})
	}
	eventually(t, 30*time.Second, "no server process or port left after stopping the orphans", func() bool {
		for _, pid := range tree {
			if pidAlive(pid) {
				return false
			}
		}
		for _, port := range ports {
			if !portClosed(port) {
				return false
			}
		}
		return true
	})
}
