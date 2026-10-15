//go:build windows

package jobobject

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const (
	// helperLifetime is a backstop: a helper that leaks anyway exits on its own.
	helperLifetime = 2 * time.Minute
	waitTimeout    = 10 * time.Second
)

// TestMain re-executes the test binary as a helper when JOBSPIKE_HELPER=1.
func TestMain(m *testing.M) {
	if IsHelper() {
		if err := runTestHelper(os.Args[1], os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runTestHelper(mode string, args []string) error {
	if mode == "ygg" {
		return runYgg(args)
	}
	return RunHelper(mode, args)
}

// runYgg acts as Yggdrasil: ygg <outfile> <approach> <brokerPID> <gate|nogate>.
// It creates the job, starts child -> grandchild with the given approach,
// starts one sleeper outside the job, writes all PIDs to outfile and sleeps.
func runYgg(args []string) error {
	if len(args) != 4 {
		return fmt.Errorf("ygg: want <out> <approach> <brokerPID> <gate|nogate>")
	}
	out, approach := args[0], args[1]
	broker, err := strconv.Atoi(args[2])
	if err != nil {
		return fmt.Errorf("broker pid: %w", err)
	}
	if args[3] == "gate" {
		// Wait until the test has put us in its outer job.
		if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
			return fmt.Errorf("read gate: %w", err)
		}
	}
	inJobBefore, err := InJob(os.Getpid())
	if err != nil {
		return err
	}
	// Never closed on purpose: the handle closing when this process dies is
	// what must kill the tree.
	job, err := New(Options{KillOnClose: true, BreakawayOK: true})
	if err != nil {
		return err
	}
	gcFile := out + ".gc"
	life := helperLifetime.String()
	var childPID int
	switch approach {
	case "self":
		if err := job.AssignSelf(); err != nil {
			return err
		}
		cmd := HelperCmd("child", gcFile, life)
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start child: %w", err)
		}
		childPID = cmd.Process.Pid
	case "perchild":
		cmd := HelperCmd("child", gcFile, life)
		if err := job.Start(cmd); err != nil {
			return err
		}
		childPID = cmd.Process.Pid
	case "createprocess":
		p, err := job.CreateProcess(HelperArgv("child", gcFile, life), HelperEnv())
		if err != nil {
			return err
		}
		childPID = p.PID
	case "joblist":
		p, err := job.StartInJobList(HelperArgv("child", gcFile, life), HelperEnv())
		if err != nil {
			return err
		}
		childPID = p.PID
	default:
		return fmt.Errorf("unknown approach %q", approach)
	}
	gc, err := WaitKV(gcFile, waitTimeout)
	if err != nil {
		return err
	}
	gcPID, err := strconv.Atoi(gc["pid"])
	if err != nil {
		return fmt.Errorf("grandchild pid: %w", err)
	}
	outside, err := StartOutside(func() *exec.Cmd { return HelperCmd("sleep", life) }, broker)
	if err != nil {
		return err
	}
	// Plain breakaway with no checks, to record where it really lands.
	breakaway := HelperCmd("sleep", life)
	if err := StartBreakaway(breakaway); err != nil {
		// CI runners put the test in a job that forbids breakaway, and then
		// CreateProcess fails with ACCESS_DENIED. Start it normally: it is
		// still outside ygg's job, which is all this process is checked for.
		if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return err
		}
		breakaway = HelperCmd("sleep", life)
		if err := breakaway.Start(); err != nil {
			return fmt.Errorf("start breakaway fallback: %w", err)
		}
	}
	pids := map[string]int{
		"child":      childPID,
		"grandchild": gcPID,
		"outside":    outside.Cmd.Process.Pid,
		"breakaway":  breakaway.Process.Pid,
	}
	kv := map[string]string{
		"outside_method":       string(outside.Method),
		"outside_still_in_job": strconv.FormatBool(outside.StillInJob),
		"breakaway_err":        fmt.Sprint(outside.BreakawayErr),
		"ygg_in_job_before":    strconv.FormatBool(inJobBefore),
	}
	for name, pid := range pids {
		in, err := job.Contains(pid)
		if err != nil {
			return err
		}
		kv[name] = strconv.Itoa(pid)
		kv[name+"_in_job"] = strconv.FormatBool(in)
	}
	if err := WriteKV(out, kv); err != nil {
		return err
	}
	time.Sleep(helperLifetime)
	return nil
}

type proc struct {
	name string
	pid  int
	h    windows.Handle
}

// openProc opens pid for waiting and killing. Holding the handle keeps the
// pid from being reused; t.Cleanup kills the process whatever the outcome.
func openProc(t *testing.T, name string, pid int) *proc {
	t.Helper()
	h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE|
		windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
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

func (p *proc) inJob(t *testing.T, j *Job) bool {
	t.Helper()
	in, err := isProcessInJob(p.h, j.h)
	if err != nil {
		t.Fatalf("%s: %v", p.name, err)
	}
	return in
}

func pidOf(t *testing.T, kv map[string]string, name string) int {
	t.Helper()
	pid, err := strconv.Atoi(kv[name])
	if err != nil {
		t.Fatalf("%s pid %q: %v", name, kv[name], err)
	}
	return pid
}

// killPIDsByNumber is the failure-only fallback for PIDs the test never got
// a handle to.
func killPIDsByNumber(kv map[string]string) {
	for _, name := range []string{"child", "grandchild", "outside", "breakaway"} {
		pid, err := strconv.Atoi(kv[name])
		if err != nil {
			continue
		}
		if h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid)); err == nil {
			_ = windows.TerminateProcess(h, 1)
			_ = windows.CloseHandle(h)
		}
	}
}

// startYgg starts the "ygg" helper. With outer set, it is put into outer
// before it does anything (gated on stdin).
func startYgg(t *testing.T, approach string, outer *Job) (*proc, map[string]*proc, map[string]string) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "pids")
	gate := "nogate"
	if outer != nil {
		gate = "gate"
	}
	cmd := HelperCmd("ygg", out, approach, strconv.Itoa(os.Getpid()), gate)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.WaitDelay = 5 * time.Second
	var stdin io.WriteCloser
	if outer != nil {
		var err error
		if stdin, err = cmd.StdinPipe(); err != nil {
			t.Fatal(err)
		}
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start ygg: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("ygg stderr: %s", stderr.String())
			if kv, err := WaitKV(out, 0); err == nil {
				killPIDsByNumber(kv)
			}
		}
	})
	if outer != nil {
		if err := outer.AssignPID(cmd.Process.Pid); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(stdin, "go\n"); err != nil {
			t.Fatal(err)
		}
	}
	kv, err := WaitKV(out, waitTimeout)
	if err != nil {
		t.Fatalf("%v (ygg stderr: %s)", err, stderr.String())
	}
	t.Logf("ygg report: %v", kv)
	procs := map[string]*proc{}
	for _, name := range []string{"child", "grandchild", "outside", "breakaway"} {
		procs[name] = openProc(t, name, pidOf(t, kv, name))
	}
	return openProc(t, "ygg", cmd.Process.Pid), procs, kv
}

func logJobContext(t *testing.T) {
	t.Helper()
	in, err := InJob(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("test process already in a job: %v", in)
}

func TestKillOnJobClose(t *testing.T) {
	logJobContext(t)
	for _, approach := range []string{"self", "perchild", "createprocess", "joblist"} {
		t.Run(approach, func(t *testing.T) {
			ygg, procs, kv := startYgg(t, approach, nil)
			for _, name := range []string{"child", "grandchild"} {
				if kv[name+"_in_job"] != "true" {
					t.Errorf("%s not in ygg's job", name)
				}
			}
			for _, name := range []string{"outside", "breakaway"} {
				if kv[name+"_in_job"] != "false" {
					t.Errorf("%s process is in ygg's job", name)
				}
			}

			if err := windows.TerminateProcess(ygg.h, 1); err != nil {
				t.Fatalf("terminate ygg: %v", err)
			}
			for _, name := range []string{"child", "grandchild"} {
				if p := procs[name]; !p.exited(waitTimeout) {
					t.Errorf("%s (pid %d) still alive %v after ygg was killed", name, p.pid, waitTimeout)
				}
			}
			for _, name := range []string{"outside", "breakaway"} {
				if p := procs[name]; p.exited(500 * time.Millisecond) {
					t.Errorf("%s process (pid %d) died with the job", name, p.pid)
				}
			}
		})
	}
}

// TestOutsideUnderOuterJob: ygg itself sits in an outer job that forbids
// breakaway (like some terminals, IDEs and CI runners).
func TestOutsideUnderOuterJob(t *testing.T) {
	outer, err := New(Options{KillOnClose: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = outer.Close() })

	ygg, procs, kv := startYgg(t, "self", outer)
	if kv["ygg_in_job_before"] != "true" {
		t.Errorf("ygg was not in the outer job at startup")
	}
	if kv["outside_method"] != string(MethodBroker) {
		t.Errorf("outside method = %q, want %q (breakaway error: %s)", kv["outside_method"], MethodBroker, kv["breakaway_err"])
	}
	for _, name := range []string{"child", "grandchild"} {
		if !procs[name].inJob(t, outer) {
			t.Errorf("%s not in the outer job: ygg's job is not nested", name)
		}
	}
	if procs["outside"].inJob(t, outer) {
		t.Errorf("broker-started process is in the outer job")
	}
	// Plain breakaway leaves ygg's job (which allows it) but silently stays in
	// the outer job (which doesn't); CreateProcess reports no error.
	if kv["breakaway_in_job"] != "false" || !procs["breakaway"].inJob(t, outer) {
		t.Errorf("plain breakaway: in ygg's job = %s, in outer job = %v; want false, true",
			kv["breakaway_in_job"], procs["breakaway"].inJob(t, outer))
	}

	if err := outer.Close(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []*proc{ygg, procs["child"], procs["grandchild"], procs["breakaway"]} {
		if !p.exited(waitTimeout) {
			t.Errorf("%s (pid %d) still alive after the outer job closed", p.name, p.pid)
		}
	}
	if p := procs["outside"]; p.exited(500 * time.Millisecond) {
		t.Errorf("broker-started process (pid %d) died with the outer job", p.pid)
	}
}

func TestContextCancelStopsTaskTree(t *testing.T) {
	mainJob, err := New(Options{KillOnClose: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mainJob.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	task, err := NewTask(ctx, mainJob)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = task.Close() })

	life := helperLifetime.String()
	gcFile := filepath.Join(t.TempDir(), "gc")
	cmd := HelperCmd("child", gcFile, life)
	if err := task.Start(cmd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	sib := HelperCmd("sleep", life)
	if err := mainJob.Start(sib); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sib.Process.Kill(); _ = sib.Wait() })

	gc, err := WaitKV(gcFile, waitTimeout)
	if err != nil {
		t.Fatal(err)
	}
	child := openProc(t, "task child", cmd.Process.Pid)
	grand := openProc(t, "task grandchild", pidOf(t, gc, "pid"))
	sibling := openProc(t, "sibling", sib.Process.Pid)

	for _, p := range []*proc{child, grand} {
		if !p.inJob(t, mainJob) || !p.inJob(t, task.Job()) {
			t.Fatalf("%s should be in both the main and the task job (nested)", p.name)
		}
	}
	if !sibling.inJob(t, mainJob) || sibling.inJob(t, task.Job()) {
		t.Fatalf("sibling should be in the main job only")
	}

	cancel()
	for _, p := range []*proc{child, grand} {
		if !p.exited(waitTimeout) {
			t.Errorf("%s (pid %d) still alive after cancel", p.name, p.pid)
		}
	}
	if sibling.exited(500 * time.Millisecond) {
		t.Errorf("sibling (pid %d) died with the task", sibling.pid)
	}
	pids, err := mainJob.PIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(pids) != 1 || pids[0] != sibling.pid {
		t.Errorf("main job pids = %v, want only the sibling %d", pids, sibling.pid)
	}
}
