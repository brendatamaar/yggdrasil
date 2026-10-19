// Package hoststest holds the test doubles of internal/hosts: a scripted Fake
// HostClient, a replay server for recorded fixtures, and HostClientContract,
// the suite every HostClient passes (the fake and each live client pointed at
// a replay server). Only tests import it.
package hoststest

import (
	"context"
	"embed"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	"github.com/brendatamaar/yggdrasil/internal/hosts"
)

// Scenario names a situation the contract puts a client in.
type Scenario string

const (
	OpenSuccess     Scenario = "open_success"
	NoMR            Scenario = "no_mr"
	Draft           Scenario = "draft"
	Merged          Scenario = "merged"
	PipelineFailed  Scenario = "pipeline_failed"
	NoPipeline      Scenario = "no_pipeline"
	NotModified     Scenario = "not_modified"
	Unauthorized    Scenario = "unauthorized"
	NotFound        Scenario = "not_found"
	RateLimited429  Scenario = "rate_limited_429"
	RateLimited403  Scenario = "rate_limited_403"
	ServerError     Scenario = "server_error"
	InboxRoles      Scenario = "inbox"
	defaultScenario Scenario = "default"
)

//go:embed testdata/status
var statusFS embed.FS

// ContractNow is the clock the contract expects a client's Getter to use.
var ContractNow = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

// Recorded is a request the replay server saw.
type Recorded struct {
	Method string
	URI    string
	Header http.Header
}

// Replay serves recorded fixtures. Files are looked up in data as
// <scenario>/<route>.json (then default/<route>.json), with an optional
// <route>.headers next to it. Scenarios with a directory under
// testdata/status answer every request with that status and those headers.
type Replay struct {
	Server *httptest.Server

	data  fs.FS
	route func(*http.Request) string
	mu    sync.Mutex
	scn   Scenario
	reqs  []Recorded
	// inFlight and Max count concurrent requests.
	inFlight, max atomic.Int32
	// Delay slows every response (used to observe concurrency).
	Delay time.Duration
}

// NewReplay starts a replay server. route names the fixture a request wants.
func NewReplay(t testing.TB, data fs.FS, route func(*http.Request) string) *Replay {
	t.Helper()
	r := &Replay{data: data, route: route, scn: defaultScenario}
	r.Server = httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(r.Server.Close)
	return r
}

// Use selects the scenario for the following requests.
func (r *Replay) Use(s Scenario) {
	r.mu.Lock()
	r.scn = s
	r.reqs = nil
	r.mu.Unlock()
}

// Requests returns what the server saw since the last Use.
func (r *Replay) Requests() []Recorded {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Recorded(nil), r.reqs...)
}

// MaxInFlight is the highest number of requests handled at once.
func (r *Replay) MaxInFlight() int { return int(r.max.Load()) }

func (r *Replay) serve(w http.ResponseWriter, req *http.Request) {
	n := r.inFlight.Add(1)
	defer r.inFlight.Add(-1)
	for {
		m := r.max.Load()
		if n <= m || r.max.CompareAndSwap(m, n) {
			break
		}
	}
	r.mu.Lock()
	scn := r.scn
	r.reqs = append(r.reqs, Recorded{Method: req.Method, URI: req.RequestURI, Header: req.Header.Clone()})
	r.mu.Unlock()
	if r.Delay > 0 {
		time.Sleep(r.Delay)
	}
	if b, err := fs.ReadFile(statusFS, "testdata/status/"+string(scn)+"/all.status"); err == nil {
		code, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		if h, err := fs.ReadFile(statusFS, "testdata/status/"+string(scn)+"/all.headers"); err == nil {
			writeHeaders(w, h)
		}
		w.WriteHeader(code)
		return
	}
	name := r.route(req)
	for _, dir := range []string{string(scn), string(defaultScenario)} {
		body, err := fs.ReadFile(r.data, dir+"/"+name+".json")
		if err != nil {
			continue
		}
		if h, err := fs.ReadFile(r.data, dir+"/"+name+".headers"); err == nil {
			writeHeaders(w, h)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
		return
	}
	http.NotFound(w, req)
}

func writeHeaders(w http.ResponseWriter, b []byte) {
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), ":"); ok {
			w.Header().Set(strings.TrimSpace(k), strings.TrimSpace(v))
		}
	}
}

// RepoFunc answers RepoState calls.
type RepoFunc func(ctx context.Context, inst corehosts.Instance, token string, project corehosts.ProjectPath, branch, etag string) (corehosts.RepoState, string, error)

// InboxFunc answers Inbox calls.
type InboxFunc func(ctx context.Context, inst corehosts.Instance, token string) ([]corehosts.InboxItem, error)

// Fake is a scripted HostClient. Set the answers with SetRepoFn, SetInboxFn or
// Script; they can change while calls are running.
type Fake struct {
	mu       sync.Mutex
	repoFn   RepoFunc
	inboxFn  InboxFunc
	delay    time.Duration
	calls    []Call
	inFlight int
	max      int
}

// Call is one recorded call.
type Call struct {
	Inbox   bool
	Inst    corehosts.InstanceID
	Token   string
	Project corehosts.ProjectPath
	Branch  string
	ETag    string
}

var _ hosts.HostClient = (*Fake)(nil)

func (f *Fake) enter(c Call) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.inFlight++
	f.max = max(f.max, f.inFlight)
	f.mu.Unlock()
}

func (f *Fake) leave() {
	f.mu.Lock()
	f.inFlight--
	f.mu.Unlock()
}

// SetRepoFn sets how RepoState calls are answered (nil: an empty result).
func (f *Fake) SetRepoFn(fn RepoFunc) { f.mu.Lock(); f.repoFn = fn; f.mu.Unlock() }

// SetInboxFn sets how Inbox calls are answered (nil: an empty result).
func (f *Fake) SetInboxFn(fn InboxFunc) { f.mu.Lock(); f.inboxFn = fn; f.mu.Unlock() }

// SetDelay holds every call for d (or until its context ends).
func (f *Fake) SetDelay(d time.Duration) { f.mu.Lock(); f.delay = d; f.mu.Unlock() }

func (f *Fake) wait(ctx context.Context) {
	f.mu.Lock()
	d := f.delay
	f.mu.Unlock()
	if d > 0 {
		select {
		case <-time.After(d):
		case <-ctx.Done():
		}
	}
}

// Calls returns the recorded calls.
func (f *Fake) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}

// MaxInFlight is the highest number of calls running at once.
func (f *Fake) MaxInFlight() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.max
}

// RepoState implements hosts.HostClient.
func (f *Fake) RepoState(ctx context.Context, inst corehosts.Instance, token string, project corehosts.ProjectPath, branch, etag string) (corehosts.RepoState, string, error) {
	f.enter(Call{Inst: inst.ID, Token: token, Project: project, Branch: branch, ETag: etag})
	defer f.leave()
	f.wait(ctx)
	f.mu.Lock()
	fn := f.repoFn
	f.mu.Unlock()
	if fn == nil {
		return corehosts.RepoState{}, "", nil
	}
	return fn(ctx, inst, token, project, branch, etag)
}

// Inbox implements hosts.HostClient.
func (f *Fake) Inbox(ctx context.Context, inst corehosts.Instance, token string) ([]corehosts.InboxItem, error) {
	f.enter(Call{Inbox: true, Inst: inst.ID, Token: token})
	defer f.leave()
	f.wait(ctx)
	f.mu.Lock()
	fn := f.inboxFn
	f.mu.Unlock()
	if fn == nil {
		return nil, nil
	}
	return fn(ctx, inst, token)
}

// ExpectedMR is the MR every contract scenario expects (URL aside).
func ExpectedMR(state corehosts.MRState) corehosts.MergeRequest {
	return corehosts.MergeRequest{
		Number: 42, Title: "Add cart badge", Author: "mara", Source: "feature/cart", Target: "main",
		State: state, UpdatedAt: time.Date(2026, 10, 12, 9, 30, 0, 0, time.UTC),
	}
}

// ExpectedPipeline is the pipeline every contract scenario expects (URL aside).
func ExpectedPipeline(status corehosts.PipelineStatus) corehosts.Pipeline {
	return corehosts.Pipeline{Status: status, UpdatedAt: time.Date(2026, 10, 12, 9, 45, 0, 0, time.UTC)}
}

// ExpectedInbox is the inbox the inbox scenario expects.
func ExpectedInbox() []corehosts.InboxItem {
	return []corehosts.InboxItem{
		{MR: corehosts.MergeRequest{Number: 7, Title: "Fix totals rounding"}, Repo: "acme-shop/shop-web", Role: corehosts.RoleReviewRequested},
		{MR: corehosts.MergeRequest{Number: 8, Title: "Update admin theme"}, Repo: "acme-shop/shop-admin", Role: corehosts.RoleAssigned},
		{MR: corehosts.MergeRequest{Number: 42, Title: "Add cart badge"}, Repo: "acme-shop/shop-api", Role: corehosts.RoleAuthored},
	}
}

// Script makes the fake behave as the scenario says, with the same canonical
// data the fixtures hold. Failure scenarios return *hosts.FetchError.
func (f *Fake) Script(s Scenario) {
	url := "https://fake.example/acme-shop/shop-api/"
	mr := func(st corehosts.MRState) *corehosts.MergeRequest {
		m := ExpectedMR(st)
		m.URL = url + "mr/42"
		return &m
	}
	pl := func(st corehosts.PipelineStatus) *corehosts.Pipeline {
		p := ExpectedPipeline(st)
		p.URL = url + "pipeline/1"
		return &p
	}
	fail := func(kind corehosts.FailKind, retry *time.Time) {
		err := &hosts.FetchError{Kind: kind, RetryAt: retry}
		f.SetRepoFn(func(context.Context, corehosts.Instance, string, corehosts.ProjectPath, string, string) (corehosts.RepoState, string, error) {
			return corehosts.RepoState{}, "", err
		})
		f.SetInboxFn(func(context.Context, corehosts.Instance, string) ([]corehosts.InboxItem, error) { return nil, err })
	}
	state := func(m *corehosts.MergeRequest, p *corehosts.Pipeline) {
		f.SetRepoFn(func(context.Context, corehosts.Instance, string, corehosts.ProjectPath, string, string) (corehosts.RepoState, string, error) {
			return corehosts.RepoState{MR: m, Pipeline: p}, `"v1"`, nil
		})
	}
	switch s {
	case OpenSuccess:
		state(mr(corehosts.MROpen), pl(corehosts.PipelineSuccess))
	case NoMR:
		state(nil, pl(corehosts.PipelineSuccess))
	case Draft:
		state(mr(corehosts.MRDraft), pl(corehosts.PipelineSuccess))
	case Merged:
		state(mr(corehosts.MRMerged), pl(corehosts.PipelineSuccess))
	case PipelineFailed:
		state(mr(corehosts.MROpen), pl(corehosts.PipelineFailed))
	case NoPipeline:
		state(mr(corehosts.MROpen), nil)
	case NotModified:
		f.SetRepoFn(func(context.Context, corehosts.Instance, string, corehosts.ProjectPath, string, string) (corehosts.RepoState, string, error) {
			return corehosts.RepoState{}, "", hosts.ErrNotModified
		})
	case Unauthorized:
		fail(corehosts.FailUnauthorized, nil)
	case NotFound:
		fail(corehosts.FailNotFound, nil)
	case RateLimited429:
		t := ContractNow.Add(120 * time.Second)
		fail(corehosts.FailRateLimited, &t)
	case RateLimited403:
		t := ContractNow.Add(5 * time.Minute)
		fail(corehosts.FailRateLimited, &t)
	case ServerError:
		fail(corehosts.FailTransient, nil)
	case InboxRoles:
		f.SetInboxFn(func(context.Context, corehosts.Instance, string) ([]corehosts.InboxItem, error) {
			items := ExpectedInbox()
			for i := range items {
				items[i].MR.URL = url + "mr/" + strconv.Itoa(items[i].MR.Number)
			}
			return items, nil
		})
	}
}
