package hosts

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

func TestContextTransitions(t *testing.T) {
	key := ContextKey{Repo: "rep_1", Kind: ContextIssues}
	now := time.Unix(100, 0)
	e := ContextEntry{Key: key, State: CtxEmpty{}}
	e, cmd := ContextStep(e, ContextRequested{Key: key, Scope: ScopeTracked, Now: now})
	if _, ok := e.State.(CtxLoading); !ok || e.Seq != 1 || len(cmd) != 1 {
		t.Fatal(e, cmd)
	}
	unchanged, c := ContextStep(e, ContextFetched{Key: key, Attempt: 0, Value: IssueList{}, Now: now})
	if !reflect.DeepEqual(e, unchanged) || len(c) != 0 {
		t.Fatal("stale result accepted")
	}
	for retry := 1; retry <= 3; retry++ {
		e, _ = ContextStep(e, ContextFetchFailed{Key: key, Attempt: e.Seq, Class: FailTransient, Now: now})
		if retry == 3 {
			if _, ok := e.State.(CtxBlocked); !ok {
				t.Fatal(e)
			}
			break
		}
		waiting := e.State.(CtxWaiting)
		if waiting.At != now.Add(ContextBackoff(retry)) {
			t.Fatal(waiting)
		}
		early, _ := ContextStep(e, ContextWake{Attempt: e.Seq, Now: now})
		if !reflect.DeepEqual(e, early) {
			t.Fatal("early wake")
		}
		e, _ = ContextStep(e, ContextWake{Attempt: e.Seq, Now: waiting.At})
	}
	e, _ = ContextStep(e, ContextRequested{Key: key, Scope: ScopeTracked, Now: now})
	e, _ = ContextStep(e, ContextFetched{Key: key, Attempt: e.Seq, Value: IssueList{}, Now: now})
	cached, c := ContextStep(e, ContextRequested{Key: key, Scope: ScopeTracked, Now: now.Add(time.Second)})
	if !reflect.DeepEqual(e, cached) {
		t.Fatal("cache lost")
	}
	if _, ok := c[0].(CtxReturnCached); !ok {
		t.Fatal(c)
	}
	expired, _ := ContextStep(e, ContextRequested{Key: key, Scope: ScopeTracked, Now: now.Add(ContextCacheTTL)})
	if _, ok := expired.State.(CtxLoading); !ok {
		t.Fatal("cache never expires")
	}
	untracked, _ := ContextStep(e, ContextRequested{Key: key, Scope: ScopeUntracked, Now: now})
	if untracked.State.(CtxBlocked).Reason != ContextUntracked {
		t.Fatal("untracked cache exposed")
	}
}

// contextPropKeys are the keys a run may use: one per kind, plus an MR diff key
// pinned to another head.
func contextPropKeys() []ContextKey {
	headA, headB := strings.Repeat("a", 40), strings.Repeat("b", 40)
	return []ContextKey{
		{Repo: "rep_1", Kind: ContextIssues},
		{Repo: "rep_1", Kind: ContextMRDiff, ObjectID: "1", Head: headA},
		{Repo: "rep_1", Kind: ContextMRDiff, ObjectID: "1", Head: headB},
		{Repo: "rep_1", Kind: ContextFailedJobs, ObjectID: "7", Head: headA},
		{Repo: "rep_2", Kind: ContextIssues},
	}
}

// contextPropPayload is a payload that matches key, or, when good is false,
// one that does not.
func contextPropPayload(key ContextKey, good bool) ContextPayload {
	if !good {
		return IssueDetail{Summary: IssueSummary{Number: 99}}
	}
	switch key.Kind {
	case ContextMRDiff:
		return MRContext{MR: MergeRequest{Number: 1}, Head: key.Head}
	case ContextFailedJobs:
		return JobLogs{RunID: key.ObjectID, Head: key.Head}
	}
	return IssueList{}
}

func contextSequence(seed int64, key ContextKey) []ContextInput {
	r := rand.New(rand.NewSource(seed))
	keys := contextPropKeys()
	classes := []FailKind{FailTransient, FailRateLimited, FailUnauthorized, FailNotFound}
	// Includes values outside the closed set: they must end the attempt too.
	reasons := []ContextRefusal{RefusedNotFound, RefusedUnsupported, RefusedTooLarge, "boom", ""}
	var inputs []ContextInput
	// The generator folds its own inputs so that most results aim at the attempt
	// that is current at that point.
	e := ContextEntry{Key: key, State: CtxEmpty{}}
	for i := 0; i < 120; i++ {
		now := time.Unix(int64(1000+i*5), 0)
		if r.Intn(25) == 0 {
			now = time.Time{} // a zero time must never be accepted
		}
		attempt := e.Seq
		if r.Intn(4) == 0 {
			attempt = r.Intn(8)
		}
		k := key
		if r.Intn(6) == 0 {
			k = keys[r.Intn(len(keys))]
		}
		before := len(inputs)
		switch r.Intn(13) {
		case 0:
			scopes := []ContextScope{ScopeTracked, ScopeTracked, ScopeUntracked, ScopeUnsupported, ScopeNoToken}
			inputs = append(inputs, ContextRequested{Key: k, Scope: scopes[r.Intn(len(scopes))], Now: now})
		case 1:
			inputs = append(inputs, ContextRequested{Key: k, Scope: ScopeTracked, Now: now})
		case 2:
			var gaps []ContextUnavailable
			switch r.Intn(4) {
			case 0:
				gaps = []ContextUnavailable{{Source: "log", Reason: ContextUnsupported}}
			case 1:
				gaps = []ContextUnavailable{{Source: "log", Reason: "not a reason"}}
			case 2:
				gaps = make([]ContextUnavailable, ContextMaxEntries+1)
			}
			inputs = append(inputs, ContextFetched{Key: k, Attempt: attempt, Value: contextPropPayload(k, r.Intn(4) != 0), Unavailable: gaps, Now: now})
		case 3:
			inputs = append(inputs, ContextFetched{Key: k, Attempt: -1, Value: contextPropPayload(k, true), Now: now})
		case 4, 5:
			var retry time.Time
			if r.Intn(3) == 0 {
				retry = now.Add(time.Duration(r.Intn(30)) * time.Second)
			}
			inputs = append(inputs, ContextFetchFailed{Key: k, Attempt: attempt, Class: classes[r.Intn(len(classes))], RetryAt: retry, Now: now})
		case 6:
			inputs = append(inputs, ContextRejected{Key: k, Attempt: attempt, Reason: reasons[r.Intn(len(reasons))], Now: now})
		case 7:
			inputs = append(inputs, ContextRateLimited{Key: k, Attempt: attempt, Until: now.Add(time.Duration(r.Intn(10)-2) * time.Second), Now: now})
		case 8:
			inputs = append(inputs, ContextWake{Attempt: attempt, Now: now})
		case 9:
			inputs = append(inputs, ContextCancelled{Attempt: attempt})
		case 10:
			inputs = append(inputs, ContextScopeChanged{})
		case 11:
			inputs = append(inputs, ContextConfigChanged{})
		case 12:
			// An answer for the attempt that is current at the time the input is
			// generated cannot be known here, so aim at small attempts.
			inputs = append(inputs, ContextFetchFailed{Key: k, Attempt: 1 + r.Intn(3), Class: FailTransient, Now: now})
		}
		for _, in := range inputs[before:] {
			e, _ = ContextStep(e, in)
		}
	}
	return inputs
}

func contextInputNow(in ContextInput) (time.Time, bool) {
	switch v := in.(type) {
	case ContextRequested:
		return v.Now, true
	case ContextFetched:
		return v.Now, true
	case ContextFetchFailed:
		return v.Now, true
	case ContextRejected:
		return v.Now, true
	case ContextRateLimited:
		return v.Now, true
	case ContextWake:
		return v.Now, true
	case ContextCancelled, ContextScopeChanged, ContextConfigChanged:
	}
	return time.Time{}, false
}

// contextResultFor returns the key and attempt of an input that reports the end
// of one attempt (Fetched, FetchFailed, Rejected, RateLimited).
func contextResultFor(in ContextInput) (ContextKey, int, bool) {
	switch v := in.(type) {
	case ContextFetched:
		return v.Key, v.Attempt, true
	case ContextFetchFailed:
		return v.Key, v.Attempt, true
	case ContextRejected:
		return v.Key, v.Attempt, true
	case ContextRateLimited:
		return v.Key, v.Attempt, true
	case ContextRequested, ContextWake, ContextCancelled, ContextScopeChanged, ContextConfigChanged:
	}
	return ContextKey{}, 0, false
}

// contextIsUselessRateLimit: a rate-limit report whose deadline is not in the
// future asks for no wait, so it changes nothing. It is not a result: the
// shell owns the read in flight and goes on reading.
func contextIsUselessRateLimit(in ContextInput) bool {
	rl, ok := in.(ContextRateLimited)
	return ok && !rl.Until.After(rl.Now)
}

func checkContextSequence(key ContextKey, ins []ContextInput) error {
	e := ContextEntry{Key: key, State: CtxEmpty{}}
	for i, in := range ins {
		next, cmd := ContextStep(e, in)
		fail := func(format string, args ...any) error {
			return fmt.Errorf("step %d (%T): %s", i, in, fmt.Sprintf(format, args...))
		}
		if next.Seq < e.Seq || next.Retries > ContextMaxRetries || next.Key != e.Key {
			return fail("attempt/retry/key bounds")
		}
		if load, ok := next.State.(CtxLoading); ok && load.Attempt != next.Seq {
			return fail("loading attempt %d is not the newest %d", load.Attempt, next.Seq)
		}
		if now, timed := contextInputNow(in); timed && now.IsZero() && (!reflect.DeepEqual(e, next) || len(cmd) > 0) {
			return fail("a zero time was accepted")
		}
		if k, attempt, result := contextResultFor(in); result {
			_, loading := e.State.(CtxLoading)
			current := loading && k == e.Key && attempt == e.Seq
			if !current && (!reflect.DeepEqual(e, next) || len(cmd) > 0) {
				return fail("a late or foreign result changed the entry")
			}
			now, _ := contextInputNow(in)
			if current && !now.IsZero() && !contextIsUselessRateLimit(in) {
				if _, still := next.State.(CtxLoading); still {
					return fail("a result for the current attempt left it Loading")
				}
			}
		}
		// Waiting names the newest attempt and the retries used so far.
		if w, ok := next.State.(CtxWaiting); ok && (w.Attempt != next.Seq || w.Retries != next.Retries) {
			return fail("waiting %d/%d does not match seq %d retries %d", w.Attempt, w.Retries, next.Seq, next.Retries)
		}
		// A block that came from the supplied scope has no window; a block that
		// ends a read lasts ContextBlockedTTL from the supplied time.
		if b, ok := next.State.(CtxBlocked); ok && !reflect.DeepEqual(e, next) {
			if _, fromScope := in.(ContextRequested); fromScope {
				if !b.Until.IsZero() {
					return fail("a scope block has a window %v", b.Until)
				}
			} else if now, _ := contextInputNow(in); !b.Until.Equal(now.Add(ContextBlockedTTL)) {
				return fail("a read block lasts until %v, want now+TTL", b.Until)
			}
		}
		for _, c := range cmd {
			switch v := c.(type) {
			case CtxReadContext:
				if l, ok := next.State.(CtxLoading); !ok || l.Attempt != v.Attempt || v.Attempt != next.Seq || v.Key != next.Key {
					return fail("ReadContext does not match the Loading attempt")
				}
			case CtxScheduleWake:
				if w, ok := next.State.(CtxWaiting); !ok || w.Attempt != v.Attempt || !w.At.Equal(v.At) {
					return fail("ScheduleWake does not match the Waiting state")
				}
				if now, _ := contextInputNow(in); !v.At.After(now) {
					return fail("wake is not in the future")
				}
			case CtxPublishContext:
				switch next.State.(type) {
				case CtxReady, CtxPartial:
				case CtxEmpty, CtxLoading, CtxWaiting, CtxBlocked:
					return fail("publish without a value")
				}
			case CtxExplain:
				if b, ok := next.State.(CtxBlocked); !ok || b.Reason != v.Reason {
					return fail("explain does not match the Blocked state")
				}
			case CtxCancelRead, CtxJoinRequest, CtxReturnCached:
			}
		}
		if v, ok := in.(ContextRequested); ok && v.Key == e.Key {
			for _, c := range cmd {
				switch c.(type) {
				case CtxReturnCached:
					if v.Scope != ScopeTracked {
						return fail("cache served for an untracked or unsupported scope")
					}
					switch st := e.State.(type) {
					case CtxReady:
						if !v.Now.Before(st.Expires) {
							return fail("expired Ready served")
						}
					case CtxPartial:
						if !v.Now.Before(st.Expires) {
							return fail("expired Partial served")
						}
					case CtxBlocked:
						if !v.Now.Before(st.Until) {
							return fail("expired Blocked served")
						}
					case CtxEmpty, CtxLoading, CtxWaiting:
						return fail("cache served from %T", e.State)
					}
				case CtxReadContext:
					if v.Scope != ScopeTracked {
						return fail("read for an untracked or unsupported scope")
					}
				case CtxScheduleWake, CtxCancelRead, CtxJoinRequest, CtxPublishContext, CtxExplain:
				}
			}
		}
		switch in.(type) {
		case ContextScopeChanged, ContextConfigChanged:
			if _, ok := next.State.(CtxEmpty); !ok {
				return fail("a scope or config change kept %T", next.State)
			}
			if next.Seq <= e.Seq {
				return fail("a scope or config change did not raise the attempt counter")
			}
		case ContextRequested, ContextFetched, ContextFetchFailed, ContextRejected, ContextRateLimited, ContextWake, ContextCancelled:
		}
		e = next
	}
	return nil
}

func TestPropContextFencingScopeAndRetries(t *testing.T) {
	for ki, key := range contextPropKeys() {
		for seed := int64(1); seed <= 300; seed++ {
			inputs := contextSequence(seed+int64(ki)*1000, key)
			if err := checkContextSequence(key, inputs); err != nil {
				for i := 0; i < len(inputs); {
					candidate := append(append([]ContextInput(nil), inputs[:i]...), inputs[i+1:]...)
					if checkContextSequence(key, candidate) != nil {
						inputs = candidate
						i = 0
					} else {
						i++
					}
				}
				t.Fatalf("key %v seed %d: %v; minimized: %#v", key, seed, err, inputs)
			}
		}
	}
}

// A cached MR diff must never answer for another head: the key carries the head,
// so a moved MR is a different key, and a payload for another head is refused.
func TestContextCacheIsHeadBound(t *testing.T) {
	headA, headB := strings.Repeat("a", 40), strings.Repeat("b", 40)
	keyA := ContextKey{Repo: "rep_1", Kind: ContextMRDiff, ObjectID: "1", Head: headA}
	keyB := keyA
	keyB.Head = headB
	now := time.Unix(1000, 0)
	e, _ := ContextStep(ContextEntry{Key: keyA, State: CtxEmpty{}}, ContextRequested{Key: keyA, Scope: ScopeTracked, Now: now})
	e, _ = ContextStep(e, ContextFetched{Key: keyA, Attempt: e.Seq, Now: now, Value: MRContext{MR: MergeRequest{Number: 1}, Head: headA}})
	if _, ok := e.State.(CtxReady); !ok {
		t.Fatalf("setup: want Ready, got %T", e.State)
	}
	if _, cmds := ContextStep(e, ContextRequested{Key: keyA, Scope: ScopeTracked, Now: now.Add(time.Second)}); len(cmds) != 1 {
		t.Fatal("same head within the TTL must be served from the cache")
	} else if _, ok := cmds[0].(CtxReturnCached); !ok {
		t.Fatalf("same head: want ReturnCached, got %T", cmds[0])
	}
	after, cmds := ContextStep(e, ContextRequested{Key: keyB, Scope: ScopeTracked, Now: now.Add(time.Second)})
	if !reflect.DeepEqual(after, e) || len(cmds) != 0 {
		t.Fatal("a request for another head must not be answered from, or change, the entry")
	}
	if ContextPayloadMatches(keyA, MRContext{MR: MergeRequest{Number: 1}, Head: headB}) {
		t.Fatal("a diff for another head matched the key")
	}
	headless := keyA
	headless.Head = ""
	if ContextPayloadMatches(headless, MRContext{MR: MergeRequest{Number: 1}, Head: headA}) {
		t.Fatal("a headless MR diff key matched a payload: the cache would not be head-bound")
	}
	if !ContextPayloadMatches(keyA, MRContext{MR: MergeRequest{Number: 1}, Head: headA}) {
		t.Fatal("the diff for the key's own head was refused")
	}
}

// The property test is only worth something if its generator reaches the
// interesting states and commands; this fails if a change to the generator
// stops doing so.
func TestPropContextGeneratorReachesEveryStateAndCommand(t *testing.T) {
	seen := map[string]bool{}
	for ki, key := range contextPropKeys() {
		for seed := int64(1); seed <= 300; seed++ {
			e := ContextEntry{Key: key, State: CtxEmpty{}}
			for _, in := range contextSequence(seed+int64(ki)*1000, key) {
				var cmds []ContextCommand
				e, cmds = ContextStep(e, in)
				seen[fmt.Sprintf("state %T", e.State)] = true
				for _, c := range cmds {
					seen[fmt.Sprintf("command %T", c)] = true
				}
			}
		}
	}
	for _, want := range []string{
		"state hosts.CtxEmpty", "state hosts.CtxLoading", "state hosts.CtxReady", "state hosts.CtxPartial",
		"state hosts.CtxWaiting", "state hosts.CtxBlocked",
		"command hosts.CtxReadContext", "command hosts.CtxScheduleWake", "command hosts.CtxCancelRead",
		"command hosts.CtxReturnCached", "command hosts.CtxJoinRequest", "command hosts.CtxPublishContext", "command hosts.CtxExplain",
	} {
		if !seen[want] {
			t.Errorf("the generator never reached %s", want)
		}
	}
}

func FuzzContextKey(f *testing.F) {
	f.Add("rep_1", "issue", "1", "")
	f.Add("rep_1", "mr_diff", "1", strings.Repeat("a", 40))
	f.Add("rep_1", "mr_diff", "1", "")
	f.Fuzz(func(t *testing.T, repo, kind, id, head string) {
		k := ContextKey{Repo: core.RepoID(repo), Kind: ContextKind(kind), ObjectID: id, Head: head}
		if k.Validate() != nil {
			return
		}
		raw, err := json.Marshal(k)
		if err != nil {
			t.Fatal(err)
		}
		var decoded ContextKey
		if json.Unmarshal(raw, &decoded) != nil || decoded != k {
			t.Fatal("key round trip")
		}
		if k.Repo == "" || k.Kind == ContextIssues && k.ObjectID != "" || k.Kind == ContextMRDiff && k.Head == "" {
			t.Fatal("invalid accepted")
		}
	})
}
