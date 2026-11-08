package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	cv "github.com/brendatamaar/yggdrasil/internal/core/verify"
	"github.com/brendatamaar/yggdrasil/internal/tasks"
)

type recHub struct {
	mu  sync.Mutex
	got []string
}

func (h *recHub) PublishVerification(id cv.VerificationID, _ core.TaskID, _ core.RepoID, state string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.got = append(h.got, string(id)+":"+state)
}

func TestLateVerifyHubForwardsOnceSet(t *testing.T) {
	var h lateVerifyHub
	h.PublishVerification("ver_1", "tsk_1", "rep_1", "passed") // nothing set: dropped, no panic
	rec := &recHub{}
	h.Set(rec)
	h.PublishVerification("ver_1", "tsk_1", "rep_1", "failed")
	if len(rec.got) != 1 || rec.got[0] != "ver_1:failed" {
		t.Fatalf("got %v", rec.got)
	}
}

type recBuilds struct{ changed, lines int }

func (r *recBuilds) PublishBuildChanged(core.BuildID, core.BuildStatus) { r.changed++ }
func (r *recBuilds) PublishBuildLog(core.BuildID, int, int, string)     { r.lines++ }

func TestWakeOnBuildForwardsAndWakes(t *testing.T) {
	next := &recBuilds{}
	w := &wakeOnBuild{next: next}
	w.PublishBuildChanged("bld_1", core.BuildSucceeded) // no service yet: forwarded only
	w.PublishBuildLog("bld_1", 0, 1, "line")
	if next.changed != 1 || next.lines != 1 {
		t.Fatalf("forwarded %d changes and %d lines", next.changed, next.lines)
	}
}

// TestVerifyServiceStartsAndStops builds the service with the wiring helpers
// and checks that its loop ends with its context.
func TestVerifyServiceStartsAndStops(t *testing.T) {
	// Store, builds, tasks and git are nil here: New only checks that the
	// dependencies are present, and the loop is cancelled before it reads.
	svc, err := newVerifyService(nil, nil, nil, nil, redactorForTest(), &lateVerifyHub{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var g errgroup.Group
	startVerify(&g, ctx, svc)
	done := make(chan error, 1)
	go func() { done <- g.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the verify loop did not stop")
	}
}

// A refusal for the missing opt-in is a known refusal in both shells and
// carries the needs-opt-in mark for the API's 409.
func TestOptInRefusalIsKnownAndMarked(t *testing.T) {
	rej := &tasks.RejectedError{Reason: "needs_opt_in", Message: "This mode edits your main checkout directly. Turn on the opt-in to use it."}
	got := verifyRefusal(rej)
	if c, ok := apperr.ClassOf(got); !ok || c != apperr.Conflict || !errors.Is(got, apperr.ErrNeedsOptIn) {
		t.Errorf("verifyRefusal = %v", got)
	}
	if m, _ := apperr.MessageOf(got); m != rej.Message {
		t.Errorf("message = %q", m)
	}
	n := noteRefusal(rej)
	var u interface{ UserMessage() string }
	if !errors.Is(n, apperr.ErrNeedsOptIn) || !errors.As(n, &u) || u.UserMessage() != rej.Message {
		t.Errorf("noteRefusal = %v", n)
	}
	// Other refusals are not marked.
	if errors.Is(verifyRefusal(&tasks.RejectedError{Reason: "task_busy", Message: "busy"}), apperr.ErrNeedsOptIn) {
		t.Error("a busy task is not an opt-in refusal")
	}
}
