package hosts

// One contract suite for ContextReader (GH-05, TM1-H), run against
//   - "fake": a scripted ContextClient, and
//   - "recorded": ContextAPI over the recorded GitLab/GitHub HTTP fixtures in
//     testdata/context, served by a real httptest TLS connection.
// Both sit under the same ContextService, fake clock and fake sleeper. The
// scenario table is the single statement of what a reader must do; neither
// backend is allowed to define its own expectation.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

type contractFlavor struct {
	name     string
	provider string
	kind     corehosts.Kind
	lab      bool
}

var contractFlavors = []contractFlavor{
	{"github", "github", corehosts.KindGitHub, false},
	{"gitlab", "gitlab", corehosts.KindGitLab, true},
}

func (f contractFlavor) root() string {
	if f.lab {
		return "/api/v4"
	}
	return "/api/v3"
}

var (
	contractHeadA = strings.Repeat("a", 40)
	contractHeadB = strings.Repeat("b", 40)
	contractT1    = time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	contractT2    = time.Date(2026, 3, 2, 9, 30, 0, 0, time.UTC)
)

// ---- the fake ----

// answerClient is a scripted ContextClient: its n-th call answers with the n-th
// named answer (the last repeats), translated exactly as the contract table says
// the provider adapter must translate the recorded answer.
type answerClient struct {
	HostClient
	fl      contractFlavor
	clock   *fakeContextClock
	answers []string
	calls   atomic.Int32
	gate    chan struct{}
	entered chan struct{}
	mu      sync.Mutex
	tokens  []string
}

func (c *answerClient) next(ctx context.Context, token string) (ContextResult, error) {
	n := int(c.calls.Add(1))
	c.mu.Lock()
	if len(c.tokens) == 0 || c.tokens[len(c.tokens)-1] != token {
		c.tokens = append(c.tokens, token)
	}
	c.mu.Unlock()
	if c.gate != nil {
		if c.entered != nil {
			select {
			case c.entered <- struct{}{}:
			default:
			}
		}
		select {
		case <-ctx.Done():
			return ContextResult{}, ctx.Err()
		case <-c.gate:
		}
	}
	name := c.answers[min(n, len(c.answers))-1]
	return c.translate(name)
}

func (c *answerClient) translate(name string) (ContextResult, error) {
	issues := corehosts.IssueList{Items: []corehosts.IssueSummary{
		{Number: 1, Title: "Cart total is wrong after coupon", Author: "ana", Labels: []string{"bug", "cart"}, UpdatedAt: contractT1},
		{Number: 2, Title: "Add dark mode to admin", Author: "ben", Labels: nil, UpdatedAt: contractT2},
	}}
	switch name {
	case "issues_ok":
		return ContextResult{Value: issues}, nil
	case "issues_empty":
		return ContextResult{Value: corehosts.IssueList{}}, nil
	case "issues_401":
		return ContextResult{}, fetchErr(corehosts.FailUnauthorized, "status 401")
	case "issues_404":
		return ContextResult{}, fetchErr(corehosts.FailNotFound, "status 404")
	case "issues_429":
		until := c.clock.Now().Add(2 * time.Second)
		return ContextResult{}, &FetchError{Kind: corehosts.FailRateLimited, RetryAt: &until, Err: errors.New("status 429")}
	case "issues_503":
		return ContextResult{}, fetchErr(corehosts.FailTransient, "status 503")
	case "issues_unusable":
		return ContextResult{}, invalidContext("invalid host context response")
	case "issues_too_large":
		return ContextResult{}, ErrContextTooLarge
	case "issue_ok":
		return ContextResult{Value: corehosts.IssueDetail{
			Summary: corehosts.IssueSummary{Number: 5, Title: "Search is slow", Author: "cy", Labels: []string{"perf"}, UpdatedAt: contractT1},
			Body:    "Steps to reproduce: open search.", URL: "https://git.acme.test/acme/shop/issues/5",
		}}, nil
	case "issue_foreign", "issue_is_pull_request", "mr_head_moved":
		return ContextResult{}, ErrContextScope
	case "jobs_ok", "jobs_partial":
		stage := ""
		if c.fl.lab {
			stage = "test"
		}
		jl := corehosts.JobLogs{RunID: "12", Head: contractHeadA, Jobs: []corehosts.JobLog{{JobID: "101", Name: "unit", Stage: stage, Log: "FAIL: TestCartTotal\n"}}}
		var gaps []corehosts.ContextUnavailable
		if name == "jobs_partial" {
			jl.Jobs = append(jl.Jobs, corehosts.JobLog{JobID: "102", Name: "lint", Stage: stage, LogUnavailable: "Log unavailable or redirect unsupported"})
			gaps = append(gaps, corehosts.ContextUnavailable{Source: "job log 102", Reason: corehosts.ContextNotFound})
		}
		if !c.fl.lab {
			gaps = append(gaps, corehosts.ContextUnavailable{Source: "external checks", Reason: corehosts.ContextUnsupported})
		}
		return ContextResult{Value: jl, Unavailable: gaps}, nil
	case "mr_ok":
		diff := "\ncart.go\n@@ -1 +1 @@\n-total\n+cappedTotal\n"
		if c.fl.lab {
			diff = "\ncart.go -> cart.go\n@@ -1 +1 @@\n-total\n+cappedTotal\n"
		}
		return ContextResult{Value: corehosts.MRContext{MR: corehosts.MergeRequest{Number: 3, Title: "Add coupon cap", Source: "feature/coupon", Target: "main", State: corehosts.MROpen}, Head: contractHeadA, Diff: diff}}, nil
	}
	return ContextResult{}, fmt.Errorf("the fake has no answer %q", name)
}

func (c *answerClient) Issues(ctx context.Context, _ corehosts.Instance, token string, _ corehosts.ProjectPath) (ContextResult, error) {
	return c.next(ctx, token)
}
func (c *answerClient) Issue(ctx context.Context, _ corehosts.Instance, token string, _ corehosts.ProjectPath, _ int) (ContextResult, error) {
	return c.next(ctx, token)
}
func (c *answerClient) MRDiff(ctx context.Context, _ corehosts.Instance, token string, _ corehosts.ProjectPath, _ int) (ContextResult, error) {
	return c.next(ctx, token)
}
func (c *answerClient) FailedJobs(ctx context.Context, _ corehosts.Instance, token string, _ corehosts.ProjectPath, _, _ string) (ContextResult, error) {
	return c.next(ctx, token)
}

// realClient puts a ContextAPI under a HostClient so the Service accepts it.
type realClient struct {
	HostClient
	ContextAPI
}

// ---- worlds ----

type contractWorld struct {
	t       *testing.T
	svc     *ContextService
	service *Service
	clock   *fakeContextClock
	sleep   *fakeContextSleeper
	inst    corehosts.Instance
	calls   func() int
	tokens  func() []string
	gate    func() chan struct{}
	hold    func(gatePath string) (release func())
	entered chan struct{}
	cancels func() int
}

type contractScenario struct {
	name    string
	key     corehosts.ContextKey
	answers []string
	noToken bool
	// count says which request path (suffix) is one read of the key on the host.
	count func(fl contractFlavor) string
	check func(t *testing.T, fl contractFlavor, w *contractWorld, out ContextOutcome, err error)
	// reads is how many calls of Read the scenario makes (default 1).
	wantCalls  int
	wantSleeps []time.Duration
}

type contractBackend struct {
	name  string
	build func(t *testing.T, fl contractFlavor, answers []string, count string, noToken bool) *contractWorld
}

func contractResolver(inst corehosts.Instance) contextResolverFunc {
	return func(context.Context, corehosts.ContextKey) (ContextTarget, error) {
		return ContextTarget{Instance: inst.ID, Project: "acme/shop", Revision: "r1"}, nil
	}
}

func buildContractWorld(t *testing.T, fl contractFlavor, base string, noToken bool, client HostClient) *contractWorld {
	t.Helper()
	vars := map[string]Var{"YGG_HOST_1": {Value: string(fl.kind) + " " + base}}
	if !noToken {
		vars["YGG_HOST_1_TOKEN"] = Var{Value: "sentinel-secret"}
	}
	cfg := ParseConfig(vars)
	var inst corehosts.Instance
	for _, in := range cfg.CoreInstances() {
		if in.Base == base {
			inst = in
		}
	}
	if inst.ID == "" {
		t.Fatalf("no instance for %s", base)
	}
	clock := &fakeContextClock{t: time.Unix(1_000_000, 0)}
	sleep := &fakeContextSleeper{clock: clock}
	service := New(Deps{Config: cfg, Clock: clock.Now, Clients: map[corehosts.Kind]HostClient{fl.kind: client}})
	return &contractWorld{
		t: t, clock: clock, sleep: sleep, inst: inst, service: service,
		svc: NewContextService(service, contractResolver(inst), WithContextSleeper(sleep)),
	}
}

func fakeBackend() contractBackend {
	return contractBackend{name: "fake", build: func(t *testing.T, fl contractFlavor, answers []string, count string, noToken bool) *contractWorld {
		client := &answerClient{fl: fl, answers: answers}
		w := buildContractWorld(t, fl, "https://git.acme.test", noToken, client)
		client.clock = w.clock
		w.calls = func() int { return int(client.calls.Load()) }
		w.tokens = func() []string {
			client.mu.Lock()
			defer client.mu.Unlock()
			return append([]string(nil), client.tokens...)
		}
		w.entered = make(chan struct{}, 8)
		w.hold = func(string) func() {
			client.gate = make(chan struct{})
			client.entered = w.entered
			return func() { close(client.gate) }
		}
		w.cancels = func() int { return 0 }
		return w
	}}
}

func recordedBackend() contractBackend {
	return contractBackend{name: "recorded", build: func(t *testing.T, fl contractFlavor, answers []string, count string, noToken bool) *contractWorld {
		replay := newContextReplay(t, loadRecordedProvider(t, fl.provider), answers)
		// The getter shares the fake clock, so a Retry-After of two seconds is
		// over once the fake sleeper has waited two seconds.
		var w *contractWorld
		getter := NewGetter(replay.srv.Client(), func() time.Time { return w.clock.Now() })
		client := realClient{ContextAPI: ContextAPI{Getter: getter, Root: replay.srv.URL + fl.root(), GitLab: fl.lab}}
		w = buildContractWorld(t, fl, replay.srv.URL, noToken, client)
		w.calls = func() int { return replay.count(count) }
		w.tokens = func() []string {
			var out []string
			for _, r := range replay.requests() {
				tok := strings.TrimPrefix(r.Auth, "Bearer ")
				if len(out) == 0 || out[len(out)-1] != tok {
					out = append(out, tok)
				}
			}
			return out
		}
		w.entered = make(chan struct{}, 8)
		w.hold = func(gatePath string) func() {
			replay.mu.Lock()
			replay.gate, replay.gatePath, replay.entered = make(chan struct{}), gatePath, w.entered
			g := replay.gate
			replay.mu.Unlock()
			return func() { close(g) }
		}
		w.cancels = func() int { replay.mu.Lock(); defer replay.mu.Unlock(); return replay.cancelled }
		return w
	}}
}

func contractBackends() []contractBackend { return []contractBackend{fakeBackend(), recordedBackend()} }

// ---- the scenario table ----

func issuesKey() corehosts.ContextKey {
	return corehosts.ContextKey{Repo: "rep_1", Kind: corehosts.ContextIssues}
}

func issueKey() corehosts.ContextKey {
	return corehosts.ContextKey{Repo: "rep_1", Kind: corehosts.ContextIssue, ObjectID: "5"}
}

func jobsKey() corehosts.ContextKey {
	return corehosts.ContextKey{Repo: "rep_1", Kind: corehosts.ContextFailedJobs, ObjectID: "12", Head: contractHeadA}
}

func mrKey() corehosts.ContextKey {
	return corehosts.ContextKey{Repo: "rep_1", Kind: corehosts.ContextMRDiff, ObjectID: "3", Head: contractHeadA}
}

func countIssues(contractFlavor) string { return "/issues" }
func countIssue(contractFlavor) string  { return "/issues/5" }
func countJobs(fl contractFlavor) string {
	if fl.lab {
		return "/pipelines/12"
	}
	return "/actions/runs/12"
}
func countMR(fl contractFlavor) string {
	if fl.lab {
		return "/versions/77"
	}
	return "..." + contractHeadA
}

// project renders a payload for comparison: independent of slice nil-ness and of
// the host's base URL.
func project(v corehosts.ContextPayload) string {
	var b strings.Builder
	sum := func(s corehosts.IssueSummary) string {
		return fmt.Sprintf("%d|%s|%s|%s|%s", s.Number, s.Title, s.Author, strings.Join(s.Labels, ","), s.UpdatedAt.UTC().Format(time.RFC3339))
	}
	switch p := v.(type) {
	case corehosts.IssueList:
		b.WriteString("issues[")
		for _, it := range p.Items {
			b.WriteString(sum(it) + ";")
		}
		fmt.Fprintf(&b, "] truncated=%v", p.Truncated)
	case corehosts.IssueDetail:
		fmt.Fprintf(&b, "issue %s body=%q url_set=%v trunc=%v", sum(p.Summary), p.Body, strings.HasPrefix(p.URL, "https://"), p.BodyTruncated)
	case corehosts.JobLogs:
		fmt.Fprintf(&b, "run %s head %s truncated=%v jobs[", p.RunID, p.Head, p.Truncated)
		for _, j := range p.Jobs {
			fmt.Fprintf(&b, "%s|%s|%s|%q|%v|%q;", j.JobID, j.Name, j.Stage, j.Log, j.LogTruncated, j.LogUnavailable)
		}
		b.WriteString("]")
	case corehosts.MRContext:
		fmt.Fprintf(&b, "mr %d|%s|%s|%s|%s head %s diff=%q trunc=%v", p.MR.Number, p.MR.Title, p.MR.Source, p.MR.Target, p.MR.State, p.Head, p.Diff, p.DiffTruncated)
	default:
		b.WriteString("nil")
	}
	return b.String()
}

func gapsText(g []corehosts.ContextUnavailable) string {
	var parts []string
	for _, x := range g {
		parts = append(parts, x.Source+"/"+string(x.Reason))
	}
	return strings.Join(parts, ";")
}

func wantResult(payload, gaps string) func(*testing.T, contractFlavor, *contractWorld, ContextOutcome, error) {
	return func(t *testing.T, _ contractFlavor, _ *contractWorld, out ContextOutcome, err error) {
		t.Helper()
		if err != nil || out.Result == nil {
			t.Fatalf("want a result, got %+v (blocked %+v), %v", out, out.Blocked, err)
		}
		if got := project(out.Result.Value); got != payload {
			t.Fatalf("payload\n got  %s\n want %s", got, payload)
		}
		if got := gapsText(out.Result.Unavailable); got != gaps {
			t.Fatalf("gaps %q, want %q", got, gaps)
		}
	}
}

func wantBlocked(reason corehosts.ContextBlockReason) func(*testing.T, contractFlavor, *contractWorld, ContextOutcome, error) {
	return func(t *testing.T, _ contractFlavor, _ *contractWorld, out ContextOutcome, err error) {
		t.Helper()
		if err != nil || out.Blocked == nil || out.Blocked.Reason != reason || out.Result != nil {
			t.Fatalf("want blocked %s, got %+v, %v", reason, out, err)
		}
	}
}

const issuesOKText = "issues[1|Cart total is wrong after coupon|ana|bug,cart|2026-03-01T10:00:00Z;2|Add dark mode to admin|ben||2026-03-02T09:30:00Z;] truncated=false"

func contractScenarios() []contractScenario {
	return []contractScenario{
		{name: "success", key: issuesKey(), answers: []string{"issues_ok"}, count: countIssues, wantCalls: 1,
			check: wantResult(issuesOKText, "")},
		{name: "empty list is a complete empty result", key: issuesKey(), answers: []string{"issues_empty"}, count: countIssues, wantCalls: 1,
			check: wantResult("issues[] truncated=false", "")},
		{name: "tokenless is explained and never reaches the host", key: issuesKey(), answers: []string{"issues_ok"}, count: countIssues, noToken: true, wantCalls: 0,
			check: wantBlocked(corehosts.ContextNoToken)},
		{name: "expired token (401) is forbidden, not retried", key: issuesKey(), answers: []string{"issues_401"}, count: countIssues, wantCalls: 1,
			check: wantBlocked(corehosts.ContextForbidden)},
		{name: "404 is not found, not retried", key: issuesKey(), answers: []string{"issues_404"}, count: countIssues, wantCalls: 1,
			check: wantBlocked(corehosts.ContextNotFound)},
		{name: "rate limit waits for the host's deadline then reads again", key: issuesKey(), answers: []string{"issues_429", "issues_ok"}, count: countIssues, wantCalls: 2,
			wantSleeps: []time.Duration{2 * time.Second}, check: wantResult(issuesOKText, "")},
		{name: "5xx is retried once after 1 s", key: issuesKey(), answers: []string{"issues_503", "issues_ok"}, count: countIssues, wantCalls: 2,
			wantSleeps: []time.Duration{time.Second}, check: wantResult(issuesOKText, "")},
		{name: "5xx that never ends stops after two retries (1 s, 3 s)", key: issuesKey(), answers: []string{"issues_503"}, count: countIssues, wantCalls: 3,
			wantSleeps: []time.Duration{time.Second, 3 * time.Second}, check: wantBlocked(corehosts.ContextRetryExhausted)},
		{name: "an unusable answer is unsupported, not retried", key: issuesKey(), answers: []string{"issues_unusable"}, count: countIssues, wantCalls: 1,
			check: wantBlocked(corehosts.ContextUnsupported)},
		{name: "an over-budget answer is too large, not retried", key: issuesKey(), answers: []string{"issues_too_large"}, count: countIssues, wantCalls: 1,
			check: wantBlocked(corehosts.ContextTooLarge)},
		{name: "issue detail", key: issueKey(), answers: []string{"issue_ok"}, count: countIssue, wantCalls: 1,
			check: wantResult("issue 5|Search is slow|cy|perf|2026-03-01T10:00:00Z body=\"Steps to reproduce: open search.\" url_set=true trunc=false", "")},
		{name: "another issue than the key names is not found", key: issueKey(), answers: []string{"issue_foreign"}, count: countIssue, wantCalls: 1,
			check: wantBlocked(corehosts.ContextNotFound)},
		{name: "a pull request where an issue was asked for is not found", key: issueKey(), answers: []string{"issue_is_pull_request"}, count: countIssue, wantCalls: 1,
			check: wantBlocked(corehosts.ContextNotFound)},
		{name: "failed jobs, all logs present", key: jobsKey(), answers: []string{"jobs_ok"}, count: countJobs, wantCalls: 1,
			check: func(t *testing.T, fl contractFlavor, w *contractWorld, out ContextOutcome, err error) {
				stage, gaps := "", "external checks/unsupported"
				if fl.lab {
					stage, gaps = "test", ""
				}
				wantResult(fmt.Sprintf("run 12 head %s truncated=false jobs[101|unit|%s|\"FAIL: TestCartTotal\\n\"|false|\"\";]", contractHeadA, stage), gaps)(t, fl, w, out, err)
			}},
		{name: "failed jobs with one missing log are a labelled partial", key: jobsKey(), answers: []string{"jobs_partial"}, count: countJobs, wantCalls: 1,
			check: func(t *testing.T, fl contractFlavor, w *contractWorld, out ContextOutcome, err error) {
				stage, gaps := "", "job log 102/not_found;external checks/unsupported"
				if fl.lab {
					stage, gaps = "test", "job log 102/not_found"
				}
				wantResult(fmt.Sprintf("run 12 head %s truncated=false jobs[101|unit|%s|\"FAIL: TestCartTotal\\n\"|false|\"\";102|lint|%s|\"\"|false|\"Log unavailable or redirect unsupported\";]", contractHeadA, stage, stage), gaps)(t, fl, w, out, err)
				if out.Result.Unavailable == nil {
					t.Fatal("a partial result without labels")
				}
			}},
		{name: "merge request diff pinned to its head", key: mrKey(), answers: []string{"mr_ok"}, count: countMR, wantCalls: 1,
			check: func(t *testing.T, fl contractFlavor, w *contractWorld, out ContextOutcome, err error) {
				diff := "\ncart.go\n@@ -1 +1 @@\n-total\n+cappedTotal\n"
				if fl.lab {
					diff = "\ncart.go -> cart.go\n@@ -1 +1 @@\n-total\n+cappedTotal\n"
				}
				wantResult(fmt.Sprintf("mr 3|Add coupon cap|feature/coupon|main|open head %s diff=%q trunc=false", contractHeadA, diff), "")(t, fl, w, out, err)
			}},
		{name: "a head that moved while the diff was read is not found", key: mrKey(), answers: []string{"mr_head_moved"}, count: countMR, wantCalls: 1,
			check: wantBlocked(corehosts.ContextNotFound)},
	}
}

func eachContractBackend(t *testing.T, fn func(t *testing.T, b contractBackend, fl contractFlavor)) {
	for _, b := range contractBackends() {
		for _, fl := range contractFlavors {
			t.Run(b.name+"/"+fl.name, func(t *testing.T) { fn(t, b, fl) })
		}
	}
}

func TestContextReaderContractScenarios(t *testing.T) {
	for _, sc := range contractScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			eachContractBackend(t, func(t *testing.T, b contractBackend, fl contractFlavor) {
				w := b.build(t, fl, sc.answers, sc.count(fl), sc.noToken)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				out, err := w.svc.Read(ctx, sc.key)
				sc.check(t, fl, w, out, err)
				if got := w.calls(); got != sc.wantCalls {
					t.Fatalf("%d host calls, want %d", got, sc.wantCalls)
				}
				if got := w.sleep.durations(); fmt.Sprint(got) != fmt.Sprint(sc.wantSleeps) {
					t.Fatalf("waits %v, want %v", got, sc.wantSleeps)
				}
			})
		})
	}
}

// Cache clauses: a second read inside 60 s costs no host call and shares
// nothing mutable with the first; a read after 60 s, or after the token changed,
// goes to the host again and presents the new credential only.
func TestContextReaderContractCacheTTLAndCredentialRotation(t *testing.T) {
	eachContractBackend(t, func(t *testing.T, b contractBackend, fl contractFlavor) {
		w := b.build(t, fl, []string{"issues_ok"}, "/issues", false)
		read := func() ContextOutcome {
			t.Helper()
			out, err := w.svc.Read(context.Background(), issuesKey())
			if err != nil || out.Result == nil {
				t.Fatalf("%+v %v", out, err)
			}
			return out
		}
		first := read()
		// Mutating what one caller got must not change what the next caller gets.
		list := first.Result.Value.(corehosts.IssueList)
		list.Items[0].Title = "changed by the first caller"
		list.Items[0].Labels[0] = "changed"
		w.clock.Advance(59 * time.Second)
		second := read()
		if w.calls() != 1 {
			t.Fatalf("a read inside the 60 s cache went to the host (%d calls)", w.calls())
		}
		if got := project(second.Result.Value); got != issuesOKText {
			t.Fatalf("a caller changed the cached value for the next one: %s", got)
		}
		w.clock.Advance(time.Second) // 60 s
		read()
		if w.calls() != 2 {
			t.Fatalf("an expired entry was served from the cache (%d calls)", w.calls())
		}
		w.service.mu.Lock()
		w.service.cfg.tokens[w.inst.ID] = "rotated-secret"
		w.service.mu.Unlock()
		read()
		if w.calls() != 3 {
			t.Fatalf("a rotated credential was served from the cache (%d calls)", w.calls())
		}
		if got := w.tokens(); fmt.Sprint(got) != "[sentinel-secret rotated-secret]" {
			t.Fatalf("credentials presented to the host: %v", got)
		}
		read()
		if w.calls() != 3 {
			t.Fatal("the entry read under the new credential was not cached")
		}
	})
}

// Joined waiters: concurrent callers of one key cost one host read, all get the
// result, and none can change another's copy.
func TestContextReaderContractJoinedWaitersShareOneRead(t *testing.T) {
	eachContractBackend(t, func(t *testing.T, b contractBackend, fl contractFlavor) {
		w := b.build(t, fl, []string{"issues_ok"}, "/issues", false)
		release := w.hold("/issues")
		const n = 6
		var parked atomic.Int32
		w.svc.onJoin = func() { parked.Add(1) }
		outs := make([]ContextOutcome, n)
		errs := make([]error, n)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				outs[i], errs[i] = w.svc.Read(context.Background(), issuesKey())
			}()
			if i == 0 {
				select {
				case <-w.entered:
				case <-time.After(5 * time.Second):
					t.Fatal("the read never reached the host")
				}
			}
		}
		deadline := time.After(5 * time.Second)
		for parked.Load() < n-1 {
			select {
			case <-deadline:
				t.Fatalf("only %d of %d callers joined", parked.Load(), n-1)
			case <-time.After(time.Millisecond):
			}
		}
		release()
		wg.Wait()
		if w.calls() != 1 {
			t.Fatalf("%d host reads for %d joined callers", w.calls(), n)
		}
		for i := range outs {
			if errs[i] != nil || outs[i].Result == nil {
				t.Fatalf("caller %d: %+v %v", i, outs[i], errs[i])
			}
			list := outs[i].Result.Value.(corehosts.IssueList)
			list.Items[0].Title = fmt.Sprintf("caller %d", i)
		}
		for i := range outs {
			if got := outs[i].Result.Value.(corehosts.IssueList).Items[0].Title; got != fmt.Sprintf("caller %d", i) {
				t.Fatalf("caller %d sees %q: callers share a slice", i, got)
			}
		}
	})
}

// Cancellation: the caller that owns the read gives up; the host request is
// cancelled, the call returns the caller's error, and no worker is left behind.
func TestContextReaderContractCancellationReleasesTheRead(t *testing.T) {
	eachContractBackend(t, func(t *testing.T, b contractBackend, fl contractFlavor) {
		w := b.build(t, fl, []string{"issues_ok"}, "/issues", false)
		w.hold("/issues")
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := w.svc.Read(ctx, issuesKey())
			done <- err
		}()
		select {
		case <-w.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("the read never reached the host")
		}
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("got %v, want the caller's cancellation", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a cancelled read did not return")
		}
		// The slot survived without an in-flight read: the next read starts afresh.
		w.svc.mu.Lock()
		for _, s := range w.svc.slots {
			if s.done != nil || s.cancel != nil {
				t.Error("a cancelled read left the slot owned")
			}
		}
		w.svc.mu.Unlock()
		deadline := time.Now().Add(3 * time.Second)
		for b.name == "recorded" && w.cancels() == 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if b.name == "recorded" && w.cancels() == 0 {
			t.Fatal("the host request was not cancelled")
		}
	})
}

// The same answers seen one layer down, as ContextClient: the class of the error
// is what the reader's retry policy keys on, so it is part of the contract.
func TestContextClientContractErrorClassification(t *testing.T) {
	type want struct {
		ok    bool
		kind  corehosts.FailKind
		is    error
		retry bool
	}
	cases := []struct {
		answer string
		key    corehosts.ContextKey
		want   want
	}{
		{"issues_ok", issuesKey(), want{ok: true}},
		{"issues_empty", issuesKey(), want{ok: true}},
		{"issues_401", issuesKey(), want{kind: corehosts.FailUnauthorized}},
		{"issues_404", issuesKey(), want{kind: corehosts.FailNotFound}},
		{"issues_429", issuesKey(), want{kind: corehosts.FailRateLimited, retry: true}},
		{"issues_503", issuesKey(), want{kind: corehosts.FailTransient}},
		{"issues_unusable", issuesKey(), want{is: ErrContextInvalid}},
		{"issues_too_large", issuesKey(), want{is: ErrContextTooLarge}},
		{"issue_foreign", issueKey(), want{is: ErrContextScope}},
		{"issue_is_pull_request", issueKey(), want{is: ErrContextScope}},
		{"mr_head_moved", mrKey(), want{is: ErrContextScope}},
		{"jobs_partial", jobsKey(), want{ok: true}},
		{"mr_ok", mrKey(), want{ok: true}},
	}
	for _, c := range cases {
		t.Run(c.answer, func(t *testing.T) {
			eachContractBackend(t, func(t *testing.T, b contractBackend, fl contractFlavor) {
				w := b.build(t, fl, []string{c.answer}, "", false)
				client := w.service.d.Clients[fl.kind].(ContextClient)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				var err error
				const token = "sentinel-secret"
				num, _ := strconv.Atoi(c.key.ObjectID)
				switch c.key.Kind {
				case corehosts.ContextIssues:
					_, err = client.Issues(ctx, w.inst, token, "acme/shop")
				case corehosts.ContextIssue:
					_, err = client.Issue(ctx, w.inst, token, "acme/shop", num)
				case corehosts.ContextFailedJobs:
					_, err = client.FailedJobs(ctx, w.inst, token, "acme/shop", c.key.ObjectID, c.key.Head)
				case corehosts.ContextMRDiff:
					_, err = client.MRDiff(ctx, w.inst, token, "acme/shop", num)
				}
				if c.want.ok {
					if err != nil {
						t.Fatalf("unexpected error %v", err)
					}
					return
				}
				var fe *FetchError
				switch {
				case c.want.kind != "":
					if !errors.As(err, &fe) || fe.Kind != c.want.kind {
						t.Fatalf("want a %s failure, got %v", c.want.kind, err)
					}
					if c.want.retry && (fe.RetryAt == nil || !fe.RetryAt.After(w.clock.Now())) {
						t.Fatalf("a rate limit without a usable deadline: %v", fe)
					}
				default:
					if !errors.Is(err, c.want.is) {
						t.Fatalf("want %v, got %v", c.want.is, err)
					}
					if errors.As(err, &fe) {
						t.Fatalf("a deterministic refusal must not be a retryable *FetchError: %v", err)
					}
				}
			})
		})
	}
}
