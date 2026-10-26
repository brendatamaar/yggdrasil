package repos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/audit"
	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/mainlock"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// The shell of the git actions (WS-06, WS-07). The decisions are in
// internal/core/repos (Act); this file parses at the boundary, keeps the state
// under a mutex, commits the audit entries and runs the commands. The design
// is docs/design/git-actions.md.

// Timeouts of one git command.
const (
	checkTimeout    = 30 * time.Second
	fetchTimeout    = 120 * time.Second
	checkoutTimeout = 60 * time.Second
	sweepEvery      = time.Minute
)

// GitRunner is the part of *git.Git the actions use. The contract suite is
// repostest.GitRunnerContract.
type GitRunner interface {
	Fetch(ctx context.Context, dir, remote string) error
	PullFFOnly(ctx context.Context, dir string) error
	Checkout(ctx context.Context, dir, branch string) error
	Status(ctx context.Context, dir string) (git.Status, error)
	BranchExists(ctx context.Context, dir, name string) (local, remote bool, err error)
}

// BuildBusy is what the actions need from the builds service. A nil BuildBusy
// means no build check. *builds.Service implements it.
type BuildBusy interface {
	BusyMainCheckouts(ctx context.Context, ids []core.RepoID) (map[core.RepoID]string, error)
	SubscribeStarted(fn func(builds.BuildStarted))
}

// BatchPublisher tells subscribers a batch changed. Nil means nobody listens.
// *api.Server implements it.
type BatchPublisher interface {
	PublishGitBatch(batch corerepos.BatchID, repos []core.RepoID, finished bool)
}

// ActionsDeps are what NewActionsService needs. Store, Redactor and Git are
// required.
type ActionsDeps struct {
	Store    *db.Store
	Redactor safety.Redactor
	Git      GitRunner
	Builds   BuildBusy
	// Status is the same refresher tracking uses; nil means no refresh.
	Status    StatusRefresher
	Publisher BatchPublisher
	// MainLock is the shared main-checkout lock. Nil means no locking.
	MainLock  mainlock.MainLock
	Log       *slog.Logger
	Clock     func() time.Time
	IDs       func(prefix string) string
	// Max is the number of concurrent items; default 4.
	Max int
}

// ActionsService runs git batches. It implements api.GitActionsService
// together with the open methods of open.go.
//
// Wiring (cmd/ygg): NewActionsService(deps), add svc.Run(ctx) to the app
// errgroup (it blocks until ctx is done, then waits for running git commands).
type ActionsService struct {
	d ActionsDeps

	mu      sync.Mutex
	st      corerepos.Actions
	group   errgroup.Group
	runCtx  context.Context
	stopped bool
	// locks are the main-checkout locks of items that may still run, released
	// when the item is done or skipped. notes say who held a repo when an item
	// was skipped for the lock, for the plain-language message.
	locks map[itemKey]func()
	notes map[itemKey]mainlock.OwnerKind
}

type itemKey struct {
	batch corerepos.BatchID
	repo  core.RepoID
}

// NewActionsService returns the service and subscribes to build starts.
func NewActionsService(d ActionsDeps) (*ActionsService, error) {
	switch {
	case d.Store == nil:
		return nil, errors.New("repos: git actions need a store")
	case d.Redactor == nil:
		return nil, errors.New("repos: git actions need a redactor (audit entries must be redacted)")
	case d.Git == nil:
		return nil, errors.New("repos: git actions need git")
	}
	if d.Log == nil {
		d.Log = slog.New(safety.NewLogHandler(slog.DiscardHandler, d.Redactor))
	}
	if d.Clock == nil {
		d.Clock = func() time.Time { return time.Now().UTC() }
	}
	if d.IDs == nil {
		d.IDs = db.NewID
	}
	s := &ActionsService{d: d, st: corerepos.NewActions(d.Max), locks: map[itemKey]func(){}, notes: map[itemKey]mainlock.OwnerKind{}}
	if d.Builds != nil {
		d.Builds.SubscribeStarted(s.onBuildStarted)
	}
	return s, nil
}

// Run owns the service until ctx is done: it sweeps old batches, and on exit it
// stops accepting work and waits for every git command to end (their context
// is ctx, so they are cancelled). Run must be called once.
func (s *ActionsService) Run(ctx context.Context) error {
	s.mu.Lock()
	s.runCtx = ctx
	s.mu.Unlock()
	t := time.NewTicker(sweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			s.mu.Lock()
			s.stopped = true
			s.mu.Unlock()
			_ = s.group.Wait()
			return nil
		case <-t.C:
			_, _ = s.apply(corerepos.ActSweep{Now: s.d.Clock().UTC()})
		}
	}
}

// ItemView is one repo of a batch, for the UI.
type ItemView struct {
	Repo core.RepoID
	// State is pending, checking, running, done or skipped.
	State string
	// Outcome (done): fetched, pulled, up_to_date, pull_refused, checked_out,
	// checkout_refused or failed.
	Outcome string
	// Reason: the skip reason, or the refusal or failure kind.
	Reason string
	// Message says what to do next; empty when nothing is needed.
	Message string
	From    string
	To      string
	Branch  string
	Created bool
	At      time.Time
}

// BatchView is a copy of a batch for the UI.
type BatchView struct {
	ID       corerepos.BatchID
	Kind     corerepos.ActionKind
	Branch   string
	Started  time.Time
	Finished bool
	Items    []ItemView
}

func viewOf(b corerepos.Batch) BatchView {
	v := BatchView{ID: b.ID, Kind: b.Kind, Branch: b.Branch, Started: b.Started, Finished: b.Finished(),
		Items: make([]ItemView, 0, len(b.Items))}
	for _, it := range b.Items {
		iv := ItemView{Repo: it.Repo}
		switch s := it.State.(type) {
		case corerepos.Pending:
			iv.State = "pending"
		case corerepos.Checking:
			iv.State = "checking"
		case corerepos.Running:
			iv.State = "running"
		case corerepos.Skipped:
			iv.State, iv.Reason, iv.At = "skipped", string(s.Reason), s.At
			iv.Message = skipMessage(s.Reason)
		case corerepos.Done:
			iv.State, iv.At = "done", s.At
			describe(&iv, s.Outcome)
		}
		v.Items = append(v.Items, iv)
	}
	return v
}

func skipMessage(r corerepos.SkipReason) string {
	switch r {
	case corerepos.SkipDirty:
		return "Skipped: this repo has uncommitted changes. Commit or stash them, then switch again."
	case corerepos.SkipNoSuchBranch:
		return "Skipped: this repo has no branch with that name."
	case corerepos.SkipBuildRunning:
		return "Skipped: a build is running in this repo. Wait for it to finish, then try again."
	case corerepos.SkipUntracked:
		return "Skipped: this repo isn't tracked any more."
	case corerepos.SkipCancelled:
		return "Skipped: the batch was cancelled."
	}
	return ""
}

func describe(iv *ItemView, o corerepos.ActOutcome) {
	switch x := o.(type) {
	case corerepos.Fetched:
		iv.Outcome = "fetched"
	case corerepos.UpToDate:
		iv.Outcome = "up_to_date"
	case corerepos.Pulled:
		iv.Outcome, iv.From, iv.To = "pulled", x.From, x.To
	case corerepos.PullRefused:
		iv.Outcome, iv.Reason, iv.Message = "pull_refused", string(x.Reason), x.Message
	case corerepos.CheckedOut:
		iv.Outcome, iv.Branch, iv.Created = "checked_out", x.Branch, x.Created
		if x.Created {
			iv.Message = fmt.Sprintf("Created local branch %s tracking origin/%s.", x.Branch, x.Branch)
		}
	case corerepos.CheckoutRefused:
		iv.Outcome, iv.Reason, iv.Message = "checkout_refused", string(x.Reason), x.Message
	case corerepos.Failed:
		iv.Outcome, iv.Reason, iv.Message = "failed", string(x.Kind), x.Message
	}
}

// Start parses nothing: req came from ParseBatchRequest. It checks that every
// repo is tracked, takes the builds snapshot and starts the batch. The start
// audit entry is committed before any git command runs.
func (s *ActionsService) Start(ctx context.Context, req BatchRequest, actor corerepos.Actor) (corerepos.BatchID, error) {
	for _, id := range req.Repos {
		if _, err := s.repo(ctx, id); err != nil {
			return "", err
		}
	}
	var busy map[core.RepoID]string
	if s.d.Builds != nil && req.Kind.ChangesCheckout() {
		var err error
		if busy, err = s.d.Builds.BusyMainCheckouts(ctx, req.Repos); err != nil {
			return "", apperr.New(apperr.Unavailable, "Yggdrasil couldn't check for running builds, so nothing was started. Try again.", err)
		}
	}
	id := corerepos.BatchID(s.d.IDs("gab"))
	busy, undo := s.lockRepos(id, req, busy)
	in := corerepos.StartBatch{ID: id, Kind: req.Kind, Branch: req.Branch, Repos: req.Repos, Busy: busy,
		Actor: actor, Now: s.d.Clock().UTC()}
	if err := in.Validate(); err != nil {
		undo()
		return "", apperr.New(apperr.Invalid, "That request isn't valid. Check the action and the repos.", err)
	}
	if _, err := s.apply(in); err != nil {
		undo()
		return "", err
	}
	return id, nil
}

// lockRepos takes the main-checkout lock of every repo of a batch that
// changes the checkout (fetch changes no working tree and takes none). A repo
// that a task, a build or another git action holds is added to busy, so its
// item is skipped. The locks stay held until the item is done or skipped.
// undo gives them back when the batch could not be started.
func (s *ActionsService) lockRepos(id corerepos.BatchID, req BatchRequest, busy map[core.RepoID]string) (map[core.RepoID]string, func()) {
	if s.d.MainLock == nil || !req.Kind.ChangesCheckout() {
		return busy, func() {}
	}
	out := make(map[core.RepoID]string, len(busy)+len(req.Repos))
	for r, why := range busy {
		out[r] = why
	}
	taken := map[itemKey]func(){}
	notes := map[itemKey]mainlock.OwnerKind{}
	for _, r := range req.Repos {
		k := itemKey{id, r}
		if _, dup := taken[k]; dup {
			continue
		}
		if _, skip := out[r]; skip {
			continue
		}
		rel, h, ok := s.d.MainLock.TryAcquire([]core.RepoID{r}, mainlock.Owner{Kind: mainlock.KindGitAction, ID: string(id) + ":" + string(r)})
		if !ok {
			out[r] = holderReason(h.Owner.Kind)
			notes[k] = h.Owner.Kind
			continue
		}
		taken[k] = rel
	}
	s.mu.Lock()
	for k, rel := range taken {
		s.locks[k] = rel
	}
	for k, n := range notes {
		s.notes[k] = n
	}
	s.mu.Unlock()
	return out, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for k, rel := range taken {
			rel()
			delete(s.locks, k)
		}
		for k := range notes {
			delete(s.notes, k)
		}
	}
}

func holderReason(k mainlock.OwnerKind) string {
	switch k {
	case mainlock.KindTask:
		return "a task is working in this repo"
	case mainlock.KindBuild:
		return "a build is running in this repo"
	case mainlock.KindGitAction:
		return "another git action is running in this repo"
	}
	return "something else is using this repo"
}

// releaseFinished gives back the locks of items that are done or skipped, and
// forgets notes of batches that are gone. Callers hold s.mu.
func (s *ActionsService) releaseFinished(st corerepos.Actions) {
	over := func(k itemKey) bool {
		b, ok := st.Batches[k.batch]
		if !ok {
			return true
		}
		for _, it := range b.Items {
			if it.Repo != k.repo {
				continue
			}
			switch it.State.(type) {
			case corerepos.Done, corerepos.Skipped:
				return true
			}
			return false
		}
		return true
	}
	for k, rel := range s.locks {
		if over(k) {
			rel()
			delete(s.locks, k)
		}
	}
	for k := range s.notes {
		if _, ok := st.Batches[k.batch]; !ok {
			delete(s.notes, k)
		}
	}
}

// Get returns a copy of a batch. ok is false for an unknown or expired batch.
func (s *ActionsService) Get(id corerepos.BatchID) (BatchView, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.st.Batches[id]
	if !ok {
		return BatchView{}, false
	}
	v := viewOf(b)
	for i, it := range v.Items {
		if n, ok := s.notes[itemKey{id, it.Repo}]; ok && it.State == "skipped" && it.Reason == string(corerepos.SkipBuildRunning) {
			v.Items[i].Message = "Skipped: " + holderReason(n) + ". Wait for it to finish, then try again."
		}
	}
	return v, true
}

// Cancel skips the items that have not started. A running command finishes.
func (s *ActionsService) Cancel(id corerepos.BatchID, actor corerepos.Actor) (BatchView, error) {
	s.mu.Lock()
	_, ok := s.st.Batches[id]
	s.mu.Unlock()
	if !ok {
		return BatchView{}, apperr.New(apperr.NotFound, "That batch is gone. Refresh the page.", fmt.Errorf("batch %s", id))
	}
	if _, err := s.apply(corerepos.CancelBatch{ID: id, Actor: actor, Now: s.d.Clock().UTC()}); err != nil {
		return BatchView{}, err
	}
	v, _ := s.Get(id)
	return v, nil
}

func (s *ActionsService) repo(ctx context.Context, id core.RepoID) (core.Repo, error) {
	r, err := s.d.Store.GetRepo(ctx, id)
	if errors.Is(err, db.ErrNotFound) {
		return core.Repo{}, apperr.New(apperr.NotFound, "A repo isn't tracked (any more). Refresh the page.", err)
	}
	if err != nil {
		return core.Repo{}, fmt.Errorf("find repo %s: %w", id, err)
	}
	return r, nil
}

func (s *ActionsService) onBuildStarted(ev builds.BuildStarted) {
	// Only main-checkout builds matter, and BuildStarted lists the repos of any
	// build: skipping a pull for a worktree build would be wrong, so the
	// builds service reports main-checkout starts only through BusyMainCheckouts.
	// Ask it which of these repos are busy in their main checkout.
	if s.d.Builds == nil || len(ev.Repos) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()
	busy, err := s.d.Builds.BusyMainCheckouts(ctx, ev.Repos)
	if err != nil {
		s.d.Log.Warn("check builds for git actions failed", "err", err)
		return
	}
	var repos []core.RepoID
	for _, r := range ev.Repos {
		if _, ok := busy[r]; ok {
			repos = append(repos, r)
		}
	}
	if len(repos) == 0 {
		return
	}
	if _, err := s.apply(corerepos.BuildStarted{Repos: repos, Now: s.d.Clock().UTC()}); err != nil {
		s.d.Log.Warn("apply build start to git actions failed", "err", err)
	}
}

// apply runs one step. The start audit entry is committed first; when that
// fails nothing is started and the state is unchanged.
func (s *ActionsService) apply(in corerepos.ActInput) (BatchView, error) {
	s.mu.Lock()
	// While shutting down only new batches are refused. Results of commands that
	// already ran are still applied so their audit entries are written (invariant 8).
	if _, isStart := in.(corerepos.StartBatch); s.stopped && isStart {
		s.mu.Unlock()
		return BatchView{}, apperr.New(apperr.Unavailable, "Yggdrasil is shutting down.", errors.New("git actions stopped"))
	}
	if _, isStart := in.(corerepos.StartBatch); isStart && s.runCtx == nil {
		s.mu.Unlock()
		return BatchView{}, apperr.New(apperr.Unavailable, "Yggdrasil is still starting. Try again in a moment.", errors.New("git actions not running"))
	}
	next, cmds := s.step(in)
	for _, c := range cmds {
		if a, ok := c.(corerepos.AuditBatchStart); ok {
			if err := s.writeAudit(s.auditContext(), batchStartEntry(a), a.At); err != nil {
				s.mu.Unlock()
				return BatchView{}, apperr.New(apperr.Unavailable, "Yggdrasil couldn't write the audit log, so nothing was started. Try again.", err)
			}
		}
	}
	s.st = next
	s.releaseFinished(next)
	var after []func()
	for _, c := range cmds {
		switch c := c.(type) {
		case corerepos.CheckStatus:
			s.spawn(func(ctx context.Context) { s.check(ctx, c) })
		case corerepos.RunGit:
			s.spawn(func(ctx context.Context) { s.run(ctx, c) })
		case corerepos.AuditRepoDone:
			after = append(after, func() { s.auditDone(c) })
		case corerepos.RefreshRepoStatus:
			after = append(after, func() { s.refresh(c.Repo) })
		case corerepos.PublishBatch:
			if b, ok := next.Batches[c.Batch]; ok && s.d.Publisher != nil {
				ids := make([]core.RepoID, 0, len(b.Items))
				for _, it := range b.Items {
					ids = append(ids, it.Repo)
				}
				fin := b.Finished()
				after = append(after, func() { s.d.Publisher.PublishGitBatch(c.Batch, ids, fin) })
			}
		}
	}
	s.mu.Unlock()
	for _, f := range after {
		f()
	}
	return BatchView{}, nil
}

// step is the pure decision, wrapped so a broken invariant cannot take the
// server down: the state stays as it was.
func (s *ActionsService) step(in corerepos.ActInput) (next corerepos.Actions, cmds []corerepos.ActCommand) {
	defer func() {
		if r := recover(); r != nil {
			s.d.Log.Error("git actions core panicked", "panic", fmt.Sprint(r))
			next, cmds = s.st, nil
		}
	}()
	return corerepos.Act(s.st, in)
}

// spawn starts a worker owned by the service. Callers hold s.mu.
func (s *ActionsService) spawn(f func(ctx context.Context)) {
	ctx := s.runCtx
	if ctx == nil || s.stopped {
		return // no new commands once shutdown began; Run is already waiting on the group
	}
	s.group.Go(func() error { f(ctx); return nil })
}

// feed hands a result to the state.
func (s *ActionsService) feed(in corerepos.ActInput) {
	if _, err := s.apply(in); err != nil {
		s.d.Log.Warn("apply git result failed", "err", err)
	}
}

func (s *ActionsService) refresh(id core.RepoID) {
	if s.d.Status == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()
	if err := s.d.Status.Refresh(ctx, []core.RepoID{id}, corerepos.ReasonGitAction); err != nil {
		s.d.Log.Warn("refresh after git action failed", "err", err)
	}
}

// check reads the status and the branch list before a switch.
func (s *ActionsService) check(ctx context.Context, c corerepos.CheckStatus) {
	now := func() time.Time { return s.d.Clock().UTC() }
	r, err := s.repo(ctx, c.Repo)
	if err != nil {
		s.feed(corerepos.RepoGone{Batch: c.Batch, Repo: c.Repo, Attempt: c.Attempt, Now: now()})
		return
	}
	cctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	st, err := s.d.Git.Status(cctx, r.Path)
	var local, remote bool
	if err == nil {
		local, remote, err = s.d.Git.BranchExists(cctx, r.Path, c.Branch)
	}
	if err != nil {
		s.feed(corerepos.ActionDone{Batch: c.Batch, Repo: c.Repo, Attempt: c.Attempt, Outcome: failureOf(cctx, err), Now: now()})
		return
	}
	s.feed(corerepos.StatusChecked{Batch: c.Batch, Repo: c.Repo, Attempt: c.Attempt,
		Dirty: st.Dirty(), HasBranch: local || remote, Now: now()})
}

// run executes one fetch, pull or checkout and reports how it ended.
func (s *ActionsService) run(ctx context.Context, c corerepos.RunGit) {
	now := func() time.Time { return s.d.Clock().UTC() }
	r, err := s.repo(ctx, c.Repo)
	if err != nil {
		s.feed(corerepos.RepoGone{Batch: c.Batch, Repo: c.Repo, Attempt: c.Attempt, Now: now()})
		return
	}
	out := RunGitOp(ctx, s.d.Git, c.Op, r.Path, c.Branch)
	s.feed(corerepos.ActionDone{Batch: c.Batch, Repo: c.Repo, Attempt: c.Attempt, Outcome: out, Now: now()})
}

// RunGitOp runs op in dir through g and maps the result to an outcome. It is
// total: every error becomes an outcome. It never runs anything but fetch,
// pull --ff-only and checkout (invariant 1).
func RunGitOp(ctx context.Context, g GitRunner, op corerepos.GitOp, dir, branch string) corerepos.ActOutcome {
	switch op {
	case corerepos.OpFetch:
		cctx, cancel := context.WithTimeout(ctx, fetchTimeout)
		defer cancel()
		if err := g.Fetch(cctx, dir, ""); err != nil {
			return failureOf(cctx, err)
		}
		return corerepos.Fetched{}
	case corerepos.OpPull:
		cctx, cancel := context.WithTimeout(ctx, fetchTimeout)
		defer cancel()
		before, err := g.Status(cctx, dir)
		if err != nil {
			return failureOf(cctx, err)
		}
		if err := g.PullFFOnly(cctx, dir); err != nil {
			var ff *git.FFError
			if errors.As(err, &ff) {
				return corerepos.PullRefused{Reason: pullReason(ff.Reason), Message: ff.UserMessage()}
			}
			return failureOf(cctx, err)
		}
		after, err := g.Status(cctx, dir)
		if err != nil || after.OID == before.OID {
			if err == nil {
				return corerepos.UpToDate{}
			}
			return corerepos.Pulled{From: shortOID(before.OID)}
		}
		return corerepos.Pulled{From: shortOID(before.OID), To: shortOID(after.OID)}
	case corerepos.OpCheckout:
		cctx, cancel := context.WithTimeout(ctx, checkoutTimeout)
		defer cancel()
		local, remote, err := g.BranchExists(cctx, dir, branch)
		if err != nil {
			return failureOf(cctx, err)
		}
		if err := g.Checkout(cctx, dir, branch); err != nil {
			return checkoutFailure(cctx, err)
		}
		return corerepos.CheckedOut{Branch: branch, Created: !local && remote}
	}
	return corerepos.Failed{Kind: corerepos.FailGit, Message: "Yggdrasil doesn't know that git action."}
}

func shortOID(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func pullReason(r git.FFReason) corerepos.PullReason {
	switch r {
	case git.FFDetached:
		return corerepos.PullDetached
	case git.FFNoUpstream:
		return corerepos.PullNoUpstream
	case git.FFDiverged:
		return corerepos.PullDiverged
	case git.FFDirty:
		return corerepos.PullDirty
	}
	return corerepos.PullOther
}

func checkoutFailure(ctx context.Context, err error) corerepos.ActOutcome {
	var ge *git.Error
	switch {
	case errors.Is(err, git.ErrDirty):
		return corerepos.CheckoutRefused{Reason: corerepos.CheckoutDirtyConflict, Message: git.UserMessage(err)}
	case errors.As(err, &ge) && ge.Code != 0:
		low := strings.ToLower(ge.Stderr)
		if strings.Contains(low, "would be overwritten") || strings.Contains(low, "local changes") {
			return corerepos.CheckoutRefused{Reason: corerepos.CheckoutDirtyConflict,
				Message: "Your uncommitted changes would be overwritten by that branch. Commit or stash them, then try again."}
		}
		return corerepos.CheckoutRefused{Reason: corerepos.CheckoutOther, Message: ge.UserMessage()}
	}
	return failureOf(ctx, err)
}

// failureOf classifies a failed git call.
func failureOf(ctx context.Context, err error) corerepos.Failed {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return corerepos.Failed{Kind: corerepos.FailTimeout,
			Message: "Git took too long and was stopped. Try again; if it keeps happening, check the remote."}
	}
	var ge *git.Error
	if errors.As(err, &ge) {
		low := strings.ToLower(ge.Stderr)
		for _, m := range []string{"authentication failed", "terminal prompts disabled", "permission denied",
			"could not read username", "could not read password", "returned error: 401", "returned error: 403"} {
			if strings.Contains(low, m) {
				return corerepos.Failed{Kind: corerepos.FailAuth,
					Message: "Git needs credentials for this remote. Fix your git sign-in in a terminal, then retry. Yggdrasil never asks for or stores git credentials."}
			}
		}
		for _, m := range []string{"could not resolve host", "could not read from remote", "unable to access",
			"network is unreachable", "connection refused", "connection timed out", "connection reset"} {
			if strings.Contains(low, m) {
				return corerepos.Failed{Kind: corerepos.FailNetwork,
					Message: "Git could not reach the remote. Check your network or VPN, then retry."}
			}
		}
		return corerepos.Failed{Kind: corerepos.FailGit, Message: ge.UserMessage()}
	}
	return corerepos.Failed{Kind: corerepos.FailGit, Message: "Git could not finish. Try again."}
}

// Audit.

func batchStartEntry(a corerepos.AuditBatchStart) core.AuditEntry {
	ids := make([]string, 0, len(a.Repos))
	for _, r := range a.Repos {
		ids = append(ids, string(r))
	}
	d, _ := json.Marshal(map[string]string{
		"batch": string(a.Batch), "kind": string(a.Kind), "branch": a.Branch, "repos": strings.Join(ids, ","),
	})
	return core.AuditEntry{Actor: string(a.Actor), Action: "git." + string(a.Kind) + ".start",
		Subject: "batch:" + string(a.Batch), Detail: d}
}

func repoDoneEntry(a corerepos.AuditRepoDone) core.AuditEntry {
	var iv ItemView
	describe(&iv, a.Outcome)
	m := map[string]string{"batch": string(a.Batch), "kind": string(a.Kind), "outcome": iv.Outcome}
	for k, v := range map[string]string{"branch": a.Branch, "reason": iv.Reason, "from": iv.From, "to": iv.To} {
		if v != "" {
			m[k] = v
		}
	}
	if iv.Created {
		m["created"] = "true"
	}
	d, _ := json.Marshal(m)
	return core.AuditEntry{Actor: string(a.Actor), Action: "git." + string(a.Kind),
		Subject: "repo:" + string(a.Repo), Detail: d}
}

func (s *ActionsService) auditContext() context.Context { return context.WithoutCancel(s.runCtx) }

func (s *ActionsService) writeAudit(ctx context.Context, e core.AuditEntry, at time.Time) error {
	e, err := audit.Prepare(e, s.d.Redactor, core.AuditID(s.d.IDs("aud")), at)
	if err != nil {
		return err
	}
	return s.d.Store.Commit(ctx, func(tx *db.Tx) error { return tx.AppendAudit(ctx, e) })
}

// auditDone commits the entry of a finished command. A failure is logged
// loudly and the batch goes on: the git change already happened.
func (s *ActionsService) auditDone(a corerepos.AuditRepoDone) {
	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()
	if err := s.writeAudit(ctx, repoDoneEntry(a), a.At); err != nil {
		s.d.Log.Error("AUDIT ENTRY LOST: git action finished but its audit entry could not be written",
			"batch", a.Batch, "repo", a.Repo, "err", err)
	}
}
