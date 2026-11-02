package verify_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/builds"
	cv "github.com/brendatamaar/yggdrasil/internal/core/verify"
)

func failedRig(t *testing.T) *rig {
	t.Helper()
	r := newRig(t)
	r.tc.verify = []builds.Step{step("lint", 0), step("test", 3)}
	r.settle()
	if _, ok := r.state().(cv.Failed); !ok {
		t.Fatalf("state = %#v", r.state())
	}
	return r
}

func TestSendFailureIsOncePerAttempt(t *testing.T) {
	r := failedRig(t)
	id := r.records()[0].ID
	if err := r.svc.SendFailure(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	err := r.svc.SendFailure(context.Background(), id)
	if c, ok := apperr.ClassOf(err); !ok || c != apperr.Conflict {
		t.Fatalf("second send = %v, want a conflict", err)
	}
	if n := len(r.msg.Messages()); n != 1 {
		t.Fatalf("%d messages, want 1", n)
	}
	// A new attempt can be sent again.
	if _, err := r.svc.Retry(context.Background(), "tsk_1"); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if err := r.svc.SendFailure(context.Background(), id); err != nil {
		t.Fatalf("send of a new attempt: %v", err)
	}
	if n := len(r.msg.Messages()); n != 2 {
		t.Fatalf("%d messages, want 2", n)
	}
}

func TestSendFailureRefusedSendIsNotRecorded(t *testing.T) {
	r := failedRig(t)
	id := r.records()[0].ID
	r.msg.Err = apperr.New(apperr.Conflict, "the task is busy", nil)
	if err := r.svc.SendFailure(context.Background(), id); err == nil {
		t.Fatal("want the refusal")
	}
	r.msg.Err = nil
	if err := r.svc.SendFailure(context.Background(), id); err != nil {
		t.Fatalf("send after a refusal: %v", err)
	}
}

func TestSendFailureParallelCallsSendOne(t *testing.T) {
	r := failedRig(t)
	id := r.records()[0].ID
	var wg sync.WaitGroup
	var ok atomic.Int32
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r.svc.SendFailure(context.Background(), id) == nil {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	if n := len(r.msg.Messages()); n != 1 || ok.Load() != 1 {
		t.Fatalf("%d messages, %d successes, want 1 and 1", n, ok.Load())
	}
}
