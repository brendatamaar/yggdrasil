//go:build windows

package platform

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Documented on Start: a TaskID process's context cancel kills the whole task
// job, siblings of the same task included.
func TestContextCancelOfOneProcessKillsWholeTaskJob(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	_, child1, grand1 := startTree(t, ctx, "shared-cancel")
	_, child2, grand2 := startTree(t, context.Background(), "shared-cancel")
	_, childOther, grandOther := startTree(t, context.Background(), "other-task")

	cancel()
	for _, p := range []*proc{child1, grand1, child2, grand2} {
		if !p.exited(waitTimeout) {
			t.Errorf("%s (pid %d) of the cancelled task is still alive", p.name, p.pid)
		}
	}
	for _, p := range []*proc{childOther, grandOther} {
		if p.exited(0) {
			t.Errorf("%s (pid %d) of another task died", p.name, p.pid)
		}
	}
}

// When every process of a task has exited, the task job closes and takes
// detached grandchildren with it.
func TestTaskJobClosesWhenAllProcessesExit(t *testing.T) {
	const id = "task-exits"
	pidFile := filepath.Join(t.TempDir(), "gc")
	spec := testEnvSpec(t, "orphan", pidFile, helperLife.String())
	spec.TaskID = id
	p, err := Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Kill() })
	pids, err := waitPIDs(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	gc := openProc(t, "detached grandchild", pids["pid"])
	if code, err := p.Wait(); err != nil || code != 0 {
		t.Fatalf("Wait = %d, %v", code, err)
	}
	if !gc.exited(waitTimeout) {
		t.Errorf("grandchild (pid %d) survived the end of its task", gc.pid)
	}
	jobs.mu.Lock()
	_, open := jobs.tasks[id]
	jobs.mu.Unlock()
	if open {
		t.Error("task job is still registered after its last process exited")
	}
}

func TestOpenFolderNeedsSystemRoot(t *testing.T) {
	// No SystemRoot: must fail, not search the PATH for an explorer.exe.
	env := []string{"PATH=" + os.Getenv("PATH")}
	if err := OpenFolder(context.Background(), t.TempDir(), env); err == nil || !strings.Contains(err.Error(), "SystemRoot") {
		t.Errorf("OpenFolder without SystemRoot = %v, want a SystemRoot error", err)
	}
}

// A path starting with "-" must reach the editor as an absolute path.
func TestOpenEditorMakesPathAbsolute(t *testing.T) {
	if selfInJob(t) {
		t.Skip("inside an outer job: the editor may start via Explorer with another environment")
	}
	t.Chdir(t.TempDir())
	out := filepath.Join(t.TempDir(), "argv")
	env := append(helperEnv(), argvOutEnvVar+"="+out)
	if err := OpenEditor(context.Background(), selfExe(t), "-flag", env); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs("-flag")
	if err != nil {
		t.Fatal(err)
	}
	var got string
	for range 200 {
		if data, err := os.ReadFile(out); err == nil && len(data) > 0 {
			got = string(data)
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.EqualFold(got, want) {
		t.Errorf("editor got %q, want %q", got, want)
	}
}
