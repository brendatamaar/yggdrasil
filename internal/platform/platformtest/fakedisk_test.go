package platformtest_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/platform/platformtest"
)

func fakeParent(d *platformtest.FakeDisk) string {
	abs, _ := filepath.Abs(filepath.FromSlash("/work/acme-shop"))
	d.Mkdir(abs)
	return abs
}

func TestFakeSafeInitFSMeetsContract(t *testing.T) {
	platformtest.RunSafeInitFSContract(t, func(t *testing.T) (platform.SafeInitFS, string) {
		d := platformtest.NewFakeDisk()
		return platformtest.NewFakeSafeInitFS(d), fakeParent(d)
	})
}

func TestFakeIgnoreWriterMeetsContract(t *testing.T) {
	platformtest.RunIgnoreWriterContract(t, func(t *testing.T) (platform.IgnoreWriter, string) {
		d := platformtest.NewFakeDisk()
		return platformtest.NewFakeIgnoreWriter(d), fakeParent(d)
	})
}

// Scripted failures stop a flow at an exact call, before or after the effect.
func TestFakeDiskScriptedFailures(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("disk unplugged")
	d := platformtest.NewFakeDisk()
	parent := fakeParent(d)
	s := platformtest.NewFakeSafeInitFS(d)

	d.FailOn("CreateChildDir", 1, boom)
	if _, err := s.CreateChildDir(ctx, parent, "a"); !errors.Is(err, boom) {
		t.Fatalf("FailOn: %v", err)
	}
	if _, err := s.CreateChildDir(ctx, parent, "a"); err != nil {
		t.Fatalf("the failed call must have no effect, so a retry works: %v", err)
	}

	d.FailAfter("CreateChildDir", 3, boom)
	if _, err := s.CreateChildDir(ctx, parent, "b"); !errors.Is(err, boom) {
		t.Fatalf("FailAfter: %v", err)
	}
	if _, err := s.CreateChildDir(ctx, parent, "b"); !errors.Is(err, platform.ErrExists) {
		t.Fatalf("a lost reply must leave the folder behind: %v", err)
	}
	if d.Calls("CreateChildDir") != 4 {
		t.Fatalf("calls = %d", d.Calls("CreateChildDir"))
	}
}
