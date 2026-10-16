//go:build windows

package platform

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	// helperLife is a backstop: a helper that leaks anyway exits on its own.
	helperLife  = 2 * time.Minute
	waitTimeout = 10 * time.Second

	// helperEnvVar marks a re-executed test binary as a helper; the mode is
	// os.Args[1]. Tests never start a real editor: the "editor" is this
	// binary, recognised by its first argument, which also works when
	// Explorer starts it with an environment that is not ours.
	helperEnvVar    = "YGG_PLATFORM_HELPER"
	editorArgPrefix = "editor-sleep:"
	argvOutEnvVar   = "YGG_PLATFORM_ARGV_OUT"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		// Recording "editor": writes its first argument to the file and exits.
		if out := os.Getenv(argvOutEnvVar); out != "" {
			exitOn(os.WriteFile(out, []byte(os.Args[1]), 0o600))
			os.Exit(0)
		}
		if pidFile, ok := strings.CutPrefix(os.Args[1], editorArgPrefix); ok {
			exitOn(writePIDs(pidFile, map[string]int{"pid": os.Getpid()}))
			time.Sleep(helperLife)
			os.Exit(0)
		}
		if os.Getenv(helperEnvVar) == "1" {
			exitOn(runHelper(os.Args[1], os.Args[2:]))
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

func exitOn(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

// helperEnv is a minimal explicit environment (tests never use os.Environ).
func helperEnv() []string {
	return []string{
		"SystemRoot=" + os.Getenv("SystemRoot"),
		"PATH=" + os.Getenv("PATH"),
		"TEMP=" + os.TempDir(),
		"TMP=" + os.TempDir(),
		helperEnvVar + "=1",
	}
}

func selfExe(t testing.TB) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

// runHelper runs the helper modes:
//
//	sleep <lifetime>
//	pwd                          print the working directory
//	exit <code>
//	child <pidfile> <lifetime>   start a "sleep" grandchild, write its pid, sleep
//	orphan <pidfile> <lifetime>  like child, but exit at once and leave the grandchild
//	listen <portfile>            listen on 127.0.0.1, write the port, sleep
//	spawn-listen <portfile>      start a "listen" grandchild, sleep
//	ygg <out> <gate|nogate> <editor|noeditor>
func runHelper(mode string, args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	switch mode {
	case "sleep":
		d, err := time.ParseDuration(args[0])
		if err != nil {
			return err
		}
		time.Sleep(d)
	case "pwd":
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		fmt.Println(wd)
	case "exit":
		n, _ := strconv.Atoi(args[0])
		os.Exit(n)
	case "child":
		gc := exec.Command(exe, "sleep", args[1])
		gc.Env = helperEnv()
		if err := gc.Start(); err != nil {
			return fmt.Errorf("start grandchild: %w", err)
		}
		if err := writePIDs(args[0], map[string]int{"pid": gc.Process.Pid}); err != nil {
			return err
		}
		d, err := time.ParseDuration(args[1])
		if err != nil {
			return err
		}
		time.Sleep(d)
	case "orphan":
		gc := exec.Command(exe, "sleep", args[1])
		gc.Env = helperEnv()
		if err := gc.Start(); err != nil {
			return fmt.Errorf("start grandchild: %w", err)
		}
		return writePIDs(args[0], map[string]int{"pid": gc.Process.Pid})
	case "listen":
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return err
		}
		defer ln.Close()
		if err := writePIDs(args[0], map[string]int{"port": ln.Addr().(*net.TCPAddr).Port}); err != nil {
			return err
		}
		time.Sleep(helperLife)
	case "spawn-listen":
		gc := exec.Command(exe, "listen", args[0])
		gc.Env = helperEnv()
		if err := gc.Start(); err != nil {
			return fmt.Errorf("start grandchild: %w", err)
		}
		time.Sleep(helperLife)
	case "ygg":
		return runYgg(exe, args[0], args[1] == "gate", args[2] == "editor")
	default:
		return fmt.Errorf("unknown helper mode %q", mode)
	}
	return nil
}

// runYgg acts as Yggdrasil: it starts child -> grandchild through Start,
// optionally an "editor" through OpenEditor, writes the pids to out and sleeps.
func runYgg(exe, out string, gate, editor bool) error {
	if gate {
		// Wait until the test has put us in its outer job.
		if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
			return fmt.Errorf("read gate: %w", err)
		}
	}
	ctx := context.Background()
	env := helperEnv()
	p, err := Start(ctx, Spec{Argv: []string{exe, "child", out + ".gc", helperLife.String()}, Env: env, TaskID: "t1"})
	if err != nil {
		return err
	}
	gc, err := waitPIDs(out + ".gc")
	if err != nil {
		return err
	}
	pids := map[string]int{"child": p.Pid(), "grandchild": gc["pid"]}
	if editor {
		if err := OpenEditor(ctx, exe, editorArgPrefix+out+".ed", env); err != nil {
			return err
		}
		ed, err := waitPIDs(out + ".ed")
		if err != nil {
			return err
		}
		pids["editor"] = ed["pid"]
	}
	if err := writePIDs(out, pids); err != nil {
		return err
	}
	time.Sleep(helperLife)
	return nil
}

// writePIDs writes name=pid lines atomically, so a poller never sees half a file.
func writePIDs(path string, pids map[string]int) error {
	var b strings.Builder
	for k, v := range pids {
		fmt.Fprintf(&b, "%s=%d\n", k, v)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func waitPIDs(path string) (map[string]int, error) {
	deadline := time.Now().Add(waitTimeout)
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			pids := map[string]int{}
			for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
				if k, v, ok := strings.Cut(line, "="); ok {
					pids[k], _ = strconv.Atoi(v)
				}
			}
			return pids, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("wait for %s: %w", path, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

type proc struct {
	name string
	pid  int
	h    windows.Handle
}

// openProc opens pid for waiting and killing. Holding the handle keeps the pid
// from being reused; t.Cleanup kills the process whatever the outcome.
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

func (p *proc) inJob(t *testing.T, job windows.Handle) bool {
	t.Helper()
	in, err := isProcessInJob(p.h, job)
	if err != nil {
		t.Fatalf("%s: %v", p.name, err)
	}
	return in
}

func selfInJob(t *testing.T) bool {
	t.Helper()
	in, err := isProcessInJob(windows.CurrentProcess(), 0)
	if err != nil {
		t.Fatal(err)
	}
	return in
}

// startYgg starts the "ygg" helper. With outer set, it is put into that job
// before it does anything.
func startYgg(t *testing.T, outer windows.Handle, editor bool) (ygg *proc, procs map[string]*proc) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "pids")
	gate, ed := "nogate", "noeditor"
	if outer != 0 {
		gate = "gate"
	}
	if editor {
		ed = "editor"
	}
	cmd := exec.Command(selfExe(t), "ygg", out, gate, ed)
	cmd.Env = helperEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.WaitDelay = 5 * time.Second
	var stdin io.WriteCloser
	if outer != 0 {
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
	})
	if outer != 0 {
		h, err := openForJob(cmd.Process.Pid)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = windows.CloseHandle(h) }()
		if err := windows.AssignProcessToJobObject(outer, h); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(stdin, "go\n"); err != nil {
			t.Fatal(err)
		}
	}
	pids, err := waitPIDs(out)
	if err != nil {
		t.Fatalf("%v (ygg stderr: %s)", err, stderr.String())
	}
	procs = map[string]*proc{}
	for name, pid := range pids {
		procs[name] = openProc(t, name, pid)
	}
	return openProc(t, "ygg", cmd.Process.Pid), procs
}

func testEnvSpec(t *testing.T, argv ...string) Spec {
	t.Helper()
	return Spec{Argv: append([]string{selfExe(t)}, argv...), Env: helperEnv()}
}

func TestStartOutputExitCodeAndDirWithSpaces(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dir with spaces")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	// The program itself lives in a folder with spaces too.
	exe := filepath.Join(dir, "my helper.exe")
	data, err := os.ReadFile(selfExe(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, data, 0o700); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	spec := Spec{Argv: []string{exe, "pwd"}, Dir: dir, Env: helperEnv(), Stdout: &out}
	p, err := Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if code, err := p.Wait(); err != nil || code != 0 {
		t.Fatalf("Wait = %d, %v", code, err)
	}
	want, err := LongPath(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); !strings.EqualFold(got, want) {
		t.Errorf("working directory = %q, want %q", got, want)
	}

	// A non-zero exit code is not an error, and Wait may be called twice.
	p, err = Start(context.Background(), testEnvSpec(t, "exit", "7"))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if code, err := p.Wait(); err != nil || code != 7 {
			t.Errorf("Wait = %d, %v; want 7, nil", code, err)
		}
	}
}

func TestStartLowerCaseDriveLetter(t *testing.T) {
	dir, err := LongPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if dir[1] != ':' {
		t.Skipf("temp dir %q has no drive letter", dir)
	}
	lower := strings.ToLower(dir[:1]) + dir[1:]
	if got, err := LongPath(lower); err != nil || got != dir {
		t.Errorf("LongPath(%q) = %q, %v; want %q", lower, got, err, dir)
	}
	var out bytes.Buffer
	spec := testEnvSpec(t, "pwd")
	spec.Dir, spec.Stdout = lower, &out
	p, err := Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Wait(); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != dir {
		t.Errorf("working directory = %q, want %q", got, dir)
	}
}

func TestStartRefusesWSLDir(t *testing.T) {
	spec := testEnvSpec(t, "pwd")
	spec.Dir = `\\wsl$\Ubuntu\home\dev\repo`
	if _, err := Start(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "WSL") {
		t.Errorf("Start in a WSL dir: %v, want the WSL message", err)
	}
}

func TestLongPathExpandsShortNames(t *testing.T) {
	if _, err := LongPath(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("LongPath of a missing folder should fail")
	}
	base, err := LongPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	long := filepath.Join(base, "a long directory name")
	if err := os.Mkdir(long, 0o750); err != nil {
		t.Fatal(err)
	}
	src, _ := windows.UTF16PtrFromString(long)
	buf := make([]uint16, 1024)
	n, err := windows.GetShortPathName(src, &buf[0], uint32(len(buf)))
	if err != nil {
		t.Fatal(err)
	}
	short := windows.UTF16ToString(buf[:n])
	if !strings.Contains(short, "~") {
		t.Skipf("8.3 names are off on this volume (%s)", short)
	}
	got, err := LongPath(short)
	if err != nil || got != long {
		t.Errorf("LongPath(%q) = %q, %v; want %q", short, got, err, long)
	}
}

func TestLongPathOver260(t *testing.T) {
	base, err := LongPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	deep := base
	for len(deep) < 300 {
		deep = filepath.Join(deep, "segment-of-thirty-characters-x")
	}
	if err := os.MkdirAll(deep, 0o750); err != nil {
		t.Fatal(err)
	}
	got, err := LongPath(deep)
	if err != nil || got != deep {
		t.Errorf("LongPath of a %d character path = %q, %v", len(deep), got, err)
	}
}

// TestShellExecuteStartsViaExplorer: the Explorer COM path on its own. The
// started process is not our child.
func TestShellExecuteStartsViaExplorer(t *testing.T) {
	if windows.GetShellWindow() == 0 {
		t.Skip("no desktop shell (Explorer) in this session")
	}
	pidFile := filepath.Join(t.TempDir(), "ed")
	if err := shellExecute(selfExe(t), windows.ComposeCommandLine([]string{editorArgPrefix + pidFile}), ""); err != nil {
		t.Fatal(err)
	}
	pids, err := waitPIDs(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	ed := openProc(t, "explorer-started", pids["pid"])

	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = windows.CloseHandle(snap) }()
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
		if int(entry.ProcessID) == ed.pid {
			if int(entry.ParentProcessID) == os.Getpid() {
				t.Error("the process is our child, not Explorer's")
			}
			return
		}
	}
	t.Error("started process not found in the process list")
}

func TestResolveWindowsExtensions(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "node bin")
	if err := os.MkdirAll(bin, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"npm", "npm.cmd", "tool.exe", "script.bat"} {
		if err := os.WriteFile(filepath.Join(bin, f), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// "Path" in mixed case, as Windows names it.
	env := []string{"Path=" + bin}
	for name, want := range map[string]string{
		"npm":        "npm.cmd", // not the extension-less shell script
		"tool":       "tool.exe",
		"script.bat": "script.bat",
	} {
		got, err := Resolve(name, "", env)
		// The extension comes back in PATHEXT's case (.CMD).
		if err != nil || !strings.EqualFold(got, filepath.Join(bin, want)) {
			t.Errorf("Resolve(%q) = %q, %v; want %q", name, got, err, want)
		}
	}
	// PATHEXT of env decides.
	env = append(env, "PATHEXT=.BAT")
	if _, err := Resolve("tool", "", env); err == nil {
		t.Error("tool.exe resolved although PATHEXT is only .BAT")
	}
	// An absolute path needs no PATH.
	if got, err := Resolve(filepath.Join(bin, "tool.exe"), "", []string{}); err != nil || got != filepath.Join(bin, "tool.exe") {
		t.Errorf("Resolve(absolute) = %q, %v", got, err)
	}
}

func TestChildrenDieWhenJobCloses(t *testing.T) {
	ygg, procs := startYgg(t, 0, false)
	for _, name := range []string{"child", "grandchild"} {
		if procs[name].inJob(t, 0) == false {
			t.Fatalf("%s is in no job", name)
		}
	}
	if err := windows.TerminateProcess(ygg.h, 1); err != nil {
		t.Fatalf("terminate ygg: %v", err)
	}
	for _, name := range []string{"child", "grandchild"} {
		if p := procs[name]; !p.exited(waitTimeout) {
			t.Errorf("%s (pid %d) still alive %v after ygg died", name, p.pid, waitTimeout)
		}
	}
}

func TestEditorSurvivesJobClose(t *testing.T) {
	ygg, procs := startYgg(t, 0, true)
	if !selfInJob(t) && procs["editor"].inJob(t, 0) {
		t.Error("editor is in a job")
	}
	if err := windows.TerminateProcess(ygg.h, 1); err != nil {
		t.Fatalf("terminate ygg: %v", err)
	}
	for _, name := range []string{"child", "grandchild"} {
		if p := procs[name]; !p.exited(waitTimeout) {
			t.Errorf("%s (pid %d) still alive after ygg died", name, p.pid)
		}
	}
	if p := procs["editor"]; p.exited(time.Second) {
		t.Errorf("editor (pid %d) died with ygg's jobs", p.pid)
	}
}

// TestEditorSurvivesOuterJob: ygg itself sits in an outer job that forbids
// breakaway (a terminal, an IDE, CI). The editor must still survive, via
// Explorer.
func TestEditorSurvivesOuterJob(t *testing.T) {
	if windows.GetShellWindow() == 0 {
		t.Skip("no desktop shell (Explorer) in this session")
	}
	outer, err := newJob()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = windows.CloseHandle(outer) })

	ygg, procs := startYgg(t, outer, true)
	if procs["editor"].inJob(t, outer) {
		t.Fatal("editor is in the outer job")
	}
	for _, name := range []string{"child", "grandchild"} {
		if !procs[name].inJob(t, outer) {
			t.Errorf("%s is not nested in the outer job", name)
		}
	}
	if err := windows.CloseHandle(outer); err != nil {
		t.Fatal(err)
	}
	for _, p := range []*proc{ygg, procs["child"], procs["grandchild"]} {
		if !p.exited(waitTimeout) {
			t.Errorf("%s (pid %d) still alive after the outer job closed", p.name, p.pid)
		}
	}
	if p := procs["editor"]; p.exited(time.Second) {
		t.Errorf("editor (pid %d) died with the outer job", p.pid)
	}
}

// startTree starts child -> grandchild through Start and returns both.
func startTree(t *testing.T, ctx context.Context, taskID string) (Process, *proc, *proc) {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "gc")
	spec := testEnvSpec(t, "child", pidFile, helperLife.String())
	spec.TaskID = taskID
	p, err := Start(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Kill() })
	gc, err := waitPIDs(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	return p, openProc(t, "child of "+taskID, p.Pid()), openProc(t, "grandchild of "+taskID, gc["pid"])
}

func TestContextCancelKillsOnlyThatTasksTree(t *testing.T) {
	ctxA, cancelA := context.WithCancel(context.Background())
	ctxB, cancelB := context.WithCancel(context.Background())
	t.Cleanup(cancelA)
	t.Cleanup(cancelB)

	pa, childA, grandA := startTree(t, ctxA, "task-a")
	_, childB, grandB := startTree(t, ctxB, "task-b")
	_, childC, grandC := startTree(t, context.Background(), "")

	cancelA()
	for _, p := range []*proc{childA, grandA} {
		if !p.exited(waitTimeout) {
			t.Errorf("%s (pid %d) still alive after cancel", p.name, p.pid)
		}
	}
	if code, err := pa.Wait(); err != nil || code == 0 {
		t.Errorf("Wait after cancel = %d, %v; want a non-zero exit code", code, err)
	}
	for _, p := range []*proc{childB, grandB, childC, grandC} {
		if p.exited(500 * time.Millisecond) {
			t.Errorf("%s (pid %d) died with task A", p.name, p.pid)
		}
	}
	// Without a TaskID, cancel still kills just that process's tree.
	cancelB()
	if !childB.exited(waitTimeout) || !grandB.exited(waitTimeout) {
		t.Error("task B still alive after its cancel")
	}
	if childC.exited(500 * time.Millisecond) {
		t.Error("task-less process died with task B")
	}
}

func TestKillStopsOnlyThatProcessesTree(t *testing.T) {
	p1, child1, grand1 := startTree(t, context.Background(), "shared")
	_, child2, grand2 := startTree(t, context.Background(), "shared")
	if err := p1.Kill(); err != nil {
		t.Fatal(err)
	}
	if !child1.exited(waitTimeout) || !grand1.exited(waitTimeout) {
		t.Error("killed process tree still alive")
	}
	if child2.exited(500*time.Millisecond) || grand2.exited(0) {
		t.Error("Kill took down a sibling of the same task")
	}
	if err := p1.Kill(); err != nil {
		t.Errorf("second Kill: %v", err)
	}
}

func TestStartCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Start(ctx, testEnvSpec(t, "sleep", "1m")); err == nil {
		t.Error("Start with a cancelled context should fail")
	}
}
