package newrepos

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	corenewrepos "github.com/brendatamaar/yggdrasil/internal/core/newrepos"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/git/gittest"
	"github.com/brendatamaar/yggdrasil/internal/hosts"
	"github.com/brendatamaar/yggdrasil/internal/mainlock"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/platform/platformtest"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// ---- scripted faults around any port ----

type faultMode int

const (
	faultBefore faultMode = iota + 1 // the call fails, the effect did not happen
	faultLost                        // the effect happens, the caller gets err (a lost reply)
	faultHang                        // the call blocks until its context ends, then fails with no effect
	faultHook                        // run a function before the call (someone else changes the disk)
)

type plan struct {
	nth  int
	mode faultMode
	err  error
	hook func()
}

// faults scripts failures by operation name and call number and counts the
// calls of every operation, so a test can prove an effect ran exactly once.
type faults struct {
	mu      sync.Mutex
	observe func(op string) // called before every counted call, outside the lock
	plans   map[string][]plan
	calls   map[string]int
	entered chan string
}

func newFaults() *faults {
	return &faults{plans: map[string][]plan{}, calls: map[string]int{}, entered: make(chan string, 64)}
}

func (f *faults) on(op string, nth int, mode faultMode, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.plans[op] = append(f.plans[op], plan{nth: nth, mode: mode, err: err})
}

// onWith scripts a failure and also runs fn just before the call, so a test can
// change the world (a foreign commit, a moved remote head) in the same call
// that then fails or loses its reply.
func (f *faults) onWith(op string, nth int, mode faultMode, err error, fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.plans[op] = append(f.plans[op], plan{nth: nth, mode: mode, err: err, hook: fn})
}

func (f *faults) hook(op string, nth int, fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.plans[op] = append(f.plans[op], plan{nth: nth, mode: faultHook, hook: fn})
}

func (f *faults) count(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[op]
}

var errInjected = errors.New("injected failure")

// run counts one call of op and applies the plan scripted for it.
func (f *faults) run(ctx context.Context, op string, fn func() error) error {
	f.mu.Lock()
	f.calls[op]++
	n := f.calls[op]
	var p *plan
	for i := range f.plans[op] {
		if f.plans[op][i].nth == n {
			p = &f.plans[op][i]
		}
	}
	obs := f.observe
	f.mu.Unlock()
	if obs != nil {
		obs(op)
	}
	if p != nil && p.mode != faultHook && p.hook != nil {
		p.hook()
	}
	if p != nil {
		switch p.mode {
		case faultBefore:
			return fmt.Errorf("%s: %w", op, p.err)
		case faultHang:
			select {
			case f.entered <- op:
			default:
			}
			<-ctx.Done()
			return ctx.Err()
		case faultHook:
			p.hook()
		case faultLost:
		}
	}
	err := fn()
	if err == nil && p != nil && p.mode == faultLost {
		return fmt.Errorf("%s: %w", op, p.err)
	}
	return err
}

// ---- port wrappers ----

type fsWrap struct {
	inner platform.SafeInitFS
	f     *faults
}

func (w fsWrap) CreateChildDir(ctx context.Context, parent, leaf string) (id platform.DirIdentity, err error) {
	err = w.f.run(ctx, "CreateChildDir", func() (e error) { id, e = w.inner.CreateChildDir(ctx, parent, leaf); return })
	return
}

func (w fsWrap) VerifyChildDir(ctx context.Context, parent, leaf string, id platform.DirIdentity) error {
	return w.inner.VerifyChildDir(ctx, parent, leaf, id)
}

func (w fsWrap) CreateFile(ctx context.Context, parent, leaf string, id platform.DirIdentity, name string, content []byte) (h string, err error) {
	err = w.f.run(ctx, "CreateFile", func() (e error) { h, e = w.inner.CreateFile(ctx, parent, leaf, id, name, content); return })
	return
}

func (w fsWrap) FileHash(ctx context.Context, parent, leaf string, id platform.DirIdentity, name string) (string, bool, error) {
	return w.inner.FileHash(ctx, parent, leaf, id, name)
}

type gitWrap struct {
	inner git.RepoInitializer
	f     *faults
}

func (w gitWrap) InitRepo(ctx context.Context, dir, branch string) error {
	return w.f.run(ctx, "InitRepo", func() error { return w.inner.InitRepo(ctx, dir, branch) })
}

func (w gitWrap) CommitFirst(ctx context.Context, dir string, files []string, msg string) (fc git.FirstCommit, err error) {
	err = w.f.run(ctx, "CommitFirst", func() (e error) { fc, e = w.inner.CommitFirst(ctx, dir, files, msg); return })
	return
}

func (w gitWrap) ReadFirstCommit(ctx context.Context, dir, branch string) (git.FirstCommit, bool, error) {
	return w.inner.ReadFirstCommit(ctx, dir, branch)
}

func (w gitWrap) HasAuthor(ctx context.Context, dir string) (bool, error) {
	return w.inner.HasAuthor(ctx, dir)
}

type originWrap struct {
	inner git.OriginManager
	f     *faults
}

func (w originWrap) Origin(ctx context.Context, dir string) (string, bool, error) {
	return w.inner.Origin(ctx, dir)
}

func (w originWrap) AddOrigin(ctx context.Context, dir, url string) error {
	return w.f.run(ctx, "AddOrigin", func() error { return w.inner.AddOrigin(ctx, dir, url) })
}

type transportWrap struct {
	inner git.PublishTransport
	f     *faults
	mu    sync.Mutex
	urls  []string
}

func (w *transportWrap) note(u string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.urls = append(w.urls, u)
}

func (w *transportWrap) RemoteBranch(ctx context.Context, req git.PublishRequest) (git.RemoteRef, error) {
	w.note(req.URL)
	var ref git.RemoteRef
	err := w.f.run(ctx, "RemoteBranch", func() (e error) { ref, e = w.inner.RemoteBranch(ctx, req); return })
	return ref, err
}

func (w *transportWrap) PublishBranch(ctx context.Context, req git.PublishRequest) (res git.PublishResult, err error) {
	w.note(req.URL)
	err = w.f.run(ctx, "PublishBranch", func() (e error) { res, e = w.inner.PublishBranch(ctx, req); return })
	return
}

// fakeHost is a RepoHost that keeps repositories by owner/name with the marker
// of the operation that created them.
type fakeHost struct {
	f       *faults
	mu      sync.Mutex
	repos   map[string]fakeRepo
	nextID  int
	creates int
}

type fakeRepo struct {
	id      string
	marker  string
	private bool
	url     string
}

func newFakeHost(f *faults) *fakeHost { return &fakeHost{f: f, repos: map[string]fakeRepo{}} }

func (h *fakeHost) ResolveOwner(context.Context, corehosts.InstanceID, string) (hosts.RepoOwnerInfo, error) {
	return hosts.RepoOwnerInfo{Kind: "org", CanCreate: true}, nil
}

func markerOf(desc string) string { return desc }

func (h *fakeHost) CreateRepo(ctx context.Context, spec hosts.CreateRepoSpec) (hosts.CreatedRepo, error) {
	var out hosts.CreatedRepo
	err := h.f.run(ctx, "CreateRepo", func() error {
		h.mu.Lock()
		defer h.mu.Unlock()
		key := spec.Owner + "/" + spec.Name
		if _, taken := h.repos[key]; taken {
			return hosts.ErrWriteRejected
		}
		h.nextID++
		r := fakeRepo{id: fmt.Sprintf("%d", 1000+h.nextID), marker: markerOf(spec.Description), private: spec.Private,
			url: "https://" + string(spec.Instance) + "/" + key + ".git"}
		h.repos[key] = r
		h.creates++
		out = hosts.CreatedRepo{RemoteID: r.id, URL: r.url}
		return nil
	})
	if errors.Is(err, errInjected) {
		return hosts.CreatedRepo{}, fmt.Errorf("%w: %w", hosts.ErrOutcomeUnknown, err)
	}
	return out, err
}

func (h *fakeHost) LookupRepo(_ context.Context, spec hosts.CreateRepoSpec) (hosts.RepoLookup, error) {
	h.f.mu.Lock()
	h.f.calls["LookupRepo"]++
	h.f.mu.Unlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	r, ok := h.repos[spec.Owner+"/"+spec.Name]
	if !ok {
		return hosts.RepoLookup{Outcome: hosts.RepoAbsent}, nil
	}
	if r.marker != markerOf(spec.Description) || r.private != spec.Private {
		return hosts.RepoLookup{Outcome: hosts.RepoConflict}, nil
	}
	return hosts.RepoLookup{Outcome: hosts.RepoExactMatch, RemoteID: r.id, URL: r.url}, nil
}

// seed puts a repository somebody else made on the host.
func (h *fakeHost) seed(owner, name, marker string, private bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextID++
	h.repos[owner+"/"+name] = fakeRepo{id: fmt.Sprintf("%d", 5000+h.nextID), marker: marker, private: private,
		url: "https://github.com/" + owner + "/" + name + ".git"}
}

// ---- hub, store and clock ----

type hubRec struct {
	mu     sync.Mutex
	msgs   []api.NewRepoChangedMessage
	check  func(api.NewRepoChangedMessage)
	storeC *db.Store
}

func (h *hubRec) PublishNotice(api.NoticeChangedMessage) {}
func (h *hubRec) PublishAction(api.ActionChangedMessage) {}
func (h *hubRec) PublishNewRepo(m api.NewRepoChangedMessage) {
	h.mu.Lock()
	h.msgs = append(h.msgs, m)
	h.mu.Unlock()
	if h.check != nil {
		h.check(m)
	}
}

// storeWrap fails the nth Commit with a simulated crash (the commit does not
// happen), so a test can stop the flow exactly between an effect and its record.
type storeWrap struct {
	ReservationStore
	mu      sync.Mutex
	commits int
	failAt  int
}

func (s *storeWrap) Commit(ctx context.Context, fn func(*db.Tx) error) error {
	s.mu.Lock()
	s.commits++
	n := s.commits
	s.mu.Unlock()
	if s.failAt != 0 && n == s.failAt {
		return errors.New("simulated crash before the commit")
	}
	return s.ReservationStore.Commit(ctx, fn)
}

func (s *storeWrap) commitCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commits
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(time.Second)
	return c.t
}

// ---- the world ----

const sentinelToken = "ghp_SENTINELtoken0123456789abcdefghijkl"

type world struct {
	t       *testing.T
	ctx     context.Context
	d       *db.DB
	store   *db.Store
	sw      *storeWrap
	project core.Project
	parent  string
	f       *faults
	fs      fsWrap
	git     gitWrap
	origin  originWrap
	tr      *transportWrap
	fake    *gittest.FakePublishTransport
	host    *fakeHost
	hub     *hubRec
	locks   *lockCount
	logs    *safeBuf
	redact  *safety.SecretRedactor
	clk     *clock
	initW   gittest.InitWorld
	svc     *Service
	nprop   int
	dbPath  string
}

type safeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type lockCount struct {
	inner MainLock
	mu    sync.Mutex
	held  int
	max   int
	total int
}

func (l *lockCount) Lock(ctx context.Context, repo core.RepoID) (func(), error) {
	rel, err := l.inner.Lock(ctx, repo)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	l.held++
	l.total++
	if l.held > l.max {
		l.max = l.held
	}
	l.mu.Unlock()
	return func() {
		l.mu.Lock()
		l.held--
		l.mu.Unlock()
		rel()
	}, nil
}

func newWorld(t *testing.T) *world {
	t.Helper()
	ctx := context.Background()
	w := &world{t: t, ctx: ctx, f: newFaults(), logs: &safeBuf{}, redact: safety.NewRedactor(sentinelToken),
		clk: &clock{t: time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)}}
	w.dbPath = filepath.Join(t.TempDir(), "y.db")
	d, err := db.Open(ctx, w.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	w.d = d
	w.store = db.NewStore(d, w.redact.Redact)
	w.sw = &storeWrap{ReservationStore: w.store}
	w.parent = platformtest.CanonicalTempDir(t)
	w.project = dbtest.Project(t, w.store, core.Project{Name: "acme-shop", Path: w.parent})
	w.initW = gittest.NewRealInitWorld(t)
	w.fs = fsWrap{inner: platformtest.OSSafeInitFS(), f: w.f}
	w.git = gitWrap{inner: w.initW.Initializer(), f: w.f}
	w.origin = originWrap{inner: w.initW.Origins(), f: w.f}
	w.fake = gittest.NewFakePublishTransport()
	w.tr = &transportWrap{inner: w.fake, f: w.f}
	w.host = newFakeHost(w.f)
	w.hub = &hubRec{storeC: w.store}
	w.locks = &lockCount{inner: TryLock(&mainlock.Registry{}, time.Millisecond)}
	w.svc = w.start()
	t.Cleanup(func() { _ = w.svc.Close() })
	return w
}

// start builds a service over the world's ports and store (a "restart" when
// called again after Close).
func (w *world) start() *Service {
	w.t.Helper()
	// A plain handler: only the service's own Redact protects the log here.
	logger := slog.New(slog.NewTextHandler(w.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	svc, err := NewService(Deps{
		Store: w.sw, FS: w.fs, Git: w.git, Origin: w.origin, Remote: w.host, Transport: w.tr, Lock: w.locks,
		Hub: w.hub, Clock: w.clk.now, IDs: db.NewID, Projects: w.store, Paths: platform.RealPaths{},
		Logger: logger, Redact: w.redact.Redact, RetryDelay: time.Millisecond, StageTimeout: 20 * time.Second,
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return svc
}

func (w *world) restart() {
	w.t.Helper()
	_ = w.svc.Close()
	w.f.mu.Lock()
	w.f.plans = map[string][]plan{}
	w.f.mu.Unlock()
	w.svc = w.start()
	w.t.Cleanup(func() { _ = w.svc.Close() })
}

var _ = errors.Is

// proposal creates a stored proposal (the owner of effect records) for the
// operation's setup draft, the way the proposals service would.
func (w *world) proposal(rec db.ReservationRecord) core.ProposalID {
	w.t.Helper()
	w.nprop++
	id := core.ProposalID(fmt.Sprintf("prp_nr%d", w.nprop))
	now := w.clk.now()
	err := w.store.Commit(w.ctx, func(tx *db.Tx) error {
		cs, err := tx.EnsureChangeSet(w.ctx, rec.SetupTask, core.ChangeSetID("cs_"+string(id)), now)
		if err != nil {
			return err
		}
		repo := rec.Operation.Repo
		p, cmds := coreprop.Step(coreprop.Proposal{}, coreprop.Created{
			Proposal: coreprop.Proposal{ID: id, ChangeSet: cs, Task: rec.SetupTask, Repo: repo,
				Source: coreprop.AgentSource{Task: rec.SetupTask}, Action: coreprop.Install{Repo: repo, Step: "install"}, Summary: "new repo"},
			Facts: coreprop.Facts{RepoTracked: true, PathsOK: true, RemoteSupported: true, TaskChangeSet: cs}, Now: now,
		})
		if p.State == nil {
			return fmt.Errorf("proposal refused: %v", cmds)
		}
		return tx.CreateProposalRecord(w.ctx, p)
	})
	if err != nil {
		w.t.Fatalf("create proposal: %v", err)
	}
	return id
}

func (w *world) reserveReq(leaf string) ReserveRequest {
	return ReserveRequest{Leaf: leaf, README: "# " + leaf + "\r\nline two\r\n", Ignore: "*.log\n", CommitMessage: "Start " + leaf}
}

// reserveAndPropose reserves a repo and attaches its proposal.
func (w *world) reserveAndPropose(leaf string) db.ReservationRecord {
	w.t.Helper()
	rec, err := w.svc.Reserve(w.ctx, w.project.ID, w.reserveReq(leaf), "")
	if err != nil {
		w.t.Fatalf("reserve: %v", err)
	}
	prop := w.proposal(rec)
	if err := w.svc.AttachProposal(w.ctx, rec.Operation.ID, prop, rec.SpecHash); err != nil {
		w.t.Fatalf("attach proposal: %v", err)
	}
	rec, err = w.svc.Get(w.ctx, rec.Operation.ID)
	if err != nil {
		w.t.Fatal(err)
	}
	return rec
}

func (w *world) get(op core.OperationID) corenewrepos.State {
	w.t.Helper()
	rec, err := w.store.GetReservation(w.ctx, op)
	if err != nil {
		w.t.Fatalf("load operation: %v", err)
	}
	return rec.Operation.State
}

// waitFor polls the stored state until pred holds.
func (w *world) waitFor(op core.OperationID, what string, pred func(corenewrepos.State) bool) corenewrepos.State {
	w.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		st := w.get(op)
		if pred(st) {
			return st
		}
		if time.Now().After(deadline) {
			w.t.Fatalf("timed out waiting for %s; state is %T %+v\nlogs:\n%s", what, st, st, w.logs.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func isState[T corenewrepos.State](st corenewrepos.State) bool { _, ok := st.(T); return ok }

// settled is a state no worker moves any more.
func settled(st corenewrepos.State) bool {
	switch st.(type) {
	case corenewrepos.Ready, corenewrepos.Published, corenewrepos.Stopped, corenewrepos.Uncertain:
		return true
	case corenewrepos.Reserved, corenewrepos.AwaitingConfirm, corenewrepos.Initializing, corenewrepos.Publishing:
	}
	return false
}

func (w *world) waitSettled(op core.OperationID) corenewrepos.State {
	w.t.Helper()
	// Give a worker that is about to move a state a moment to do so, then wait
	// for a state that stays put.
	var last corenewrepos.State
	stable := 0
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		st := w.get(op)
		// Settled also means no worker owns the operation: an Uncertain state is
		// transient while its reconciliation runs.
		if settled(st) && !w.svc.hasWorker(op) && fmt.Sprintf("%+v", st) == fmt.Sprintf("%+v", last) {
			stable++
			if stable >= 4 {
				return st
			}
		} else {
			stable = 0
		}
		last = st
		time.Sleep(10 * time.Millisecond)
	}
	w.t.Fatalf("timed out waiting for a settled state; last %T %+v\nlogs:\n%s", last, last, w.logs.String())
	return nil
}

func (w *world) dir(leaf string) string { return filepath.Join(w.parent, leaf) }

func (w *world) readFile(leaf, name string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(w.dir(leaf), name))
	if err != nil {
		return "", false
	}
	return string(b), true
}

// mutations counts every call of a mutating port operation.
func (w *world) mutations() map[string]int {
	m := map[string]int{}
	for _, op := range []string{"CreateChildDir", "CreateFile", "InitRepo", "CommitFirst", "AddOrigin", "CreateRepo", "PublishBranch"} {
		m[op] = w.f.count(op)
	}
	return m
}

func (w *world) anyMutation() bool {
	for _, n := range w.mutations() {
		if n > 0 {
			return true
		}
	}
	return false
}

// publish previews and confirms a publish of a ready operation.
func (w *world) publish(rec db.ReservationRecord, name string) (PublishPreview, error) {
	w.t.Helper()
	ready, ok := w.get(rec.Operation.ID).(corenewrepos.Ready)
	if !ok {
		w.t.Fatalf("operation is not ready: %T", w.get(rec.Operation.ID))
	}
	w.fake.SetLocalHead(w.dir(rec.Leaf), "main", ready.InitialHead)
	prop := w.proposal(rec)
	pv, err := w.svc.PreparePublish(w.ctx, rec.Operation.ID, PublishRequest{HostInstance: "github.com", Owner: "acme", OwnerKind: corenewrepos.OwnerOrg, Name: name}, prop)
	if err != nil {
		w.t.Fatalf("prepare publish: %v", err)
	}
	if len(pv.Problems) != 0 {
		w.t.Fatalf("publish problems: %v", pv.Problems)
	}
	return pv, w.svc.ConfirmPublish(w.ctx, rec.Operation.ID, pv.Hash, 1)
}

func (s *Service) hasWorker(op core.OperationID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.workers[op]
	return ok
}

func timeAfter() <-chan time.Time { return time.After(5 * time.Millisecond) }
