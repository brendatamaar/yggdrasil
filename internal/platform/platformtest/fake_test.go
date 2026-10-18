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
	platformtest.ProcessContract(t, f.Start, cmds)
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
