package hosts

// A reference-model property for ContextStep (GH-05, TM1-H). The existing
// TestPropContextFencingScopeAndRetries checks structural invariants; this one
// keeps a second, independent implementation of the design's transition table
// (docs/design/phase-6-host-context.md) and requires the production reducer to
// agree with it after EVERY step, including exact times: 60 s cache, 5 s blocked
// window, 1 s / 3 s backoff, the later of backoff and rate-limit deadline, and
// joined requests starting no extra read.

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"
)

type mKind int

const (
	mEmpty mKind = iota
	mLoading
	mReady
	mPartial
	mWaiting
	mBlocked
)

type mEntry struct {
	kind    mKind
	seq     int
	retries int
	t       time.Time // Expires, At or Until
	reason  ContextBlockReason
	wait    WaitReason
}

func (m mEntry) String() string {
	return fmt.Sprintf("{kind %d seq %d retries %d t %v reason %q wait %q}", m.kind, m.seq, m.retries, m.t.Unix(), m.reason, m.wait)
}

// modelStep is the table of the design, restated. Fields that mean nothing in
// the resulting state are cleared, as the reducer's states carry none.
func modelStep(m mEntry, in ContextInput, key ContextKey) (mEntry, []string) {
	next, cmds := modelStepRaw(m, in, key)
	if next.kind != mBlocked {
		next.reason = ""
	}
	if next.kind != mWaiting {
		next.wait = ""
	}
	if next.kind == mEmpty || next.kind == mLoading {
		next.t = time.Time{}
	}
	return next, cmds
}

func modelStepRaw(m mEntry, in ContextInput, key ContextKey) (mEntry, []string) {
	current := func(k ContextKey, attempt int) bool { return m.kind == mLoading && k == key && attempt == m.seq }
	cancelCmd := func() []string {
		if m.kind == mLoading || m.kind == mWaiting {
			return []string{"cancel"}
		}
		return nil
	}
	block := func(reason ContextBlockReason, now time.Time) (mEntry, []string) {
		m.kind, m.reason, m.t = mBlocked, reason, now.Add(5*time.Second)
		return m, []string{"explain"}
	}
	switch v := in.(type) {
	case ContextRequested:
		if v.Now.IsZero() || v.Key != key {
			return m, nil
		}
		if v.Scope != ScopeTracked {
			cmds := cancelCmd()
			reason := ContextUntracked
			if v.Scope == ScopeUnsupported {
				reason = ContextUnsupported
			}
			if v.Scope == ScopeNoToken {
				reason = ContextNoToken
			}
			m.seq++
			m.retries = 0
			m.kind, m.reason, m.t = mBlocked, reason, time.Time{}
			return m, append(cmds, "explain")
		}
		switch m.kind {
		case mReady, mPartial, mBlocked:
			if v.Now.Before(m.t) {
				return m, []string{"cached"}
			}
		case mLoading, mWaiting:
			return m, []string{"join"}
		case mEmpty:
		}
		m.retries = 0
		m.seq++
		m.kind = mLoading
		return m, []string{"read"}
	case ContextFetched:
		if !current(v.Key, v.Attempt) || v.Now.IsZero() {
			return m, nil
		}
		if !oraclePayload(key, v.Value) {
			return block(ContextNotFound, v.Now)
		}
		if len(v.Unavailable) > 500 {
			return block(ContextUnsupported, v.Now)
		}
		for _, g := range v.Unavailable {
			switch g.Reason {
			case ContextNoToken, ContextForbidden, ContextNotFound, ContextUnsupported, ContextRetryExhausted, ContextUntracked, ContextTooLarge:
			default:
				return block(ContextUnsupported, v.Now)
			}
		}
		m.kind = mReady
		if len(v.Unavailable) > 0 {
			m.kind = mPartial
		}
		m.t = v.Now.Add(60 * time.Second)
		return m, []string{"publish"}
	case ContextFetchFailed:
		if !current(v.Key, v.Attempt) || v.Now.IsZero() {
			return m, nil
		}
		switch v.Class {
		case FailTransient, FailRateLimited:
			if m.retries < 2 {
				m.retries++
				wait := time.Second
				if m.retries >= 2 {
					wait = 3 * time.Second
				}
				at := v.Now.Add(wait)
				if v.RetryAt.After(at) {
					at = v.RetryAt
				}
				m.kind, m.t = mWaiting, at
				m.wait = WaitTransient
				if v.Class == FailRateLimited {
					m.wait = WaitRateLimited
				}
				return m, []string{"wake"}
			}
			return block(ContextRetryExhausted, v.Now)
		case FailUnauthorized:
			return block(ContextForbidden, v.Now)
		case FailNotFound:
			return block(ContextNotFound, v.Now)
		}
		return block(ContextRetryExhausted, v.Now)
	case ContextRejected:
		if !current(v.Key, v.Attempt) || v.Now.IsZero() {
			return m, nil
		}
		switch v.Reason {
		case RefusedNotFound:
			return block(ContextNotFound, v.Now)
		case RefusedTooLarge:
			return block(ContextTooLarge, v.Now)
		}
		return block(ContextUnsupported, v.Now)
	case ContextRateLimited:
		if !current(v.Key, v.Attempt) || v.Now.IsZero() || !v.Until.After(v.Now) {
			return m, nil
		}
		m.kind, m.t, m.wait = mWaiting, v.Until, WaitRateLimited
		return m, []string{"wake"}
	case ContextWake:
		if m.kind != mWaiting || v.Now.IsZero() || v.Attempt != m.seq || v.Now.Before(m.t) {
			return m, nil
		}
		m.seq++
		m.kind = mLoading
		return m, []string{"read"}
	case ContextCancelled:
		if v.Attempt == m.seq && (m.kind == mLoading || m.kind == mWaiting) {
			m.seq++
			m.retries = 0
			m.kind = mEmpty
			return m, []string{"cancel"}
		}
		return m, nil
	case ContextScopeChanged, ContextConfigChanged:
		cmds := cancelCmd()
		m.seq++
		m.retries = 0
		m.kind = mEmpty
		return m, cmds
	}
	return m, nil
}

func modelCmdName(c ContextCommand) string {
	switch c.(type) {
	case CtxReadContext:
		return "read"
	case CtxScheduleWake:
		return "wake"
	case CtxCancelRead:
		return "cancel"
	case CtxReturnCached:
		return "cached"
	case CtxJoinRequest:
		return "join"
	case CtxPublishContext:
		return "publish"
	case CtxExplain:
		return "explain"
	}
	return "?"
}

func modelOf(e ContextEntry) mEntry {
	m := mEntry{seq: e.Seq, retries: e.Retries}
	switch st := e.State.(type) {
	case CtxEmpty:
		m.kind = mEmpty
	case CtxLoading:
		m.kind = mLoading
	case CtxReady:
		m.kind, m.t = mReady, st.Expires
	case CtxPartial:
		m.kind, m.t = mPartial, st.Expires
	case CtxWaiting:
		m.kind, m.t, m.wait = mWaiting, st.At, st.Reason
	case CtxBlocked:
		m.kind, m.t, m.reason = mBlocked, st.Until, st.Reason
	}
	return m
}

func modelPayload(key ContextKey, r *rand.Rand) ContextPayload {
	good := r.Intn(5) != 0
	switch key.Kind {
	case ContextMRDiff:
		head := key.Head
		if !good {
			head = strings.Repeat("c", 40)
		}
		return MRContext{MR: MergeRequest{Number: 1}, Head: head}
	case ContextFailedJobs:
		run := key.ObjectID
		if !good {
			run = "99"
		}
		return JobLogs{RunID: run, Head: key.Head}
	case ContextIssue:
		n := 1
		if !good {
			n = 2
		}
		return IssueDetail{Summary: IssueSummary{Number: n}}
	}
	if !good {
		return IssueDetail{Summary: IssueSummary{Number: 1}}
	}
	return IssueList{}
}

func modelInputs(seed int64, key ContextKey, n int) []ContextInput {
	r := rand.New(rand.NewSource(seed))
	var ins []ContextInput
	m := mEntry{}
	tick := int64(1000)
	for i := 0; i < n; i++ {
		tick += int64(r.Intn(40)) // time only moves forward, in whole seconds
		now := time.Unix(tick, 0)
		attempt := m.seq
		if r.Intn(5) == 0 {
			attempt = r.Intn(m.seq + 3)
		}
		var in ContextInput
		switch r.Intn(11) {
		case 0, 1:
			scopes := []ContextScope{ScopeTracked, ScopeTracked, ScopeTracked, ScopeUntracked, ScopeUnsupported, ScopeNoToken}
			in = ContextRequested{Key: key, Scope: scopes[r.Intn(len(scopes))], Now: now}
		case 2:
			var gaps []ContextUnavailable
			switch r.Intn(4) {
			case 0:
				gaps = []ContextUnavailable{{Source: "log", Reason: ContextUnsupported}}
			case 1:
				gaps = []ContextUnavailable{{Source: "log", Reason: "invented"}}
			}
			in = ContextFetched{Key: key, Attempt: attempt, Value: modelPayload(key, r), Unavailable: gaps, Now: now}
		case 3, 4:
			classes := []FailKind{FailTransient, FailRateLimited, FailUnauthorized, FailNotFound, "unclassified"}
			var retry time.Time
			if r.Intn(3) == 0 {
				retry = now.Add(time.Duration(r.Intn(12)) * time.Second)
			}
			in = ContextFetchFailed{Key: key, Attempt: attempt, Class: classes[r.Intn(len(classes))], RetryAt: retry, Now: now}
		case 5:
			reasons := []ContextRefusal{RefusedNotFound, RefusedUnsupported, RefusedTooLarge, "other"}
			in = ContextRejected{Key: key, Attempt: attempt, Reason: reasons[r.Intn(len(reasons))], Now: now}
		case 6:
			in = ContextRateLimited{Key: key, Attempt: attempt, Until: now.Add(time.Duration(r.Intn(15)-3) * time.Second), Now: now}
		case 7, 8:
			// Often exactly at, just before or after the due time.
			at := now
			if m.kind == mWaiting {
				at = m.t.Add(time.Duration(r.Intn(3)-1) * time.Second)
			}
			in = ContextWake{Attempt: attempt, Now: at}
		case 9:
			in = ContextCancelled{Attempt: attempt}
		default:
			if r.Intn(2) == 0 {
				in = ContextScopeChanged{}
			} else {
				in = ContextConfigChanged{}
			}
		}
		// The generator follows the model so that most results aim at a live attempt.
		m, _ = modelStep(m, in, key)
		ins = append(ins, in)
	}
	return ins
}

func checkModelSequence(key ContextKey, ins []ContextInput) error {
	e := ContextEntry{Key: key, State: CtxEmpty{}}
	m := mEntry{}
	for i, in := range ins {
		var cmds []ContextCommand
		e, cmds = ContextStep(e, in)
		var wantCmds []string
		m, wantCmds = modelStep(m, in, key)
		got := modelOf(e)
		if got != m {
			return fmt.Errorf("step %d %T: reducer %v, model %v", i, in, got, m)
		}
		var names []string
		for _, c := range cmds {
			names = append(names, modelCmdName(c))
		}
		if fmt.Sprint(names) != fmt.Sprint(wantCmds) {
			return fmt.Errorf("step %d %T: commands %v, model %v", i, in, names, wantCmds)
		}
		// Joined readers never start a second read.
		if r, ok := in.(ContextRequested); ok && r.Scope == ScopeTracked {
			for _, c := range cmds {
				if _, isRead := c.(CtxReadContext); isRead && (modelOfPrev(ins[:i], key).kind == mLoading || modelOfPrev(ins[:i], key).kind == mWaiting) {
					return fmt.Errorf("step %d: a request joined a read in flight and started another", i)
				}
			}
		}
	}
	return nil
}

func modelOfPrev(ins []ContextInput, key ContextKey) mEntry {
	m := mEntry{}
	for _, in := range ins {
		m, _ = modelStep(m, in, key)
	}
	return m
}

func TestPropContextStepAgreesWithReferenceModel(t *testing.T) {
	reached := map[string]bool{}
	for ki, key := range contextPropKeys() {
		for seed := int64(1); seed <= 250; seed++ {
			ins := modelInputs(seed+int64(ki)*7919, key, 80)
			if err := checkModelSequence(key, ins); err != nil {
				for i := 0; i < len(ins); { // shrink to a small failing sequence
					cand := append(append([]ContextInput(nil), ins[:i]...), ins[i+1:]...)
					if checkModelSequence(key, cand) != nil {
						ins = cand
						i = 0
					} else {
						i++
					}
				}
				t.Fatalf("key %v seed %d: %v\nminimal sequence: %#v", key, seed, checkModelSequence(key, ins), ins)
			}
			m := mEntry{}
			for _, in := range ins {
				m, _ = modelStep(m, in, key)
				reached[fmt.Sprintf("%d/%s/%s", m.kind, m.reason, m.wait)] = true
			}
		}
	}
	// The generator must reach waits for both reasons, every block reason of a
	// read, retry exhaustion and cache states.
	for _, want := range []string{
		fmt.Sprintf("%d//", mReady), fmt.Sprintf("%d//", mPartial),
		fmt.Sprintf("%d//%s", mWaiting, WaitTransient), fmt.Sprintf("%d//%s", mWaiting, WaitRateLimited),
		fmt.Sprintf("%d/%s/", mBlocked, ContextRetryExhausted), fmt.Sprintf("%d/%s/", mBlocked, ContextForbidden),
		fmt.Sprintf("%d/%s/", mBlocked, ContextNotFound), fmt.Sprintf("%d/%s/", mBlocked, ContextTooLarge),
		fmt.Sprintf("%d/%s/", mBlocked, ContextUnsupported), fmt.Sprintf("%d/%s/", mBlocked, ContextUntracked),
		fmt.Sprintf("%d/%s/", mBlocked, ContextNoToken),
	} {
		if !reached[want] {
			t.Errorf("the model generator never reached %s", want)
		}
	}
}

// Exact times of the backoff chain, written out rather than derived.
func TestContextBackoffChainTimes(t *testing.T) {
	key := ContextKey{Repo: "rep_1", Kind: ContextIssues}
	t0 := time.Unix(5000, 0)
	e := ctxStart(key, t0)
	e, c := ContextStep(e, ContextFetchFailed{Key: key, Attempt: e.Seq, Class: FailTransient, Now: t0})
	if w := e.State.(CtxWaiting); !w.At.Equal(t0.Add(1*time.Second)) || w.Retries != 1 || len(c) != 1 {
		t.Fatalf("first wait %+v", w)
	}
	t1 := t0.Add(time.Second)
	e, _ = ContextStep(e, ContextWake{Attempt: e.Seq, Now: t1})
	e, _ = ContextStep(e, ContextFetchFailed{Key: key, Attempt: e.Seq, Class: FailTransient, Now: t1})
	if w := e.State.(CtxWaiting); !w.At.Equal(t1.Add(3*time.Second)) || w.Retries != 2 {
		t.Fatalf("second wait %+v", w)
	}
	t2 := t1.Add(3 * time.Second)
	e, _ = ContextStep(e, ContextWake{Attempt: e.Seq, Now: t2})
	e, c = ContextStep(e, ContextFetchFailed{Key: key, Attempt: e.Seq, Class: FailTransient, Now: t2})
	b, ok := e.State.(CtxBlocked)
	if !ok || b.Reason != ContextRetryExhausted || !b.Until.Equal(t2.Add(5*time.Second)) || len(c) != 1 {
		t.Fatalf("after two retries: %#v", e.State)
	}
}
