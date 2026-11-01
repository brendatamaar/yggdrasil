package builds_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

func TestEnqueueWithIDIsIdempotentByID(t *testing.T) {
	h := newHarness(t)
	h.start()
	ctx := context.Background()
	id, err := h.svc.EnqueueWithID(ctx, "bld_fixed", steps(exit(0)), "system")
	if err != nil || id != "bld_fixed" {
		t.Fatalf("EnqueueWithID = %q, %v", id, err)
	}
	h.finished(id)
	// A second call after the build finished returns the same build and
	// starts nothing.
	again, err := h.svc.EnqueueWithID(ctx, "bld_fixed", steps(exit(0)), "system")
	if err != nil || again != id {
		t.Fatalf("second EnqueueWithID = %q, %v", again, err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := h.fake.Started(); len(got) != 1 {
		t.Errorf("%d processes started, want 1", len(got))
	}
	if n := len(h.audit("build:" + string(id))); n == 0 {
		t.Error("no audit for the first enqueue")
	}
	list, err := h.svc.List(ctx, db.BuildFilter{})
	if err != nil || len(list) != 1 {
		t.Errorf("%d builds, %v; want 1", len(list), err)
	}
	for _, bad := range []core.BuildID{"", "a b", "a/b"} {
		if _, err := h.svc.EnqueueWithID(ctx, bad, steps(exit(0)), "system"); err == nil {
			t.Errorf("EnqueueWithID(%q) accepted", bad)
		}
	}
	if _, err := h.svc.EnqueueWithID(ctx, "bld_x", corebuilds.Spec{Target: corebuilds.MainCheckout{}}, "system"); err == nil {
		t.Error("a spec with no step was accepted")
	}
}

func TestStepEnvIsAddedToTheCleanEnvironment(t *testing.T) {
	t.Setenv("YGG_TEST_LEAK", "leaked")
	h := newHarness(t)
	h.start()
	spec := steps(exit(0))
	spec.Steps[0].Env = []string{"MY_FLAG=on", "EMPTY="}
	id := h.enqueue(spec)
	h.finished(id)
	got := h.fake.Started()
	if len(got) != 1 {
		t.Fatalf("started %d", len(got))
	}
	for _, want := range []string{"CI=true", "MY_FLAG=on", "EMPTY="} {
		if !slices.Contains(got[0].Env, want) {
			t.Errorf("env lacks %q", want)
		}
	}
	for _, e := range got[0].Env {
		if strings.HasPrefix(e, "YGG_TEST_LEAK=") {
			t.Error("the process environment leaked into the step")
		}
	}
}

func TestStepEnvWithATokenNameDoesNotStart(t *testing.T) {
	h := newHarness(t)
	h.start()
	spec := steps(exit(0))
	spec.Steps[0].Env = []string{"GITHUB_TOKEN=abc"}
	id := h.enqueue(spec)
	h.finished(id)
	if got := h.fake.Started(); len(got) != 0 {
		t.Errorf("a step with a token-like env name started: %+v", got)
	}
}
