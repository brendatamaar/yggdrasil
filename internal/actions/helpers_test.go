package actions

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/core"
	coreactions "github.com/brendatamaar/yggdrasil/internal/core/actions"
	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	coreproposals "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
	"github.com/brendatamaar/yggdrasil/internal/hosts"
	"github.com/brendatamaar/yggdrasil/internal/safety"
	"github.com/brendatamaar/yggdrasil/internal/tasks"
)

var (
	head40  = strings.Repeat("a", 40)
	head40b = strings.Repeat("b", 40)
	secret  = "s3cr3t-value-for-tests"
	token   = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
)

// ---- fake ScopeReader ----

type fakeScope struct {
	mu      sync.Mutex
	facts   coreactions.Facts
	target  Target
	noise   corerepos.NoiseFacts
	pull    PullTarget
	tracked bool
	// factsCalls counts Facts reads; onFacts runs before each one (1-based).
	factsCalls int
	onFacts    func(n int, s *fakeScope)
	repos      []core.RepoID
}

func (s *fakeScope) Facts(_ context.Context, repo core.RepoID) (coreactions.Facts, error) {
	s.mu.Lock()
	s.factsCalls++
	n := s.factsCalls
	hook := s.onFacts
	s.mu.Unlock()
	if hook != nil {
		hook(n, s)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.tracked || repo != s.facts.Repo {
		return coreactions.Facts{}, db.ErrNotFound
	}
	return s.facts, nil
}

func (s *fakeScope) StillTracked(_ context.Context, repo core.RepoID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.tracked || repo != s.facts.Repo {
		return db.ErrNotFound
	}
	return nil
}

func (s *fakeScope) NoiseFacts(context.Context, core.RepoID) (corerepos.NoiseFacts, error) {
	return s.noise, nil
}

func (s *fakeScope) Target(_ context.Context, repo core.RepoID) (Target, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.tracked || repo != s.facts.Repo {
		return Target{}, db.ErrNotFound
	}
	return s.target, nil
}

func (s *fakeScope) ReposOf(context.Context, core.ProjectID) ([]core.RepoID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]core.RepoID(nil), s.repos...), nil
}

func (s *fakeScope) PullTarget(context.Context, core.RepoID) (PullTarget, error) { return s.pull, nil }

func (s *fakeScope) set(f func(*coreactions.Facts)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(&s.facts)
}

func (s *fakeScope) untrack() {
	s.mu.Lock()
	s.tracked = false
	s.mu.Unlock()
}

// ---- fake ContextReader ----

type fakeContext struct {
	mu       sync.Mutex
	host     map[corehosts.ContextKind]hosts.ContextOutcome
	hostErr  error
	hostCall []corehosts.ContextKey
	build    string
	buildErr error
	diff     DiffContext
	diffErr  error
}

func (c *fakeContext) Host(_ context.Context, key corehosts.ContextKey) (hosts.ContextOutcome, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hostCall = append(c.hostCall, key)
	if c.hostErr != nil {
		return hosts.ContextOutcome{}, c.hostErr
	}
	out, ok := c.host[key.Kind]
	if !ok {
		return hosts.ContextOutcome{Blocked: &corehosts.CtxBlocked{Reason: corehosts.ContextNoToken}}, nil
	}
	return out, nil
}

func (c *fakeContext) BuildLog(context.Context, core.RepoID, core.BuildID) (string, bool, error) {
	return c.build, false, c.buildErr
}

func (c *fakeContext) Diff(context.Context, core.RepoID) (DiffContext, error) {
	return c.diff, c.diffErr
}

func ready(v corehosts.ContextPayload) hosts.ContextOutcome {
	return hosts.ContextOutcome{Result: &hosts.ContextResult{Value: v}}
}

// ---- fake TaskSubmitter (writes real task rows so the FKs hold) ----

type fakeTasks struct {
	t       *testing.T
	store   *db.Store
	project core.ProjectID
	repo    core.RepoID

	mu       sync.Mutex
	byReq    map[core.RequestID]core.TaskID
	digests  map[core.RequestID]string
	requests []tasks.MessageRequest
	submits  int
	drafts   int
	starts   int
	// failBefore returns an error without creating anything; failAfter creates
	// the task and then returns an error (a lost reply). beforeCreate runs first.
	failBefore   error
	failAfter    error
	beforeCreate func()
	afterCreate  func()
	lookupErr    error
	nextID       int
}

func newFakeTasks(t *testing.T, store *db.Store, project core.ProjectID, repo core.RepoID) *fakeTasks {
	return &fakeTasks{t: t, store: store, project: project, repo: repo, byReq: map[core.RequestID]core.TaskID{}, digests: map[core.RequestID]string{}}
}

func (f *fakeTasks) create(id core.RequestID, status core.TaskStatus, mode core.TaskMode, title, prompt string, src *core.TaskSource) core.TaskID {
	f.nextID++
	tid := core.TaskID(fmt.Sprintf("tsk_%s_%d", strings.ReplaceAll(string(id), "-", "_"), f.nextID))
	now := time.Now().UTC()
	task := core.Task{
		ID: tid, ProjectID: f.project, RepoIDs: []core.RepoID{f.repo}, Title: title, Prompt: prompt, Scope: core.ScopeRepo,
		Mode: mode, Agent: "mock", Status: status, Source: src, CreatedAt: now, UpdatedAt: now,
	}
	if err := f.store.Commit(context.Background(), func(tx *db.Tx) error { _, err := tx.CreateTask(context.Background(), task); return err }); err != nil {
		f.t.Fatalf("fake task create: %v", err)
	}
	f.byReq[id] = tid
	return tid
}

func (f *fakeTasks) SubmitWithRequest(_ context.Context, id core.RequestID, req tasks.MessageRequest) (tasks.MessageResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.submits++
	f.requests = append(f.requests, req)
	if f.beforeCreate != nil {
		f.beforeCreate()
	}
	if f.failBefore != nil {
		return tasks.MessageResult{}, f.failBefore
	}
	if tid, ok := f.byReq[id]; ok {
		return tasks.MessageResult{Task: core.Task{ID: tid}}, nil
	}
	tid := f.create(id, core.TaskQueued, core.TaskMode(req.Mode), req.Title, req.Prompt, nil)
	if f.afterCreate != nil {
		f.afterCreate()
	}
	if f.failAfter != nil {
		return tasks.MessageResult{}, f.failAfter
	}
	return tasks.MessageResult{Task: core.Task{ID: tid}}, nil
}

func (f *fakeTasks) LookupRequest(_ context.Context, id core.RequestID) (core.TaskID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lookupErr != nil {
		return "", f.lookupErr
	}
	if tid, ok := f.byReq[id]; ok {
		return tid, nil
	}
	return "", db.ErrNotFound
}

func (f *fakeTasks) ImportIssueDraft(_ context.Context, spec coretasks.DraftSpec) (tasks.DraftResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.drafts++
	if f.failBefore != nil {
		return tasks.DraftResult{}, f.failBefore
	}
	if tid, ok := f.byReq[spec.RequestID]; ok {
		return tasks.DraftResult{Task: core.Task{ID: tid}}, nil
	}
	src := spec.Issue.Source()
	tid := f.create(spec.RequestID, core.TaskDraft, core.ModeAsk, spec.Title, spec.Brief, &src)
	f.requests = append(f.requests, tasks.MessageRequest{Prompt: spec.Brief, Title: spec.Title})
	return tasks.DraftResult{Task: core.Task{ID: tid}, Created: true}, nil
}

func (f *fakeTasks) StartDraft(context.Context, coretasks.DraftStart, string) (tasks.MessageResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	return tasks.MessageResult{}, errors.New("a draft import must never start")
}

func (f *fakeTasks) agentCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.submits + f.starts
}

func (f *fakeTasks) taskCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.byReq)
}

// ---- fake ProposalCreator (writes real pending proposal rows) ----

type fakeProposals struct {
	t     *testing.T
	store *db.Store

	mu      sync.Mutex
	byReq   map[core.RequestID][]core.ProposalID
	creates int
	specs   []ProposalSpec
	// failAfter creates the proposal and returns an error (a lost reply).
	failAfter  error
	findErr    error
	afterWrite func()
	items      map[core.RequestID][]ProposalSpec
}

func newFakeProposals(t *testing.T, store *db.Store) *fakeProposals {
	return &fakeProposals{t: t, store: store, byReq: map[core.RequestID][]core.ProposalID{}}
}

func (p *fakeProposals) CreateForRequest(ctx context.Context, id core.RequestID, items []ProposalSpec) ([]core.ProposalID, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.creates++
	p.specs = append(p.specs, items...)
	// The contract of the live creator: a bad request, a new-repo kind or an
	// over-long list is refused before anything is stored.
	if id.Validate() != nil || len(items) == 0 || len(items) > coreproposals.MaxRequestProposals {
		return nil, errors.New("proposal request refused")
	}
	for _, it := range items {
		if it.Kind == core.ProposalInitRepo || it.Kind == core.ProposalPublish {
			return nil, errors.New("proposal request refused: new-repo kinds start from their dialog")
		}
	}
	if ids, ok := p.byReq[id]; ok {
		// Same list: the first IDs. A different list is refused, never merged.
		first := p.items[id]
		if len(first) != len(items) {
			return nil, errors.New("this request already created different proposals")
		}
		for i := range items {
			if first[i].Summary != items[i].Summary || first[i].Kind != items[i].Kind || first[i].Repo != items[i].Repo ||
				string(first[i].Payload) != string(items[i].Payload) {
				return nil, errors.New("this request already created different proposals")
			}
		}
		return ids, nil
	}
	var ids []core.ProposalID
	for _, it := range items {
		repo := it.Repo
		prop := core.Proposal{ID: it.ID, RepoID: &repo, Kind: it.Kind, Status: core.ProposalPending, Summary: it.Summary, Payload: it.Payload, CreatedAt: time.Now().UTC()}
		if err := p.store.Commit(ctx, func(tx *db.Tx) error { _, err := tx.CreateProposal(ctx, prop); return err }); err != nil {
			p.t.Fatalf("fake proposal create: %v", err)
		}
		ids = append(ids, it.ID)
	}
	p.byReq[id] = ids
	if p.items == nil {
		p.items = map[core.RequestID][]ProposalSpec{}
	}
	p.items[id] = append([]ProposalSpec(nil), items...)
	if p.afterWrite != nil {
		p.afterWrite()
	}
	if p.failAfter != nil {
		return nil, p.failAfter
	}
	return ids, nil
}

func (p *fakeProposals) FindForRequest(_ context.Context, id core.RequestID) ([]core.ProposalID, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.findErr != nil {
		return nil, p.findErr
	}
	if ids, ok := p.byReq[id]; ok {
		return ids, nil
	}
	return nil, db.ErrNotFound
}

// ---- fake FolderDrafts ----

type fakeDrafts struct {
	tasks *fakeTasks
	calls int
	last  WaitingDraft
	// scope, when set, is the tracked-repo check (the live one reads the repo row).
	scope   *fakeScope
	digests map[core.RequestID]WaitingDraft
}

func (d *fakeDrafts) SaveWaitingDraft(_ context.Context, id core.RequestID, w WaitingDraft) (core.TaskID, error) {
	d.tasks.mu.Lock()
	defer d.tasks.mu.Unlock()
	d.calls++
	d.last = w
	if d.scope != nil {
		d.scope.mu.Lock()
		ok := d.scope.tracked && w.Repo == d.scope.facts.Repo && w.Project == d.scope.target.Project
		d.scope.mu.Unlock()
		if !ok {
			return "", db.ErrNotFound
		}
	}
	if tid, ok := d.tasks.byReq[id]; ok {
		if d.digests != nil && d.digests[id] != w {
			return "", db.ErrDigestMismatch
		}
		return tid, nil
	}
	if d.digests == nil {
		d.digests = map[core.RequestID]WaitingDraft{}
	}
	d.digests[id] = w
	return d.tasks.create(id, core.TaskDraft, core.ModeAsk, w.Title, w.Brief, nil), nil
}

// ---- fake hub ----

type fakeHub struct {
	mu   sync.Mutex
	msgs []api.ActionChangedMessage
	// check runs on every action publish (to verify "after commit").
	check func(api.ActionChangedMessage)
}

func (h *fakeHub) PublishNotice(api.NoticeChangedMessage)   {}
func (h *fakeHub) PublishNewRepo(api.NewRepoChangedMessage) {}
func (h *fakeHub) PublishAction(m api.ActionChangedMessage) {
	h.mu.Lock()
	h.msgs = append(h.msgs, m)
	chk := h.check
	h.mu.Unlock()
	if chk != nil {
		chk(m)
	}
}

// ---- a store that can "die" ----

// dyingStore passes commits through until dead is set; after that every commit
// fails, like a process killed between two commits.
type dyingStore struct {
	RequestStore
	mu   sync.Mutex
	dead bool
}

func (d *dyingStore) kill() {
	d.mu.Lock()
	d.dead = true
	d.mu.Unlock()
}

func (d *dyingStore) Commit(ctx context.Context, fn func(*db.Tx) error) error {
	d.mu.Lock()
	dead := d.dead
	d.mu.Unlock()
	if dead {
		return errors.New("process died")
	}
	return d.RequestStore.Commit(ctx, fn)
}

// ---- environment ----

type env struct {
	t        *testing.T
	store    *db.Store
	project  core.Project
	repo     core.Repo
	scope    *fakeScope
	ctxr     *fakeContext
	tasks    *fakeTasks
	props    *fakeProposals
	drafts   *fakeDrafts
	hub      *fakeHub
	redactor safety.Redactor
	svc      *Service
	n        int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	red := safety.NewRedactor(secret)
	store := db.NewStore(d, red.Redact)
	proj := dbtest.Project(t, store, core.Project{Name: "acme-shop", Path: t.TempDir()})
	repo := dbtest.Repo(t, store, core.Repo{ProjectID: proj.ID, Name: "shop-api", Path: filepath.Join(proj.Path, "shop-api"), DefaultBranch: "main"})
	e := &env{t: t, store: store, project: proj, repo: repo, redactor: red}
	e.scope = &fakeScope{
		tracked: true, repos: []core.RepoID{repo.ID},
		facts:  coreactions.Facts{Repo: repo.ID, Readable: true, Head: head40},
		target: Target{Project: proj.ID, HasHost: true, Instance: "git.acme.dev", HostProject: "acme/shop-api"},
		pull:   PullTarget{Branch: "main", Upstream: "origin/main", Head: head40},
	}
	e.ctxr = &fakeContext{host: map[corehosts.ContextKind]hosts.ContextOutcome{}}
	e.tasks = newFakeTasks(t, store, proj.ID, repo.ID)
	e.props = newFakeProposals(t, store)
	e.drafts = &fakeDrafts{tasks: e.tasks, scope: e.scope}
	e.hub = &fakeHub{}
	e.svc = e.build(store)
	t.Cleanup(func() { _ = e.svc.Close() })
	return e
}

func (e *env) deps(rs RequestStore) Deps {
	return Deps{
		Scope: e.scope, Context: e.ctxr, Tasks: e.tasks, Proposals: e.props, Requests: rs, Drafts: e.drafts,
		Hub: e.hub, Redactor: e.redactor, Clock: time.Now,
		IDs: func(p string) string { e.n++; return fmt.Sprintf("%s_%d_%d", p, time.Now().UnixNano(), e.n) },
	}
}

func (e *env) build(rs RequestStore) *Service {
	e.t.Helper()
	s, err := New(e.deps(rs))
	if err != nil {
		e.t.Fatal(err)
	}
	s.retryEvery = 20 * time.Millisecond
	return s
}

func (e *env) req(id string, kind coreactions.Kind, source string) coreactions.Request {
	e.t.Helper()
	agent := "mock"
	r, err := coreactions.ParseRequest(id, string(e.repo.ID), string(kind), source, agent, "")
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

func (e *env) dispatch(id string, kind coreactions.Kind, source string) Outcome {
	e.t.Helper()
	out, err := e.svc.Dispatch(context.Background(), Input{Request: e.req(id, kind, source)})
	if err != nil {
		e.t.Fatalf("dispatch %s: %v", kind, err)
	}
	return out
}

func (e *env) auditText() string {
	e.t.Helper()
	rows, err := e.store.ListAudit(context.Background(), "", 1000)
	if err != nil {
		e.t.Fatal(err)
	}
	var b strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&b, "%s %s %s %s\n", r.Actor, r.Action, r.Subject, r.Detail)
	}
	return b.String()
}

func (e *env) auditActions() []string {
	e.t.Helper()
	rows, err := e.store.ListAudit(context.Background(), "", 1000)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, r := range rows {
		if strings.HasPrefix(r.Action, "action.") {
			out = append(out, r.Action)
		}
	}
	return out
}

func (e *env) record(id string) db.ActionRecord {
	e.t.Helper()
	rec, err := e.store.GetActionRequest(context.Background(), core.RequestID(id))
	if err != nil {
		e.t.Fatal(err)
	}
	return rec
}

// ciFacts sets a failed pipeline with a readable job log.
func (e *env) ciFacts(log string) {
	e.scope.set(func(f *coreactions.Facts) {
		f.Reasons = []coreactions.Reason{coreactions.ReasonFailedCI}
		f.CIRunID, f.CIHead = "9001", head40
		f.Caps.HostLogs = true
	})
	e.ctxr.host[corehosts.ContextFailedJobs] = ready(corehosts.JobLogs{RunID: "9001", Head: head40,
		Jobs: []corehosts.JobLog{{JobID: "1", Name: "unit", Stage: "test", Log: log}}})
}

var _ = coreproposals.PullFF{}

func nowUTC() time.Time { return time.Now().UTC() }

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
