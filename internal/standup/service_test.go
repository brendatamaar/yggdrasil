package standup_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corestandup "github.com/brendatamaar/yggdrasil/internal/core/standup"
	"github.com/brendatamaar/yggdrasil/internal/repos"
	"github.com/brendatamaar/yggdrasil/internal/standup"
	"github.com/brendatamaar/yggdrasil/internal/standup/standuptest"
)

var (
	from = time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	to   = from.Add(24 * time.Hour)
)

const project = core.ProjectID("prj_1")

var (
	apiRepo = standup.RepoRef{ID: "rep_api", Name: "shop-api"}
	webRepo = standup.RepoRef{ID: "rep_web", Name: "shop-web"}
	// newRepo is reserved: it is never in the scope.
	newRepo = standup.RepoRef{ID: "rep_new", Name: "shop-new"}
)

type env struct {
	scope   *standuptest.Scope
	commits *standuptest.Commits
	mrs     *standuptest.MRs
	builds  *standuptest.Builds
	polish  *standuptest.Polisher
	hub     *standuptest.Hub
	svc     *standup.Service
}

func newEnv(t *testing.T, ready ...standup.RepoRef) *env {
	t.Helper()
	e := &env{scope: &standuptest.Scope{}, commits: &standuptest.Commits{}, mrs: &standuptest.MRs{}, builds: &standuptest.Builds{}, polish: &standuptest.Polisher{}, hub: &standuptest.Hub{}}
	e.scope.Set(project, ready...)
	for _, r := range ready {
		e.commits.Seed(r.ID, nil)
		e.mrs.Seed(r.ID, nil)
		e.builds.Seed(r.ID, nil)
	}
	svc, err := standup.New(standup.Deps{Scope: e.scope, Commits: e.commits, MRs: e.mrs, Builds: e.builds, Polish: e.polish, Hub: e.hub, Clock: func() time.Time { return from }})
	if err != nil {
		t.Fatal(err)
	}
	e.svc = svc
	return e
}

func hc(i int, subject string, at time.Time) repos.HistoryCommit {
	return repos.HistoryCommit{OID: fmt.Sprintf("%040x", i), Subject: subject, Author: "Ada Test", At: at}
}

func mr(r standup.RepoRef, n int, at time.Time) corestandup.MR {
	return corestandup.MR{Repo: r.ID, RepoName: r.Name, Number: n, Title: fmt.Sprintf("mr %d", n), State: "open", At: at}
}

func bld(r standup.RepoRef, n int, at time.Time) corestandup.Build {
	return corestandup.Build{Repo: r.ID, RepoName: r.Name, ID: core.BuildID(fmt.Sprintf("bld_%d", n)), Status: core.BuildSucceeded, At: at}
}

func (e *env) get(t *testing.T) api.StandupResponse {
	t.Helper()
	r, err := e.svc.Get(context.Background(), project, from, to)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestStandupDateRangeAndEmpty(t *testing.T) {
	e := newEnv(t, apiRepo, webRepo)
	r := e.get(t)
	if r.Phase != api.StandupReady || len(r.Commits)+len(r.MRs)+len(r.Builds) != 0 || r.Unavailable != nil {
		t.Fatalf("empty project = %+v", r)
	}
	if !strings.Contains(r.Text, "none in this range") {
		t.Errorf("empty text:\n%s", r.Text)
	}
	if r.Commits == nil || r.MRs == nil || r.Builds == nil {
		t.Errorf("empty sections must be arrays, not null")
	}
	// A project with no ready repo at all is an empty result too, not a failure.
	e.scope.Set(project)
	if r := e.get(t); r.Phase != api.StandupReady {
		t.Errorf("no ready repos: phase %s", r.Phase)
	}

	for name, c := range map[string][3]any{
		"inverted":    {project, to, from},
		"empty":       {project, from, from},
		"zero":        {project, time.Time{}, to},
		"too long":    {project, from, from.Add(31*24*time.Hour + time.Second)},
		"bad project": {core.ProjectID("a b"), from, to},
	} {
		_, err := e.svc.Get(context.Background(), c[0].(core.ProjectID), c[1].(time.Time), c[2].(time.Time))
		if class, ok := apperr.ClassOf(err); !ok || class != apperr.Invalid || !errors.Is(err, corestandup.ErrInvalidRange) {
			t.Errorf("%s: %v, want an Invalid error", name, err)
		}
	}
	if _, err := e.svc.Get(context.Background(), "prj_other", from, to); !errors.Is(err, standuptest.ErrNoProject) {
		t.Errorf("untracked project: %v", err)
	}
	if n := e.polish.CallCount(); n != 0 {
		t.Errorf("polisher called %d times by refreshes", n)
	}
}

func TestStandupAggregatesAndLabelsSources(t *testing.T) {
	e := newEnv(t, apiRepo, webRepo)
	e.commits.Seed(apiRepo.ID, []repos.HistoryCommit{hc(1, "fix cart", from.Add(time.Hour)), hc(2, "before range", from.Add(-time.Minute)), hc(3, "at the end", to)})
	e.commits.Seed(webRepo.ID, []repos.HistoryCommit{hc(4, "button", from.Add(2*time.Hour))})
	e.mrs.Seed(apiRepo.ID, []corestandup.MR{mr(apiRepo, 7, from.Add(3*time.Hour))})
	e.builds.Seed(webRepo.ID, []corestandup.Build{bld(webRepo, 1, from.Add(4*time.Hour))})
	r := e.get(t)
	if r.Phase != api.StandupReady || len(r.Commits) != 2 || len(r.MRs) != 1 || len(r.Builds) != 1 {
		t.Fatalf("response = %+v", r)
	}
	if r.Commits[0].RepoName != "shop-api" || r.Commits[1].RepoName != "shop-web" {
		t.Errorf("not ordered by repo name: %+v", r.Commits)
	}
	// Same inputs, same text.
	if again := e.get(t); again.Text != r.Text {
		t.Errorf("text changed between identical refreshes")
	}

	// One repo's MR source fails: Partial, labelled, the rest still shown.
	e.mrs.Fail(webRepo.ID, errors.New("secret token path /home/ada/.cache"))
	r = e.get(t)
	if r.Phase != api.StandupPartial || len(r.Unavailable) != 1 || r.Unavailable[0].Source != "merge_requests" || r.Unavailable[0].RepoID != webRepo.ID {
		t.Fatalf("partial = %+v", r)
	}
	if len(r.Commits) != 2 || len(r.MRs) != 1 {
		t.Errorf("readable data dropped: %+v", r)
	}
	if strings.Contains(r.Unavailable[0].Reason, "secret") || strings.Contains(r.Text, "secret") || strings.Contains(r.Text, "/home/ada") {
		t.Errorf("raw error text leaked: %q / %s", r.Unavailable[0].Reason, r.Text)
	}

	// Every repo's builds fail: the whole source is unavailable.
	e.builds.Fail(webRepo.ID, standuptest.ErrMissing)
	e.builds.Fail(apiRepo.ID, standuptest.ErrMissing)
	r = e.get(t)
	if r.Phase != api.StandupPartial || len(r.Builds) != 0 || !strings.Contains(r.Text, "Builds: not read.") {
		t.Fatalf("whole source = %+v\n%s", r, r.Text)
	}
	var whole bool
	for _, u := range r.Unavailable {
		whole = whole || (u.Source == "builds" && u.RepoID == "")
	}
	if !whole {
		t.Errorf("no whole-source label: %+v", r.Unavailable)
	}

	// Nothing readable at all: Failed, no summary.
	for _, repo := range []core.RepoID{apiRepo.ID, webRepo.ID} {
		e.commits.Fail(repo, standuptest.ErrMissing)
		e.mrs.Fail(repo, standuptest.ErrMissing)
	}
	r = e.get(t)
	if r.Phase != api.StandupFailed || r.Message == "" || r.Text != "" || len(r.Commits) != 0 {
		t.Fatalf("failed = %+v", r)
	}
}

// A refresh never starts an agent, publishes nothing, and polishing starts one
// task only on explicit input.
func TestPropRefreshMakesNoModelCall(t *testing.T) {
	for seed := int64(0); seed < 60; seed++ {
		r := rand.New(rand.NewSource(seed))
		e := newEnv(t, apiRepo, webRepo)
		for _, repo := range []standup.RepoRef{apiRepo, webRepo} {
			var cs []repos.HistoryCommit
			for i := 0; i < r.Intn(5); i++ {
				cs = append(cs, hc(r.Intn(1000), "c", from.Add(time.Duration(r.Intn(1440))*time.Minute)))
			}
			e.commits.Seed(repo.ID, cs)
			if r.Intn(3) == 0 {
				e.mrs.Fail(repo.ID, standuptest.ErrMissing)
			}
			if r.Intn(4) == 0 {
				e.commits.Fail(repo.ID, standuptest.ErrMissing)
			}
		}
		for i := 0; i < 5; i++ {
			ctx, cancel := context.WithCancel(context.Background())
			if r.Intn(5) == 0 {
				cancel()
			}
			_, _ = e.svc.Get(ctx, project, from, from.Add(time.Duration(1+r.Intn(48))*time.Hour))
			cancel()
		}
		if n := e.polish.CallCount(); n != 0 {
			t.Fatalf("seed %d: refresh called the polisher %d times", seed, n)
		}
		if n := len(e.hub.Published()); n != 0 {
			t.Fatalf("seed %d: refresh published %d messages", seed, n)
		}
	}
}

func TestStandupPolishExplicitAsk(t *testing.T) {
	e := newEnv(t, apiRepo)
	e.commits.Seed(apiRepo.ID, []repos.HistoryCommit{hc(1, "fix cart", from.Add(time.Hour))})
	shown := e.get(t)
	if e.polish.CallCount() != 0 {
		t.Fatal("refresh polished")
	}
	req := api.StandupPolishRequest{RequestID: "req_1", From: from, To: to, Agent: "claude-code", Model: "opus/4"}
	res, err := e.svc.Polish(context.Background(), project, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != api.ActionStateCompleted || res.TaskID == "" || res.RequestID != "req_1" {
		t.Fatalf("result = %+v", res)
	}
	if e.polish.CallCount() != 1 {
		t.Fatalf("polisher calls = %d", e.polish.CallCount())
	}
	call := e.polish.Calls[0]
	// The text is the server's, the same the refresh showed; the client cannot supply it.
	if call.Text != shown.Text || call.Project != project || call.Agent != "claude-code" || call.Model != "opus/4" || call.ID != "req_1" {
		t.Errorf("call = %+v", call)
	}
	if msgs := e.hub.Published(); len(msgs) != 1 || msgs[0].RequestID != "req_1" {
		t.Errorf("published = %+v", msgs)
	}
	// A refresh afterwards is unchanged and starts nothing.
	if again := e.get(t); again.Text != shown.Text || e.polish.CallCount() != 1 {
		t.Errorf("refresh after polish changed state")
	}
	// A repeat of the same request answers the same task without a second submission.
	res2, err := e.svc.Polish(context.Background(), project, req)
	if err != nil || res2.TaskID != res.TaskID || e.polish.CallCount() != 1 {
		t.Fatalf("repeat = %+v, %v, calls %d", res2, err, e.polish.CallCount())
	}
}

func TestStandupPolishRefusals(t *testing.T) {
	e := newEnv(t, apiRepo)
	ok := api.StandupPolishRequest{RequestID: "req_1", From: from, To: to, Agent: "claude-code"}
	for name, mut := range map[string]func(r *api.StandupPolishRequest){
		"no request id": func(r *api.StandupPolishRequest) { r.RequestID = "" },
		"bad request":   func(r *api.StandupPolishRequest) { r.RequestID = "a b" },
		"no agent":      func(r *api.StandupPolishRequest) { r.Agent = "" },
		"agent syntax":  func(r *api.StandupPolishRequest) { r.Agent = "x; rm -rf" },
		"long agent":    func(r *api.StandupPolishRequest) { r.Agent = strings.Repeat("a", 65) },
		"model syntax":  func(r *api.StandupPolishRequest) { r.Model = "a b" },
		"inverted":      func(r *api.StandupPolishRequest) { r.From, r.To = r.To, r.From },
	} {
		req := ok
		mut(&req)
		if _, err := e.svc.Polish(context.Background(), project, req); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Nothing readable: nothing to polish.
	e.commits.Fail(apiRepo.ID, standuptest.ErrMissing)
	e.mrs.Fail(apiRepo.ID, standuptest.ErrMissing)
	e.builds.Fail(apiRepo.ID, standuptest.ErrMissing)
	if _, err := e.svc.Polish(context.Background(), project, ok); err == nil {
		t.Error("polished a failed read")
	}
	if n := e.polish.CallCount(); n != 0 {
		t.Errorf("polisher called %d times for refused requests", n)
	}
	// The polisher's own refusal reaches the caller; the hub says nothing.
	e2 := newEnv(t, apiRepo)
	e2.polish.Err = errors.New("admission refused")
	if _, err := e2.svc.Polish(context.Background(), project, ok); err == nil || len(e2.hub.Published()) != 0 {
		t.Errorf("polisher error: %v, published %v", err, e2.hub.Published())
	}
}

// A double click (the same request ID twice at once) starts one task.
func TestStandupPolishDoubleClickStartsOneTask(t *testing.T) {
	e := newEnv(t, apiRepo)
	e.polish.Gate = make(chan struct{})
	req := api.StandupPolishRequest{RequestID: "req_1", From: from, To: to, Agent: "claude-code"}
	var wg sync.WaitGroup
	results := make([]api.ActionResultDTO, 2)
	errs := make([]error, 2)
	wg.Add(1)
	go func() {
		defer wg.Done()
		results[0], errs[0] = e.svc.Polish(context.Background(), project, req)
	}()
	waitFor(t, func() bool { return e.polish.CallCount() == 1 })
	wg.Add(1)
	go func() {
		defer wg.Done()
		results[1], errs[1] = e.svc.Polish(context.Background(), project, req)
	}()
	// The same ID with another range is refused, not merged.
	other := req
	other.To = to.Add(time.Hour)
	if _, err := e.svc.Polish(context.Background(), project, other); err == nil {
		t.Error("same request id with another range accepted")
	}
	close(e.polish.Gate)
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || results[0].TaskID != results[1].TaskID || results[0].RequestID != results[1].RequestID {
		t.Fatalf("results %+v %+v errs %v %v", results[0], results[1], errs[0], errs[1])
	}
	if n := e.polish.CallCount(); n != 1 {
		t.Fatalf("polisher called %d times for one request id: %+v", n, e.polish.Calls)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}

// Over any random sequence of refreshes and explicit polishes, the polisher is
// called once per explicit polish at most, and never by a refresh.
func TestPropPolishOnlyOnExplicitInput(t *testing.T) {
	for seed := int64(0); seed < 40; seed++ {
		r := rand.New(rand.NewSource(seed))
		e := newEnv(t, apiRepo, webRepo)
		e.commits.Seed(apiRepo.ID, []repos.HistoryCommit{hc(1, "x", from.Add(time.Hour))})
		explicit := 0
		for i := 0; i < 20; i++ {
			before := e.polish.CallCount()
			if r.Intn(3) == 0 {
				explicit++
				_, _ = e.svc.Polish(context.Background(), project, api.StandupPolishRequest{
					RequestID: core.RequestID(fmt.Sprintf("req_%d", i)), From: from, To: to, Agent: "claude-code"})
				if d := e.polish.CallCount() - before; d != 1 {
					t.Fatalf("seed %d: explicit polish made %d calls", seed, d)
				}
			} else {
				_, _ = e.svc.Get(context.Background(), project, from, to)
				if e.polish.CallCount() != before {
					t.Fatalf("seed %d: a refresh called the polisher", seed)
				}
			}
		}
		if e.polish.CallCount() != explicit {
			t.Fatalf("seed %d: %d calls for %d explicit polishes", seed, e.polish.CallCount(), explicit)
		}
	}
}

// A reserved or untracked repo never appears, whatever the sources hold, and is
// never even read; a repo that stops being ready during the read disappears;
// a source that returns another repo's records is refused.
func TestPropReservedRepoNeverInStandup(t *testing.T) {
	for seed := int64(0); seed < 60; seed++ {
		r := rand.New(rand.NewSource(seed))
		e := newEnv(t, apiRepo, webRepo)
		// The reserved repo has data everywhere.
		e.commits.Seed(newRepo.ID, []repos.HistoryCommit{hc(99, "reserved commit", from.Add(time.Hour))})
		e.mrs.Seed(newRepo.ID, []corestandup.MR{mr(newRepo, 1, from.Add(time.Hour))})
		e.builds.Seed(newRepo.ID, []corestandup.Build{bld(newRepo, 1, from.Add(time.Hour))})
		e.commits.Seed(apiRepo.ID, []repos.HistoryCommit{hc(1, "api", from.Add(time.Hour))})
		// A misbehaving MR source returns the reserved repo's MR for the api repo.
		if r.Intn(2) == 0 {
			e.mrs.Seed(apiRepo.ID, []corestandup.MR{mr(newRepo, 2, from.Add(time.Hour))})
		}
		// The web repo is dropped from scope while the sources are read.
		dropWeb := r.Intn(2) == 0
		if dropWeb {
			e.scope.OnCall = func(n int) {
				if n == 2 {
					e.scope.Set(project, apiRepo)
				}
			}
		}
		res := e.get(t)
		for _, c := range res.Commits {
			if c.RepoID == newRepo.ID || (dropWeb && c.RepoID == webRepo.ID) {
				t.Fatalf("seed %d: out-of-scope commit %+v", seed, c)
			}
		}
		for _, m := range res.MRs {
			if m.RepoID != apiRepo.ID && m.RepoID != webRepo.ID {
				t.Fatalf("seed %d: foreign MR %+v", seed, m)
			}
		}
		for _, b := range res.Builds {
			if b.RepoID == newRepo.ID {
				t.Fatalf("seed %d: reserved build", seed)
			}
		}
		if strings.Contains(res.Text, "shop-new") || strings.Contains(res.Text, "reserved") {
			t.Fatalf("seed %d: reserved repo in text:\n%s", seed, res.Text)
		}
		for _, u := range res.Unavailable {
			if u.RepoID == newRepo.ID || (dropWeb && u.RepoID == webRepo.ID) {
				t.Fatalf("seed %d: label for an out-of-scope repo: %+v", seed, u)
			}
		}
		if e.commits.Calls(newRepo.ID)+e.mrs.Calls(newRepo.ID)+e.builds.Calls(newRepo.ID) != 0 {
			t.Fatalf("seed %d: the reserved repo was read", seed)
		}
	}
}

func TestStandupBounds(t *testing.T) {
	// More repos than the cap: the first 50 by name are read, the cut is labelled.
	var refs []standup.RepoRef
	for i := 0; i < corestandup.MaxRepos+7; i++ {
		refs = append(refs, standup.RepoRef{ID: core.RepoID(fmt.Sprintf("rep_%03d", i)), Name: fmt.Sprintf("repo-%03d", i)})
	}
	e := newEnv(t, refs...)
	for _, r := range refs {
		e.commits.Seed(r.ID, []repos.HistoryCommit{hc(1, strings.Repeat("s", 150), from.Add(time.Hour))})
	}
	res := e.get(t)
	if len(res.Commits) != corestandup.MaxRepos {
		t.Fatalf("%d commits, want %d", len(res.Commits), corestandup.MaxRepos)
	}
	if got := e.commits.Calls(refs[corestandup.MaxRepos].ID); got != 0 {
		t.Errorf("repo beyond the cap was read %d times", got)
	}
	if len(res.Truncated) != 3 || len(res.Text) > corestandup.MaxTextBytes || !utf8.ValidString(res.Text) {
		t.Errorf("truncated %v, text %d bytes", res.Truncated, len(res.Text))
	}

	// One repo with more than the per-source cap.
	e = newEnv(t, apiRepo)
	var cs []repos.HistoryCommit
	var ms []corestandup.MR
	for i := 0; i < 260; i++ {
		at := from.Add(time.Duration(i) * time.Minute)
		cs = append(cs, hc(i+1, fmt.Sprintf("c%d", i), at))
		ms = append(ms, mr(apiRepo, i+1, at))
	}
	e.commits.Seed(apiRepo.ID, cs)
	e.mrs.Seed(apiRepo.ID, ms)
	res = e.get(t)
	if len(res.Commits) != 200 || len(res.MRs) != 200 || len(res.Truncated) != 2 {
		t.Fatalf("%d commits %d MRs truncated %v", len(res.Commits), len(res.MRs), res.Truncated)
	}
	// The newest 200 are kept.
	newest := from.Add(259 * time.Minute)
	last := res.Commits[len(res.Commits)-1].At
	if !last.Equal(newest) {
		t.Errorf("newest commit %v, want %v", last, newest)
	}
}

// A source returning records outside the range or of a foreign repo is refused
// with a label, never trusted.
func TestStandupRefusesBadSourceData(t *testing.T) {
	e := newEnv(t, apiRepo)
	e.mrs.Seed(apiRepo.ID, []corestandup.MR{mr(webRepo, 1, from.Add(time.Hour))}) // foreign repo
	e.builds.Seed(apiRepo.ID, []corestandup.Build{{Repo: apiRepo.ID, ID: "bld_1", Status: "bogus", At: from.Add(time.Hour)}})
	res := e.get(t)
	if res.Phase != api.StandupPartial || len(res.MRs) != 0 || len(res.Builds) != 0 {
		t.Fatalf("response = %+v", res)
	}
}

func TestStandupCancellationAndSupersede(t *testing.T) {
	// Cancelling the caller's context stops the read promptly.
	e := newEnv(t, apiRepo)
	e.commits.Block(apiRepo.ID)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := e.svc.Get(ctx, project, from, to); done <- err }()
	waitFor(t, func() bool { return e.commits.Calls(apiRepo.ID) == 1 })
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled read did not stop")
	}

	// A newer refresh of the same project supersedes an older, still-reading one.
	e = newEnv(t, apiRepo)
	e.commits.BlockFirst(apiRepo.ID)
	first := make(chan error, 1)
	go func() { _, err := e.svc.Get(context.Background(), project, from, to); first <- err }()
	waitFor(t, func() bool { return e.commits.Calls(apiRepo.ID) == 1 })
	if _, err := e.svc.Get(context.Background(), project, from, to.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-first:
		if !errors.Is(err, standup.ErrSuperseded) {
			t.Fatalf("first read: %v, want ErrSuperseded", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("superseded read did not stop")
	}
}

func TestStandupConcurrentRefreshesAreSafe(t *testing.T) {
	e := newEnv(t, apiRepo, webRepo)
	e.commits.Seed(apiRepo.ID, []repos.HistoryCommit{hc(1, "x", from.Add(time.Hour))})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.svc.Get(context.Background(), project, from, to)
			if err != nil && !errors.Is(err, standup.ErrSuperseded) {
				t.Errorf("get: %v", err)
			}
			_, _ = e.svc.Polish(context.Background(), project, api.StandupPolishRequest{RequestID: core.RequestID(fmt.Sprintf("req_%d", i)), From: from, To: to, Agent: "claude-code"})
		}()
	}
	wg.Wait()
}

func TestNewRequiresDeps(t *testing.T) {
	if _, err := standup.New(standup.Deps{}); err == nil {
		t.Error("New accepted empty Deps")
	}
}

func FuzzGetRange(f *testing.F) {
	f.Add(int64(0), int64(3600), "prj_1")
	f.Add(int64(5), int64(1), "prj_1")
	f.Add(int64(0), int64(40*24*3600), "a b")
	f.Fuzz(func(t *testing.T, a, b int64, p string) {
		e := newEnv(t, apiRepo)
		fr, tt := time.Unix(a, 0), time.Unix(b, 0)
		_, err := e.svc.Get(context.Background(), core.ProjectID(p), fr, tt)
		if _, perr := corestandup.ParseRange(core.ProjectID(p), fr, tt); perr != nil {
			if !errors.Is(err, corestandup.ErrInvalidRange) {
				t.Fatalf("invalid range %v..%v %q not refused: %v", fr, tt, p, err)
			}
			return
		}
		if err != nil && !errors.Is(err, standuptest.ErrNoProject) {
			t.Fatalf("valid range refused: %v", err)
		}
	})
}
