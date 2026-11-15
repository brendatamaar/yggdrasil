package hosts

// Host context reads (GH-05, GH-06; docs/design/phase-6-host-context.md):
// failed CI job logs, issues and merge-request diffs, fetched on demand for one
// tracked repo. This file holds the closed state, input, command and payload
// types and the pure context reducer.
//
// Names carry a Context or Ctx prefix because this package already has the
// poller's Idle/Waiting/Blocked phases, Fetched and FetchFailed inputs.
// Tokens never enter any of these values.

import (
	"fmt"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Bounds of one context read (design "Commands and cardinality"). The shell
// enforces them while reading; the core's parsers re-check them.
const (
	// ContextCacheTTL is how long a Ready/Partial result serves repeated
	// requests for the same key and head.
	ContextCacheTTL = 60 * time.Second
	// ContextRequestTimeout bounds one whole read.
	ContextRequestTimeout = 20 * time.Second
	// ContextMaxPages and ContextMaxEntries bound paging and list size.
	ContextMaxPages   = 10
	ContextMaxEntries = 500
	// ContextMaxJobLogBytes bounds one redacted job log; ContextMaxResponseBytes
	// the aggregate host response before redaction.
	ContextMaxJobLogBytes   = 256 << 10
	ContextMaxResponseBytes = 1 << 20
	// ContextMaxPromptBytes bounds what one agent prompt may carry from a
	// context (it stays under the 100 KiB prompt limit).
	ContextMaxPromptBytes = 64 << 10
	// ContextMaxRetries is the number of retries after the first attempt for a
	// temporary failure; the waits are ContextBackoff.
	ContextMaxRetries = 2
)

// ContextBackoff is the wait before retry n (1-based): 1 s, then 3 s.
func ContextBackoff(n int) time.Duration {
	if n <= 1 {
		return time.Second
	}
	return 3 * time.Second
}

// ContextKind is what is read. It is a closed set.
type ContextKind string

const (
	// ContextFailedJobs lists the failed jobs of a run and their logs.
	ContextFailedJobs ContextKind = "failed_jobs"
	// ContextIssues lists open issues of the repo's host project.
	ContextIssues ContextKind = "issues"
	// ContextIssue is one issue.
	ContextIssue ContextKind = "issue"
	// ContextMRDiff is the diff and head of one merge request.
	ContextMRDiff ContextKind = "mr_diff"
)

// Validate reports whether k is a known kind.
func (k ContextKind) Validate() error {
	switch k {
	case ContextFailedJobs, ContextIssues, ContextIssue, ContextMRDiff:
		return nil
	}
	return fmt.Errorf("unknown host context kind %q", string(k))
}

// ContextKey identifies one read. The instance and project are resolved on the
// server from the tracked repo; a key never carries a URL. ObjectID is the
// provider run ID (failed jobs), issue number or MR number, as a positive
// decimal; it is empty for the issue list. Head is the immutable head commit a
// result is pinned to (MR diff, failed jobs); empty for issues. Keys are
// comparable.
type ContextKey struct {
	Repo     core.RepoID
	Kind     ContextKind
	ObjectID string
	Head     string
}

// Validate checks the shape. It does not check that the repo is tracked or the
// object exists: that is the Scope input's and the host's business.
func (k ContextKey) Validate() error {
	if !utf8.ValidString(string(k.Repo)) {
		return fmt.Errorf("repo id must be valid UTF-8: %w", core.ErrInvalidID)
	}
	if err := k.Repo.Validate(); err != nil {
		return err
	}
	if err := k.Kind.Validate(); err != nil {
		return err
	}
	needID := k.Kind != ContextIssues
	if needID {
		if n, err := strconv.Atoi(k.ObjectID); err != nil || n <= 0 || strconv.Itoa(n) != k.ObjectID {
			return fmt.Errorf("host context %s needs a positive number, got %q", k.Kind, k.ObjectID)
		}
	} else if k.ObjectID != "" {
		return fmt.Errorf("the issue list takes no object id")
	}
	switch k.Kind {
	case ContextIssues, ContextIssue:
		if k.Head != "" {
			return fmt.Errorf("host context %s is not pinned to a head", k.Kind)
		}
	case ContextFailedJobs, ContextMRDiff:
		if n := len(k.Head); k.Head != "" && ((n != 40 && n != 64) || !isLowerHex(k.Head)) {
			return fmt.Errorf("the head must be a full lowercase commit id")
		}
	}
	return nil
}

func isLowerHex(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return s != ""
}

// ContextPayload is what a successful read produced (sealed): JobLogs,
// IssueList, IssueDetail or MRContext. All text is already redacted and bounded
// by the shell's parser; Truncated says so.
//
//sumtype:decl
type ContextPayload interface{ isContextPayload() }

// JobLogs is the failed jobs of one run.
type JobLogs struct {
	// RunID is the provider's run or pipeline ID (not a URL).
	RunID string
	// Head is the commit the run built.
	Head string
	Jobs []JobLog
	// Truncated: more jobs existed than ContextMaxEntries or the aggregate
	// budget allowed.
	Truncated bool
}

// JobLog is one failed job. Log is plain redacted text, at most
// ContextMaxJobLogBytes; LogUnavailable names why the log is absent
// (expired, no permission, external check) and then Log is empty.
type JobLog struct {
	JobID          string
	Name           string
	Stage          string
	Log            string
	LogTruncated   bool
	LogUnavailable string
}

// IssueList is a page of open issues.
type IssueList struct {
	Items     []IssueSummary
	Truncated bool
}

// IssueSummary is one issue in a list. Number is the identity within the
// project; Title is plain text.
type IssueSummary struct {
	Number    int
	Title     string
	Author    string
	Labels    []string
	UpdatedAt time.Time
}

// IssueDetail is one issue. Body is plain text, never rendered HTML.
type IssueDetail struct {
	Summary       IssueSummary
	Body          string
	BodyTruncated bool
	// URL is the issue's link on the host, for display only.
	URL string
}

// MRContext is a merge request with its diff, pinned to Head.
type MRContext struct {
	MR            MergeRequest
	Head          string
	Diff          string
	DiffTruncated bool
}

func (JobLogs) isContextPayload()     {}
func (IssueList) isContextPayload()   {}
func (IssueDetail) isContextPayload() {}
func (MRContext) isContextPayload()   {}

// ContextUnavailable says why one part of a Partial result is missing, so the
// UI labels it instead of inventing text.
type ContextUnavailable struct {
	// Source names the part ("job log 1234", "diff").
	Source string
	Reason ContextBlockReason
}

// ContextBlockReason says why a read cannot give data. It is a closed set.
type ContextBlockReason string

const (
	// ContextNoToken: the instance has no token and the host needs one.
	ContextNoToken ContextBlockReason = "no_token"
	// ContextForbidden: 401/403, an expired or too narrow token.
	ContextForbidden ContextBlockReason = "forbidden"
	// ContextNotFound: 404, a private repo without a token, or a wrong object.
	ContextNotFound ContextBlockReason = "not_found"
	// ContextUnsupported: the host kind or the check provider cannot give this
	// (Gitea logs, GitHub external checks).
	ContextUnsupported ContextBlockReason = "unsupported"
	// ContextRetryExhausted: temporary failures kept happening.
	ContextRetryExhausted ContextBlockReason = "retry_exhausted"
	// ContextUntracked: the repo is not tracked (any more).
	ContextUntracked ContextBlockReason = "untracked"
	// ContextTooLarge: the object is over the aggregate budget.
	ContextTooLarge ContextBlockReason = "too_large"
)

// Validate reports whether r is a known reason.
func (r ContextBlockReason) Validate() error {
	switch r {
	case ContextNoToken, ContextForbidden, ContextNotFound, ContextUnsupported,
		ContextRetryExhausted, ContextUntracked, ContextTooLarge:
		return nil
	}
	return fmt.Errorf("unknown host context block reason %q", string(r))
}

// ContextState is where one key is (sealed): CtxEmpty, CtxLoading, CtxReady,
// CtxPartial, CtxWaiting or CtxBlocked.
//
//sumtype:decl
type ContextState interface{ isContextState() }

// CtxEmpty: nothing is held (never read, expired, cancelled or invalidated).
type CtxEmpty struct{}

// CtxLoading: a read of attempt Attempt is in flight.
type CtxLoading struct{ Attempt int }

// CtxReady: the full value, valid until Expires.
type CtxReady struct {
	Value   ContextPayload
	Expires time.Time
}

// CtxPartial: a value with labelled missing parts, valid until Expires.
type CtxPartial struct {
	Value       ContextPayload
	Unavailable []ContextUnavailable
	Expires     time.Time
}

// CtxWaiting: backing off after a temporary failure; the next attempt starts
// at At. Retries counts the retries already used.
type CtxWaiting struct {
	Attempt int
	At      time.Time
	Reason  WaitReason
	Retries int
}

// CtxBlocked: no read until the config changes or the user asks again.
type CtxBlocked struct{ Reason ContextBlockReason }

func (CtxEmpty) isContextState()   {}
func (CtxLoading) isContextState() {}
func (CtxReady) isContextState()   {}
func (CtxPartial) isContextState() {}
func (CtxWaiting) isContextState() {}
func (CtxBlocked) isContextState() {}

// ContextEntry is the state of one key plus the highest attempt number issued
// for it, so a late result of a cancelled attempt can never match a later one.
type ContextEntry struct {
	Key     ContextKey
	Seq     int
	Retries int
	State   ContextState
}

// ContextScope is the shell's answer to "may this key be read now".
type ContextScope string

const (
	// ScopeTracked: the repo is tracked, ready, linked to a configured
	// instance and the object belongs to it.
	ScopeTracked ContextScope = "tracked"
	// ScopeUntracked: not tracked, not ready or not linked.
	ScopeUntracked ContextScope = "untracked"
	// ScopeUnsupported: tracked but the host kind cannot give this context.
	ScopeUnsupported ContextScope = "unsupported"
)

// Validate reports whether s is a known scope.
func (s ContextScope) Validate() error {
	switch s {
	case ScopeTracked, ScopeUntracked, ScopeUnsupported:
		return nil
	}
	return fmt.Errorf("unknown host context scope %q", string(s))
}

// ContextInput is one event for ContextStep (sealed).
//
//sumtype:decl
type ContextInput interface{ isContextInput() }

// ContextRequested: a caller wants the key. Scope is what the shell resolved.
type ContextRequested struct {
	Key   ContextKey
	Scope ContextScope
	Now   time.Time
}

// ContextFetched: attempt Attempt produced Value, with labelled gaps.
type ContextFetched struct {
	Key         ContextKey
	Attempt     int
	Value       ContextPayload
	Unavailable []ContextUnavailable
	Now         time.Time
}

// ContextFetchFailed: attempt Attempt failed with a classified failure.
// RetryAt is the instance-wide rate-limit deadline, zero when none.
type ContextFetchFailed struct {
	Key     ContextKey
	Attempt int
	Class   FailKind
	RetryAt time.Time
	Now     time.Time
}

// ContextWake: the timer of a waiting attempt fired.
type ContextWake struct {
	Attempt int
	Now     time.Time
}

// ContextCancelled: the caller gave up on an attempt.
type ContextCancelled struct{ Attempt int }

// ContextScopeChanged: the repo was untracked or the head the key is pinned to
// moved.
type ContextScopeChanged struct {
	Scope ContextScope
	Head  string
}

// ContextConfigChanged: instances or tokens changed; a Blocked key may retry.
type ContextConfigChanged struct{ Now time.Time }

func (ContextRequested) isContextInput()     {}
func (ContextFetched) isContextInput()       {}
func (ContextFetchFailed) isContextInput()   {}
func (ContextWake) isContextInput()          {}
func (ContextCancelled) isContextInput()     {}
func (ContextScopeChanged) isContextInput()  {}
func (ContextConfigChanged) isContextInput() {}

// ContextCommand is plain data asking the shell for an effect (sealed).
//
//sumtype:decl
type ContextCommand interface{ isContextCommand() }

// CtxReadContext: one bounded, idempotent GET read, deduplicated per key.
type CtxReadContext struct {
	Key     ContextKey
	Attempt int
}

// CtxScheduleWake: wake the entry at At with ContextWake{Attempt}.
type CtxScheduleWake struct {
	Attempt int
	At      time.Time
}

// CtxCancelRead stops the read of an attempt.
type CtxCancelRead struct{ Attempt int }

// CtxReturnCached answers the caller from the held value.
type CtxReturnCached struct{ Key ContextKey }

// CtxJoinRequest makes the caller wait for the read already in flight.
type CtxJoinRequest struct{ Key ContextKey }

// CtxPublishContext hands the value of an attempt to waiters and the cache.
type CtxPublishContext struct {
	Key     ContextKey
	Attempt int
}

// CtxExplain tells waiters, in plain words, why there is no data.
type CtxExplain struct {
	Key    ContextKey
	Reason ContextBlockReason
}

func (CtxReadContext) isContextCommand()    {}
func (CtxScheduleWake) isContextCommand()   {}
func (CtxCancelRead) isContextCommand()     {}
func (CtxReturnCached) isContextCommand()   {}
func (CtxJoinRequest) isContextCommand()    {}
func (CtxPublishContext) isContextCommand() {}
func (CtxExplain) isContextCommand()        {}

// ContextStep decides one context transition. Scope is checked before cache use;
// the shell revalidates scope before executing and accepting a read.
func ContextStep(e ContextEntry, in ContextInput) (ContextEntry, []ContextCommand) {
	switch v := in.(type) {
	case ContextRequested:
		if v.Key.Validate() != nil || v.Scope.Validate() != nil {
			return e, nil
		}
		if e.Key != v.Key {
			return e, nil
		}
		if v.Scope != ScopeTracked {
			reason := ContextUntracked
			if v.Scope == ScopeUnsupported {
				reason = ContextUnsupported
			}
			old := e.Seq
			e.Seq++
			e.State = CtxBlocked{Reason: reason}
			return e, []ContextCommand{CtxCancelRead{Attempt: old}, CtxExplain{Key: e.Key, Reason: reason}}
		}
		switch st := e.State.(type) {
		case CtxReady:
			if v.Now.Before(st.Expires) {
				return e, []ContextCommand{CtxReturnCached{Key: e.Key}}
			}
		case CtxPartial:
			if v.Now.Before(st.Expires) {
				return e, []ContextCommand{CtxReturnCached{Key: e.Key}}
			}
		case CtxLoading:
			return e, []ContextCommand{CtxJoinRequest{Key: e.Key}}
		case CtxWaiting:
			return e, []ContextCommand{CtxJoinRequest{Key: e.Key}}
		case CtxEmpty, CtxBlocked:
		}
		e.Retries = 0
		return contextStart(e)
	case ContextFetched:
		st, ok := e.State.(CtxLoading)
		if !ok || v.Key != e.Key || st.Attempt != v.Attempt || v.Attempt != e.Seq || !ContextPayloadMatches(e.Key, v.Value) {
			return e, nil
		}
		if len(v.Unavailable) > ContextMaxEntries {
			return e, nil
		}
		for _, gap := range v.Unavailable {
			if gap.Reason.Validate() != nil {
				return e, nil
			}
		}
		expires := v.Now.Add(ContextCacheTTL)
		if len(v.Unavailable) > 0 {
			e.State = CtxPartial{Value: v.Value, Unavailable: v.Unavailable, Expires: expires}
		} else {
			e.State = CtxReady{Value: v.Value, Expires: expires}
		}
		return e, []ContextCommand{CtxPublishContext{Key: e.Key, Attempt: v.Attempt}}
	case ContextFetchFailed:
		st, ok := e.State.(CtxLoading)
		if !ok || v.Key != e.Key || st.Attempt != v.Attempt || v.Attempt != e.Seq {
			return e, nil
		}
		reason := ContextRetryExhausted
		switch v.Class {
		case FailTransient, FailRateLimited:
			if e.Retries < ContextMaxRetries {
				e.Retries++
				at := v.Now.Add(ContextBackoff(e.Retries))
				if v.RetryAt.After(at) {
					at = v.RetryAt
				}
				why := WaitTransient
				if v.Class == FailRateLimited {
					why = WaitRateLimited
				}
				e.State = CtxWaiting{Attempt: e.Seq, At: at, Reason: why, Retries: e.Retries}
				return e, []ContextCommand{CtxScheduleWake{Attempt: e.Seq, At: at}}
			}
		case FailUnauthorized:
			reason = ContextForbidden
		case FailNotFound:
			reason = ContextNotFound
		}
		e.State = CtxBlocked{Reason: reason}
		return e, []ContextCommand{CtxExplain{Key: e.Key, Reason: reason}}
	case ContextWake:
		st, ok := e.State.(CtxWaiting)
		if !ok || st.Attempt != v.Attempt || e.Seq != v.Attempt || v.Now.Before(st.At) {
			return e, nil
		}
		return contextStart(e)
	case ContextCancelled:
		if v.Attempt != e.Seq {
			return e, nil
		}
		switch e.State.(type) {
		case CtxLoading, CtxWaiting:
			e.Seq++
			e.State = CtxEmpty{}
			return e, []ContextCommand{CtxCancelRead(v)}
		case CtxEmpty, CtxReady, CtxPartial, CtxBlocked:
		}
	case ContextScopeChanged:
		if v.Scope != ScopeTracked || e.Key.Head != v.Head {
			old := e.Seq
			e.Seq++
			e.State = CtxEmpty{}
			return e, []ContextCommand{CtxCancelRead{Attempt: old}}
		}
	case ContextConfigChanged:
		// Configuration alone supplies no current scope proof. Require Requested.
		if _, ok := e.State.(CtxBlocked); ok {
			e.State = CtxEmpty{}
		}
	}
	return e, nil
}

func contextStart(e ContextEntry) (ContextEntry, []ContextCommand) {
	e.Seq++
	e.State = CtxLoading{Attempt: e.Seq}
	return e, []ContextCommand{CtxReadContext{Key: e.Key, Attempt: e.Seq}}
}

// ContextRun folds inputs without performing effects.
func ContextRun(e ContextEntry, inputs []ContextInput) ContextEntry {
	for _, in := range inputs {
		e, _ = ContextStep(e, in)
	}
	return e
}

// ContextPayloadMatches fences provider results to their immutable identity.
func ContextPayloadMatches(k ContextKey, p ContextPayload) bool {
	switch v := p.(type) {
	case JobLogs:
		if len(v.Jobs) > ContextMaxEntries || !contextHead(v.Head) {
			return false
		}
		for _, j := range v.Jobs {
			n, err := strconv.Atoi(j.JobID)
			if err != nil || n <= 0 || strconv.Itoa(n) != j.JobID || len(j.Log) > ContextMaxJobLogBytes {
				return false
			}
		}
		return k.Kind == ContextFailedJobs && v.RunID == k.ObjectID && (k.Head == "" || v.Head == k.Head)
	case IssueList:
		if len(v.Items) > ContextMaxEntries {
			return false
		}
		for _, issue := range v.Items {
			if issue.Number <= 0 {
				return false
			}
		}
		return k.Kind == ContextIssues
	case IssueDetail:
		return len(v.Body) <= ContextMaxPromptBytes && k.Kind == ContextIssue && strconv.Itoa(v.Summary.Number) == k.ObjectID
	case MRContext:
		return len(v.Diff) <= ContextMaxPromptBytes && k.Kind == ContextMRDiff && strconv.Itoa(v.MR.Number) == k.ObjectID && (len(v.Head) == 40 || len(v.Head) == 64) && isLowerHex(v.Head) && (k.Head == "" || v.Head == k.Head)
	}
	return false
}

// ContextReject classifies terminal capability refusals that the polling
// FailKind cannot represent (unsupported source or an oversized context).
// Like a fetch result it is fenced to the current loading attempt.
func ContextReject(e ContextEntry, attempt int, reason ContextBlockReason) (ContextEntry, []ContextCommand) {
	loading, ok := e.State.(CtxLoading)
	if !ok || loading.Attempt != attempt || e.Seq != attempt || reason.Validate() != nil {
		return e, nil
	}
	e.State = CtxBlocked{Reason: reason}
	return e, []ContextCommand{CtxExplain{Key: e.Key, Reason: reason}}
}

func contextHead(s string) bool { return (len(s) == 40 || len(s) == 64) && isLowerHex(s) }
