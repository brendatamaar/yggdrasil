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
type ContextResolver interface {
	ResolveContext(context.Context, corehosts.ContextKey) (ContextTarget, error)
}

type contextSlot struct {
	entry      corehosts.ContextEntry
	target     ContextTarget
	credential [32]byte
	instance   corehosts.Instance
	done       chan struct{}
}

// ContextService owns only an ephemeral read cache. Reads execute synchronously
// in their caller's context: no background worker or detached timer is created.
type ContextService struct {
	service  *Service
	resolver ContextResolver
	mu       sync.Mutex
	slots    map[corehosts.ContextKey]*contextSlot
	limited  map[corehosts.InstanceID]time.Time
}

func NewContextService(s *Service, resolver ContextResolver) *ContextService {
	return &ContextService{service: s, resolver: resolver, slots: map[corehosts.ContextKey]*contextSlot{}, limited: map[corehosts.InstanceID]time.Time{}}
}

var _ ContextReader = (*ContextService)(nil)

func (c *ContextService) Read(parent context.Context, key corehosts.ContextKey) (ContextOutcome, error) {
	if err := key.Validate(); err != nil {
		return ContextOutcome{}, fmt.Errorf("host context key: %w", err)
	}
	ctx, cancel := context.WithTimeout(parent, corehosts.ContextRequestTimeout)
	defer cancel()
	if c.resolver == nil {
		return ContextOutcome{}, ErrContextScope
	}
	var joined *contextSlot
	for {
		target, err := c.resolver.ResolveContext(ctx, key)
		if err != nil {
			if ctx.Err() != nil {
				return ContextOutcome{}, ctx.Err()
			}
			return ContextOutcome{}, ErrContextScope
		}
		s := c.service
		s.mu.Lock()
		var inst corehosts.Instance
		for _, v := range s.cfg.CoreInstances() {
			if v.ID == target.Instance {
				inst = v
			}
		}
		token := s.cfg.Token(target.Instance)
		client, supported := s.d.Clients[inst.Kind].(ContextClient)
		s.mu.Unlock()
		if inst.ID == "" || !validContextProject(target.Project) || target.Revision == "" {
			return ContextOutcome{}, ErrContextScope
		}
		if !supported {
			return blockedContext(corehosts.ContextUnsupported), nil
		}
		if token == "" {
			return blockedContext(corehosts.ContextNoToken), nil
		}
		c.mu.Lock()
		slot := c.slots[key]
		if slot != nil && slot.done != nil {
			done := slot.done
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return ContextOutcome{}, ctx.Err()
			case <-done:
				joined = slot
				continue
			}
		}
		if slot == nil || slot.target != target || slot.credential != sha256.Sum256([]byte(token)) || slot.instance != inst {
			slot = &contextSlot{entry: corehosts.ContextEntry{Key: key, State: corehosts.CtxEmpty{}}, target: target, credential: sha256.Sum256([]byte(token)), instance: inst}
			c.slots[key] = slot
		}
		if joined == slot {
			if _, ok := slot.entry.State.(corehosts.CtxBlocked); ok {
				out := contextAnswer(slot.entry)
				c.mu.Unlock()
				return out, nil
			}
		}
		var cmds []corehosts.ContextCommand
		slot.entry, cmds = corehosts.ContextStep(slot.entry, corehosts.ContextRequested{Key: key, Scope: corehosts.ScopeTracked, Now: s.d.Clock()})
		if len(cmds) > 0 {
			if _, ok := cmds[0].(corehosts.CtxReturnCached); ok {
				out := contextAnswer(slot.entry)
				c.mu.Unlock()
				return out, nil
			}
		}
		slot.done = make(chan struct{})
		c.mu.Unlock()
		out, err := c.execute(ctx, slot, inst, token, client)
		c.mu.Lock()
		close(slot.done)
		slot.done = nil
		// A caller owns the returned slices; never expose the cache's backing data.
		if err != nil {
			delete(c.slots, key)
		}
		c.mu.Unlock()
		return out, err
	}
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
		return blockedContext(v.Reason)
	case corehosts.CtxWaiting:
		return ContextOutcome{RetryAt: v.At}
	case corehosts.CtxEmpty, corehosts.CtxLoading:
	}
	return ContextOutcome{}
}

func (c *ContextService) execute(ctx context.Context, slot *contextSlot, inst corehosts.Instance, token string, client ContextClient) (ContextOutcome, error) {
	e := slot.entry
	key := e.Key
	for {
		target, err := c.resolver.ResolveContext(ctx, key)
		if err != nil || target != slot.target {
			return ContextOutcome{}, ErrContextScope
		}
		c.service.mu.Lock()
		until := c.service.state.Limited[inst.ID]
		c.service.mu.Unlock()
		c.mu.Lock()
		if c.limited[inst.ID].After(until) {
			until = c.limited[inst.ID]
		}
		c.mu.Unlock()
		if wait := until.Sub(c.service.d.Clock()); wait > 0 {
			if deadline, ok := ctx.Deadline(); ok && !until.Before(deadline) {
				e, _ = corehosts.ContextStep(e, corehosts.ContextCancelled{Attempt: e.Seq})
				c.mu.Lock()
				slot.entry = e
				c.mu.Unlock()
				return ContextOutcome{RetryAt: until}, nil
			}

			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ContextOutcome{}, ctx.Err()
			case <-timer.C:
			}
			// Scope may have changed during the instance-wide rate-limit wait.
			refreshed, e := c.resolver.ResolveContext(ctx, key)
			if e != nil || refreshed != target {
				return ContextOutcome{}, ErrContextScope
			}
		}
		var result ContextResult
		number, _ := strconv.Atoi(key.ObjectID)
		switch key.Kind {
		case corehosts.ContextFailedJobs:
			result, err = client.FailedJobs(ctx, inst, token, target.Project, key.ObjectID, key.Head)
		case corehosts.ContextIssues:
			result, err = client.Issues(ctx, inst, token, target.Project)
		case corehosts.ContextIssue:
			result, err = client.Issue(ctx, inst, token, target.Project, number)
		case corehosts.ContextMRDiff:
			result, err = client.MRDiff(ctx, inst, token, target.Project, number)
		}
		if ctx.Err() != nil {
			return ContextOutcome{}, ctx.Err()
		}
		again, scopeErr := c.resolver.ResolveContext(ctx, key)
		if scopeErr != nil || again != target {
			return ContextOutcome{}, ErrContextScope
		}
		c.service.mu.Lock()
		current := c.service.cfg.Token(inst.ID)
		sameInstance := false
		for _, v := range c.service.cfg.CoreInstances() {
			if v == inst {
				sameInstance = true
			}
		}
		c.service.mu.Unlock()
		if current != token || !sameInstance {
			return ContextOutcome{}, ErrContextScope
		}
		if errors.Is(err, ErrContextTooLarge) || errors.Is(err, ErrContextUnsupported) {
			reason := corehosts.ContextUnsupported
			if errors.Is(err, ErrContextTooLarge) {
				reason = corehosts.ContextTooLarge
			}
			e, _ = corehosts.ContextReject(e, e.Seq, reason)
			c.mu.Lock()
			slot.entry = e
			c.mu.Unlock()
			return contextAnswer(e), nil
		}
		now := c.service.d.Clock()
		if err == nil {
			c.service.mu.Lock()
			secrets := c.service.cfg.Secrets()
			c.service.mu.Unlock()
			if len(result.Unavailable) > corehosts.ContextMaxEntries {
				return ContextOutcome{}, ErrContextScope
			}
			for _, gap := range result.Unavailable {
				if gap.Reason.Validate() != nil {
					return ContextOutcome{}, ErrContextScope
				}
			}
			result = redactContext(result, secrets...)
			if !corehosts.ContextPayloadMatches(key, result.Value) {
				return ContextOutcome{}, ErrContextScope
			}
			e, _ = corehosts.ContextStep(e, corehosts.ContextFetched{Key: key, Attempt: e.Seq, Value: result.Value, Unavailable: result.Unavailable, Now: now})
			c.mu.Lock()
			slot.entry = e
			c.mu.Unlock()
			return contextAnswer(e), nil
		}
		failure := corehosts.ContextFetchFailed{Key: key, Attempt: e.Seq, Class: corehosts.FailTransient, Now: now}
		var fe *FetchError
		if errors.As(err, &fe) {
			failure.Class = fe.Kind
			if fe.RetryAt != nil {
				failure.RetryAt = *fe.RetryAt
				c.mu.Lock()
				if failure.RetryAt.After(c.limited[inst.ID]) {
					c.limited[inst.ID] = failure.RetryAt
				}
				c.mu.Unlock()
			}
		}
		e, _ = corehosts.ContextStep(e, failure)
		wait, ok := e.State.(corehosts.CtxWaiting)
		if !ok {
			c.mu.Lock()
			slot.entry = e
			c.mu.Unlock()
			return contextAnswer(e), nil
		}
		if deadline, ok := ctx.Deadline(); ok && !wait.At.Before(deadline) {
			e, _ = corehosts.ContextStep(e, corehosts.ContextCancelled{Attempt: e.Seq})
			c.mu.Lock()
			slot.entry = e
			c.mu.Unlock()
			return ContextOutcome{RetryAt: wait.At}, nil
		}
		timer := time.NewTimer(wait.At.Sub(now))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ContextOutcome{}, ctx.Err()
		case <-timer.C:
		}
		e, _ = corehosts.ContextStep(e, corehosts.ContextWake{Attempt: e.Seq, Now: wait.At})
	}
}
