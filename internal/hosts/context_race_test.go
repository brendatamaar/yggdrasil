package hosts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

// Deterministic tests of the races between callers of one key (GH-05, review
// follow-up): no time.Sleep, the interleavings are forced by the test hooks of
// ContextService, the fake sleeper and a scripted client.

type contextAnswerOrErr struct {
	out ContextOutcome
	err error
}

func waitContextAnswer(t *testing.T, ch <-chan contextAnswerOrErr, what string) contextAnswerOrErr {
	t.Helper()
	select {
	case a := <-ch:
		return a
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: no answer", what)
	}
	return contextAnswerOrErr{}
}

func (h *contextHarness) readAsync() <-chan contextAnswerOrErr {
	ch := make(chan contextAnswerOrErr, 1)
	go func() {
		out, err := h.read()
		ch <- contextAnswerOrErr{out, err}
	}()
	return ch
}

// The owner's answer is taken in the same critical section as the step that
// produced it. Another call that changes the revision right after the step
// (forced here through afterStep) empties the entry; the owner must still return
// the value it just published, never a success without an answer.
func TestContextShellPublishedAnswerSurvivesAChangeAfterTheStep(t *testing.T) {
	h := newContextHarness(t, func(int) (ContextResult, error) { return okIssues() })
	parked := make(chan struct{}, 1)
	h.c.onJoin = func() {
		select {
		case parked <- struct{}{}:
		default:
		}
	}
	var once sync.Once
	var second <-chan contextAnswerOrErr
	h.c.afterStep = func() {
		once.Do(func() {
			h.setRevision("v2")
			second = h.readAsync()
			// The second call stepped ScopeChanged (the entry is Empty now) and is
			// parked on the owner's read.
			select {
			case <-parked:
			case <-time.After(5 * time.Second):
				t.Error("the second call never reached the owner's read")
			}
		})
	}
	out, err := h.read()
	if err != nil || out.Result == nil {
		t.Fatalf("the owner's published value was lost: %+v %v", out, err)
	}
	if got := out.Result.Value.(corehosts.IssueList); len(got.Items) != 1 {
		t.Fatalf("%+v", got)
	}
	// The second call (the `changed && done != nil` branch of Read) then reads
	// under the new revision.
	a := waitContextAnswer(t, second, "second call")
	if a.err != nil || a.out.Result == nil {
		t.Fatalf("second call: %+v %v", a.out, a.err)
	}
	if h.client.calls.Load() != 2 {
		t.Fatalf("reads = %d, want 2 (one per revision)", h.client.calls.Load())
	}
}

// A call that finds the target changed while another call's read is in flight
// cancels that read, waits for its owner to leave and reads again itself. The
// overtaken owner hears "superseded", not "not tracked".
func TestContextShellChangeWhileReadingCancelsTheOwnerAndReadsAgain(t *testing.T) {
	h := newContextHarness(t, func(int) (ContextResult, error) { return okIssues() })
	h.client.started = make(chan struct{}, 2)
	h.client.gate = make(chan struct{})
	first := h.readAsync()
	select {
	case <-h.client.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the read did not start")
	}
	h.setRevision("v2")
	second := h.readAsync()
	a := waitContextAnswer(t, first, "overtaken owner")
	if !errors.Is(a.err, ErrContextSuperseded) || errors.Is(a.err, ErrContextScope) {
		t.Fatalf("overtaken owner: %+v %v", a.out, a.err)
	}
	close(h.client.gate)
	b := waitContextAnswer(t, second, "second call")
	if b.err != nil || b.out.Result == nil {
		t.Fatalf("second call: %+v %v", b.out, b.err)
	}
	if h.client.calls.Load() != 2 {
		t.Fatalf("reads = %d", h.client.calls.Load())
	}
}

// Item 2: a configuration or target change in the middle of a read is
// "superseded, ask again", never "not tracked".
func TestContextShellMidReadChangeIsSupersededNotScope(t *testing.T) {
	for name, change := range map[string]func(h *contextHarness){
		"token rotated":    func(h *contextHarness) { h.setToken("rotated-secret") },
		"revision changed": func(h *contextHarness) { h.setRevision("v2") },
		"token replaced directly": func(h *contextHarness) {
			h.s.mu.Lock()
			h.s.cfg.tokens[h.inst.ID] = "other-secret"
			h.s.mu.Unlock()
		},
	} {
		t.Run(name, func(t *testing.T) {
			var h *contextHarness
			h = newContextHarness(t, func(n int) (ContextResult, error) {
				if n == 1 {
					change(h)
				}
				return okIssues()
			})
			_, err := h.read()
			if !errors.Is(err, ErrContextSuperseded) || errors.Is(err, ErrContextScope) {
				t.Fatalf("got %v, want ErrContextSuperseded", err)
			}
			// Asking again gives a fresh, successful read.
			out, err := h.read()
			if err != nil || out.Result == nil {
				t.Fatalf("ask again: %+v %v", out, err)
			}
			if h.client.calls.Load() != 2 {
				t.Fatalf("reads = %d", h.client.calls.Load())
			}
		})
	}
	// A resolver that says the repo is gone in the middle of a read stays a
	// scope error.
	h := newContextHarness(t, func(int) (ContextResult, error) { return okIssues() })
	calls := 0
	h.setResolve(func(corehosts.ContextKey) error {
		calls++
		if calls == 3 {
			return ErrContextScope
		}
		return nil
	})
	if _, err := h.read(); !errors.Is(err, ErrContextScope) || errors.Is(err, ErrContextSuperseded) {
		t.Fatalf("got %v, want ErrContextScope", err)
	}
}

// The overtaken owner of a rate-limit or retry wait is told the same.
func TestContextShellChangeDuringRetryWaitIsSuperseded(t *testing.T) {
	h := newContextHarness(t, func(n int) (ContextResult, error) {
		if n == 1 {
			return ContextResult{}, fetchErr(corehosts.FailTransient, "host busy")
		}
		return okIssues()
	})
	h.sleep.block = true
	h.sleep.entered = make(chan struct{}, 1)
	first := h.readAsync()
	select {
	case <-h.sleep.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the owner never waited")
	}
	h.sleep.block = false
	h.setToken("rotated-secret")
	second := h.readAsync() // steps ConfigChanged, which cancels the owner's wait
	a := waitContextAnswer(t, first, "overtaken owner")
	if !errors.Is(a.err, ErrContextSuperseded) || errors.Is(a.err, ErrContextScope) {
		t.Fatalf("overtaken owner: %+v %v", a.out, a.err)
	}
	b := waitContextAnswer(t, second, "second call")
	if b.err != nil || b.out.Result == nil {
		t.Fatalf("second call: %+v %v", b.out, b.err)
	}
}

func (h *contextHarness) keyN(n int) corehosts.ContextKey {
	return corehosts.ContextKey{Repo: core.RepoID("rep_" + fmt.Sprint(n)), Kind: corehosts.ContextIssues}
}

func (h *contextHarness) slotCount() int {
	h.c.mu.Lock()
	defer h.c.mu.Unlock()
	return len(h.c.slots)
}

func (h *contextHarness) hasSlot(key corehosts.ContextKey) bool {
	h.c.mu.Lock()
	defer h.c.mu.Unlock()
	_, ok := h.c.slots[key]
	return ok
}

// Item 4: the slot map is capped, the least recently used idle slot goes first.
func TestContextShellSlotMapIsCapped(t *testing.T) {
	h := newContextHarness(t, func(int) (ContextResult, error) { return okIssues() })
	for i := 1; i <= contextMaxSlots+50; i++ {
		if _, err := h.readKey(h.keyN(i)); err != nil {
			t.Fatal(i, err)
		}
		if n := h.slotCount(); n > contextMaxSlots {
			t.Fatalf("after %d keys the map holds %d slots, cap %d", i, n, contextMaxSlots)
		}
		h.clock.Advance(time.Millisecond) // distinct use times, well inside the 60 s TTL
	}
	if h.hasSlot(h.keyN(1)) {
		t.Fatal("the least recently used slot survived")
	}
	if !h.hasSlot(h.keyN(contextMaxSlots + 50)) {
		t.Fatal("the newest slot was evicted")
	}
	// A key used again moves to the front of the line.
	oldest := h.keyN(51)
	if !h.hasSlot(oldest) {
		t.Fatalf("setup: %v should still be held", oldest)
	}
	if _, err := h.readKey(oldest); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(time.Millisecond)
	if _, err := h.readKey(h.keyN(10_000)); err != nil {
		t.Fatal(err)
	}
	if !h.hasSlot(oldest) || h.hasSlot(h.keyN(52)) {
		t.Fatal("eviction ignored recent use")
	}
}

// Item 4: an expired idle slot is dropped on the next insert, even when the map
// is far from the cap.
func TestContextShellExpiredIdleSlotsAreEvictedOnInsert(t *testing.T) {
	h := newContextHarness(t, func(int) (ContextResult, error) { return okIssues() })
	if _, err := h.readKey(h.keyN(1)); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(corehosts.ContextCacheTTL - time.Second)
	if _, err := h.readKey(h.keyN(2)); err != nil {
		t.Fatal(err)
	}
	if h.slotCount() != 2 {
		t.Fatalf("a slot inside its TTL was evicted: %d", h.slotCount())
	}
	h.clock.Advance(2 * time.Second) // key 1 is now expired, key 2 is not
	if _, err := h.readKey(h.keyN(3)); err != nil {
		t.Fatal(err)
	}
	if h.hasSlot(h.keyN(1)) || !h.hasSlot(h.keyN(2)) || !h.hasSlot(h.keyN(3)) {
		t.Fatalf("expired eviction: 1=%v 2=%v 3=%v", h.hasSlot(h.keyN(1)), h.hasSlot(h.keyN(2)), h.hasSlot(h.keyN(3)))
	}
}

// Item 4: a slot with a read in flight is never evicted, even when it is the
// least recently used one of a full map.
func TestContextShellInFlightSlotSurvivesEviction(t *testing.T) {
	h := newContextHarness(t, func(n int) (ContextResult, error) {
		if n == 1 {
			return ContextResult{}, fetchErr(corehosts.FailTransient, "host busy")
		}
		return okIssues()
	})
	h.sleep.block = true
	h.sleep.entered = make(chan struct{}, 1)
	ownerCtx, cancelOwner := context.WithCancel(context.Background())
	defer cancelOwner()
	ownerDone := make(chan error, 1)
	go func() {
		_, err := h.c.Read(ownerCtx, h.keyN(0))
		ownerDone <- err
	}()
	select {
	case <-h.sleep.entered: // the owner waits for its retry: a read in flight
	case <-time.After(5 * time.Second):
		t.Fatal("the owner never waited")
	}
	for i := 1; i <= contextMaxSlots+20; i++ {
		h.clock.Advance(time.Millisecond)
		if _, err := h.readKey(h.keyN(i)); err != nil {
			t.Fatal(i, err)
		}
	}
	if !h.hasSlot(h.keyN(0)) {
		t.Fatal("a slot with a read in flight was evicted")
	}
	if n := h.slotCount(); n > contextMaxSlots {
		t.Fatalf("map holds %d slots, cap %d", n, contextMaxSlots)
	}
	cancelOwner()
	select {
	case err := <-ownerDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the owner was not released")
	}
}

// Item 5: every kind goes through readOnce with its own arguments.
func TestContextShellReadsEveryKind(t *testing.T) {
	head := strings.Repeat("a", 40)
	moved := strings.Repeat("b", 40)
	t.Run("issue", func(t *testing.T) {
		var seen int
		h := newContextHarness(t, func(int) (ContextResult, error) { return okIssues() })
		h.client.issue = func(n int) (ContextResult, error) {
			seen = n
			return ContextResult{Value: corehosts.IssueDetail{Summary: corehosts.IssueSummary{Number: n, Title: "bug"}}}, nil
		}
		out, err := h.readKey(corehosts.ContextKey{Repo: "rep_1", Kind: corehosts.ContextIssue, ObjectID: "3"})
		if err != nil || out.Result == nil || seen != 3 {
			t.Fatalf("%+v %v number %d", out, err, seen)
		}
		if d := out.Result.Value.(corehosts.IssueDetail); d.Summary.Number != 3 {
			t.Fatal(d)
		}
	})
	t.Run("issue that is another one", func(t *testing.T) {
		h := newContextHarness(t, func(int) (ContextResult, error) { return okIssues() })
		h.client.issue = func(int) (ContextResult, error) {
			return ContextResult{Value: corehosts.IssueDetail{Summary: corehosts.IssueSummary{Number: 4}}}, nil
		}
		out, err := h.readKey(corehosts.ContextKey{Repo: "rep_1", Kind: corehosts.ContextIssue, ObjectID: "3"})
		if got := blockedReason(t, out, err); got != corehosts.ContextNotFound {
			t.Fatal(got)
		}
	})
	t.Run("mr diff", func(t *testing.T) {
		var seen int
		h := newContextHarness(t, func(int) (ContextResult, error) { return okIssues() })
		h.client.mr = func(n int) (ContextResult, error) {
			seen = n
			return ContextResult{Value: corehosts.MRContext{MR: corehosts.MergeRequest{Number: n}, Head: head, Diff: "+x"}}, nil
		}
		out, err := h.readKey(corehosts.ContextKey{Repo: "rep_1", Kind: corehosts.ContextMRDiff, ObjectID: "7", Head: head})
		if err != nil || out.Result == nil || seen != 7 {
			t.Fatalf("%+v %v number %d", out, err, seen)
		}
	})
	t.Run("mr diff whose head moved is not found", func(t *testing.T) {
		h := newContextHarness(t, func(int) (ContextResult, error) { return okIssues() })
		h.client.mr = func(n int) (ContextResult, error) {
			return ContextResult{Value: corehosts.MRContext{MR: corehosts.MergeRequest{Number: n}, Head: moved}}, nil
		}
		key := corehosts.ContextKey{Repo: "rep_1", Kind: corehosts.ContextMRDiff, ObjectID: "7", Head: head}
		out, err := h.readKey(key)
		if got := blockedReason(t, out, err); got != corehosts.ContextNotFound {
			t.Fatal(got)
		}
		// A request for the new head is a different key and reads again.
		newKey := key
		newKey.Head = moved
		if out, err = h.readKey(newKey); err != nil || out.Result == nil {
			t.Fatalf("%+v %v", out, err)
		}
	})
	t.Run("failed jobs", func(t *testing.T) {
		var gotRun, gotHead string
		h := newContextHarness(t, func(int) (ContextResult, error) { return okIssues() })
		h.client.jobs = func(run, h string) (ContextResult, error) {
			gotRun, gotHead = run, h
			return ContextResult{
				Value:       corehosts.JobLogs{RunID: run, Head: head, Jobs: []corehosts.JobLog{{JobID: "19", Name: "test", Log: "boom"}}},
				Unavailable: []corehosts.ContextUnavailable{{Source: "external checks", Reason: corehosts.ContextUnsupported}},
			}, nil
		}
		out, err := h.readKey(corehosts.ContextKey{Repo: "rep_1", Kind: corehosts.ContextFailedJobs, ObjectID: "12", Head: head})
		if err != nil || out.Result == nil || gotRun != "12" || gotHead != head {
			t.Fatalf("%+v %v run %q head %q", out, err, gotRun, gotHead)
		}
		if len(out.Result.Unavailable) != 1 {
			t.Fatalf("the labelled gap was lost: %+v", out.Result)
		}
	})
	t.Run("kind the client cannot give", func(t *testing.T) {
		h := newContextHarness(t, func(int) (ContextResult, error) { return okIssues() })
		out, err := h.readKey(corehosts.ContextKey{Repo: "rep_1", Kind: corehosts.ContextFailedJobs, ObjectID: "12"})
		if got := blockedReason(t, out, err); got != corehosts.ContextUnsupported {
			t.Fatal(got)
		}
	})
}
