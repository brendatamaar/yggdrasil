package verify_test

import (
	"context"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/builds"
	cv "github.com/brendatamaar/yggdrasil/internal/core/verify"
)

func TestFailureDetailNamesTheStepAndRedactsTheTail(t *testing.T) {
	r := newRig(t)
	r.tc.verify = []builds.Step{step("lint", 0), step("test", 3)}
	r.settle()
	rec := r.records()[0]
	f := rec.State.(cv.Failed)
	r.runner.SetLog(f.Build, 1, "FAIL TestX\ntoken="+secret+"\n")
	stepName, tail := r.svc.FailureDetail(context.Background(), rec)
	if stepName != "test" {
		t.Fatalf("step = %q, want test", stepName)
	}
	if !strings.Contains(tail, "FAIL TestX") {
		t.Fatalf("tail lacks the log: %q", tail)
	}
	if strings.Contains(tail, secret) {
		t.Fatalf("tail leaks the secret: %q", tail)
	}
}

func TestFailureDetailIsEmptyForAPassedRecord(t *testing.T) {
	r := newRig(t)
	r.settle()
	rec := r.records()[0]
	if _, failed := rec.State.(cv.Failed); failed {
		t.Skip("rig failed unexpectedly")
	}
	if s, tail := r.svc.FailureDetail(context.Background(), rec); s != "" || tail != "" {
		t.Fatalf("got %q %q", s, tail)
	}
}
