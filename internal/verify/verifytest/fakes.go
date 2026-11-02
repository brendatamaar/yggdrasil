// Package verifytest has the fakes and the contract suites of internal/verify:
// an in-memory Store, a scripted BuildRunner, and the suites that run against
// both the fakes and the real implementations (BuildRunnerContract,
// VerifyStoreContract). Import it from _test.go files only.
package verifytest

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	cv "github.com/brendatamaar/yggdrasil/internal/core/verify"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/verify"
)

// Store is an in-memory verify.Store with the semantics of the SQLite one:
// versions, unique (task, repo), a Commit that writes nothing on error.
type Store struct {
	mu   sync.Mutex
	data data

	// FailGet makes the read methods fail while it returns an error.
	FailGet func() error
	// FailCommit makes Commit fail before running fn while it returns an error.
	FailCommit func() error
}

type data struct {
	rows      map[string]db.VerificationRow // by ID
	audit     []core.AuditEntry
	turns     map[core.TaskID]db.ReviewTurn
	worktrees []fakeWorktree
	base      map[core.WorktreeID]string
	trusted   map[core.RepoID]bool
	settings  map[core.RepoID]db.BuildSettingsRecord
	noAuto    bool
}

type fakeWorktree struct {
	w core.Worktree
}

func (d data) clone() data {
	out := data{
		rows: map[string]db.VerificationRow{}, audit: slices.Clone(d.audit), turns: map[core.TaskID]db.ReviewTurn{},
		worktrees: slices.Clone(d.worktrees), base: map[core.WorktreeID]string{}, trusted: map[core.RepoID]bool{},
		settings: map[core.RepoID]db.BuildSettingsRecord{}, noAuto: d.noAuto,
	}
	for k, v := range d.rows {
		out.rows[k] = v
	}
	for k, v := range d.turns {
		out.turns[k] = v
	}
	for k, v := range d.base {
		out.base[k] = v
	}
	for k, v := range d.trusted {
		out.trusted[k] = v
	}
	for k, v := range d.settings {
		out.settings[k] = v
	}
	return out
}

// NewStore returns an empty fake store.
func NewStore() *Store {
	return &Store{data: data{
		rows: map[string]db.VerificationRow{}, turns: map[core.TaskID]db.ReviewTurn{}, base: map[core.WorktreeID]string{},
		trusted: map[core.RepoID]bool{}, settings: map[core.RepoID]db.BuildSettingsRecord{},
	}}
}

// AddWorktree adds a worktree with its base commit.
func (s *Store) AddWorktree(w core.Worktree, base string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if w.Status == "" {
		w.Status = core.WorktreeActive
	}
	s.data.worktrees = append(s.data.worktrees, fakeWorktree{w})
	s.data.base[w.ID] = base
}

// SetWorktreeStatus changes the status of a worktree.
func (s *Store) SetWorktreeStatus(id core.WorktreeID, st core.WorktreeStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.worktrees {
		if s.data.worktrees[i].w.ID == id {
			s.data.worktrees[i].w.Status = st
		}
	}
}

// SetReview marks the task as idle in review at turn (seq); seq 0 removes it.
func (s *Store) SetReview(task core.TaskID, turn string, seq int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if seq == 0 {
		delete(s.data.turns, task)
		return
	}
	s.data.turns[task] = db.ReviewTurn{Task: task, Turn: turn, Seq: seq}
}

// SetTrusted sets the trust of a repo.
func (s *Store) SetTrusted(repo core.RepoID, v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.trusted[repo] = v
}

// SetAuto sets the "run verification automatically" switch.
func (s *Store) SetAuto(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.noAuto = !v
}

// SetSettings stores the build settings of a repo.
func (s *Store) SetSettings(rec db.BuildSettingsRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.settings[rec.Repo] = rec
}

// PutRow stores a row as it is, bypassing every check (tests of malformed
// rows).
func (s *Store) PutRow(row db.VerificationRow) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.rows[row.ID] = row
}

// Audit returns the audit entries written so far.
func (s *Store) Audit() []core.AuditEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.data.audit)
}

// Rows returns every row, ordered by ID.
func (s *Store) Rows() []db.VerificationRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sorted(func(db.VerificationRow) bool { return true }, func(a, b db.VerificationRow) int { return strings.Compare(a.ID, b.ID) })
}

type fakeTx struct{ d *data }

func notFound(what string) error { return fmt.Errorf("%s: %w", what, db.ErrNotFound) }
func conflict(what string) error { return fmt.Errorf("%s: %w", what, db.ErrConflict) }

func (t fakeTx) LoadByTaskRepo(_ context.Context, task core.TaskID, repo core.RepoID) (db.VerificationRow, error) {
	for _, r := range t.d.rows {
		if r.Task == task && r.Repo == repo {
			return r, nil
		}
	}
	return db.VerificationRow{}, notFound("verification " + string(task) + "/" + string(repo))
}

func (t fakeTx) Save(_ context.Context, row db.VerificationRow, expected int64, now time.Time) (int64, error) {
	if now.IsZero() {
		return 0, fmt.Errorf("verification needs a time from the caller")
	}
	now = now.UTC().Truncate(time.Millisecond)
	if row.ID == "" {
		return 0, fmt.Errorf("save verification: the verification has no ID")
	}
	if expected == db.NewVerification {
		for _, r := range t.d.rows {
			if r.ID == row.ID || (r.Task == row.Task && r.Repo == row.Repo) {
				return 0, conflict("save verification " + row.ID)
			}
		}
		row.Version, row.CreatedAt, row.UpdatedAt = 0, now, now
		t.d.rows[row.ID] = row
		return 0, nil
	}
	cur, ok := t.d.rows[row.ID]
	if !ok {
		return 0, notFound("save verification " + row.ID)
	}
	if cur.Version != expected {
		return 0, conflict("save verification " + row.ID)
	}
	row.Version, row.CreatedAt, row.UpdatedAt = expected+1, cur.CreatedAt, now
	row.Task, row.Repo = cur.Task, cur.Repo
	t.d.rows[row.ID] = row
	return expected + 1, nil
}

func (t fakeTx) AppendAudit(_ context.Context, e core.AuditEntry) error {
	if err := e.ID.Validate(); err != nil {
		return err
	}
	if e.Actor == "" || e.Action == "" || e.Subject == "" {
		return fmt.Errorf("audit entry needs actor, action and subject")
	}
	t.d.audit = append(t.d.audit, e)
	return nil
}

// Commit runs fn on a copy and keeps the copy only if fn succeeds.
func (s *Store) Commit(_ context.Context, fn func(verify.Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.FailCommit != nil {
		if err := s.FailCommit(); err != nil {
			return err
		}
	}
	work := s.data.clone()
	if err := fn(fakeTx{&work}); err != nil {
		return err
	}
	s.data = work
	return nil
}

func (s *Store) read() error {
	if s.FailGet != nil {
		return s.FailGet()
	}
	return nil
}

func (s *Store) sorted(keep func(db.VerificationRow) bool, cmp func(a, b db.VerificationRow) int) []db.VerificationRow {
	out := []db.VerificationRow{}
	for _, r := range s.data.rows {
		if keep(r) {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, cmp)
	return out
}

func (s *Store) Get(_ context.Context, id string) (db.VerificationRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.read(); err != nil {
		return db.VerificationRow{}, err
	}
	if r, ok := s.data.rows[id]; ok {
		return r, nil
	}
	return db.VerificationRow{}, notFound("verification " + id)
}

func (s *Store) GetByTaskRepo(_ context.Context, task core.TaskID, repo core.RepoID) (db.VerificationRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.read(); err != nil {
		return db.VerificationRow{}, err
	}
	return fakeTx{&s.data}.LoadByTaskRepo(context.Background(), task, repo)
}

func (s *Store) ListByTask(_ context.Context, task core.TaskID) ([]db.VerificationRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.read(); err != nil {
		return nil, err
	}
	return s.sorted(func(r db.VerificationRow) bool { return r.Task == task },
		func(a, b db.VerificationRow) int { return strings.Compare(string(a.Repo), string(b.Repo)) }), nil
}

func byUpdate(a, b db.VerificationRow) int {
	if c := a.UpdatedAt.Compare(b.UpdatedAt); c != 0 {
		return c
	}
	return strings.Compare(a.ID, b.ID)
}

func (s *Store) ListLive(context.Context) ([]db.VerificationRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.read(); err != nil {
		return nil, err
	}
	return s.sorted(func(r db.VerificationRow) bool { return verify.IsLiveCode(r.State) }, byUpdate), nil
}

func (s *Store) ListInterrupted(context.Context) ([]db.VerificationRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.read(); err != nil {
		return nil, err
	}
	return s.sorted(func(r db.VerificationRow) bool { return r.State == "interrupted" }, byUpdate), nil
}

func (s *Store) ReviewTurns(context.Context) ([]db.ReviewTurn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.read(); err != nil {
		return nil, err
	}
	out := []db.ReviewTurn{}
	for _, t := range s.data.turns {
		out = append(out, t)
	}
	slices.SortFunc(out, func(a, b db.ReviewTurn) int { return strings.Compare(string(a.Task), string(b.Task)) })
	return out, nil
}

func (s *Store) Worktrees(_ context.Context, task core.TaskID) ([]core.Worktree, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.read(); err != nil {
		return nil, err
	}
	out := []core.Worktree{}
	for _, w := range s.data.worktrees {
		if w.w.TaskID == task && w.w.Status == core.WorktreeActive {
			out = append(out, w.w)
		}
	}
	return out, nil
}

func (s *Store) BaseCommit(_ context.Context, wt core.WorktreeID) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.read(); err != nil {
		return "", err
	}
	b, ok := s.data.base[wt]
	if !ok {
		return "", notFound("worktree " + string(wt))
	}
	return b, nil
}

func (s *Store) BuildSettings(_ context.Context, repo core.RepoID) (db.BuildSettingsRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.read(); err != nil {
		return db.BuildSettingsRecord{}, err
	}
	if r, ok := s.data.settings[repo]; ok {
		return r, nil
	}
	return db.DefaultBuildSettingsRecord(repo), nil
}

func (s *Store) RepoTrusted(_ context.Context, repo core.RepoID) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.read(); err != nil {
		return false, err
	}
	return s.data.trusted[repo], nil
}

func (s *Store) AutoVerify(context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.read(); err != nil {
		return false, err
	}
	return !s.data.noAuto, nil
}

var _ verify.Store = (*Store)(nil)

// Runner is a scripted verify.BuildRunner and verify.LogReader. A build runs
// as soon as it is queued unless Hold is set; its outcome comes from the
// "fake exit N" argv of its steps (the same words the platform fake uses), so
// one spec means the same on the fake and on the real service.
type Runner struct {
	mu     sync.Mutex
	builds map[core.BuildID]corebuilds.State
	order  []core.BuildID
	calls  map[core.BuildID]int
	logs   map[core.BuildID]map[int]string
	n      int

	// Hold keeps queued builds queued; use Finish to end them.
	Hold bool
	// EnqueueErr, when set, is called with the call number (from 1) and the
	// build ID; a non-nil result fails the call.
	EnqueueErr func(n int, id core.BuildID) error
	// GetErr, when set and non-nil, fails Get.
	GetErr func(id core.BuildID) error
	// ListErr, when set, fails List.
	ListErr error
	// CancelErr, when set, is called for every Cancel; a non-nil result fails
	// the call and the build stays as it is.
	CancelErr func(id core.BuildID) error
	// Cancelled lists the IDs Cancel was called for.
	Cancelled []core.BuildID
}

// NewRunner returns an empty runner.
func NewRunner() *Runner {
	return &Runner{builds: map[core.BuildID]corebuilds.State{}, calls: map[core.BuildID]int{}, logs: map[core.BuildID]map[int]string{}}
}

// Enqueued returns the IDs of the builds that exist, in creation order.
func (r *Runner) Enqueued() []core.BuildID {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.order)
}

// Calls returns how often EnqueueWithID was called for the ID.
func (r *Runner) Calls(id core.BuildID) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[id]
}

// Forget drops a build as if it had never been queued (a crash between the
// commit and the enqueue).
func (r *Runner) Forget(id core.BuildID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.builds, id)
	r.order = slices.DeleteFunc(r.order, func(x core.BuildID) bool { return x == id })
}

// SetLog sets the log of a step.
func (r *Runner) SetLog(id core.BuildID, step int, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.logs[id] == nil {
		r.logs[id] = map[int]string{}
	}
	r.logs[id][step] = text
}

// Put stores a build state as it is (tests of malformed states).
func (r *Runner) Put(id core.BuildID, st corebuilds.State) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.builds[id]; !ok {
		r.order = append(r.order, id)
	}
	r.builds[id] = st
}

func (r *Runner) tick() time.Time {
	r.n++
	return time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC).Add(time.Duration(r.n) * time.Second)
}

func (r *Runner) EnqueueWithID(_ context.Context, id core.BuildID, spec corebuilds.Spec, actor corebuilds.Actor) (core.BuildID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls[id]++
	if err := id.Validate(); err != nil {
		return "", apperr.New(apperr.Invalid, "That isn't a build ID.", err)
	}
	if err := spec.Validate(); err != nil {
		return "", apperr.New(apperr.Invalid, "The build is not valid.", err)
	}
	if err := actor.Validate(); err != nil {
		return "", apperr.New(apperr.Invalid, "The build has no valid actor.", err)
	}
	if r.EnqueueErr != nil {
		if err := r.EnqueueErr(r.calls[id], id); err != nil {
			return "", err
		}
	}
	if _, ok := r.builds[id]; ok {
		return id, nil
	}
	r.order = append(r.order, id)
	q := corebuilds.Queued{ID: id, Spec: spec, QueuedAt: r.tick(), Actor: actor}
	r.builds[id] = q
	if !r.Hold {
		r.builds[id] = r.finishLocked(q, "")
	}
	return id, nil
}

// Finish ends a held build as the argv of its steps say.
func (r *Runner) Finish(id core.BuildID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if q, ok := r.builds[id].(corebuilds.Queued); ok {
		r.builds[id] = r.finishLocked(q, "")
	}
}

// FinishWith ends a held build with a result you choose: kind is "pass",
// "cancel", "interrupt", or "fail:<step>:<code>".
func (r *Runner) FinishWith(id core.BuildID, kind string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if q, ok := r.builds[id].(corebuilds.Queued); ok {
		r.builds[id] = r.finishLocked(q, kind)
	}
}

func (r *Runner) finishLocked(q corebuilds.Queued, kind string) corebuilds.Finished {
	at := r.tick()
	f := corebuilds.Finished{ID: q.ID, Spec: q.Spec, QueuedAt: q.QueuedAt, Actor: q.Actor, StartedAt: &at, FinishedAt: at}
	steps := make([]corebuilds.StepState, len(q.Spec.Steps))
	for i := range steps {
		steps[i] = corebuilds.StepSkipped{}
	}
	done := func(i int, o corebuilds.Outcome) {
		steps[i] = corebuilds.StepDone{Attempt: 1, StartedAt: at, FinishedAt: at, Outcome: o}
	}
	f.Result = corebuilds.ResultSucceeded{}
	switch {
	case kind == "cancel":
		f.Result, f.CancelledAt = corebuilds.ResultCancelled{}, &at
	case kind == "interrupt":
		f.Result = corebuilds.ResultInterrupted{Step: 0}
	case strings.HasPrefix(kind, "fail:"):
		parts := strings.Split(kind, ":")
		step, _ := strconv.Atoi(parts[1])
		code, _ := strconv.Atoi(parts[2])
		for i := 0; i < step && i < len(steps); i++ {
			done(i, corebuilds.Succeeded{})
		}
		if step < len(steps) {
			done(step, corebuilds.Failed{ExitCode: code})
		}
		f.Result = corebuilds.ResultFailed{Step: step}
	default:
		for i, st := range q.Spec.Steps {
			if code, ok := exitCode(st.Argv); ok && code != 0 {
				done(i, corebuilds.Failed{ExitCode: code})
				f.Result = corebuilds.ResultFailed{Step: i}
				break
			}
			done(i, corebuilds.Succeeded{})
		}
	}
	if kind == "interrupt" && len(steps) > 0 {
		done(0, corebuilds.Interrupted{})
	}
	f.Steps = steps
	return f
}

func exitCode(argv []string) (int, bool) {
	if len(argv) == 3 && argv[0] == "fake" && argv[1] == "exit" {
		n, err := strconv.Atoi(argv[2])
		return n, err == nil
	}
	return 0, false
}

func (r *Runner) Cancel(_ context.Context, id core.BuildID, actor corebuilds.Actor) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := actor.Validate(); err != nil {
		return apperr.New(apperr.Invalid, "The cancel has no valid actor.", err)
	}
	r.Cancelled = append(r.Cancelled, id)
	if r.CancelErr != nil {
		if err := r.CancelErr(id); err != nil {
			return err
		}
	}
	if q, ok := r.builds[id].(corebuilds.Queued); ok {
		r.builds[id] = r.finishLocked(q, "cancel")
	}
	return nil
}

// List returns the queued and running builds with the status of the filter
// (the other filter fields are ignored), in creation order. It makes Runner a
// verify.BuildLister.
func (r *Runner) List(_ context.Context, f db.BuildFilter) ([]corebuilds.State, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ListErr != nil {
		return nil, r.ListErr
	}
	var out []corebuilds.State
	for _, id := range r.order {
		switch st := r.builds[id].(type) {
		case corebuilds.Queued:
			if f.Status == core.BuildQueued {
				out = append(out, st)
			}
		case corebuilds.Running:
			if f.Status == core.BuildRunning {
				out = append(out, st)
			}
		}
	}
	return out, nil
}

func (r *Runner) Get(_ context.Context, id core.BuildID) (corebuilds.State, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.GetErr != nil {
		if err := r.GetErr(id); err != nil {
			return nil, err
		}
	}
	st, ok := r.builds[id]
	if !ok {
		return nil, apperr.New(apperr.NotFound, "That build doesn't exist.", db.ErrNotFound)
	}
	return st, nil
}

func (r *Runner) StepLog(_ context.Context, id core.BuildID, step int, after int64) ([]byte, int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.builds[id]; !ok {
		return nil, 0, apperr.New(apperr.NotFound, "That build doesn't exist.", db.ErrNotFound)
	}
	text := r.logs[id][step]
	if after >= int64(len(text)) {
		return nil, after, nil
	}
	return []byte(text[after:]), int64(len(text)), nil
}

var (
	_ verify.BuildRunner = (*Runner)(nil)
	_ verify.LogReader   = (*Runner)(nil)
	_ verify.BuildLister = (*Runner)(nil)
)

// Messenger records the messages sent to agents.
type Messenger struct {
	mu   sync.Mutex
	Sent []Message
	// Err, when set, is returned instead of sending.
	Err error
}

// Message is one recorded message.
type Message struct {
	Task core.TaskID
	Text string
}

func (m *Messenger) SendMessage(_ context.Context, task core.TaskID, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return m.Err
	}
	m.Sent = append(m.Sent, Message{task, text})
	return nil
}

// Messages returns a copy of the recorded messages.
func (m *Messenger) Messages() []Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.Sent)
}

// Probe answers HasChanges from a map of worktree folders.
type Probe struct {
	mu      sync.Mutex
	Changes map[string]bool
	Err     error
	Calls   int
}

func (p *Probe) HasChanges(_ context.Context, dir, _ string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Calls++
	if p.Err != nil {
		return false, p.Err
	}
	return p.Changes[dir], nil
}

// Hub records the verification changes published.
type Hub struct {
	mu     sync.Mutex
	Events []string
}

func (h *Hub) PublishVerification(id cv.VerificationID, _ core.TaskID, _ core.RepoID, state string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.Events = append(h.Events, string(id)+":"+state)
}

// Published returns a copy of the events.
func (h *Hub) Published() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.Events)
}
