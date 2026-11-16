package hosts

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

// Tests of the context shell as an interpreter of ContextStep commands (GH-05,
// Batch 2 follow-up). They use a fake clock and a fake sleeper, so no test waits
// for the real 1 s / 3 s backoff.

type fakeContextClock struct {
	mu sync.Mutex
	t  time.Time
	// zeroFrom, when positive, makes the zeroFrom-th and every later reading
	// return the zero time (a broken clock).
	zeroFrom int
	reads    int
}

func (f *fakeContextClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.zeroFrom > 0 && f.reads >= f.zeroFrom {
		return time.Time{}
	}
	return f.t
}

func (f *fakeContextClock) Advance(d time.Duration) {
	f.mu.Lock()
	f.t = f.t.Add(d)
	f.mu.Unlock()
}

// fakeContextSleeper records every wait and moves the fake clock by it, as time
// passing would. With block set it signals entered and waits for ctx instead.
type fakeContextSleeper struct {
	clock   *fakeContextClock
	mu      sync.Mutex
	slept   []time.Duration
	block   bool
	entered chan struct{}
}

func (f *fakeContextSleeper) Sleep(ctx context.Context, d time.Duration) error {
	f.mu.Lock()
	f.slept = append(f.slept, d)
	f.mu.Unlock()
	if f.block {
		select {
		case f.entered <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	f.clock.Advance(d)
	return nil
}

func (f *fakeContextSleeper) durations() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Duration(nil), f.slept...)
}

// scriptedContextClient answers Issues from a script (called with the 1-based
// call number). The other kinds are unsupported.
type scriptedContextClient struct {
	HostClient
	calls  atomic.Int32
	script func(n int) (ContextResult, error)
	// started is signalled (non-blocking) at the start of every call; gate, when
	// set, holds the call until it is closed or the call's context ends.
	started chan struct{}
	gate    chan struct{}
	// Scripts of the other kinds; nil means the kind is unsupported.
	issue func(number int) (ContextResult, error)
	mr    func(number int) (ContextResult, error)
	jobs  func(runID, head string) (ContextResult, error)
}

func (f *scriptedContextClient) Issues(ctx context.Context, _ corehosts.Instance, _ string, _ corehosts.ProjectPath) (ContextResult, error) {
	n := int(f.calls.Add(1))
	if f.started != nil {
		select {
		case f.started <- struct{}{}:
		default:
		}
	}
	if f.gate != nil {
		select {
		case <-ctx.Done():
			return ContextResult{}, ctx.Err()
		case <-f.gate:
		}
	}
	return f.script(n)
}
func (f *scriptedContextClient) Issue(_ context.Context, _ corehosts.Instance, _ string, _ corehosts.ProjectPath, number int) (ContextResult, error) {
	if f.issue == nil {
		return ContextResult{}, ErrContextUnsupported
	}
	return f.issue(number)
}
func (f *scriptedContextClient) MRDiff(_ context.Context, _ corehosts.Instance, _ string, _ corehosts.ProjectPath, number int) (ContextResult, error) {
	if f.mr == nil {
		return ContextResult{}, ErrContextUnsupported
	}
	return f.mr(number)
}
func (f *scriptedContextClient) FailedJobs(_ context.Context, _ corehosts.Instance, _ string, _ corehosts.ProjectPath, runID, head string) (ContextResult, error) {
	if f.jobs == nil {
		return ContextResult{}, ErrContextUnsupported
	}
	return f.jobs(runID, head)
}

func okIssues() (ContextResult, error) {
	return ContextResult{Value: corehosts.IssueList{Items: []corehosts.IssueSummary{{Number: 1, Title: "hello"}}}}, nil
}

type contextHarness struct {
	t      *testing.T
	c      *ContextService
	s      *Service
	clock  *fakeContextClock
	sleep  *fakeContextSleeper
	client *scriptedContextClient
	inst   corehosts.Instance
	key    corehosts.ContextKey

	mu       sync.Mutex
	revision string
	resolve  func(corehosts.ContextKey) error
}

func newContextHarness(t *testing.T, script func(n int) (ContextResult, error)) *contextHarness {
	t.Helper()
	_, inst, cfg := contextFixture(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected HTTP") })
	h := &contextHarness{
		t:        t,
		clock:    &fakeContextClock{t: time.Unix(1_000_000, 0)},
		client:   &scriptedContextClient{script: script},
		inst:     inst,
		key:      corehosts.ContextKey{Repo: "rep_1", Kind: corehosts.ContextIssues},
		revision: "v1",
	}
	h.sleep = &fakeContextSleeper{clock: h.clock}
	h.s = New(Deps{Config: cfg, Clock: h.clock.Now, Clients: map[corehosts.Kind]HostClient{corehosts.KindGitHub: h.client}})
	h.c = NewContextService(h.s, contextResolverFunc(func(_ context.Context, k corehosts.ContextKey) (ContextTarget, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.resolve != nil {
			if err := h.resolve(k); err != nil {
				return ContextTarget{}, err
			}
		}
		return ContextTarget{Instance: inst.ID, Project: "acme/shop", Revision: h.revision}, nil
	}), WithContextSleeper(h.sleep))
	return h
}

func (h *contextHarness) setRevision(r string) {
	h.mu.Lock()
	h.revision = r
	h.mu.Unlock()
}

func (h *contextHarness) setResolve(f func(corehosts.ContextKey) error) {
	h.mu.Lock()
	h.resolve = f
	h.mu.Unlock()
}

func (h *contextHarness) setToken(token string) {
	h.s.mu.Lock()
	defer h.s.mu.Unlock()
	if token == "" {
		delete(h.s.cfg.tokens, h.inst.ID)
		return
	}
	h.s.cfg.tokens[h.inst.ID] = token
}

func (h *contextHarness) read() (ContextOutcome, error) {
	h.t.Helper()
	return h.readKey(h.key)
}

func (h *contextHarness) readKey(key corehosts.ContextKey) (ContextOutcome, error) {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return h.c.Read(ctx, key)
}

func (h *contextHarness) slot() *contextSlot {
	h.c.mu.Lock()
	defer h.c.mu.Unlock()
	return h.c.slots[h.key]
}

func (h *contextHarness) entry() corehosts.ContextEntry {
	h.t.Helper()
	h.c.mu.Lock()
	defer h.c.mu.Unlock()
	s := h.c.slots[h.key]
	if s == nil {
		h.t.Fatal("the key has no slot")
	}
	return s.entry
}

func blockedReason(t *testing.T, out ContextOutcome, err error) corehosts.ContextBlockReason {
	t.Helper()
	if err != nil || out.Blocked == nil {
		t.Fatalf("want a blocked answer, got %+v, %v", out, err)
	}
	return out.Blocked.Reason
}

// FE S1: the unsupported-host and no-token decisions are the core's. The entry
// holds the core's Blocked state; the shell no longer answers around it.
func TestContextShellNoClientAndNoTokenGoThroughTheCore(t *testing.T) {
	t.Run("no client", func(t *testing.T) {
		h := newContextHarness(t, func(int) (ContextResult, error) { return okIssues() })
		h.s.mu.Lock()
		h.s.d.Clients = map[corehosts.Kind]HostClient{}
		h.s.mu.Unlock()
		out, err := h.read()
		if got := blockedReason(t, out, err); got != corehosts.ContextUnsupported {
			t.Fatal(got)
		}
		if b, ok := h.entry().State.(corehosts.CtxBlocked); !ok || b.Reason != corehosts.ContextUnsupported || h.entry().Seq != 1 {
			t.Fatalf("the core did not decide: %+v", h.entry())
		}
	})
	t.Run("no token", func(t *testing.T) {
		h := newContextHarness(t, func(int) (ContextResult, error) { return okIssues() })
		h.setToken("")
		out, err := h.read()
		if got := blockedReason(t, out, err); got != corehosts.ContextNoToken {
			t.Fatal(got)
		}
		if b, ok := h.entry().State.(corehosts.CtxBlocked); !ok || b.Reason != corehosts.ContextNoToken {
			t.Fatalf("the core did not decide: %+v", h.entry())
		}
		if h.client.calls.Load() != 0 {
			t.Fatal("a read without a token reached the host")
		}
		// A scope block has no window: adding the token reads at once.
		h.setToken("sentinel-secret")
		out, err = h.read()
		if err != nil || out.Result == nil || h.client.calls.Load() != 1 {
			t.Fatalf("a token that arrived was not used: %+v %v", out, err)
		}
	})
}

// FE S1 + item 6: the retry wake is the core's ScheduleWake (1 s, then 3 s),
// run by the injected sleeper, so the test takes no real time.
func TestContextShellRetriesFollowScheduleWake(t *testing.T) {
	h := newContextHarness(t, func(n int) (ContextResult, error) {
		if n < 3 {
			return ContextResult{}, fetchErr(corehosts.FailTransient, "host busy")
		}
		return okIssues()
	})
	start := time.Now()
	out, err := h.read()
	if err != nil || out.Result == nil {
		t.Fatalf("%+v %v", out, err)
	}
	if got := h.client.calls.Load(); got != 3 {
		t.Fatalf("reads = %d, want 3", got)
	}
	if got := h.sleep.durations(); len(got) != 2 || got[0] != time.Second || got[1] != 3*time.Second {
		t.Fatalf("waits = %v, want [1s 3s]", got)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("the backoff ran in real time")
	}
}

func TestContextShellRetryExhaustionBlocksAfterTwoRetries(t *testing.T) {
	h := newContextHarness(t, func(int) (ContextResult, error) {
		return ContextResult{}, fetchErr(corehosts.FailTransient, "host busy")
	})
	out, err := h.read()
	if got := blockedReason(t, out, err); got != corehosts.ContextRetryExhausted {
		t.Fatal(got)
	}
	if h.client.calls.Load() != 3 || len(h.sleep.durations()) != 2 {
		t.Fatalf("reads %d waits %v", h.client.calls.Load(), h.sleep.durations())
	}
	// The block answers from cache for a short window (item 9): no more calls.
	if _, err = h.read(); err != nil || h.client.calls.Load() != 3 {
		t.Fatal("a blocked key reached the host again", err, h.client.calls.Load())
	}
}

// A rate-limit deadline shorter than the request is waited for through the
// core (RateLimited then Wake), without using up a retry.
func TestContextShellRateLimitWaitDoesNotUseARetry(t *testing.T) {
	h := newContextHarness(t, func(int) (ContextResult, error) { return okIssues() })
	h.s.mu.Lock()
	h.s.state.Limited = map[corehosts.InstanceID]time.Time{h.inst.ID: h.clock.Now().Add(2 * time.Second)}
	h.s.mu.Unlock()
	out, err := h.read()
	if err != nil || out.Result == nil {
		t.Fatalf("%+v %v", out, err)
	}
	if got := h.sleep.durations(); len(got) != 1 || got[0] != 2*time.Second {
		t.Fatalf("waits = %v, want [2s]", got)
	}
	if h.client.calls.Load() != 1 || h.entry().Retries != 0 {
		t.Fatalf("reads %d retries %d", h.client.calls.Load(), h.entry().Retries)
	}
}

// Item 7: the poller's per-instance deadline is honoured by context reads.
func TestContextShellHonoursPollerRateLimit(t *testing.T) {
	h := newContextHarness(t, func(int) (ContextResult, error) { return okIssues() })
	until := h.clock.Now().Add(10 * time.Minute)
	h.s.mu.Lock()
	h.s.state.Limited = map[corehosts.InstanceID]time.Time{h.inst.ID: until}
	h.s.mu.Unlock()
	out, err := h.read()
	if err != nil || !out.RetryAt.Equal(until) || h.client.calls.Load() != 0 {
		t.Fatalf("%+v %v reads %d", out, err, h.client.calls.Load())
	}
	if len(h.sleep.durations()) != 0 {
		t.Fatal("waited for a deadline beyond the request")
	}
}

// FE S1: CancelRead is run. A caller that finds the repo untracked while another
// caller's read is in flight cancels that read at once.
func TestContextShellCancelReadStopsTheReadInFlight(t *testing.T) {
	h := newContextHarness(t, func(int) (ContextResult, error) { return okIssues() })
	h.client.started = make(chan struct{}, 1)
	h.client.gate = make(chan struct{})
	defer close(h.client.gate)
	type answer struct {
		out ContextOutcome
		err error
	}
	first := make(chan answer, 1)
	go func() {
		out, err := h.read()
		first <- answer{out, err}
	}()
	select {
	case <-h.client.started:
	case <-time.After(3 * time.Second):
		t.Fatal("the read did not start")
	}
	h.setResolve(func(corehosts.ContextKey) error { return ErrContextScope })
	if _, err := h.read(); !errors.Is(err, ErrContextScope) {
		t.Fatalf("second caller: %v", err)
	}
	select {
	case a := <-first:
		if !errors.Is(a.err, ErrContextScope) {
			t.Fatalf("first caller: %+v %v", a.out, a.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the read in flight was not cancelled")
	}
	if b, ok := h.entry().State.(corehosts.CtxBlocked); !ok || b.Reason != corehosts.ContextUntracked {
		t.Fatalf("the core was not told: %+v", h.entry())
	}
}

// Untracking drops what was held, even when the repo is tracked again at the
// same revision.
func TestContextShellUntrackedCacheIsNotServedAfterRetracking(t *testing.T) {
	h := newContextHarness(t, func(int) (ContextResult, error) { return okIssues() })
	if _, err := h.read(); err != nil {
		t.Fatal(err)
	}
	h.setResolve(func(corehosts.ContextKey) error { return ErrContextScope })
	if _, err := h.read(); !errors.Is(err, ErrContextScope) {
		t.Fatal(err)
	}
	h.setResolve(nil)
	out, err := h.read()
	if err != nil || out.Result == nil {
		t.Fatalf("%+v %v", out, err)
	}
	if h.client.calls.Load() != 2 {
		t.Fatalf("reads = %d: the cache of an untracked repo came back", h.client.calls.Load())
	}
}

// FE S2: a payload the core refuses for the current attempt ends the attempt.
func TestContextShellUnusablePayloadBlocksInsteadOfHanging(t *testing.T) {
	h := newContextHarness(t, func(int) (ContextResult, error) {
		return ContextResult{Value: corehosts.IssueDetail{Summary: corehosts.IssueSummary{Number: 3}}}, nil
	})
	out, err := h.read()
	// Not the object the key names (this is also what a moved MR head looks
	// like): not found.
	if got := blockedReason(t, out, err); got != corehosts.ContextNotFound {
		t.Fatal(got)
	}
	if _, loading := h.entry().State.(corehosts.CtxLoading); loading {
		t.Fatal("the entry stayed Loading")
	}
	gaps := newContextHarness(t, func(int) (ContextResult, error) {
		r, _ := okIssues()
		r.Unavailable = make([]corehosts.ContextUnavailable, corehosts.ContextMaxEntries+1)
		return r, nil
	})
	out, err = gaps.read()
	if got := blockedReason(t, out, err); got != corehosts.ContextUnsupported {
		t.Fatal(got)
	}
}

// Item 10: a clock that returns no time must not create an instantly expired
// entry, and must not leave the entry Loading.
func TestContextShellZeroClockIsRefused(t *testing.T) {
	// The clock breaks at the first reading, at the request, or only when the
	// result arrives (the third reading): either way no Ready entry appears and
	// nothing stays Loading.
	for _, zeroFrom := range []int{1, 3} {
		t.Run(fmt.Sprintf("zero from reading %d", zeroFrom), func(t *testing.T) {
			h := newContextHarness(t, func(int) (ContextResult, error) { return okIssues() })
			h.clock.mu.Lock()
			h.clock.zeroFrom = zeroFrom
			h.clock.mu.Unlock()
			assertZeroClockRefused(t, h)
		})
	}
}

func assertZeroClockRefused(t *testing.T, h *contextHarness) {
	t.Helper()
	out, err := h.read()
	if err == nil || out.Result != nil {
		t.Fatalf("a zero time was accepted: %+v", out)
	}
	if s := h.slot(); s != nil {
		if _, ok := s.entry.State.(corehosts.CtxReady); ok {
			t.Fatal("a zero time produced a Ready entry")
		}
		if _, ok := s.entry.State.(corehosts.CtxLoading); ok {
			t.Fatal("the entry stayed Loading")
		}
	}
}

// FE S3: deterministic failures are refusals: one read, no retry, the right
// reason.
func TestContextShellDeterministicFailuresAreNotRetried(t *testing.T) {
	for name, c := range map[string]struct {
		err  error
		want corehosts.ContextBlockReason
	}{
		"foreign object":  {fmt.Errorf("run belongs to another repo: %w", ErrContextScope), corehosts.ContextNotFound},
		"invalid answer":  {invalidContext("invalid host context response"), corehosts.ContextUnsupported},
		"unsupported":     {ErrContextUnsupported, corehosts.ContextUnsupported},
		"too large":       {ErrContextTooLarge, corehosts.ContextTooLarge},
		"not found":       {fetchErr(corehosts.FailNotFound, "404"), corehosts.ContextNotFound},
		"forbidden":       {fetchErr(corehosts.FailUnauthorized, "403"), corehosts.ContextForbidden},
		"wrapped invalid": {fmt.Errorf("decode: %w", invalidContext("invalid issue identity")), corehosts.ContextUnsupported},
	} {
		t.Run(name, func(t *testing.T) {
			h := newContextHarness(t, func(int) (ContextResult, error) { return ContextResult{}, c.err })
			out, err := h.read()
			if got := blockedReason(t, out, err); got != c.want {
				t.Fatalf("reason %s, want %s", got, c.want)
			}
			if h.client.calls.Load() != 1 || len(h.sleep.durations()) != 0 {
				t.Fatalf("reads %d waits %v: a deterministic failure was retried", h.client.calls.Load(), h.sleep.durations())
			}
		})
	}
}

// Item 4: only a real scope error is ErrContextScope.
func TestContextShellResolverErrorsKeepTheirKind(t *testing.T) {
	h := newContextHarness(t, func(int) (ContextResult, error) { return okIssues() })
	dbDown := errors.New("database is locked")
	h.setResolve(func(corehosts.ContextKey) error { return dbDown })
	_, err := h.read()
	if errors.Is(err, ErrContextScope) || !errors.Is(err, dbDown) {
		t.Fatalf("an infrastructure failure was reported as %v", err)
	}
	if !strings.Contains(err.Error(), "resolve host context") {
		t.Fatalf("no context in %q", err)
	}
	h.setResolve(func(corehosts.ContextKey) error { return fmt.Errorf("lookup rep_1: %w", ErrContextScope) })
	if _, err = h.read(); !errors.Is(err, ErrContextScope) {
		t.Fatalf("a scope error was reported as %v", err)
	}
	// The same split applies in the middle of a read.
	calls := 0
	h.setResolve(func(corehosts.ContextKey) error {
		calls++
		if calls >= 3 {
			return dbDown
		}
		return nil
	})
	if _, err = h.read(); errors.Is(err, ErrContextScope) || !errors.Is(err, dbDown) {
		t.Fatalf("a mid-read infrastructure failure was reported as %v", err)
	}
}

// Item 5: a failed call never deletes the slot; its counter keeps fencing late
// results and the key is usable again at once.
func TestContextShellFailedReadKeepsTheSlot(t *testing.T) {
	h := newContextHarness(t, func(int) (ContextResult, error) { return okIssues() })
	calls := 0
	h.setResolve(func(corehosts.ContextKey) error {
		calls++
		if calls == 3 { // the revalidation after the read
			return ErrContextScope
		}
		return nil
	})
	if _, err := h.read(); !errors.Is(err, ErrContextScope) {
		t.Fatal(err)
	}
	slot := h.slot()
	if slot == nil {
		t.Fatal("the slot was deleted by a failed read")
	}
	if _, empty := slot.entry.State.(corehosts.CtxEmpty); !empty || slot.entry.Seq != 2 {
		t.Fatalf("the unfinished attempt was not closed through the core: %+v", slot.entry)
	}
	out, err := h.read()
	if err != nil || out.Result == nil {
		t.Fatalf("%+v %v", out, err)
	}
	if h.slot() != slot || h.entry().Seq != 3 {
		t.Fatalf("the slot or its counter was reset: %+v", h.entry())
	}
}

// Item 5: a value that is still valid survives a failing caller.
func TestContextShellValidEntrySurvivesAFailingCaller(t *testing.T) {
	h := newContextHarness(t, func(int) (ContextResult, error) { return okIssues() })
	if _, err := h.read(); err != nil {
		t.Fatal(err)
	}
	dbDown := errors.New("database is locked")
	h.setResolve(func(corehosts.ContextKey) error { return dbDown })
	if _, err := h.read(); !errors.Is(err, dbDown) {
		t.Fatal(err)
	}
	h.setResolve(nil)
	out, err := h.read()
	if err != nil || out.Result == nil || h.client.calls.Load() != 1 {
		t.Fatalf("the cached value was lost: %+v %v reads %d", out, err, h.client.calls.Load())
	}
}

// Item 9: a blocked key answers from cache for a short window, driven by the
// supplied time; a credential change resets it.
func TestContextShellBlockedKeyNegativeCache(t *testing.T) {
	h := newContextHarness(t, func(int) (ContextResult, error) {
		return ContextResult{}, fetchErr(corehosts.FailUnauthorized, "403")
	})
	for i := 0; i < 3; i++ {
		out, err := h.read()
		if got := blockedReason(t, out, err); got != corehosts.ContextForbidden {
			t.Fatal(got)
		}
		h.clock.Advance(time.Second)
	}
	if got := h.client.calls.Load(); got != 1 {
		t.Fatalf("reads = %d: an unauthorized key reached the host on every click", got)
	}
	h.clock.Advance(corehosts.ContextBlockedTTL)
	if _, err := h.read(); err != nil || h.client.calls.Load() != 2 {
		t.Fatalf("the block never expired: reads %d %v", h.client.calls.Load(), err)
	}
	// A new token is not held back by the window.
	h.setToken("another-secret")
	if _, err := h.read(); err != nil || h.client.calls.Load() != 3 {
		t.Fatalf("a credential change did not reset the block: reads %d %v", h.client.calls.Load(), err)
	}
}

// Item 11: the shell tells the core when the target or the configuration
// changed; the slot (and its counter) is kept, not replaced.
func TestContextShellProducesScopeAndConfigChanges(t *testing.T) {
	h := newContextHarness(t, func(int) (ContextResult, error) { return okIssues() })
	if _, err := h.read(); err != nil {
		t.Fatal(err)
	}
	slot, seq := h.slot(), h.entry().Seq
	h.setRevision("v2")
	if _, err := h.read(); err != nil || h.client.calls.Load() != 2 {
		t.Fatalf("a changed revision was served from cache: %v reads %d", err, h.client.calls.Load())
	}
	if h.slot() != slot || h.entry().Seq <= seq+1 {
		t.Fatalf("the shell did not report a scope change to the core: seq %d -> %d", seq, h.entry().Seq)
	}
	seq = h.entry().Seq
	h.setToken("changed-secret")
	if _, err := h.read(); err != nil || h.client.calls.Load() != 3 {
		t.Fatalf("a changed credential was served from cache: %v reads %d", err, h.client.calls.Load())
	}
	if h.slot() != slot || h.entry().Seq <= seq+1 {
		t.Fatalf("the shell did not report a config change to the core: seq %d -> %d", seq, h.entry().Seq)
	}
}

// Item 6: cancelling the caller during a retry wait releases the owner and the
// callers that joined it. Nothing is left behind.
func TestContextShellCancellationDuringRetryWaitReleasesWaiters(t *testing.T) {
	h := newContextHarness(t, func(n int) (ContextResult, error) {
		if n == 1 {
			return ContextResult{}, fetchErr(corehosts.FailTransient, "host busy")
		}
		return okIssues()
	})
	h.sleep.block = true
	h.sleep.entered = make(chan struct{}, 1)
	parked := make(chan struct{}, 1)
	h.c.onJoin = func() {
		select {
		case parked <- struct{}{}:
		default:
		}
	}
	ownerCtx, cancelOwner := context.WithCancel(context.Background())
	defer cancelOwner()
	ownerDone := make(chan error, 1)
	go func() {
		_, err := h.c.Read(ownerCtx, h.key)
		ownerDone <- err
	}()
	select {
	case <-h.sleep.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the owner never waited")
	}
	type answer struct {
		out ContextOutcome
		err error
	}
	joiner := make(chan answer, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := h.c.Read(ctx, h.key)
		joiner <- answer{out, err}
	}()
	// Wait until the joiner is parked on the owner's read (no sleeping): only
	// then is "one read so far" a statement about the joiner.
	select {
	case <-parked:
	case <-time.After(3 * time.Second):
		t.Fatal("the joiner never parked on the owner's read")
	}
	if h.client.calls.Load() != 1 {
		t.Fatal("the joiner started a second read")
	}
	h.sleep.block = false
	cancelOwner()
	select {
	case err := <-ownerDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("owner: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelling did not release the owner's wait")
	}
	select {
	case a := <-joiner:
		if a.err != nil || a.out.Result == nil {
			t.Fatalf("joiner: %+v %v", a.out, a.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the joined caller was never released")
	}
}

// Item 5 (retry path): a wake that does not fit in the request ends the read
// with the retry time and leaves the entry Empty, not Waiting.
func TestContextShellWakeBeyondTheRequestReturnsRetryAt(t *testing.T) {
	until := time.Unix(1_000_000, 0).Add(10 * time.Minute)
	h := newContextHarness(t, func(int) (ContextResult, error) {
		return ContextResult{}, &FetchError{Kind: corehosts.FailRateLimited, RetryAt: &until, Err: errors.New("limited")}
	})
	out, err := h.read()
	if err != nil || !out.RetryAt.Equal(until) {
		t.Fatalf("%+v %v", out, err)
	}
	if _, empty := h.entry().State.(corehosts.CtxEmpty); !empty {
		t.Fatalf("%+v", h.entry())
	}
	if len(h.sleep.durations()) != 0 {
		t.Fatal("slept past the request")
	}
	// A second caller does not reach the host before the deadline.
	if out, err = h.read(); err != nil || !out.RetryAt.Equal(until) || h.client.calls.Load() != 1 {
		t.Fatalf("%+v %v reads %d", out, err, h.client.calls.Load())
	}
}
