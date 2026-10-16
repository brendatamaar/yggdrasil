//go:build windows

package platform

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The checks of docs/spikes.md B1, ported to StartPTY. They match English
// console output (`mode con`, `choice`).

const ptyWait = 15 * time.Second

var procGetProcessHandleCount = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessHandleCount")

// ptyEnv is an explicit environment for shells: the few variables they need,
// never os.Environ.
func ptyEnv() []string {
	var env []string
	for _, k := range []string{"SystemRoot", "windir", "SystemDrive", "ComSpec", "PATH", "PATHEXT", "TEMP", "TMP",
		"USERPROFILE", "APPDATA", "LOCALAPPDATA", "ProgramData", "ProgramFiles", "PSModulePath", "USERNAME", "COMPUTERNAME"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

func shellArgv(t testing.TB, shell string) []string {
	t.Helper()
	sys32, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	switch shell {
	case "powershell":
		return []string{filepath.Join(sys32, "WindowsPowerShell", "v1.0", "powershell.exe"), "-NoLogo", "-NoProfile"}
	case "cmd":
		return []string{filepath.Join(sys32, "cmd.exe")}
	}
	t.Fatalf("unknown shell %q", shell)
	return nil
}

// session drives one PTY and records everything it prints.
type session struct {
	t        *testing.T
	p        PTY
	mu       sync.Mutex
	out      bytes.Buffer
	readDone chan struct{}
}

func startSession(t *testing.T, shell string) *session {
	t.Helper()
	return startArgvSession(t, shellArgv(t, shell), "")
}

func startArgvSession(t *testing.T, argv []string, taskID string) *session {
	t.Helper()
	p, err := StartPTY(context.Background(), PTYSpec{
		Spec: Spec{Argv: argv, Env: ptyEnv(), TaskID: taskID},
		Size: Size{Cols: 80, Rows: 25},
	})
	if err != nil {
		t.Fatalf("StartPTY %v: %v", argv, err)
	}
	s := &session{t: t, p: p, readDone: make(chan struct{})}
	go func() {
		defer close(s.readDone)
		buf := make([]byte, 4096)
		for {
			n, err := p.Read(buf)
			s.mu.Lock()
			s.out.Write(buf[:n])
			s.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		pid := p.Pid()
		if err := p.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
		select {
		case <-s.readDone:
		case <-time.After(ptyWait):
			t.Errorf("output did not reach EOF after Close")
		}
		if !processGone(pid, 5*time.Second) {
			t.Errorf("shell %d still running after Close", pid)
			killPid(pid)
		}
		if t.Failed() {
			t.Logf("output:\n%q", s.raw())
		}
	})
	return s
}

func (s *session) send(input string) {
	s.t.Helper()
	if _, err := s.p.Write([]byte(input)); err != nil {
		s.t.Fatalf("write %q: %v", input, err)
	}
}

func (s *session) raw() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.out.String()
}

// waitFor polls the screen text (escape sequences stripped) until pattern matches.
func (s *session) waitFor(pattern string) string {
	s.t.Helper()
	re := regexp.MustCompile(pattern)
	deadline := time.Now().Add(ptyWait)
	for time.Now().Before(deadline) {
		if m := re.FindString(screenText(s.raw())); m != "" {
			return m
		}
		time.Sleep(50 * time.Millisecond)
	}
	s.t.Fatalf("timed out waiting for %q; screen text:\n%s", pattern, screenText(s.raw()))
	return ""
}

// wait returns the exit code once the process has exited and the output is drained.
func (s *session) wait() int {
	s.t.Helper()
	type result struct {
		code int
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		code, err := s.p.Wait()
		ch <- result{code, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			s.t.Fatalf("wait: %v", r.err)
		}
		select {
		case <-s.readDone:
		case <-time.After(ptyWait):
			s.t.Fatalf("output did not reach EOF after the process exited")
		}
		return r.code
	case <-time.After(ptyWait):
		s.t.Fatalf("process did not exit")
		return 0
	}
}

var (
	// Cursor positioning becomes a line break so "^marker" matches output lines.
	cursorMove = regexp.MustCompile(`\x1b\[[0-9;]*[Hf]`)
	escapes    = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[()][0-9A-Za-z]|\x1b[=>]`)
)

func screenText(raw string) string {
	s := cursorMove.ReplaceAllString(raw, "\n")
	s = escapes.ReplaceAllString(s, "")
	return strings.ReplaceAll(s, "\r", "")
}

// processGone reports whether pid exits within timeout.
func processGone(pid int, timeout time.Duration) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return true // no such process
	}
	defer func() { _ = windows.CloseHandle(h) }()
	ev, err := windows.WaitForSingleObject(h, uint32(timeout.Milliseconds()))
	return err == nil && ev == windows.WAIT_OBJECT_0
}

type procInfo struct {
	pid  int
	name string
}

// childProcesses lists the direct children of pid.
func childProcesses(t *testing.T, pid int) []procInfo {
	t.Helper()
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		t.Fatalf("process snapshot: %v", err)
	}
	defer func() { _ = windows.CloseHandle(snap) }()
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	var out []procInfo
	for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		if int(pe.ParentProcessID) == pid {
			out = append(out, procInfo{pid: int(pe.ProcessID), name: windows.UTF16ToString(pe.ExeFile[:])})
		}
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		t.Fatalf("walk processes: %v", err)
	}
	return out
}

func waitForChild(t *testing.T, parent int, name string) int {
	t.Helper()
	deadline := time.Now().Add(ptyWait)
	for time.Now().Before(deadline) {
		for _, c := range childProcesses(t, parent) {
			if strings.EqualFold(c.name, name) {
				return c.pid
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no %s child of %d appeared", name, parent)
	return 0
}

// conhosts lists the conhost.exe/OpenConsole.exe processes owned by this test binary.
func conhosts(t *testing.T) []procInfo {
	t.Helper()
	var out []procInfo
	for _, c := range childProcesses(t, os.Getpid()) {
		if strings.EqualFold(c.name, "conhost.exe") || strings.EqualFold(c.name, "OpenConsole.exe") {
			out = append(out, c)
		}
	}
	return out
}

func killPid(pid int) {
	if h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid)); err == nil {
		_ = windows.TerminateProcess(h, 1)
		_ = windows.CloseHandle(h)
	}
}

// drainPTY reads until the terminal fails, like a real caller must.
func drainPTY(p PTY) { _, _ = io.Copy(io.Discard, p) }

func handleCount(t *testing.T) uint32 {
	t.Helper()
	var n uint32
	r, _, err := procGetProcessHandleCount.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&n)))
	if r == 0 {
		t.Fatalf("GetProcessHandleCount: %v", err)
	}
	return n
}

// Checks (a) output and keystrokes, (b) resize and (d) exit code with cmd.
func TestPTYCmdEchoResizeExit(t *testing.T) {
	s := startSession(t, "cmd")
	s.send("echo ygg-pty-ok\r")
	s.waitFor(`(?m)^ygg-pty-ok`)

	s.send("mode con\r")
	s.waitFor(`Columns:\s+80\b`)
	if err := s.p.Resize(Size{Cols: 100, Rows: 30}); err != nil {
		t.Fatalf("resize: %v", err)
	}
	s.send("mode con\r")
	s.waitFor(`Columns:\s+100\b`)

	s.send("exit 3\r")
	if code := s.wait(); code != 3 {
		t.Fatalf("exit code = %d, want 3", code)
	}
	if err := s.p.Resize(Size{Cols: 90, Rows: 30}); err == nil {
		t.Error("Resize after the terminal closed should fail")
	}
}

// Checks (a), (b) and (d) with PowerShell.
func TestPTYPowerShellEchoResizeExit(t *testing.T) {
	s := startSession(t, "powershell")
	s.send("echo ygg-pty-ok\r")
	s.waitFor(`(?m)^ygg-pty-ok`)

	const size = `"ygg-size=" + $Host.UI.RawUI.WindowSize.Width + "x" + $Host.UI.RawUI.WindowSize.Height` + "\r"
	s.send(size)
	s.waitFor(`ygg-size=80x25`)
	if err := s.p.Resize(Size{Cols: 120, Rows: 40}); err != nil {
		t.Fatalf("resize: %v", err)
	}
	s.send(size)
	s.waitFor(`ygg-size=120x40`)

	s.send("exit 3\r")
	if code := s.wait(); code != 3 {
		t.Fatalf("exit code = %d, want 3", code)
	}
}

// Check (a): Ctrl+C (0x03 through the PTY) interrupts the running command and
// not the shell, without SetConsoleCtrlHandler.
func TestPTYCtrlC(t *testing.T) {
	for _, tc := range []struct{ shell, long string }{
		{"cmd", "ping -n 30 127.0.0.1"},
		{"powershell", "Start-Sleep -Seconds 30"},
	} {
		t.Run(tc.shell, func(t *testing.T) {
			s := startSession(t, tc.shell)
			s.send("echo ygg-ready\r")
			s.waitFor(`(?m)^ygg-ready`)
			s.send(tc.long + "\r")
			time.Sleep(1500 * time.Millisecond)
			start := time.Now()
			s.send("\x03")
			// Ctrl+C flushes the console input buffer, so don't type ahead.
			time.Sleep(500 * time.Millisecond)
			s.send("echo ygg-after-ctrlc\r")
			s.waitFor(`(?m)^ygg-after-ctrlc`)
			if d := time.Since(start); d > 10*time.Second {
				t.Fatalf("Ctrl+C took %v; the command was not interrupted", d)
			}
		})
	}
}

// Check (c): interactive programs read keystrokes.
func TestPTYInteractive(t *testing.T) {
	t.Run("powershell Read-Host", func(t *testing.T) {
		s := startSession(t, "powershell")
		s.send(`$x = Read-Host 'ygg-prompt'; "ygg-got-$x"` + "\r")
		s.waitFor(`ygg-prompt: `)
		s.send("abc\r")
		s.waitFor(`ygg-got-abc`)
	})
	t.Run("cmd choice", func(t *testing.T) {
		s := startSession(t, "cmd")
		s.send("choice /c yn /m ygg-choice\r")
		s.waitFor(`ygg-choice \[Y,N\]\?`)
		s.send("n")
		// choice flushes the input buffer when it exits; wait before typing on.
		s.waitFor(`ygg-choice \[Y,N\]\?N`)
		time.Sleep(200 * time.Millisecond)
		s.send("echo ygg-el=%errorlevel%\r")
		s.waitFor(`ygg-el=2`)
	})
}

// Check (e): Close kills the shell, its children and the pseudo console host,
// and is safe to call twice.
func TestPTYCloseKillsTree(t *testing.T) {
	s := startSession(t, "cmd")
	hosts := conhosts(t)
	t.Logf("console hosts owned by the test while the shell runs: %v", hosts)
	s.send("start /b ping -n 60 127.0.0.1 >nul\r")
	ping := waitForChild(t, s.p.Pid(), "PING.EXE")
	t.Cleanup(func() { killPid(ping) })

	if err := s.p.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !processGone(s.p.Pid(), 5*time.Second) {
		t.Fatal("shell still running")
	}
	if !processGone(ping, 5*time.Second) {
		t.Fatalf("grandchild ping %d survived Close", ping)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(conhosts(t)) > 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if left := conhosts(t); len(left) > 0 {
		t.Fatalf("console hosts left after Close: %v", left)
	}
	if err := s.p.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	if _, err := s.p.Write([]byte("x")); err == nil {
		t.Error("Write after Close should fail")
	}
}

// The PTY process is in the main job and its task job, and cancelling the
// context kills its tree.
func TestPTYJobsAndContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	p, err := StartPTY(ctx, PTYSpec{
		Spec: Spec{Argv: shellArgv(t, "cmd"), Env: ptyEnv(), TaskID: "pty-task"},
		Size: Size{Cols: 80, Rows: 25},
	})
	if err != nil {
		t.Fatal(err)
	}
	go drainPTY(p)
	t.Cleanup(func() { _ = p.Close() })
	sh := openProc(t, "shell", p.Pid())

	jobs.mu.Lock()
	main, task := jobs.main, jobs.tasks["pty-task"]
	jobs.mu.Unlock()
	if task == nil {
		t.Fatal("no task job registered")
	}
	if !sh.inJob(t, main) {
		t.Error("shell is not in the main job")
	}
	if !sh.inJob(t, task.h) {
		t.Error("shell is not in the task job")
	}

	cancel()
	if !sh.exited(waitTimeout) {
		t.Error("shell survived the cancel of its context")
	}
	if code, _ := p.Wait(); code == 0 {
		t.Errorf("exit code after cancel = %d, want non-zero", code)
	}
}

func TestPTYValidation(t *testing.T) {
	ok := Size{Cols: 80, Rows: 25}
	argv := shellArgv(t, "cmd")
	for name, tc := range map[string]struct {
		spec PTYSpec
		want error
	}{
		"nil env":   {PTYSpec{Spec: Spec{Argv: argv}, Size: ok}, ErrNoEnv},
		"no argv":   {PTYSpec{Spec: Spec{Env: ptyEnv()}, Size: ok}, nil},
		"zero cols": {PTYSpec{Spec: Spec{Argv: argv, Env: ptyEnv()}, Size: Size{Cols: 0, Rows: 25}}, nil},
		"huge rows": {PTYSpec{Spec: Spec{Argv: argv, Env: ptyEnv()}, Size: Size{Cols: 80, Rows: 1001}}, nil},
	} {
		p, err := StartPTY(context.Background(), tc.spec)
		if err == nil {
			_ = p.Close()
			t.Errorf("%s: StartPTY succeeded", name)
		} else if tc.want != nil && !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
	// Size limits are inclusive.
	for _, s := range []Size{{Cols: 1, Rows: 1}, {Cols: 1000, Rows: 1000}} {
		p, err := StartPTY(context.Background(), PTYSpec{Spec: Spec{Argv: argv, Env: ptyEnv()}, Size: s})
		if err != nil {
			t.Errorf("size %v: %v", s, err)
			continue
		}
		go drainPTY(p)
		if err := p.Close(); err != nil {
			t.Errorf("close %v: %v", s, err)
		}
	}
}
