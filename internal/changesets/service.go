package changesets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	cs "github.com/brendatamaar/yggdrasil/internal/core/changesets"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

const (
	systemActor = "system"
	userActor   = "user"
	// confirmActor is the "by" of a confirmation made by a run: the user
	// confirmed the hashes, the run only carries them out.
	confirmActor = "user:confirm-all"
	maxAttempts  = 4
	// confirmTries is how many times a transient failure of Confirm is
	// tried before the run is halted.
	confirmTries = 2
)

// Deps are what the service needs. Store, Proposals and Messenger are
// required.
type Deps struct {
	Store     Store
	Proposals ProposalsAPI
	Messenger Messenger
	Deps      DepSource  // optional: without it there are no dependency warnings
	Diffs     DiffSource // optional: without it notes are never marked outdated
	Hub       Publisher  // optional
	Redactor  safety.Redactor
	Clock     func() time.Time
	IDs       func(prefix string) string
	Log       *slog.Logger
	// Interval is the time between reconcile passes over change sets.
	// Default 5 s. NotesInterval is the one for notes. Default 30 s.
	Interval      time.Duration
	NotesInterval time.Duration
}

// Service runs change sets and review notes.
type Service struct {
	d Deps

	mu       sync.Mutex // serializes every change-set pass and user action
	failures map[string]int
	nmu      sync.Mutex // serializes every note action
	wake     chan struct{}
}

// New checks deps and returns the service. Nothing runs until Run.
func New(d Deps) (*Service, error) {
	switch {
	case d.Store == nil:
		return nil, errors.New("changesets: a store is required")
	case d.Proposals == nil:
		return nil, errors.New("changesets: a proposals service is required")
	case d.Messenger == nil:
		return nil, errors.New("changesets: a task messenger is required")
	}
	if d.Redactor == nil {
		d.Redactor = safety.NewRedactor()
	}
	if d.Clock == nil {
		d.Clock = func() time.Time { return time.Now().UTC() }
	}
	if d.IDs == nil {
		d.IDs = db.NewID
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.Interval <= 0 {
		d.Interval = 5 * time.Second
	}
	if d.NotesInterval <= 0 {
		d.NotesInterval = 30 * time.Second
	}
	return &Service{d: d, failures: map[string]int{}, wake: make(chan struct{}, 1)}, nil
}

// Run recovers notes that were being sent when the last run ended, then
// reconciles change sets and notes until ctx is done. Both loops are owned by
// ctx through an errgroup.
func (s *Service) Run(ctx context.Context) error {
	if ctx.Err() != nil {
		return nil //nolint:nilerr // shutting down is not an error
	}
	if err := s.RecoverNotes(ctx, true); err != nil && ctx.Err() == nil {
		s.d.Log.Warn("recover review notes failed", "err", err)
	}
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		t := time.NewTicker(s.d.Interval)
		defer t.Stop()
		for {
			if gctx.Err() != nil {
				return nil
			}
			if err := s.Reconcile(gctx); err != nil && gctx.Err() == nil {
				s.d.Log.Warn("change-set pass failed", "err", err)
			}
			select {
			case <-gctx.Done():
				return nil
			case <-t.C:
			case <-s.wake:
			}
		}
	})
	g.Go(func() error {
		t := time.NewTicker(s.d.NotesInterval)
		defer t.Stop()
		for {
			if gctx.Err() != nil {
				return nil
			}
			if err := s.ReconcileNotes(gctx); err != nil && gctx.Err() == nil {
				s.d.Log.Warn("review-note pass failed", "err", err)
			}
			select {
			case <-gctx.Done():
				return nil
			case <-t.C:
			}
		}
	})
	return g.Wait()
}

// Wake asks the change-set loop for a pass soon. It never blocks.
func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) now() time.Time { return s.d.Clock().UTC().Truncate(time.Millisecond) }

func isNotFound(err error) bool {
	if errors.Is(err, db.ErrNotFound) {
		return true
	}
	c, ok := apperr.ClassOf(err)
	return ok && c == apperr.NotFound
}

func isConflict(err error) bool { return errors.Is(err, db.ErrConflict) }

// Change sets

type setRec struct {
	St  cs.State
	Row db.ChangeSetRow
}

func (r setRec) open() cs.Open { return openOf(r.St) }

// loadSet reads the rows of a set and parses them into a state.
func (s *Service) loadSet(ctx context.Context, id core.ChangeSetID) (setRec, error) {
	row, err := s.d.Store.Set(ctx, id)
	if err != nil {
		return setRec{}, fmt.Errorf("load change set %s: %w", id, err)
	}
	repos, err := s.d.Store.TaskRepos(ctx, row.Task)
	if err != nil {
		return setRec{}, fmt.Errorf("load repos of change set %s: %w", id, err)
	}
	members, err := s.d.Store.Members(ctx, id)
	if err != nil {
		return setRec{}, fmt.Errorf("load proposals of change set %s: %w", id, err)
	}
	st, err := StateFromRows(row, repos, members)
	if err != nil {
		return setRec{}, err
	}
	return setRec{St: st, Row: row}, nil
}

type setPrep func(setRec) (in cs.Input, ok bool, err error)

// applySet is the one path every change-set input takes: load, prepare (reads
// outside data), Step, then one Commit with the new state and its audit
// entries, then the commands. A version conflict reloads and starts again.
func (s *Service) applySet(ctx context.Context, id core.ChangeSetID, actor string, prep setPrep) (setRec, []cs.Command, error) {
	var last setRec
	for try := 0; try < maxAttempts; try++ {
		rec, err := s.loadSet(ctx, id)
		if err != nil {
			return setRec{}, nil, err
		}
		last = rec
		in, ok, err := prep(rec)
		if err != nil || !ok {
			return rec, nil, err
		}
		next, cmds := cs.StepSet(rec.St, in)
		if len(cmds) == 0 {
			return rec, nil, nil
		}
		after, err := s.commitSet(ctx, rec, next, cmds, actor)
		if isConflict(err) {
			continue
		}
		if err != nil {
			return rec, nil, err
		}
		s.interpretSet(ctx, after, cmds)
		return after, cmds, nil
	}
	return last, nil, fmt.Errorf("change set %s changed while it was being updated: %w", id, db.ErrConflict)
}

func (s *Service) commitSet(ctx context.Context, rec setRec, next cs.State, cmds []cs.Command, actor string) (setRec, error) {
	row, err := RowOfState(next, rec.Row)
	if err != nil {
		return rec, err
	}
	if c, ok := next.(cs.Confirming); ok {
		// Remember the hash the user saw for Current: the one this step sends,
		// else the one already stored for the same proposal.
		seen := ""
		for _, cmd := range cmds {
			if cc, ok := cmd.(cs.ConfirmProposal); ok && cc.ID == c.Current {
				seen = cc.Hash
			}
		}
		if seen == "" {
			if prev, ok := rec.St.(cs.Confirming); ok && prev.Current == c.Current {
				seen = SeenCurrentHash(rec.Row.RunResult)
			}
		}
		if seen != "" {
			if row.RunResult, err = withCurrentHash(row.RunResult, seen); err != nil {
				return rec, err
			}
		}
	}
	now := s.now()
	err = s.d.Store.Commit(ctx, func(tx Tx) error {
		cur, err := tx.LoadSet(ctx, rec.Row.ID)
		if err != nil {
			return err
		}
		if cur.Version != rec.Row.Version {
			return fmt.Errorf("change set %s moved to version %d: %w", rec.Row.ID, cur.Version, db.ErrConflict)
		}
		v, err := tx.SaveSet(ctx, row, rec.Row.Version)
		if err != nil {
			return err
		}
		row.Version = v
		for _, c := range cmds {
			w, ok := c.(cs.WriteAudit)
			if !ok {
				continue
			}
			detail, err := json.Marshal(map[string]string{"task": string(rec.Row.Task), "outcome": w.Outcome})
			if err != nil {
				return fmt.Errorf("audit detail: %w", err)
			}
			e := core.AuditEntry{Actor: actor, Action: w.Action, Subject: "changeset:" + string(w.Set), Detail: detail}
			if err := tx.WriteAudit(ctx, e, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return rec, err
	}
	return setRec{St: next, Row: row}, nil
}

// interpretSet runs the commands of a committed step. The state and the audit
// entries are already stored, so a failure here is a log line or a new input,
// never a rollback. A panic is recovered and logged.
func (s *Service) interpretSet(ctx context.Context, rec setRec, cmds []cs.Command) {
	defer func() {
		if r := recover(); r != nil {
			s.d.Log.Error("change-set command panicked", "set", rec.Row.ID, "panic", fmt.Sprint(r))
		}
	}()
	for _, c := range cmds {
		switch c := c.(type) {
		case cs.Publish:
			if s.d.Hub != nil {
				s.d.Hub.PublishChangeSet(rec.Row.ID, rec.Row.Task, rec.St.Status())
			}
		case cs.ConfirmProposal:
			s.confirm(ctx, rec.Row.ID, c, false)
		case cs.WriteAudit:
			// Stored with the state in commitSet.
		}
	}
}

// confirm asks the proposals service to confirm one proposal with the hash the
// user saw. A "no" halts the run; a transient failure is tried again by the
// next pass, and halts the run after confirmTries.
func (s *Service) confirm(ctx context.Context, set core.ChangeSetID, c cs.ConfirmProposal, resend bool) {
	err := s.d.Proposals.Confirm(ctx, c.ID, c.Hash, confirmActor)
	key := string(set) + "/" + string(c.ID)
	var ref *Refused
	switch {
	case err == nil:
		delete(s.failures, key)
		s.Wake()
	case errors.As(err, &ref):
		delete(s.failures, key)
		if resend && ref.AlreadyDecided {
			s.Wake() // the first attempt had landed
			return
		}
		s.halt(ctx, set, c.ID, ref.Message)
	case ctx.Err() != nil:
		return
	default:
		s.failures[key]++
		s.d.Log.Warn("could not confirm a proposal of a run", "set", set, "proposal", c.ID, "tries", s.failures[key], "err", err)
		if s.failures[key] >= confirmTries {
			delete(s.failures, key)
			// A late error after the proposal moved on must not halt the run.
			if !s.stillPending(ctx, set, c.ID) {
				s.Wake()
				return
			}
			s.halt(ctx, set, c.ID, "the proposal could not be confirmed; try again")
		}
	}
}

// stillPending re-reads the set and says whether the member is still pending.
// When the set cannot be read it answers true: halting is the safe side.
func (s *Service) stillPending(ctx context.Context, set core.ChangeSetID, id core.ProposalID) bool {
	rec, err := s.loadSet(ctx, set)
	if err != nil {
		return true
	}
	m, ok := findMember(openOf(rec.St).Members, id)
	return !ok || m.Status == core.ProposalPending
}

func (s *Service) halt(ctx context.Context, set core.ChangeSetID, id core.ProposalID, reason string) {
	_, _, err := s.applySet(ctx, set, systemActor, func(setRec) (cs.Input, bool, error) {
		return cs.ConfirmRefused{ID: id, Reason: reason, Now: s.now()}, true, nil
	})
	if err != nil && ctx.Err() == nil {
		s.d.Log.Error("could not record a refused confirmation", "set", set, "proposal", id, "err", err)
	}
}

func hasConfirm(cmds []cs.Command) bool {
	return slices.ContainsFunc(cmds, func(c cs.Command) bool { _, ok := c.(cs.ConfirmProposal); return ok })
}

// refresh re-derives a set from its rows (the proposals changed) and moves a
// running confirm run on. It is idempotent: duplicate or late deliveries
// change nothing.
func (s *Service) refresh(ctx context.Context, id core.ChangeSetID) error {
	rec, cmds, err := s.applySet(ctx, id, systemActor, func(rec setRec) (cs.Input, bool, error) {
		o := rec.open()
		return cs.MembersChanged{Members: o.Members, Links: o.Links, Now: s.now()}, true, nil
	})
	if err != nil {
		return err
	}
	// A confirmation that never landed (crash between the commit and the call,
	// or a transient error) is sent again. Not when this pass just sent one:
	// the members read above are older than that confirmation.
	if c, ok := rec.St.(cs.Confirming); ok && !hasConfirm(cmds) {
		if m, found := findMember(c.Members, c.Current); found && m.Status == core.ProposalPending {
			// Verify against the hash the user saw, not the one just loaded.
			seen := SeenCurrentHash(rec.Row.RunResult)
			if seen == "" || seen != m.Hash {
				s.halt(ctx, id, m.ID, "the proposal changed after you reviewed it; review it again")
				return nil
			}
			s.confirm(ctx, id, cs.ConfirmProposal{ID: m.ID, Hash: seen}, true)
		}
	}
	return nil
}

func findMember(ms []cs.Member, id core.ProposalID) (cs.Member, bool) {
	for _, m := range ms {
		if m.ID == id {
			return m, true
		}
	}
	return cs.Member{}, false
}

// Reconcile re-derives every open change set from its rows once. Rows that
// cannot be read are logged and skipped, one set at a time.
func (s *Service) Reconcile(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.d.Store.OpenSets(ctx)
	if err != nil {
		return fmt.Errorf("list open change sets: %w", err)
	}
	var errs []error
	for _, row := range rows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.refresh(ctx, row.ID); err != nil {
			s.d.Log.Warn("could not update a change set", "set", row.ID, "err", err)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// OnProposalChanged tells the service that a proposal was created or changed
// (hub topic "proposals"). It is how a settled (shipped or closed) set is
// reopened by a new proposal, which the periodic pass does not look at.
func (s *Service) OnProposalChanged(ctx context.Context, id core.ProposalID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, err := s.d.Store.SetOfProposal(ctx, id)
	if err != nil {
		if isNotFound(err) {
			return nil // a proposal without a change set
		}
		return err
	}
	return s.refresh(ctx, row.ID)
}

// View is a change set for the UI.
type View struct {
	ID      core.ChangeSetID
	Task    core.TaskID
	State   cs.State
	Version int64
	// Suggested is the order that puts dependencies first; Cycle lists the
	// repos on a dependency cycle (then Suggested is the current order).
	Suggested []core.RepoID
	Cycle     []core.RepoID
	// Warnings are what a run over everything would raise now.
	Warnings []cs.Warning
}

// edges reads the dependency edges of the repos; a failure means no edges
// (the warnings are advisory).
func (s *Service) edges(ctx context.Context, repos []core.RepoID) []cs.Edge {
	if s.d.Deps == nil {
		return nil
	}
	e, err := s.d.Deps.Edges(ctx, repos)
	if err != nil {
		s.d.Log.Warn("could not read repo dependencies", "err", err)
		return nil
	}
	return e
}

func (s *Service) view(ctx context.Context, rec setRec) View {
	o := rec.open()
	edges := s.edges(ctx, o.Set.Repos)
	sug, cyc := cs.SuggestOrder(o.Set.Repos, edges, o.Set.Order)
	return View{
		ID: o.Set.ID, Task: o.Set.Task, State: rec.St, Version: rec.Row.Version, Suggested: sug, Cycle: cyc,
		Warnings: cs.Preflight(rec.St, cs.ScopeAll{}, edges),
	}
}

// Get returns a change set with its suggested order and warnings.
func (s *Service) Get(ctx context.Context, id core.ChangeSetID) (View, error) {
	if err := id.Validate(); err != nil {
		return View{}, fmt.Errorf("get change set: %w", db.ErrNotFound)
	}
	rec, err := s.loadSet(ctx, id)
	if err != nil {
		return View{}, err
	}
	return s.view(ctx, rec), nil
}

// ForTask returns the ID of the task's change set, or "" when it has none.
func (s *Service) ForTask(ctx context.Context, task core.TaskID) (core.ChangeSetID, error) {
	return s.d.Store.SetOfTask(ctx, task)
}

// Preflight returns the warnings a run with this scope would raise now.
func (s *Service) Preflight(ctx context.Context, id core.ChangeSetID, scope, repo string) ([]cs.Warning, error) {
	rec, err := s.loadSet(ctx, id)
	if err != nil {
		return nil, err
	}
	o := rec.open()
	if err := scopeErr(scope, repo, o.Set.Repos); err != nil {
		return nil, userErr(err)
	}
	return cs.Preflight(rec.St, scopeOf(scope, repo), s.edges(ctx, o.Set.Repos)), nil
}

func scopeOf(scope, repo string) cs.Scope {
	if scope == "repo" {
		return cs.ScopeRepo{Repo: core.RepoID(repo)}
	}
	return cs.ScopeAll{}
}

func scopeErr(scope, repo string, repos []core.RepoID) error {
	switch scope {
	case "all":
		if repo != "" {
			return errors.New("a run over everything doesn't take a repo")
		}
		return nil
	case "repo":
		if err := core.RepoID(repo).Validate(); err != nil {
			return err
		}
		if !slices.Contains(repos, core.RepoID(repo)) {
			return fmt.Errorf("repo %s isn't part of this change set", repo)
		}
		return nil
	}
	return fmt.Errorf("unknown scope %q: use all or repo", scope)
}

func userErr(err error) error {
	if err == nil {
		return nil
	}
	return &UserError{Message: err.Error()}
}

// SetOrder stores the merge order. The order is frozen while a run is in
// progress.
func (s *Service) SetOrder(ctx context.Context, id core.ChangeSetID, raw []string, actor string) (View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := s.loadSet(ctx, id)
	if err != nil {
		return View{}, err
	}
	o := rec.open()
	order, err := ParseOrder(raw, o.Set.Repos)
	if err != nil {
		return View{}, userErr(err)
	}
	if _, ok := rec.St.(cs.Confirming); ok {
		return View{}, &UserError{Message: "The order can't change while proposals are being confirmed. Stop the run first.", Kind: KindConflict}
	}
	edges := s.edges(ctx, o.Set.Repos)
	after, _, err := s.applySet(ctx, id, actor, func(setRec) (cs.Input, bool, error) {
		return cs.SetOrder{Order: order, Deps: edges, Now: s.now()}, true, nil
	})
	if err != nil {
		return View{}, err
	}
	return s.view(ctx, after), nil
}

// ConfirmRun starts a run that confirms the pending proposals in scope, one
// at a time, in merge order, with the hashes the user saw. It returns at once;
// the run goes on as the proposals finish.
func (s *Service) ConfirmRun(ctx context.Context, id core.ChangeSetID, raw RawConfirm, actor string) (View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := s.loadSet(ctx, id)
	if err != nil {
		return View{}, err
	}
	o := rec.open()
	scope, seen, ack, err := ParseConfirmRun(raw, o.Set.Repos, o.Members)
	if err != nil {
		return View{}, userErr(err)
	}
	switch rec.St.(type) {
	case cs.Confirming:
		return View{}, &UserError{Message: "Proposals of this change set are already being confirmed. Wait for the run to end or stop it.", Kind: KindConflict}
	case cs.Open:
	default:
		return View{}, &UserError{Message: "This change set is finished. Nothing is left to confirm.", Kind: KindConflict}
	}
	edges := s.edges(ctx, o.Set.Repos)
	after, cmds, err := s.applySet(ctx, id, actor, func(setRec) (cs.Input, bool, error) {
		return cs.ConfirmRun{Scope: scope, Seen: seen, Ack: ack, Deps: edges, Now: s.now()}, true, nil
	})
	if err != nil {
		return View{}, err
	}
	if len(cmds) == 0 {
		return View{}, &UserError{Message: whyNoRun(rec.St, scope, seen, ack, edges), Kind: KindConflict}
	}
	return s.view(ctx, after), nil
}

// whyNoRun explains in plain language why ConfirmRun changed nothing. The
// step decides; this only mirrors its guards to say which one held.
func whyNoRun(st cs.State, scope cs.Scope, seen map[core.ProposalID]string, ack []cs.WarningCode, edges []cs.Edge) string {
	plan := cs.Plan(st, scope)
	switch {
	case len(plan) == 0:
		return "Nothing is waiting for confirmation here."
	case len(seen) != len(plan):
		return "The list of proposals changed since you reviewed it. Reload and review it again."
	}
	for _, p := range plan {
		if seen[p.ID] != p.Hash {
			return "A proposal changed since you reviewed it. Reload and review it again."
		}
	}
	for _, w := range cs.Preflight(st, scope, edges) {
		if !slices.Contains(ack, w.Code) {
			return "A repo is ordered before a repo it depends on. Change the order, or confirm that this is what you want."
		}
	}
	return "The proposals could not be confirmed. Reload and try again."
}

// StopRun ends a run. The proposal being executed finishes by itself.
func (s *Service) StopRun(ctx context.Context, id core.ChangeSetID, actor string) (View, error) {
	return s.simple(ctx, id, actor, "There is no run to stop.", func() cs.Input { return cs.StopRun{Now: s.now()} })
}

// Close closes a change set that has nothing pending.
func (s *Service) Close(ctx context.Context, id core.ChangeSetID, actor string) (View, error) {
	return s.simple(ctx, id, actor, "This change set still has proposals waiting. Decide on them first.",
		func() cs.Input { return cs.Close{Now: s.now()} })
}

func (s *Service) simple(ctx context.Context, id core.ChangeSetID, actor, nothing string, mk func() cs.Input) (View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	after, cmds, err := s.applySet(ctx, id, actor, func(setRec) (cs.Input, bool, error) { return mk(), true, nil })
	if err != nil {
		return View{}, err
	}
	if len(cmds) == 0 {
		return View{}, &UserError{Message: nothing, Kind: KindConflict}
	}
	return s.view(ctx, after), nil
}
