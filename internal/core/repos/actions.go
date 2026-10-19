package repos

import (
	"fmt"
	"sort"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// This file is the pure core of the git actions (WS-06, WS-07). The design is
// docs/design/git-actions.md. A user starts a batch (even one repo is a batch
// of one); Act decides which items may start and returns commands as data.
// The shell (internal/repos) performs them. The core never runs git.

// Defaults of the git actions.
const (
	// DefaultActionsMax is the number of items that may run at the same time,
	// across all batches.
	DefaultActionsMax = 4
	// BatchKeep is how long a finished batch stays in memory for the UI.
	BatchKeep = 10 * time.Minute
	// MaxBatchRepos is the most repos one batch holds.
	MaxBatchRepos = 500
)

// BatchID names a batch. The shell generates it.
type BatchID string

// ActionKind is what a batch does.
type ActionKind string

// The closed set of action kinds.
const (
	KindFetch    ActionKind = "fetch"
	KindPull     ActionKind = "pull"
	KindCheckout ActionKind = "checkout"
	KindSwitch   ActionKind = "switch"
)

// Validate reports whether k is a known kind.
func (k ActionKind) Validate() error {
	switch k {
	case KindFetch, KindPull, KindCheckout, KindSwitch:
		return nil
	}
	return fmt.Errorf("unknown git action %q", string(k))
}

// NeedsBranch reports whether the kind carries a branch name.
func (k ActionKind) NeedsBranch() bool { return k == KindCheckout || k == KindSwitch }

// ChangesCheckout reports whether the kind changes the files or the HEAD of
// the main checkout, so it must not run beside a build.
func (k ActionKind) ChangesCheckout() bool { return k != KindFetch }

// GitOp is the one git command a RunGit runs. It is a closed set: nothing
// destructive exists in it (invariant 1).
type GitOp string

// The closed set of git operations of this feature.
const (
	OpFetch    GitOp = "fetch"
	OpPull     GitOp = "pull"     // always --ff-only
	OpCheckout GitOp = "checkout" // never forced
)

func (k ActionKind) op() GitOp {
	switch k {
	case KindFetch:
		return OpFetch
	case KindPull:
		return OpPull
	}
	return OpCheckout
}

// Actions is the state of the git actions: the live batches and a per-repo
// lock.
type Actions struct {
	Batches map[BatchID]Batch
	// Running maps a repo to the batch whose item is Checking or Running.
	// At most one git action runs per repo.
	Running map[core.RepoID]BatchID
	// Max is the number of items that may be Checking or Running.
	Max int
}

// Batch is one user request on one or more repos.
type Batch struct {
	ID      BatchID
	Kind    ActionKind
	Branch  string // "" for fetch and pull
	Actor   Actor
	Started time.Time
	Items   []Item // in the order the user gave them
}

// Item is one repo of a batch.
type Item struct {
	Repo core.RepoID
	// Attempt is the number of the last start (0 before it).
	Attempt int
	State   ItemState
}

// ItemState is the state of an item. It is a closed set.
type ItemState interface{ isItemState() }

// Pending waits for its turn: the repo lock or a free slot.
type Pending struct{}

// Checking reads fresh status before a branch switch.
type Checking struct{ Attempt int }

// Running means the git command runs.
type Running struct{ Attempt int }

// Done means the command finished, whether it worked or git refused.
type Done struct {
	Outcome ActOutcome
	At      time.Time
}

// Skipped means nothing ran for this repo.
type Skipped struct {
	Reason SkipReason
	At     time.Time
}

func (Pending) isItemState()  {}
func (Checking) isItemState() {}
func (Running) isItemState()  {}
func (Done) isItemState()     {}
func (Skipped) isItemState()  {}

// SkipReason says why an item did not run.
type SkipReason string

// The closed set of skip reasons.
const (
	SkipDirty        SkipReason = "dirty"
	SkipNoSuchBranch SkipReason = "no_such_branch"
	SkipBuildRunning SkipReason = "build_running"
	SkipUntracked    SkipReason = "untracked"
	SkipCancelled    SkipReason = "cancelled"
)

// ActOutcome is how a command ended. It is a closed set.
type ActOutcome interface{ isActOutcome() }

// Fetched: fetch finished.
type Fetched struct{}

// Pulled: the branch moved from From to To (commit IDs).
type Pulled struct{ From, To string }

// UpToDate: a pull found nothing new.
type UpToDate struct{}

// PullReason says why a pull could not fast-forward.
type PullReason string

// The closed set of pull refusals.
const (
	PullDetached   PullReason = "detached"
	PullNoUpstream PullReason = "no_upstream"
	PullDiverged   PullReason = "diverged"
	PullDirty      PullReason = "dirty"
	PullOther      PullReason = "other"
)

// PullRefused: the repo is unchanged and the user must act. Message says what
// to do next.
type PullRefused struct {
	Reason  PullReason
	Message string
}

// CheckedOut: HEAD is on Branch now. Created: a local branch was made from the
// remote one (git's default for a remote-only name).
type CheckedOut struct {
	Branch  string
	Created bool
}

// CheckoutReason says why a checkout was refused.
type CheckoutReason string

// The closed set of checkout refusals.
const (
	CheckoutDirtyConflict CheckoutReason = "dirty_conflict"
	CheckoutOther         CheckoutReason = "other"
)

// CheckoutRefused: HEAD did not move.
type CheckoutRefused struct {
	Reason  CheckoutReason
	Message string
}

// FailKind classifies a command that failed.
type FailKind string

// The closed set of failure kinds.
const (
	FailNetwork FailKind = "network"
	FailAuth    FailKind = "auth"
	FailTimeout FailKind = "timeout"
	FailGit     FailKind = "git"
)

// Failed: the command did not finish. Message says what to do next.
type Failed struct {
	Kind    FailKind
	Message string
}

func (Fetched) isActOutcome()         {}
func (Pulled) isActOutcome()          {}
func (UpToDate) isActOutcome()        {}
func (PullRefused) isActOutcome()     {}
func (CheckedOut) isActOutcome()      {}
func (CheckoutRefused) isActOutcome() {}
func (Failed) isActOutcome()          {}

// ActInput is one event for Act. It is a closed set.
type ActInput interface{ isActInput() }

// StartBatch is the user's request. The shell validates it first (Validate)
// and fills Busy from the builds service.
type StartBatch struct {
	ID     BatchID
	Kind   ActionKind
	Branch string
	Repos  []core.RepoID
	// Busy holds the repos with a build in their main checkout right now,
	// with a plain-language reason.
	Busy  map[core.RepoID]string
	Actor Actor
	Now   time.Time
}

// Validate reports why a StartBatch would be ignored by Act.
func (s StartBatch) Validate() error {
	if err := s.Kind.Validate(); err != nil {
		return err
	}
	switch {
	case s.ID == "":
		return fmt.Errorf("a batch needs an ID")
	case len(s.Repos) < 1:
		return fmt.Errorf("a batch needs at least one repo")
	case len(s.Repos) > MaxBatchRepos:
		return fmt.Errorf("a batch holds at most %d repos", MaxBatchRepos)
	case s.Kind.NeedsBranch() != (s.Branch != ""):
		return fmt.Errorf("a branch is needed for checkout and switch only")
	case s.Kind == KindCheckout && len(s.Repos) != 1:
		return fmt.Errorf("a checkout takes exactly one repo")
	}
	seen := map[core.RepoID]bool{}
	for _, r := range s.Repos {
		if seen[r] {
			return fmt.Errorf("repo %s appears twice", r)
		}
		seen[r] = true
	}
	return nil
}

// CancelBatch skips every item that has not started. A command that runs is
// never killed half way.
type CancelBatch struct {
	ID    BatchID
	Actor Actor
	Now   time.Time
}

// StatusChecked answers CheckStatus (switch only).
type StatusChecked struct {
	Batch     BatchID
	Repo      core.RepoID
	Attempt   int
	Dirty     bool
	HasBranch bool
	Now       time.Time
}

// RepoGone answers a CheckStatus or RunGit that could not start because the
// repo is not tracked any more. Nothing ran.
type RepoGone struct {
	Batch   BatchID
	Repo    core.RepoID
	Attempt int
	Now     time.Time
}

// ActionDone answers RunGit (and a CheckStatus that failed).
type ActionDone struct {
	Batch   BatchID
	Repo    core.RepoID
	Attempt int
	Outcome ActOutcome
	Now     time.Time
}

// BuildStarted says builds started in the main checkout of these repos.
type BuildStarted struct {
	Repos []core.RepoID
	Now   time.Time
}

// ActSweep drops finished batches kept longer than BatchKeep.
type ActSweep struct{ Now time.Time }

func (StartBatch) isActInput()    {}
func (CancelBatch) isActInput()   {}
func (StatusChecked) isActInput() {}
func (RepoGone) isActInput()      {}
func (ActionDone) isActInput()    {}
func (BuildStarted) isActInput()  {}
func (ActSweep) isActInput()      {}

// ActCommand is an effect Act asks the shell for. It is a closed set. The
// only git it can name is fetch, pull --ff-only and checkout (invariant 1).
type ActCommand interface{ isActCommand() }

// CheckStatus reads the status and branch list of a repo. Read-only.
type CheckStatus struct {
	Batch   BatchID
	Repo    core.RepoID
	Attempt int
	Branch  string
}

// RunGit runs one git command. It is never retried by the shell.
type RunGit struct {
	Batch   BatchID
	Repo    core.RepoID
	Attempt int
	Op      GitOp
	Branch  string // checkout only
}

// AuditBatchStart is committed before any git command of the batch runs.
type AuditBatchStart struct {
	Batch  BatchID
	Kind   ActionKind
	Branch string
	Actor  Actor
	Repos  []core.RepoID
	At     time.Time
}

// AuditRepoDone is committed after a command ended.
type AuditRepoDone struct {
	Batch   BatchID
	Kind    ActionKind
	Branch  string
	Actor   Actor
	Repo    core.RepoID
	Outcome ActOutcome
	At      time.Time
}

// RefreshRepoStatus asks repo-status to read the repo again.
type RefreshRepoStatus struct{ Repo core.RepoID }

// PublishBatch tells subscribers that a batch changed.
type PublishBatch struct{ Batch BatchID }

func (CheckStatus) isActCommand()       {}
func (RunGit) isActCommand()            {}
func (AuditBatchStart) isActCommand()   {}
func (AuditRepoDone) isActCommand()     {}
func (RefreshRepoStatus) isActCommand() {}
func (PublishBatch) isActCommand()      {}

// NewActions returns the empty state.
func NewActions(max int) Actions {
	if max < 1 {
		max = DefaultActionsMax
	}
	return Actions{Batches: map[BatchID]Batch{}, Running: map[core.RepoID]BatchID{}, Max: max}
}

// Finished reports whether every item is Done or Skipped.
func (b Batch) Finished() bool {
	for _, it := range b.Items {
		switch it.State.(type) {
		case Done, Skipped:
		default:
			return false
		}
	}
	return true
}

// LastAt is the time of the last item to end, or Started.
func (b Batch) LastAt() time.Time {
	at := b.Started
	for _, it := range b.Items {
		var t time.Time
		switch s := it.State.(type) {
		case Done:
			t = s.At
		case Skipped:
			t = s.At
		}
		if t.After(at) {
			at = t
		}
	}
	return at
}

// clone copies the maps and item slices so Act never changes its input.
func (a Actions) clone() Actions {
	n := Actions{Max: a.Max, Batches: make(map[BatchID]Batch, len(a.Batches)), Running: make(map[core.RepoID]BatchID, len(a.Running))}
	if n.Max < 1 {
		n.Max = DefaultActionsMax
	}
	for id, b := range a.Batches {
		b.Items = append([]Item(nil), b.Items...)
		n.Batches[id] = b
	}
	for r, id := range a.Running {
		n.Running[r] = id
	}
	return n
}

// walker collects the commands and the batches that changed.
type walker struct {
	a       Actions
	cmds    []ActCommand
	changed map[BatchID]struct{}
}

func (w *walker) touch(id BatchID) { w.changed[id] = struct{}{} }

// Act is the step function: (state, input) -> (state, commands). An input
// that does not fit the state changes nothing.
func Act(in Actions, input ActInput) (Actions, []ActCommand) {
	w := &walker{a: in.clone(), changed: map[BatchID]struct{}{}}
	switch i := input.(type) {
	case StartBatch:
		w.start(i)
	case CancelBatch:
		w.cancel(i)
	case StatusChecked:
		w.statusChecked(i)
	case RepoGone:
		w.repoGone(i)
	case ActionDone:
		w.done(i)
	case BuildStarted:
		w.buildStarted(i)
	case ActSweep:
		w.sweep(i)
		return w.a, nil
	default:
		return in, nil
	}
	if len(w.changed) == 0 {
		return in, nil
	}
	w.startReady()
	ids := make([]string, 0, len(w.changed))
	for id := range w.changed {
		if _, ok := w.a.Batches[id]; ok {
			ids = append(ids, string(id))
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		w.cmds = append(w.cmds, PublishBatch{Batch: BatchID(id)})
	}
	return w.a, w.cmds
}

// RunActions applies the inputs in order and collects every command.
func RunActions(a Actions, inputs []ActInput) (Actions, []ActCommand) {
	var all []ActCommand
	for _, in := range inputs {
		var cmds []ActCommand
		a, cmds = Act(a, in)
		all = append(all, cmds...)
	}
	return a, all
}

func (w *walker) setItem(b BatchID, idx int, st ItemState, attempt int) {
	bt := w.a.Batches[b]
	bt.Items[idx].State = st
	if attempt > 0 {
		bt.Items[idx].Attempt = attempt
	}
	w.a.Batches[b] = bt
	w.touch(b)
}

func (w *walker) find(b BatchID, repo core.RepoID) (Batch, int, bool) {
	bt, ok := w.a.Batches[b]
	if !ok {
		return Batch{}, 0, false
	}
	for i, it := range bt.Items {
		if it.Repo == repo {
			return bt, i, true
		}
	}
	return Batch{}, 0, false
}

func (w *walker) release(repo core.RepoID, b BatchID) {
	if w.a.Running[repo] == b {
		delete(w.a.Running, repo)
	}
}

func (w *walker) start(in StartBatch) {
	if in.Validate() != nil {
		return
	}
	if _, dup := w.a.Batches[in.ID]; dup {
		return
	}
	b := Batch{ID: in.ID, Kind: in.Kind, Branch: in.Branch, Actor: in.Actor, Started: in.Now,
		Items: make([]Item, 0, len(in.Repos))}
	for _, r := range in.Repos {
		var st ItemState = Pending{}
		if _, busy := in.Busy[r]; busy && in.Kind.ChangesCheckout() {
			st = Skipped{Reason: SkipBuildRunning, At: in.Now}
		}
		b.Items = append(b.Items, Item{Repo: r, State: st})
	}
	w.a.Batches[in.ID] = b
	w.cmds = append(w.cmds, AuditBatchStart{Batch: in.ID, Kind: in.Kind, Branch: in.Branch,
		Actor: in.Actor, Repos: append([]core.RepoID(nil), in.Repos...), At: in.Now})
	w.touch(in.ID)
}

func (w *walker) cancel(in CancelBatch) {
	b, ok := w.a.Batches[in.ID]
	if !ok {
		return
	}
	for i, it := range b.Items {
		if _, p := it.State.(Pending); p {
			w.setItem(in.ID, i, Skipped{Reason: SkipCancelled, At: in.Now}, 0)
		}
	}
}

func (w *walker) buildStarted(in BuildStarted) {
	for _, repo := range in.Repos {
		for _, id := range w.batchOrder() {
			b := w.a.Batches[id]
			if !b.Kind.ChangesCheckout() {
				continue
			}
			for i, it := range b.Items {
				if _, p := it.State.(Pending); p && it.Repo == repo {
					w.setItem(id, i, Skipped{Reason: SkipBuildRunning, At: in.Now}, 0)
				}
			}
		}
	}
}

func (w *walker) statusChecked(in StatusChecked) {
	b, i, ok := w.find(in.Batch, in.Repo)
	if !ok {
		return
	}
	c, isChecking := b.Items[i].State.(Checking)
	if !isChecking || c.Attempt != in.Attempt {
		return
	}
	switch {
	case in.Dirty:
		w.release(in.Repo, in.Batch)
		w.setItem(in.Batch, i, Skipped{Reason: SkipDirty, At: in.Now}, 0)
	case !in.HasBranch:
		w.release(in.Repo, in.Batch)
		w.setItem(in.Batch, i, Skipped{Reason: SkipNoSuchBranch, At: in.Now}, 0)
	default:
		w.setItem(in.Batch, i, Running(c), 0)
		w.cmds = append(w.cmds, RunGit{Batch: in.Batch, Repo: in.Repo, Attempt: c.Attempt, Op: OpCheckout, Branch: b.Branch})
	}
}

func (w *walker) repoGone(in RepoGone) {
	b, i, ok := w.find(in.Batch, in.Repo)
	if !ok {
		return
	}
	switch s := b.Items[i].State.(type) {
	case Checking:
		if s.Attempt != in.Attempt {
			return
		}
	case Running:
		if s.Attempt != in.Attempt {
			return
		}
	default:
		return
	}
	w.release(in.Repo, in.Batch)
	w.setItem(in.Batch, i, Skipped{Reason: SkipUntracked, At: in.Now}, 0)
}

func (w *walker) done(in ActionDone) {
	b, i, ok := w.find(in.Batch, in.Repo)
	if !ok || in.Outcome == nil {
		return
	}
	r, isRunning := b.Items[i].State.(Running)
	if isRunning && r.Attempt != in.Attempt {
		return
	}
	c, isChecking := b.Items[i].State.(Checking)
	if isChecking && c.Attempt != in.Attempt {
		return
	}
	if !isRunning && !isChecking {
		return
	}
	w.release(in.Repo, in.Batch)
	w.setItem(in.Batch, i, Done{Outcome: in.Outcome, At: in.Now}, 0)
	w.cmds = append(w.cmds,
		AuditRepoDone{Batch: in.Batch, Kind: b.Kind, Branch: b.Branch, Actor: b.Actor, Repo: in.Repo, Outcome: in.Outcome, At: in.Now},
		RefreshRepoStatus{Repo: in.Repo})
}

func (w *walker) sweep(in ActSweep) {
	for id, b := range w.a.Batches {
		if b.Finished() && !in.Now.Before(b.LastAt().Add(BatchKeep)) {
			delete(w.a.Batches, id)
		}
	}
}

// batchOrder lists batch IDs by start time, then ID.
func (w *walker) batchOrder() []BatchID {
	ids := make([]BatchID, 0, len(w.a.Batches))
	for id := range w.a.Batches {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		bi, bj := w.a.Batches[ids[i]], w.a.Batches[ids[j]]
		if !bi.Started.Equal(bj.Started) {
			return bi.Started.Before(bj.Started)
		}
		return ids[i] < ids[j]
	})
	return ids
}

// startReady starts every Pending item whose repo is free while slots remain,
// in batch then item order.
func (w *walker) startReady() {
	for _, id := range w.batchOrder() {
		b := w.a.Batches[id]
		for i, it := range b.Items {
			if len(w.a.Running) >= w.a.Max {
				return
			}
			if _, p := it.State.(Pending); !p {
				continue
			}
			if _, locked := w.a.Running[it.Repo]; locked {
				continue
			}
			w.a.Running[it.Repo] = id
			attempt := it.Attempt + 1
			if b.Kind == KindSwitch {
				w.setItem(id, i, Checking{Attempt: attempt}, attempt)
				w.cmds = append(w.cmds, CheckStatus{Batch: id, Repo: it.Repo, Attempt: attempt, Branch: b.Branch})
				continue
			}
			w.setItem(id, i, Running{Attempt: attempt}, attempt)
			w.cmds = append(w.cmds, RunGit{Batch: id, Repo: it.Repo, Attempt: attempt, Op: b.Kind.op(), Branch: b.Branch})
		}
	}
}
