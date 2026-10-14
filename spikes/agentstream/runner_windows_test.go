//go:build windows

package agentstream

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const waitTimeout = 10 * time.Second

type proc struct {
	name string
	pid  int
	h    windows.Handle
}

// openProc opens pid for waiting. Holding the handle keeps the pid from being
// reused; t.Cleanup kills the process whatever the outcome.
func openProc(t *testing.T, name string, pid int) *proc {
	t.Helper()
	h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		t.Fatalf("open %s (pid %d): %v", name, pid, err)
	}
	t.Cleanup(func() {
		_ = windows.TerminateProcess(h, 1)
		_ = windows.CloseHandle(h)
	})
	return &proc{name: name, pid: pid, h: h}
}

func (p *proc) exited(timeout time.Duration) bool {
	ev, err := windows.WaitForSingleObject(p.h, uint32(timeout.Milliseconds()))
	return err == nil && ev == windows.WAIT_OBJECT_0
}

func waitPIDs(t *testing.T, path string) (fake, child int) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			kv := map[string]int{}
			for line := range strings.FieldsSeq(string(data)) {
				k, v, _ := strings.Cut(line, "=")
				kv[k], _ = strconv.Atoi(v)
			}
			return kv["fake"], kv["child"]
		}
		if time.Now().After(deadline) {
			t.Fatalf("no pid file: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// drain reads events until the channel closes.
func drain(t *testing.T, ch <-chan Event) []Event {
	t.Helper()
	var events []Event
	timeout := time.After(waitTimeout)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return events
			}
			events = append(events, ev)
		case <-timeout:
			t.Fatalf("events channel still open after %v", waitTimeout)
		}
	}
}

func waitGoroutines(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > want {
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			t.Fatalf("goroutines: %d, want <= %d\n%s", runtime.NumGoroutine(), want, buf[:runtime.Stack(buf, true)])
		}
		time.Sleep(20 * time.Millisecond)
	}
}

type fakeRun struct {
	cfg  Config
	pids string
}

func newFakeRun(t *testing.T, fixture string, delay, hold time.Duration) fakeRun {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	stderr, err := os.Create(filepath.Join(dir, "stderr.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stderr.Close()
		if t.Failed() {
			data, _ := os.ReadFile(stderr.Name())
			t.Logf("fake stderr: %s", data)
		}
	})
	pids := filepath.Join(dir, "pids")
	return fakeRun{pids: pids, cfg: Config{
		Binary: exe, Dir: dir, Prompt: "Summarize the acme-shop README", Stderr: stderr,
		Env: map[string]string{
			fakeEnv: "stream", fakeFixtureEnv: abs, fakePIDsEnv: pids,
			fakeDelayEnv: delay.String(), fakeHoldEnv: hold.String(),
		},
	}}
}

func TestRunCancelKillsTree(t *testing.T) {
	before := runtime.NumGoroutine()
	f := newFakeRun(t, "subagents.jsonl", 100*time.Millisecond, fakeLifetime)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r, err := Start(ctx, f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); drain(t, r.Events); r.Wait() })

	first := <-r.Events
	if first.Kind != KindInit {
		t.Fatalf("first event = %+v", first)
	}
	fakePID, childPID := waitPIDs(t, f.pids)
	if fakePID != r.PID {
		t.Fatalf("pid file says %d, run says %d", fakePID, r.PID)
	}
	fake, child := openProc(t, "fake", fakePID), openProc(t, "child", childPID)
	if n := r.g.active(); n < 2 {
		t.Fatalf("job has %d active processes, want fake and child", n)
	}

	cancel()
	rest := drain(t, r.Events)
	exit := r.Wait()
	if !exit.Cancelled {
		t.Errorf("exit = %+v, want cancelled", exit)
	}
	if total := 1 + len(rest); total >= 15 {
		t.Errorf("got all %d events: cancel wasn't mid-stream", total)
	}
	for _, p := range []*proc{fake, child} {
		if !p.exited(waitTimeout) {
			t.Errorf("%s (pid %d) still alive after cancel", p.name, p.pid)
		}
	}
	waitGoroutines(t, before)
}

func TestRunCompletesAndKillsLeftovers(t *testing.T) {
	before := runtime.NumGoroutine()
	f := newFakeRun(t, "subagents.jsonl", 0, 0)
	r, err := Start(context.Background(), f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	events := drain(t, r.Events)
	exit := r.Wait()
	if exit.Code != 0 || exit.Cancelled || exit.Err != "" {
		t.Errorf("exit = %+v", exit)
	}
	if len(events) != 15 || events[len(events)-1].Kind != KindResult {
		t.Errorf("got %d events, want 15 ending in result", len(events))
	}
	// The child shared stdout and was still sleeping: the channel only closes
	// because the job was terminated when the fake exited.
	_, childPID := waitPIDs(t, f.pids)
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(childPID))
	if err == nil {
		ev, _ := windows.WaitForSingleObject(h, uint32(waitTimeout.Milliseconds()))
		_ = windows.CloseHandle(h)
		if ev != windows.WAIT_OBJECT_0 {
			t.Errorf("leftover child (pid %d) still alive", childPID)
		}
	}
	waitGoroutines(t, before)
}

func TestRunCleanEnv(t *testing.T) {
	for _, name := range []string{"GITHUB_TOKEN", "GITLAB_TOKEN", "GITEA_TOKEN", "ANTHROPIC_API_KEY"} {
		t.Setenv(name, "fictional-secret")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	r, err := Start(context.Background(), Config{
		Binary: exe, Dir: t.TempDir(), Prompt: "env",
		Env: map[string]string{fakeEnv: "env", EnvMaxConcurrentSubagent: "4"},
	})
	if err != nil {
		t.Fatal(err)
	}
	events := drain(t, r.Events)
	if exit := r.Wait(); exit.Code != 0 {
		t.Fatalf("exit = %+v", exit)
	}
	if len(events) != 1 || events[0].Kind != KindUnknown {
		t.Fatalf("events = %+v", events)
	}
	var line struct{ Env map[string]string }
	if err := json.Unmarshal([]byte(events[0].Raw), &line); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for k, v := range line.Env {
		got[strings.ToUpper(k)] = v
	}
	for _, name := range []string{"GITHUB_TOKEN", "GITLAB_TOKEN", "GITEA_TOKEN", "ANTHROPIC_API_KEY"} {
		if _, ok := got[name]; ok {
			t.Errorf("%s reached the child", name)
		}
	}
	for _, name := range BaseEnvAllowlist {
		if _, ours := os.LookupEnv(name); ours {
			if _, ok := got[strings.ToUpper(name)]; !ok {
				t.Errorf("allowlisted %s missing in the child", name)
			}
		}
	}
	if got[EnvMaxConcurrentSubagent] != "4" {
		t.Errorf("extra %s = %q", EnvMaxConcurrentSubagent, got[EnvMaxConcurrentSubagent])
	}
	for k := range got {
		if !strings.HasPrefix(k, "AGENTSTREAM_") && k != EnvMaxConcurrentSubagent && !containsFold(BaseEnvAllowlist, k) {
			t.Errorf("unexpected var %s in the child", k)
		}
	}
}

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}
