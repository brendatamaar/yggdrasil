//go:build windows

package main

import (
	"bytes"
	"errors"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const waitTimeout = 15 * time.Second

// session drives one shell in a PTY and records everything it prints.
type session struct {
	t        *testing.T
	p        PTY
	mu       sync.Mutex
	out      bytes.Buffer
	readDone chan struct{}
}

func startSession(t *testing.T, shell string, size Size, noJob bool) *session {
	t.Helper()
	path, args, err := shellCommand(shell)
	if err != nil {
		t.Fatal(err)
	}
	p, err := startGoPTY(Options{Path: path, Args: args, Size: size, NoJob: noJob})
	if err != nil {
		t.Fatalf("start %s: %v", shell, err)
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
		case <-time.After(waitTimeout):
			t.Errorf("output did not reach EOF after Close")
		}
		if !processGone(pid, 5*time.Second) {
			t.Errorf("shell %d still running after Close", pid)
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
	deadline := time.Now().Add(waitTimeout)
	for time.Now().Before(deadline) {
		if m := re.FindString(screenText(s.raw())); m != "" {
			return m
		}
		time.Sleep(50 * time.Millisecond)
	}
	s.t.Fatalf("timed out waiting for %q; screen text:\n%s", pattern, screenText(s.raw()))
	return ""
}

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
		case <-time.After(waitTimeout):
			s.t.Fatalf("output did not reach EOF after the process exited")
		}
		return r.code
	case <-time.After(waitTimeout):
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
	deadline := time.Now().Add(waitTimeout)
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
