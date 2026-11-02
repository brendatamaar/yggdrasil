// Package changesetstest has the fakes and the contract suites for
// internal/changesets: an in-memory store, a scripted proposals API, a
// messenger, a hub, a dependency source and a diff source.
package changesetstest

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/audit"
	"github.com/brendatamaar/yggdrasil/internal/changesets"
	"github.com/brendatamaar/yggdrasil/internal/core"
	cs "github.com/brendatamaar/yggdrasil/internal/core/changesets"
	"github.com/brendatamaar/yggdrasil/internal/core/review"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// Hash returns a valid 64-hex action hash for n.
func Hash(n int) string { return fmt.Sprintf("%064x", n) }

// proposal is a fake proposal row.
type proposal struct {
	set    core.ChangeSetID
	m      db.ChangeSetMemberRow
	serial int
}

// World is the state behind the fakes: tasks, change sets, proposals, notes and
// the audit log. Store, API and the other fakes are views on it, so a test can
// move a proposal on and the store sees it.
type World struct {
	mu        sync.Mutex
	taskRepos map[core.TaskID][]core.RepoID
	sets      map[core.ChangeSetID]db.ChangeSetRow
	props     map[core.ProposalID]*proposal
	notes     map[string]db.NoteRow
	audit     []core.AuditEntry
	nextAudit int
	nProps    int
	// FailCommits makes the next n Commit calls fail before anything is
	// written (for crash-point tests).
	FailCommits int
}

// NewWorld returns an empty world.
func NewWorld() *World {
	return &World{
		taskRepos: map[core.TaskID][]core.RepoID{}, sets: map[core.ChangeSetID]db.ChangeSetRow{},
		props: map[core.ProposalID]*proposal{}, notes: map[string]db.NoteRow{},
	}
}

// AddTask registers a task and its repos.
func (w *World) AddTask(task core.TaskID, repos ...core.RepoID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.taskRepos[task] = slices.Clone(repos)
}

// AddSet creates an open change set for the task, as proposals.Create does.
func (w *World) AddSet(id core.ChangeSetID, task core.TaskID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sets[id] = db.ChangeSetRow{
		ID: id, Task: task, Status: core.ChangeSetOpen, MergeOrder: []core.RepoID{},
		RunResult: []byte(`{"kind":"idle"}`), CreatedAt: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC),
	}
}

// AddProposal adds a pending proposal to a set and returns its ID and hash.
func (w *World) AddProposal(set core.ChangeSetID, repo core.RepoID, kind core.ProposalKind) (core.ProposalID, string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.nProps++
	id := core.ProposalID(fmt.Sprintf("prp_%03d", w.nProps))
	h := Hash(w.nProps)
	w.props[id] = &proposal{set: set, serial: w.nProps, m: db.ChangeSetMemberRow{ID: id, Repo: repo, Kind: kind, Status: core.ProposalPending, Hash: h}}
	return id, h
}

// SetStatus moves a proposal to a status (mrURL is its result for an executed
// merge request).
func (w *World) SetStatus(id core.ProposalID, st core.ProposalStatus, mrURL string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	p := w.props[id]
	p.m.Status, p.m.MRURL = st, mrURL
}

// BumpNoteVersion raises a note's stored version without changing it, as a
// write by another process would.
func (w *World) BumpNoteVersion(id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := w.notes[id]
	n.Version++
	w.notes[id] = n
}

// SetHash changes the action hash of a proposal, as an edit of the proposal
// after the user reviewed it would.
func (w *World) SetHash(id core.ProposalID, hash string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.props[id].m.Hash = hash
}

// Status returns a proposal's status.
func (w *World) Status(id core.ProposalID) core.ProposalStatus {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.props[id].m.Status
}

// SetRow returns a change set row.
func (w *World) SetRow(id core.ChangeSetID) db.ChangeSetRow {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.sets[id]
}

// Audit returns the audit entries for a subject.
func (w *World) Audit(subject string) []core.AuditEntry {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []core.AuditEntry
	for _, e := range w.audit {
		if e.Subject == subject {
			out = append(out, e)
		}
	}
	return out
}

// AllAudit returns every audit entry.
func (w *World) AllAudit() []core.AuditEntry {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.audit)
}

// Store is the in-memory changesets.Store.
type Store struct{ W *World }

// NewStore returns a store on w.
func NewStore(w *World) *Store { return &Store{W: w} }

type fakeTx struct {
	w     *World
	sets  map[core.ChangeSetID]db.ChangeSetRow
	notes map[string]db.NoteRow
	audit []core.AuditEntry
}

// Commit runs fn on a copy and keeps it only if fn succeeds.
func (s *Store) Commit(ctx context.Context, fn func(changesets.Tx) error) error {
	w := s.W
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.FailCommits > 0 {
		w.FailCommits--
		return errors.New("fake store: commit failed")
	}
	tx := &fakeTx{w: w, sets: maps.Clone(w.sets), notes: maps.Clone(w.notes), audit: slices.Clone(w.audit)}
	if err := fn(tx); err != nil {
		return err
	}
	w.sets, w.notes, w.audit = tx.sets, tx.notes, tx.audit
	return nil
}

func (t *fakeTx) LoadSet(_ context.Context, id core.ChangeSetID) (db.ChangeSetRow, error) {
	r, ok := t.sets[id]
	if !ok {
		return db.ChangeSetRow{}, fmt.Errorf("set %s: %w", id, db.ErrNotFound)
	}
	return r, nil
}

func (t *fakeTx) SaveSet(_ context.Context, row db.ChangeSetRow, expected int64) (int64, error) {
	cur, ok := t.sets[row.ID]
	if !ok {
		return 0, fmt.Errorf("set %s: %w", row.ID, db.ErrNotFound)
	}
	if cur.Version != expected {
		return 0, fmt.Errorf("set %s: %w", row.ID, db.ErrConflict)
	}
	if err := row.Status.Validate(); err != nil {
		return 0, err
	}
	cur.Status, cur.MergeOrder, cur.RunResult = row.Status, slices.Clone(row.MergeOrder), slices.Clone(row.RunResult)
	cur.Version++
	t.sets[row.ID] = cur
	return cur.Version, nil
}

func (t *fakeTx) LoadNote(_ context.Context, id string) (db.NoteRow, error) {
	n, ok := t.notes[id]
	if !ok {
		return db.NoteRow{}, fmt.Errorf("note %s: %w", id, db.ErrNotFound)
	}
	return n, nil
}

func (t *fakeTx) CountOpenNotes(_ context.Context, task core.TaskID) (int, error) {
	c := 0
	for _, n := range t.notes {
		if n.Task == task && (n.State == "open" || n.State == "outdated") {
			c++
		}
	}
	return c, nil
}

func (t *fakeTx) InsertNote(_ context.Context, n db.NoteRow) error {
	if _, ok := t.notes[n.ID]; ok {
		return fmt.Errorf("note %s: %w", n.ID, db.ErrConflict)
	}
	n.Version = 0
	t.notes[n.ID] = n
	return nil
}

func (t *fakeTx) UpdateNote(_ context.Context, n db.NoteRow, expected int64) (int64, error) {
	cur, ok := t.notes[n.ID]
	if !ok {
		return 0, fmt.Errorf("note %s: %w", n.ID, db.ErrNotFound)
	}
	if cur.Version != expected {
		return 0, fmt.Errorf("note %s: %w", n.ID, db.ErrConflict)
	}
	n.Version, n.Task, n.Repo, n.Path, n.Line, n.Side, n.DiffHash, n.CreatedAt = expected+1, cur.Task, cur.Repo, cur.Path, cur.Line, cur.Side, cur.DiffHash, cur.CreatedAt
	t.notes[n.ID] = n
	return n.Version, nil
}

func (t *fakeTx) DeleteNote(_ context.Context, id string) error {
	n, ok := t.notes[id]
	if !ok || (n.State != "open" && n.State != "outdated") {
		return fmt.Errorf("note %s: %w", id, db.ErrNotFound)
	}
	delete(t.notes, id)
	return nil
}

func (t *fakeTx) WriteAudit(_ context.Context, e core.AuditEntry, now time.Time) error {
	t.w.nextAudit++
	p, err := audit.Prepare(e, safety.NewRedactor(), core.AuditID(fmt.Sprintf("aud_%04d", t.w.nextAudit)), now)
	if err != nil {
		return err
	}
	t.audit = append(t.audit, p)
	return nil
}

func (s *Store) Set(_ context.Context, id core.ChangeSetID) (db.ChangeSetRow, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	r, ok := s.W.sets[id]
	if !ok {
		return db.ChangeSetRow{}, fmt.Errorf("set %s: %w", id, db.ErrNotFound)
	}
	return r, nil
}

func (s *Store) SetOfProposal(_ context.Context, id core.ProposalID) (db.ChangeSetRow, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	p, ok := s.W.props[id]
	if !ok {
		return db.ChangeSetRow{}, fmt.Errorf("proposal %s: %w", id, db.ErrNotFound)
	}
	return s.W.sets[p.set], nil
}

func (s *Store) SetOfTask(_ context.Context, task core.TaskID) (core.ChangeSetID, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	var ids []core.ChangeSetID
	for id, r := range s.W.sets {
		if r.Task == task {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return "", nil
	}
	slices.Sort(ids)
	return ids[0], nil
}

func (s *Store) OpenSets(_ context.Context) ([]db.ChangeSetRow, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	var out []db.ChangeSetRow
	for _, r := range s.W.sets {
		if r.Status == core.ChangeSetOpen {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b db.ChangeSetRow) int { return strings.Compare(string(a.ID), string(b.ID)) })
	return out, nil
}

func (s *Store) Members(_ context.Context, id core.ChangeSetID) ([]db.ChangeSetMemberRow, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	var ps []*proposal
	for _, p := range s.W.props {
		if p.set == id {
			ps = append(ps, p)
		}
	}
	slices.SortFunc(ps, func(a, b *proposal) int { return a.serial - b.serial })
	out := make([]db.ChangeSetMemberRow, len(ps))
	for i, p := range ps {
		out[i] = p.m
	}
	return out, nil
}

func (s *Store) TaskRepos(_ context.Context, task core.TaskID) ([]core.RepoID, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	r := slices.Clone(s.W.taskRepos[task])
	slices.Sort(r)
	return r, nil
}

func (s *Store) Note(_ context.Context, id string) (db.NoteRow, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	n, ok := s.W.notes[id]
	if !ok {
		return db.NoteRow{}, fmt.Errorf("note %s: %w", id, db.ErrNotFound)
	}
	return n, nil
}

func (s *Store) notesWhere(keep func(db.NoteRow) bool) []db.NoteRow {
	var out []db.NoteRow
	for _, n := range s.W.notes {
		if keep(n) {
			out = append(out, n)
		}
	}
	slices.SortFunc(out, func(a, b db.NoteRow) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

func (s *Store) TaskNotes(_ context.Context, task core.TaskID) ([]db.NoteRow, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	return s.notesWhere(func(n db.NoteRow) bool { return n.Task == task }), nil
}

func (s *Store) NotesInState(_ context.Context, state string) ([]db.NoteRow, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	return s.notesWhere(func(n db.NoteRow) bool { return n.State == state }), nil
}

func (s *Store) TasksWithNotesInState(_ context.Context, state string) ([]core.TaskID, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	seen := map[core.TaskID]bool{}
	var out []core.TaskID
	for _, n := range s.notesWhere(func(n db.NoteRow) bool { return n.State == state }) {
		if !seen[n.Task] {
			seen[n.Task] = true
			out = append(out, n.Task)
		}
	}
	slices.Sort(out)
	return out, nil
}

// API is a scripted changesets.ProposalsAPI over the world: it confirms a
// pending proposal with the right hash and refuses everything else, like the
// real service.
type API struct {
	W *World
	// Fail, when set, is returned (once per call, FailTimes times) as a
	// transient error before anything else is checked.
	Fail      error
	FailTimes int
	// OnConfirm runs after a successful confirmation, under no lock.
	OnConfirm func(id core.ProposalID)
	// OnFail runs each time Fail is returned, with the number of failures so far.
	OnFail func(id core.ProposalID, n int)
	nFail  int

	mu    sync.Mutex
	calls []Call
}

// Call is one Confirm call.
type Call struct {
	ID   core.ProposalID
	Hash string
	By   string
	Err  error
}

// NewAPI returns a confirming API on w.
func NewAPI(w *World) *API { return &API{W: w} }

// Calls returns the Confirm calls so far.
func (a *API) Calls() []Call {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.calls)
}

// Confirm implements changesets.ProposalsAPI.
func (a *API) Confirm(_ context.Context, id core.ProposalID, hash, by string) error {
	err := a.confirm(id, hash)
	a.mu.Lock()
	a.calls = append(a.calls, Call{ID: id, Hash: hash, By: by, Err: err})
	a.mu.Unlock()
	if err == nil && a.OnConfirm != nil {
		a.OnConfirm(id)
	}
	return err
}

func (a *API) confirm(id core.ProposalID, hash string) error {
	a.mu.Lock()
	if a.Fail != nil && a.FailTimes > 0 {
		a.FailTimes--
		err := a.Fail
		a.nFail++
		n := a.nFail
		a.mu.Unlock()
		if a.OnFail != nil {
			a.OnFail(id, n)
		}
		return err
	}
	a.mu.Unlock()
	a.W.mu.Lock()
	defer a.W.mu.Unlock()
	p, ok := a.W.props[id]
	switch {
	case !ok:
		return &changesets.Refused{Message: "That proposal no longer exists."}
	case p.m.Status != core.ProposalPending:
		return &changesets.Refused{Message: "That proposal was already decided.", AlreadyDecided: true}
	case p.m.Hash != hash:
		return &changesets.Refused{Message: "What you confirmed is not what this proposal now says."}
	}
	p.m.Status = core.ProposalConfirmed
	return nil
}

// Messenger records messages and answers as scripted.
type Messenger struct {
	mu   sync.Mutex
	Sent []Message
	// Err, when set, refuses every message.
	Err error
	// Turn is the turn ID returned; default "trn_1", "trn_2", ...
	Turn string
	// During runs inside SendMessage (a test hook for crash points).
	During func()
}

// Message is one sent message.
type Message struct {
	Task core.TaskID
	Text string
}

// SendMessage implements changesets.Messenger.
func (m *Messenger) SendMessage(_ context.Context, task core.TaskID, text string) (string, error) {
	if m.During != nil {
		m.During()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return "", m.Err
	}
	m.Sent = append(m.Sent, Message{Task: task, Text: text})
	if m.Turn != "" {
		return m.Turn, nil
	}
	return fmt.Sprintf("trn_%d", len(m.Sent)), nil
}

// Messages returns what was sent.
func (m *Messenger) Messages() []Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.Sent)
}

// Hub records published events.
type Hub struct {
	mu     sync.Mutex
	Sets   []core.ChangeSetID
	Status []core.ChangeSetStatus
	Notes  []core.TaskID
}

func (h *Hub) PublishChangeSet(id core.ChangeSetID, _ core.TaskID, st core.ChangeSetStatus) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.Sets, h.Status = append(h.Sets, id), append(h.Status, st)
}

func (h *Hub) PublishNotes(task core.TaskID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.Notes = append(h.Notes, task)
}

// SetEvents returns how many change-set events were published.
func (h *Hub) SetEvents() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.Sets)
}

// Deps is a DepSource over a fixed list of edges.
type Deps struct {
	Edge []cs.Edge
	Err  error
}

func (d *Deps) Edges(context.Context, []core.RepoID) ([]cs.Edge, error) { return d.Edge, d.Err }

// Diffs is a DiffSource over fixed hashes per (task, repo); a missing key is a
// read failure.
type Diffs struct {
	mu sync.Mutex
	M  map[string]map[review.RelPath]string
}

// Set stores the hashes of one repo of a task.
func (d *Diffs) Set(task core.TaskID, repo core.RepoID, h map[review.RelPath]string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.M == nil {
		d.M = map[string]map[review.RelPath]string{}
	}
	d.M[string(task)+"/"+string(repo)] = h
}

func (d *Diffs) FileHashes(_ context.Context, task core.TaskID, repo core.RepoID) (map[review.RelPath]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	h, ok := d.M[string(task)+"/"+string(repo)]
	if !ok {
		return nil, errors.New("fake diff: cannot read the worktree")
	}
	return h, nil
}
