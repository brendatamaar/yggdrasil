package hosts

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// Tests of the context inputs added by the Batch 2 follow-up (GH-05):
// Rejected, RateLimited, zero time, negative cache, scope and config resets.

func ctxStart(key ContextKey, now time.Time) ContextEntry {
	e, _ := ContextStep(ContextEntry{Key: key, State: CtxEmpty{}}, ContextRequested{Key: key, Scope: ScopeTracked, Now: now})
	return e
}

func TestContextHeadFenceAndCancellation(t *testing.T) {
	head := strings.Repeat("a", 40)
	now := time.Unix(1000, 0)
	key := ContextKey{Repo: "rep_1", Kind: ContextMRDiff, ObjectID: "1", Head: head}
	e := ctxStart(key, now)
	other := MRContext{MR: MergeRequest{Number: 1}, Head: strings.Repeat("b", 40)}
	// A payload pinned to another head for the current attempt is refused: the
	// entry leaves Loading for Blocked and nothing is published.
	next, cmd := ContextStep(e, ContextFetched{Key: key, Attempt: e.Seq, Value: other, Now: now})
	if _, ok := next.State.(CtxBlocked); !ok || len(cmd) != 1 {
		t.Fatal("wrong head published", next.State, cmd)
	}
	if _, publish := cmd[0].(CtxPublishContext); publish {
		t.Fatal("wrong head published")
	}
	cancelled, _ := ContextStep(e, ContextCancelled{Attempt: e.Seq})
	restarted, _ := ContextStep(cancelled, ContextRequested{Key: key, Scope: ScopeTracked, Now: now})
	if restarted.Seq <= e.Seq {
		t.Fatal("attempt reused")
	}
	next, cmd = ContextStep(restarted, ContextFetched{Key: key, Attempt: e.Seq, Value: MRContext{MR: MergeRequest{Number: 1}, Head: head}, Now: now})
	if !reflect.DeepEqual(restarted, next) || len(cmd) > 0 {
		t.Fatal("cancelled attempt published")
	}
}

func TestContextRejectsMalformedPayloadBoundaries(t *testing.T) {
	head := strings.Repeat("a", 40)
	now := time.Unix(1000, 0)
	key := ContextKey{Repo: "rep_1", Kind: ContextFailedJobs, ObjectID: "1", Head: head}
	// An unusable payload for the current attempt is a terminal answer, not a
	// silent no-op that leaves the entry Loading for ever.
	assertBlocked := func(t *testing.T, e ContextEntry, in ContextInput) {
		t.Helper()
		got, cmd := ContextStep(e, in)
		b, ok := got.State.(CtxBlocked)
		if !ok || b.Reason != ContextUnsupported || !b.Until.Equal(now.Add(ContextBlockedTTL)) {
			t.Fatalf("unusable payload left %#v", got.State)
		}
		if len(cmd) != 1 {
			t.Fatal(cmd)
		}
		if ex, ok := cmd[0].(CtxExplain); !ok || ex.Reason != ContextUnsupported {
			t.Fatal(cmd)
		}
	}
	for name, payload := range map[string]ContextPayload{
		"bad job id":    JobLogs{RunID: "1", Head: head, Jobs: []JobLog{{JobID: "https://foreign.test/1"}}},
		"missing head":  JobLogs{RunID: "1", Jobs: []JobLog{{JobID: "1"}}},
		"too many jobs": JobLogs{RunID: "1", Head: head, Jobs: make([]JobLog, ContextMaxEntries+1)},
		"oversized log": JobLogs{RunID: "1", Head: head, Jobs: []JobLog{{JobID: "1", Log: strings.Repeat("x", ContextMaxJobLogBytes+1)}}},
		"other kind":    IssueList{},
		"other run":     JobLogs{RunID: "2", Head: head},
	} {
		t.Run(name, func(t *testing.T) {
			e := ctxStart(key, now)
			assertBlocked(t, e, ContextFetched{Key: key, Attempt: e.Seq, Value: payload, Now: now})
		})
	}
	issueKey := ContextKey{Repo: "rep_1", Kind: ContextIssues}
	t.Run("unknown gap reason", func(t *testing.T) {
		e := ctxStart(issueKey, now)
		assertBlocked(t, e, ContextFetched{Key: issueKey, Attempt: e.Seq, Value: IssueList{}, Unavailable: []ContextUnavailable{{Source: "log", Reason: "unparsed outside text"}}, Now: now})
	})
	t.Run("more than the gap limit", func(t *testing.T) {
		e := ctxStart(issueKey, now)
		assertBlocked(t, e, ContextFetched{Key: issueKey, Attempt: e.Seq, Value: IssueList{}, Unavailable: make([]ContextUnavailable, ContextMaxEntries+1), Now: now})
	})
	t.Run("late attempt is still ignored", func(t *testing.T) {
		e := ctxStart(issueKey, now)
		got, cmd := ContextStep(e, ContextFetched{Key: issueKey, Attempt: e.Seq + 1, Value: IssueDetail{}, Now: now})
		if !reflect.DeepEqual(got, e) || len(cmd) > 0 {
			t.Fatal("a result for another attempt changed the entry")
		}
	})
}

func TestContextRejectedIsPartOfTheInputSet(t *testing.T) {
	now := time.Unix(1000, 0)
	key := ContextKey{Repo: "rep_1", Kind: ContextIssues}
	for _, reason := range []ContextBlockReason{ContextNotFound, ContextUnsupported, ContextTooLarge} {
		e := ctxStart(key, now)
		got, cmd := ContextStep(e, ContextRejected{Key: key, Attempt: e.Seq, Reason: reason, Now: now})
		b, ok := got.State.(CtxBlocked)
		if !ok || b.Reason != reason || len(cmd) != 1 {
			t.Fatalf("%s: %#v %v", reason, got.State, cmd)
		}
		if ex, ok := cmd[0].(CtxExplain); !ok || ex.Reason != reason {
			t.Fatalf("%s: %v", reason, cmd)
		}
	}
	// Not a refusal the shell may report, a stale attempt, a foreign key and a
	// zero time all change nothing.
	e := ctxStart(key, now)
	for name, in := range map[string]ContextRejected{
		"retry exhausted is not a refusal": {Key: key, Attempt: e.Seq, Reason: ContextRetryExhausted, Now: now},
		"unknown reason":                   {Key: key, Attempt: e.Seq, Reason: "boom", Now: now},
		"stale attempt":                    {Key: key, Attempt: e.Seq - 1, Reason: ContextNotFound, Now: now},
		"foreign key":                      {Key: ContextKey{Repo: "rep_2", Kind: ContextIssues}, Attempt: e.Seq, Reason: ContextNotFound, Now: now},
		"zero time":                        {Key: key, Attempt: e.Seq, Reason: ContextNotFound},
	} {
		got, cmd := ContextStep(e, in)
		if !reflect.DeepEqual(got, e) || len(cmd) > 0 {
			t.Fatalf("%s: changed the entry", name)
		}
	}
	// A rejection of a finished attempt is ignored too.
	done, _ := ContextStep(e, ContextRejected{Key: key, Attempt: e.Seq, Reason: ContextNotFound, Now: now})
	got, cmd := ContextStep(done, ContextRejected{Key: key, Attempt: e.Seq, Reason: ContextUnsupported, Now: now})
	if !reflect.DeepEqual(got, done) || len(cmd) > 0 {
		t.Fatal("a second rejection changed a blocked entry")
	}
}

func TestContextZeroTimeIsInvalidInput(t *testing.T) {
	now := time.Unix(1000, 0)
	key := ContextKey{Repo: "rep_1", Kind: ContextIssues}
	loading := ctxStart(key, now)
	waiting, _ := ContextStep(loading, ContextFetchFailed{Key: key, Attempt: loading.Seq, Class: FailTransient, Now: now})
	for name, c := range map[string]struct {
		e  ContextEntry
		in ContextInput
	}{
		"requested": {ContextEntry{Key: key, State: CtxEmpty{}}, ContextRequested{Key: key, Scope: ScopeTracked}},
		"fetched":   {loading, ContextFetched{Key: key, Attempt: loading.Seq, Value: IssueList{}}},
		"failed":    {loading, ContextFetchFailed{Key: key, Attempt: loading.Seq, Class: FailTransient}},
		"rejected":  {loading, ContextRejected{Key: key, Attempt: loading.Seq, Reason: ContextNotFound}},
		"limited":   {loading, ContextRateLimited{Key: key, Attempt: loading.Seq, Until: now.Add(time.Second)}},
		"wake":      {waiting, ContextWake{Attempt: waiting.Seq}},
	} {
		got, cmd := ContextStep(c.e, c.in)
		if !reflect.DeepEqual(got, c.e) || len(cmd) > 0 {
			t.Errorf("%s: a zero time was accepted: %#v %v", name, got.State, cmd)
		}
	}
	// A zero Now must not create an instantly expired Ready entry.
	got, _ := ContextStep(loading, ContextFetched{Key: key, Attempt: loading.Seq, Value: IssueList{}})
	if _, ok := got.State.(CtxReady); ok {
		t.Fatal("zero Now produced a Ready entry")
	}
}

func TestContextBlockedNegativeCache(t *testing.T) {
	now := time.Unix(1000, 0)
	key := ContextKey{Repo: "rep_1", Kind: ContextIssues}
	loading := ctxStart(key, now)
	blocked, _ := ContextStep(loading, ContextFetchFailed{Key: key, Attempt: loading.Seq, Class: FailUnauthorized, Now: now})
	if b := blocked.State.(CtxBlocked); b.Reason != ContextForbidden || !b.Until.Equal(now.Add(ContextBlockedTTL)) {
		t.Fatal(b)
	}
	// Inside the window the block answers from cache: no host call.
	got, cmd := ContextStep(blocked, ContextRequested{Key: key, Scope: ScopeTracked, Now: now.Add(ContextBlockedTTL - time.Nanosecond)})
	if !reflect.DeepEqual(got, blocked) || len(cmd) != 1 {
		t.Fatal("a blocked key refetched inside the window", cmd)
	}
	if _, ok := cmd[0].(CtxReturnCached); !ok {
		t.Fatal(cmd)
	}
	// At the deadline the user may ask again.
	got, cmd = ContextStep(blocked, ContextRequested{Key: key, Scope: ScopeTracked, Now: now.Add(ContextBlockedTTL)})
	if _, ok := got.State.(CtxLoading); !ok || len(cmd) != 1 {
		t.Fatal("blocked key never retried", got.State)
	}
	// A config or scope change resets it before the deadline.
	for _, in := range []ContextInput{ContextConfigChanged{}, ContextScopeChanged{}} {
		reset, _ := ContextStep(blocked, in)
		if _, ok := reset.State.(CtxEmpty); !ok || reset.Seq <= blocked.Seq {
			t.Fatalf("%T did not reset the block: %#v", in, reset)
		}
		again, cmd := ContextStep(reset, ContextRequested{Key: key, Scope: ScopeTracked, Now: now.Add(time.Second)})
		if _, ok := again.State.(CtxLoading); !ok || len(cmd) != 1 {
			t.Fatalf("%T: a reset key did not read again", in)
		}
	}
	// Scope-derived blocks carry no window.
	for scope, reason := range map[ContextScope]ContextBlockReason{ScopeUntracked: ContextUntracked, ScopeUnsupported: ContextUnsupported, ScopeNoToken: ContextNoToken} {
		got, cmd := ContextStep(loading, ContextRequested{Key: key, Scope: scope, Now: now})
		b, ok := got.State.(CtxBlocked)
		if !ok || b.Reason != reason || !b.Until.IsZero() {
			t.Fatalf("%s: %#v", scope, got.State)
		}
		if len(cmd) != 2 {
			t.Fatalf("%s: %v", scope, cmd)
		}
		if _, ok := cmd[0].(CtxCancelRead); !ok {
			t.Fatalf("%s: the read in flight was not cancelled: %v", scope, cmd)
		}
		if ex, ok := cmd[1].(CtxExplain); !ok || ex.Reason != reason {
			t.Fatalf("%s: %v", scope, cmd)
		}
		// Once the scope is tracked again the key reads at once.
		back, _ := ContextStep(got, ContextRequested{Key: key, Scope: ScopeTracked, Now: now})
		if _, ok := back.State.(CtxLoading); !ok {
			t.Fatalf("%s: a scope block was cached", scope)
		}
	}
}

func TestContextConfigAndScopeChangeDropEveryHeldState(t *testing.T) {
	now := time.Unix(1000, 0)
	key := ContextKey{Repo: "rep_1", Kind: ContextIssues}
	loading := ctxStart(key, now)
	ready, _ := ContextStep(loading, ContextFetched{Key: key, Attempt: loading.Seq, Value: IssueList{}, Now: now})
	waiting, _ := ContextStep(loading, ContextFetchFailed{Key: key, Attempt: loading.Seq, Class: FailTransient, Now: now})
	for _, in := range []ContextInput{ContextConfigChanged{}, ContextScopeChanged{}} {
		for name, e := range map[string]ContextEntry{"ready": ready, "loading": loading, "waiting": waiting} {
			got, cmd := ContextStep(e, in)
			if _, ok := got.State.(CtxEmpty); !ok || got.Seq <= e.Seq {
				t.Fatalf("%T on %s: %#v", in, name, got)
			}
			_, held := e.State.(CtxReady)
			if held == (len(cmd) > 0) {
				t.Fatalf("%T on %s: cancel commands %v", in, name, cmd)
			}
			// A late result of the dropped attempt can never match.
			late, lateCmd := ContextStep(got, ContextFetched{Key: key, Attempt: e.Seq, Value: IssueList{}, Now: now})
			if !reflect.DeepEqual(late, got) || len(lateCmd) > 0 {
				t.Fatalf("%T on %s: the dropped attempt published", in, name)
			}
		}
	}
}

func TestContextRateLimitedWaitsWithoutUsingARetry(t *testing.T) {
	now := time.Unix(1000, 0)
	until := now.Add(10 * time.Second)
	key := ContextKey{Repo: "rep_1", Kind: ContextIssues}
	loading := ctxStart(key, now)
	got, cmd := ContextStep(loading, ContextRateLimited{Key: key, Attempt: loading.Seq, Until: until, Now: now})
	w, ok := got.State.(CtxWaiting)
	if !ok || !w.At.Equal(until) || w.Reason != WaitRateLimited || got.Retries != 0 {
		t.Fatalf("%#v", got)
	}
	if sw, ok := cmd[0].(CtxScheduleWake); len(cmd) != 1 || !ok || !sw.At.Equal(until) || sw.Attempt != got.Seq {
		t.Fatal(cmd)
	}
	woken, cmd := ContextStep(got, ContextWake{Attempt: got.Seq, Now: until})
	if rc, ok := cmd[0].(CtxReadContext); !ok || rc.Attempt != woken.Seq {
		t.Fatal(cmd)
	}
	// A deadline that is not in the future asks for no wait.
	same, cmd := ContextStep(loading, ContextRateLimited{Key: key, Attempt: loading.Seq, Until: now, Now: now})
	if !reflect.DeepEqual(same, loading) || len(cmd) > 0 {
		t.Fatal("a past deadline changed the entry")
	}
}

func TestContextFailureClassesMapToReasons(t *testing.T) {
	now := time.Unix(1000, 0)
	key := ContextKey{Repo: "rep_1", Kind: ContextIssues}
	for class, want := range map[FailKind]ContextBlockReason{FailUnauthorized: ContextForbidden, FailNotFound: ContextNotFound} {
		loading := ctxStart(key, now)
		got, cmd := ContextStep(loading, ContextFetchFailed{Key: key, Attempt: loading.Seq, Class: class, Now: now})
		if b, ok := got.State.(CtxBlocked); !ok || b.Reason != want || len(cmd) != 1 {
			t.Fatalf("%s: %#v", class, got.State)
		}
	}
	// A rate limit deadline later than the backoff wins.
	loading := ctxStart(key, now)
	got, _ := ContextStep(loading, ContextFetchFailed{Key: key, Attempt: loading.Seq, Class: FailRateLimited, RetryAt: now.Add(30 * time.Second), Now: now})
	if w := got.State.(CtxWaiting); !w.At.Equal(now.Add(30 * time.Second)) {
		t.Fatal(w)
	}
}
