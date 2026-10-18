package platformtest_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/platform/platformtest"
)

func TestFakeMeetsProcessContract(t *testing.T) {
	var f platformtest.Fake
	cmds := platformtest.FakeCommands()
	f.DirExists = func(dir string) bool { return dir != cmds.MissingDir }
	cmds.KillTask = f.KillTask
	platformtest.ProcessContract(t, f.Start, cmds)
}

func TestFakeKillTaskRecordsAndKillsOnlyThatTask(t *testing.T) {
	var f platformtest.Fake
	block := []string{platformtest.ArgvPrefix, "block"}
	env := []string{"FAKE=1"}
	a, err := f.Start(context.Background(), platform.Spec{Argv: block, Env: env, TaskID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.Start(context.Background(), platform.Spec{Argv: block, Env: env, TaskID: "b"})
	if err != nil {
		t.Fatal(err)
	}
	_ = f.KillTask("a")
	_ = f.KillTask("nobody")
	if code, _ := a.Wait(); code != platformtest.KilledExit {
		t.Errorf("task a process exit = %d", code)
	}
	done := make(chan struct{})
	go func() { _, _ = b.Wait(); close(done) }()
	select {
	case <-done:
		t.Error("KillTask(a) killed a process of task b")
	default:
	}
	_ = b.Kill()
	if got := f.KilledTasks(); len(got) != 2 || got[0] != "a" || got[1] != "nobody" {
		t.Errorf("KilledTasks = %v", got)
	}
}

func TestFakeScriptsAndRecording(t *testing.T) {
	var f platformtest.Fake
	f.Push(platformtest.Script{Stdout: "built\n", Exit: 3}, platformtest.Script{StartErr: platform.ErrNotFound})

	var out bytes.Buffer
	spec := platform.Spec{Argv: []string{"mvn", "verify"}, Env: []string{}, Stdout: &out, TaskID: "t1"}
	p, err := f.Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if code, err := p.Wait(); err != nil || code != 3 || out.String() != "built\n" {
		t.Errorf("Wait = %d, %v, stdout %q", code, err, out.String())
	}
	if _, err := f.Start(context.Background(), spec); !errors.Is(err, platform.ErrNotFound) {
		t.Errorf("second script: %v, want ErrNotFound", err)
	}
	if got := f.Started(); len(got) != 1 || got[0].TaskID != "t1" {
		t.Errorf("Started = %+v, want the one successful start", got)
	}
}
