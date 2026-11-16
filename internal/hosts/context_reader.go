package hosts

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

// ContextTarget is resolved afresh from tracked-ready storage, including the
// tracked project. Revision must change on relinking, untracking or head change.
// The resolver must reject foreign object identities before returning a target.
type ContextTarget struct {
	Instance corehosts.InstanceID
	Project  corehosts.ProjectPath
	Revision string
}

// ContextResolver is injected by application wiring; it must never resolve a
// reserved, untracked or foreign repo. It is called before cache and publication.
//
// A repo that is not tracked, not ready, not linked to a host or whose object
// is foreign is reported with an error that wraps ErrContextScope. Every other
// error (a database failure, say) is an infrastructure failure: the reader
// passes it on with its kind intact and never turns it into ErrContextScope.
type ContextResolver interface {
	ResolveContext(context.Context, corehosts.ContextKey) (ContextTarget, error)
}

// ContextSleeper waits for d or until ctx is done, whichever comes first, and
// returns ctx.Err() in the second case. The default uses a real timer; tests
// inject a fake so the 1 s / 3 s retry backoff does not run in real time.
// Nothing outlives the call: no detached timer or goroutine.
type ContextSleeper interface {
	Sleep(ctx context.Context, d time.Duration) error
}

type realContextSleeper struct{}

func (realContextSleeper) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// ContextOption configures a ContextService.
type ContextOption func(*ContextService)

// WithContextSleeper replaces the real timer used for retry and rate-limit
// waits.
func WithContextSleeper(s ContextSleeper) ContextOption {
	return func(c *ContextService) {
		if s != nil {
			c.sleeper = s
		}
	}
}

// contextSlot is the shell's record of one key: the core entry plus what the
// shell needs to notice changes (target, credential, instance) and to run one
// read at a time.
type contextSlot struct {
	entry      corehosts.ContextEntry
	target     ContextTarget
	credential [32]byte
	instance   corehosts.Instance
	// done is non-nil while one caller owns the read; it is closed when that
	// caller returns, and the callers that joined it then ask again.
	done chan struct{}
	// attempt and cancel describe the read in flight (valid while done != nil).
	attempt int
	cancel  context.CancelFunc
	// used is the last time (injected clock) a call asked for this key; the
	// least recently used idle slot is evicted first.
	used time.Time
}

// contextMaxSlots caps the slot map. Every key (repo, kind, object, head) holds
// up to 64 KiB (MR diff) or 1 MiB (job logs), and an MR head is an open key
// space, so idle slots are evicted: expired ones on every insert, then the
// least recently used idle one when the map is still full. A slot with a read
// in flight is never evicted, so the cap can be exceeded only by reads that are
// running at that moment (bounded by the callers' own concurrency).
const contextMaxSlots = 256

// idleExpired reports a slot that holds nothing worth keeping at now and has no
// read in flight: Empty, an expired Ready/Partial, a Blocked past its window, or
// a Loading/Waiting entry nobody owns.
func (s *contextSlot) idleExpired(now time.Time) bool {
	if s.done != nil {
		return false
	}
	switch st := s.entry.State.(type) {
	case corehosts.CtxReady:
		return !now.Before(st.Expires)
	case corehosts.CtxPartial:
		return !now.Before(st.Expires)
	case corehosts.CtxBlocked:
		return !now.Before(st.Until)
	case corehosts.CtxEmpty, corehosts.CtxLoading, corehosts.CtxWaiting:
	}
	return true
}

// insertSlotLocked adds an Empty slot for key, evicting first so the map stays
// within contextMaxSlots (when idle slots allow it).
func (c *ContextService) insertSlotLocked(key corehosts.ContextKey, target ContextTarget, credential [32]byte, inst corehosts.Instance, now time.Time) *contextSlot {
	for k, s := range c.slots {
		if s.idleExpired(now) {
			delete(c.slots, k)
		}
	}
	for len(c.slots) >= contextMaxSlots {
		var oldest corehosts.ContextKey
		var found bool
		for k, s := range c.slots {
			if s.done != nil {
				continue
			}
			if !found || s.used.Before(c.slots[oldest].used) {
				oldest, found = k, true
			}
		}
		if !found {
			break // every slot has a read in flight
		}
		delete(c.slots, oldest)
	}
	slot := &contextSlot{entry: corehosts.ContextEntry{Key: key, State: corehosts.CtxEmpty{}}, target: target, credential: credential, instance: inst, used: now}
	c.slots[key] = slot
	return slot
}

// ContextService owns only an ephemeral read cache. Reads execute synchronously
// in their caller's context: no background worker or detached timer is created.
//
// The shell follows the core (corehosts.ContextStep): it feeds one input at a
// time and interprets every command that comes back; it decides nothing about
// retries, caching or blocking itself.
//
// Rate limits (docs/design/phase-6-host-context.md, "Rate limits"): the
// authority for context reads is the larger of the poller's per-instance
// deadline (Service.state.Limited, read-only here) and limited, the deadline
// learned from the context reads themselves. The Getter's per-origin deadline
// is only the transport backstop that fails a request fast.
type ContextService struct {
	service  *Service
	resolver ContextResolver
	sleeper  ContextSleeper
	mu       sync.Mutex
	slots    map[corehosts.ContextKey]*contextSlot
	limited  map[corehosts.InstanceID]time.Time

	// Test hooks (nil in production). onJoin runs when a caller is about to park
	// on another caller's read; afterStep runs on the owner right after it fed a
	// result to the core and took its answer, outside the lock.
	onJoin    func()
	afterStep func()
}

func NewContextService(s *Service, resolver ContextResolver, opts ...ContextOption) *ContextService {
	c := &ContextService{
		service:  s,
		resolver: resolver,
		sleeper:  realContextSleeper{},
		slots:    map[corehosts.ContextKey]*contextSlot{},
		limited:  map[corehosts.InstanceID]time.Time{},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

var _ ContextReader = (*ContextService)(nil)

// contextEnv is what one Read resolved from the configuration.
type contextEnv struct {
	inst   corehosts.Instance
	token  string
	client ContextClient
	target ContextTarget
}

// contextCalls carries the two contexts of one read: the caller's (with the
// request deadline) and the read's own, which a CancelRead command cancels.
type contextCalls struct {
	caller context.Context
	read   context.Context
	slot   *contextSlot
}

func (c *ContextService) Read(parent context.Context, key corehosts.ContextKey) (ContextOutcome, error) {
	if err := key.Validate(); err != nil {
		return ContextOutcome{}, fmt.Errorf("host context key: %w", err)
	}
	ctx, cancel := context.WithTimeout(parent, corehosts.ContextRequestTimeout)
	defer cancel()
	if c.resolver == nil {
		return ContextOutcome{}, ErrContextScope
	}
	for {
		target, err := c.resolver.ResolveContext(ctx, key)
		if err != nil {
			return ContextOutcome{}, c.resolveFailed(ctx, key, err)
		}
		env, scope, ok := c.environment(target)
		if !ok {
			return ContextOutcome{}, c.scopeGone(key)
		}
		env.target = target
		credential := sha256.Sum256([]byte(env.token))

		now := c.service.d.Clock()
		c.mu.Lock()
		slot := c.slots[key]
		if slot == nil {
			slot = c.insertSlotLocked(key, target, credential, env.inst, now)
		}
		slot.used = now
		// What the shell can see change, the core is told about. Anything held
		// for the key was read under the old target or configuration.
		changed := false
		if slot.target != target {
			c.stepLocked(slot, corehosts.ContextScopeChanged{})
			slot.target = target
			changed = true
		}
		if slot.credential != credential || slot.instance != env.inst {
			c.stepLocked(slot, corehosts.ContextConfigChanged{})
			slot.credential, slot.instance = credential, env.inst
			changed = true
		}
		if changed && slot.done != nil {
			// The change just cancelled the read in flight. Wait for its owner
			// to leave before this caller starts a read of its own.
			done := slot.done
			c.mu.Unlock()
			if err := c.waitDone(ctx, done); err != nil {
				return ContextOutcome{}, err
			}
			continue
		}
		cmds := c.stepLocked(slot, corehosts.ContextRequested{Key: key, Scope: scope, Now: now})
		var (
			answer *ContextOutcome
			join   chan struct{}
			read   *corehosts.CtxReadContext
		)
		for _, cmd := range cmds {
			switch v := cmd.(type) {
			case corehosts.CtxReturnCached:
				out := contextAnswer(slot.entry)
				answer = &out
			case corehosts.CtxExplain:
				out := blockedContext(v.Reason)
				answer = &out
			case corehosts.CtxJoinRequest:
				join = slot.done
				if join == nil {
					// A Loading or Waiting entry nobody owns cannot finish on its
					// own: drop it and decide again.
					c.stepLocked(slot, corehosts.ContextCancelled{Attempt: slot.entry.Seq})
					join = closedContextChan
				}
			case corehosts.CtxReadContext:
				read = &v
			case corehosts.CtxCancelRead, corehosts.CtxScheduleWake, corehosts.CtxPublishContext:
				// CancelRead was run by stepLocked; the others only follow a
				// result input, never a request.
			}
		}
		switch {
		case answer != nil:
			c.mu.Unlock()
			return *answer, nil
		case join != nil:
			c.mu.Unlock()
			if err := c.waitDone(ctx, join); err != nil {
				return ContextOutcome{}, err
			}
			continue
		case read == nil:
			c.mu.Unlock()
			return ContextOutcome{}, errors.New("host context: the request was not accepted")
		}
		readCtx, cancelRead := context.WithCancel(ctx)
		slot.done = make(chan struct{})
		slot.attempt = read.Attempt
		slot.cancel = cancelRead
		c.mu.Unlock()

		out, err := c.run(contextCalls{caller: ctx, read: readCtx, slot: slot}, slot, env, *read)

		cancelRead()
		c.mu.Lock()
		close(slot.done)
		slot.done, slot.cancel = nil, nil
		// The slot stays on every error: its attempt counter must keep fencing
		// late results. An attempt the call left unfinished goes back to Empty
		// through the core; a still-valid value held for the key (which this
		// call never touched) is kept.
		c.stepLocked(slot, corehosts.ContextCancelled{Attempt: slot.entry.Seq})
		slot.used = c.service.d.Clock()
		c.mu.Unlock()
		if err == nil && contextOutcomeEmpty(out) {
			// Never a success without an answer: if nothing was left to say, the
			// read was overtaken.
			err = ErrContextSuperseded
		}
		return out, err
	}
}

// contextOutcomeEmpty reports an outcome that carries no answer at all.
func contextOutcomeEmpty(o ContextOutcome) bool {
	return o.Result == nil && o.Blocked == nil && o.RetryAt.IsZero()
}

var closedContextChan = func() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}()

// waitDone waits for the owner of a read to leave (or for ctx). The onJoin hook
// is for tests: it runs once the caller is about to park.
func (c *ContextService) waitDone(ctx context.Context, done <-chan struct{}) error {
	if c.onJoin != nil {
		c.onJoin()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

// stepLocked feeds one input to the core under c.mu and runs the commands that
// only touch the shell's own bookkeeping (CancelRead). The rest is returned for
// the caller to interpret.
func (c *ContextService) stepLocked(slot *contextSlot, in corehosts.ContextInput) []corehosts.ContextCommand {
	var cmds []corehosts.ContextCommand
	slot.entry, cmds = corehosts.ContextStep(slot.entry, in)
	rest := cmds[:0:0]
	for _, cmd := range cmds {
		switch v := cmd.(type) {
		case corehosts.CtxCancelRead:
			if slot.cancel != nil && slot.attempt == v.Attempt {
				slot.cancel()
			}
		case corehosts.CtxReadContext:
			slot.attempt = v.Attempt
			rest = append(rest, cmd)
		case corehosts.CtxScheduleWake, corehosts.CtxReturnCached, corehosts.CtxJoinRequest, corehosts.CtxPublishContext, corehosts.CtxExplain:
			rest = append(rest, cmd)
		}
	}
	return rest
}

func (c *ContextService) step(slot *contextSlot, in corehosts.ContextInput) []corehosts.ContextCommand {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stepLocked(slot, in)
}

// environment reads the configuration for target. ok is false when the target
// is unusable (unknown instance, bad project, no revision). scope is what the
// core is told: tracked, or why the key cannot be read.
func (c *ContextService) environment(target ContextTarget) (env contextEnv, scope corehosts.ContextScope, ok bool) {
	s := c.service
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.cfg.CoreInstances() {
		if v.ID == target.Instance {
			env.inst = v
		}
	}
	env.token = s.cfg.Token(target.Instance)
	var supported bool
	env.client, supported = s.d.Clients[env.inst.Kind].(ContextClient)
	if env.inst.ID == "" || !validContextProject(target.Project) || target.Revision == "" {
		return env, "", false
	}
	switch {
	case !supported:
		return env, corehosts.ScopeUnsupported, true
	case env.token == "":
		return env, corehosts.ScopeNoToken, true
	}
	return env, corehosts.ScopeTracked, true
}

// resolveFailed maps an error of the resolver. Only a real scope error (the
// repo is not tracked, linked or owned) is ErrContextScope; anything else keeps
// its kind, with context.
func (c *ContextService) resolveFailed(ctx context.Context, key corehosts.ContextKey, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, ErrContextScope) {
		return c.scopeGone(key)
	}
	return fmt.Errorf("resolve host context for %s: %w", key.Repo, err)
}

// scopeGone tells the core that the key is out of scope, so whatever is held
// for it (and any read in flight) is dropped, and reports ErrContextScope.
func (c *ContextService) scopeGone(key corehosts.ContextKey) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if slot := c.slots[key]; slot != nil {
		for _, cmd := range c.stepLocked(slot, corehosts.ContextRequested{Key: key, Scope: corehosts.ScopeUntracked, Now: c.service.d.Clock()}) {
			if ex, ok := cmd.(corehosts.CtxExplain); ok && ex.Reason == corehosts.ContextUntracked {
				// The core's explanation of an untracked key is this error.
				return ErrContextScope
			}
		}
	}
	// No slot, nothing held to drop: nothing to tell the core.
	return ErrContextScope
}

func blockedContext(reason corehosts.ContextBlockReason) ContextOutcome {
	return ContextOutcome{Blocked: &corehosts.CtxBlocked{Reason: reason}}
}

func contextAnswer(e corehosts.ContextEntry) ContextOutcome {
	switch v := e.State.(type) {
	case corehosts.CtxReady:
		return ContextOutcome{Result: &ContextResult{Value: cloneContext(v.Value), Expires: v.Expires}}
	case corehosts.CtxPartial:
		return ContextOutcome{Result: &ContextResult{Value: cloneContext(v.Value), Unavailable: append([]corehosts.ContextUnavailable(nil), v.Unavailable...), Expires: v.Expires}}
	case corehosts.CtxBlocked:
		return ContextOutcome{Blocked: &corehosts.CtxBlocked{Reason: v.Reason, Until: v.Until}}
	case corehosts.CtxWaiting:
		return ContextOutcome{RetryAt: v.At}
	case corehosts.CtxEmpty, corehosts.CtxLoading:
	}
	return ContextOutcome{}
}

// run is the owner of one read. It executes ReadContext, feeds the result to
// the core, and interprets what comes back: publish, explain, or schedule a
// wake and read again.
func (c *ContextService) run(calls contextCalls, slot *contextSlot, env contextEnv, start corehosts.CtxReadContext) (ContextOutcome, error) {
	key := start.Key
	attempt := start.Attempt
	for {
		in, err := c.readOnce(calls, env, key, attempt)
		if err != nil {
			return ContextOutcome{}, err
		}
		answer, wake, err := c.feed(slot, in, attempt)
		if c.afterStep != nil {
			c.afterStep()
		}
		if err != nil {
			return ContextOutcome{}, err
		}
		if answer != nil {
			return *answer, nil
		}
		out, next, err := c.sleepUntil(calls, slot, *wake)
		switch {
		case err != nil:
			return ContextOutcome{}, err
		case out != nil:
			return *out, nil
		}
		attempt = next.Attempt
	}
}

// feed gives one result input to the core and interprets the commands that come
// back. The step and the answer are taken in ONE critical section: the answer
// is read from the entry the step just stored, so a change another call makes
// afterwards cannot turn a published value into an empty outcome. Exactly one
// of answer and wake is set when err is nil.
func (c *ContextService) feed(slot *contextSlot, in corehosts.ContextInput, attempt int) (answer *ContextOutcome, wake *corehosts.CtxScheduleWake, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, cmd := range c.stepLocked(slot, in) {
		switch v := cmd.(type) {
		case corehosts.CtxPublishContext:
			out := contextAnswer(slot.entry)
			answer = &out
		case corehosts.CtxExplain:
			out := blockedContext(v.Reason)
			if _, ok := slot.entry.State.(corehosts.CtxBlocked); ok {
				out = contextAnswer(slot.entry) // carries the block's deadline
			}
			answer = &out
		case corehosts.CtxScheduleWake:
			wake = &v
		case corehosts.CtxReadContext, corehosts.CtxReturnCached, corehosts.CtxJoinRequest, corehosts.CtxCancelRead:
			// Not produced by a result input.
		}
	}
	if answer != nil && contextOutcomeEmpty(*answer) {
		return nil, nil, c.cancelledErrLocked(slot)
	}
	if answer != nil || wake != nil {
		return answer, wake, nil
	}
	// The core gave no command: it ignored the input. Either the attempt was
	// superseded (a scope or config change dropped it) or the input itself was
	// invalid (a clock that returned no time). Never leave it Loading.
	if slot.entry.Seq == attempt {
		return nil, nil, errors.New("host context: the read result was not accepted")
	}
	return nil, nil, c.cancelledErrLocked(slot)
}

// cancelledErrLocked names why this call's attempt was dropped by someone else:
// the repo went out of scope (the core holds Blocked{untracked}: a real scope
// error), or anything else (target, instance or token changed, another call
// dropped the attempt): superseded, ask again.
func (c *ContextService) cancelledErrLocked(slot *contextSlot) error {
	if b, ok := slot.entry.State.(corehosts.CtxBlocked); ok && b.Reason == corehosts.ContextUntracked {
		return ErrContextScope
	}
	return ErrContextSuperseded
}

func (c *ContextService) cancelledErr(calls contextCalls) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cancelledErrLocked(calls.slot)
}

// sleepUntil interprets ScheduleWake: wait until the wake time with the
// sleeper, then tell the core the timer fired. A wake that would not fit in the
// request's remaining time is not waited for: the attempt is cancelled and the
// caller gets the retry time.
func (c *ContextService) sleepUntil(calls contextCalls, slot *contextSlot, wake corehosts.CtxScheduleWake) (*ContextOutcome, *corehosts.CtxReadContext, error) {
	d := wake.At.Sub(c.service.d.Clock())
	if deadline, ok := calls.caller.Deadline(); ok && d >= time.Until(deadline) {
		c.step(slot, corehosts.ContextCancelled{Attempt: wake.Attempt})
		return &ContextOutcome{RetryAt: wake.At}, nil, nil
	}
	if err := c.sleeper.Sleep(calls.read, d); err != nil {
		return nil, nil, c.endContextErr(calls, err)
	}
	for _, cmd := range c.step(slot, corehosts.ContextWake{Attempt: wake.Attempt, Now: wake.At}) {
		if read, ok := cmd.(corehosts.CtxReadContext); ok {
			return nil, &read, nil
		}
	}
	// The core dropped the wait: a scope or config change cancelled it.
	return nil, nil, c.cancelledErr(calls)
}

// endContextErr names why a read stopped: the caller gave up (their error), or
// the core cancelled the read because scope or configuration changed.
func (c *ContextService) endContextErr(calls contextCalls, err error) error {
	if calls.caller.Err() != nil {
		return calls.caller.Err()
	}
	if calls.read.Err() != nil {
		return c.cancelledErr(calls)
	}
	return err
}

// limitedUntil is the instance-wide rate-limit deadline for context reads.
func (c *ContextService) limitedUntil(inst corehosts.InstanceID, now time.Time) time.Time {
	c.service.mu.Lock()
	until := c.service.state.Limited[inst]
	c.service.mu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if mine, ok := c.limited[inst]; ok {
		if !mine.After(now) {
			delete(c.limited, inst)
		} else if mine.After(until) {
			until = mine
		}
	}
	return until
}

func (c *ContextService) noteLimited(inst corehosts.InstanceID, until time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if until.After(c.limited[inst]) {
		c.limited[inst] = until
	}
}

// stillInScope reports an error when the key's target or the configuration is
// no longer what this read started with.
func (c *ContextService) stillInScope(calls contextCalls, env contextEnv, key corehosts.ContextKey) error {
	target, err := c.resolver.ResolveContext(calls.read, key)
	if err != nil {
		if calls.caller.Err() != nil {
			return calls.caller.Err()
		}
		if calls.read.Err() != nil {
			return c.cancelledErr(calls)
		}
		if errors.Is(err, ErrContextScope) {
			return ErrContextScope
		}
		return fmt.Errorf("resolve host context for %s: %w", key.Repo, err)
	}
	if target != env.target {
		// Relinked or another revision while the read ran: not a scope error.
		return ErrContextSuperseded
	}
	return nil
}

// readOnce executes one ReadContext: it rechecks scope, honours the rate-limit
// deadline, reads, rechecks scope and configuration, redacts, and returns the
// core input that says what happened. Errors are only the ones that end the
// call without an answer (the caller gave up, scope or configuration changed).
func (c *ContextService) readOnce(calls contextCalls, env contextEnv, key corehosts.ContextKey, attempt int) (corehosts.ContextInput, error) {
	if err := c.stillInScope(calls, env, key); err != nil {
		return nil, err
	}
	now := c.service.d.Clock()
	if until := c.limitedUntil(env.inst.ID, now); until.After(now) {
		return corehosts.ContextRateLimited{Key: key, Attempt: attempt, Until: until, Now: now}, nil
	}
	var result ContextResult
	var err error
	number, _ := strconv.Atoi(key.ObjectID)
	switch key.Kind {
	case corehosts.ContextFailedJobs:
		result, err = env.client.FailedJobs(calls.read, env.inst, env.token, env.target.Project, key.ObjectID, key.Head)
	case corehosts.ContextIssues:
		result, err = env.client.Issues(calls.read, env.inst, env.token, env.target.Project)
	case corehosts.ContextIssue:
		result, err = env.client.Issue(calls.read, env.inst, env.token, env.target.Project, number)
	case corehosts.ContextMRDiff:
		result, err = env.client.MRDiff(calls.read, env.inst, env.token, env.target.Project, number)
	}
	if calls.caller.Err() != nil {
		return nil, calls.caller.Err()
	}
	if calls.read.Err() != nil {
		return nil, c.cancelledErr(calls)
	}
	if scopeErr := c.stillInScope(calls, env, key); scopeErr != nil {
		return nil, scopeErr
	}
	c.service.mu.Lock()
	current := c.service.cfg.Token(env.inst.ID)
	sameInstance := false
	for _, v := range c.service.cfg.CoreInstances() {
		if v == env.inst {
			sameInstance = true
		}
	}
	secrets := c.service.cfg.Secrets()
	c.service.mu.Unlock()
	if current != env.token || !sameInstance {
		// The token or instance changed while the read ran: its result was read
		// under the old configuration and is dropped, but this is not a scope error.
		return nil, ErrContextSuperseded
	}
	now = c.service.d.Clock()
	if err == nil {
		// Redaction happens before anything is stored; the core then checks the
		// payload against the key and the bounds.
		result = redactContext(result, secrets...)
		return corehosts.ContextFetched{Key: key, Attempt: attempt, Value: result.Value, Unavailable: result.Unavailable, Now: now}, nil
	}
	if reason, refused := contextRefusal(err); refused {
		return corehosts.ContextRejected{Key: key, Attempt: attempt, Reason: reason, Now: now}, nil
	}
	failure := corehosts.ContextFetchFailed{Key: key, Attempt: attempt, Class: corehosts.FailTransient, Now: now}
	var fe *FetchError
	if errors.As(err, &fe) {
		failure.Class = fe.Kind
		if fe.RetryAt != nil {
			failure.RetryAt = *fe.RetryAt
			c.noteLimited(env.inst.ID, failure.RetryAt)
		}
	}
	return failure, nil
}

// contextRefusal classifies the errors a retry cannot fix, with the reason the
// user is told. Everything else is a temporary failure unless it is a
// *FetchError with its own class.
//
//   - too large: over the response budget;
//   - unsupported: the host cannot give this kind, or sent an answer that cannot
//     be used (the same request gets the same answer);
//   - not found: the object is not the one asked for (foreign run, a pull
//     request where an issue was asked for).
func contextRefusal(err error) (corehosts.ContextRefusal, bool) {
	switch {
	case errors.Is(err, ErrContextTooLarge):
		return corehosts.RefusedTooLarge, true
	case errors.Is(err, ErrContextUnsupported), errors.Is(err, ErrContextInvalid):
		return corehosts.RefusedUnsupported, true
	case errors.Is(err, ErrContextScope):
		return corehosts.RefusedNotFound, true
	}
	return "", false
}
