package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/changesets"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	cs "github.com/brendatamaar/yggdrasil/internal/core/changesets"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/proposals"
	"github.com/brendatamaar/yggdrasil/internal/safety"
	"github.com/brendatamaar/yggdrasil/internal/verify"
)

// Wiring of the Phase 4 services that the lead owns: the live probe and the
// change-set links of the proposals service, the fan-out of proposal changes
// to the change-set service, and the repo namer of the API adapters.

// liveProbe implements proposals.LiveProbe: "something is still running in
// this worktree" for the merge and removal executors. A probe it cannot answer
// is an error, never "not live". The verify service is bound after the
// proposals service exists (verify needs the task service, which needs
// proposals).
type liveProbe struct {
	store  *db.Store
	builds *builds.Service
	verify atomic.Pointer[verify.Service]
}

var _ proposals.LiveProbe = (*liveProbe)(nil)

// TaskLive: the task has an agent that is queued, running or waiting for the
// user (its process is alive).
func (p *liveProbe) TaskLive(ctx context.Context, task core.TaskID) (bool, error) {
	t, err := p.store.GetTask(ctx, task)
	if err != nil {
		return false, fmt.Errorf("read task %s: %w", task, err)
	}
	switch t.Status {
	case core.TaskQueued, core.TaskRunning, core.TaskNeedsYou:
		return true, nil
	}
	return false, nil
}

// BuildLive: a build is queued or running in the worktree, or a verification
// of it is bootstrapping or verifying.
func (p *liveProbe) BuildLive(ctx context.Context, wt core.WorktreeID) (bool, error) {
	for _, status := range []core.BuildStatus{core.BuildQueued, core.BuildRunning} {
		list, err := p.builds.List(ctx, db.BuildFilter{Status: status, Limit: 1000})
		if err != nil {
			return false, fmt.Errorf("list %s builds: %w", status, err)
		}
		for _, st := range list {
			if buildTargets(st, wt) {
				return true, nil
			}
		}
	}
	v := p.verify.Load()
	if v == nil {
		return false, errors.New("verification is not running yet")
	}
	w, err := p.store.GetWorktree(ctx, wt)
	if err != nil {
		return false, fmt.Errorf("read worktree %s: %w", wt, err)
	}
	recs, err := v.List(ctx, w.TaskID)
	if err != nil {
		return false, fmt.Errorf("list verifications of task %s: %w", w.TaskID, err)
	}
	for _, r := range recs {
		if r.Worktree == wt && verify.IsLiveCode(verify.StateCode(r.State)) {
			return true, nil
		}
	}
	return false, nil
}

func buildTargets(st corebuilds.State, wt core.WorktreeID) bool {
	var spec corebuilds.Spec
	switch s := st.(type) {
	case corebuilds.Queued:
		spec = s.Spec
	case corebuilds.Running:
		spec = s.Spec
	default:
		return false
	}
	in, ok := spec.Target.(corebuilds.InWorktree)
	return ok && in.WorktreeID == wt
}

// changeSetLinks implements proposals.ChangeSetLinks over the change-set
// store (RV-03): the merge order, the MR branch of each repo that has a
// merge_request proposal, and the MR URL of each executed one.
type changeSetLinks struct {
	store *db.Store
	red   safety.Redactor
}

var _ proposals.ChangeSetLinks = changeSetLinks{}

func (l changeSetLinks) MRLinks(ctx context.Context, set core.ChangeSetID) (proposals.SetLinks, error) {
	cstore := changesets.NewSQLiteStore(l.store, l.red)
	row, err := cstore.Set(ctx, set)
	if err != nil {
		return proposals.SetLinks{}, fmt.Errorf("read change set %s: %w", set, err)
	}
	members, err := cstore.Members(ctx, set)
	if err != nil {
		return proposals.SetLinks{}, fmt.Errorf("read the members of %s: %w", set, err)
	}
	repos, err := cstore.TaskRepos(ctx, row.Task)
	if err != nil {
		return proposals.SetLinks{}, fmt.Errorf("read the repos of task %s: %w", row.Task, err)
	}
	st, err := changesets.StateFromRows(row, repos, members)
	if err != nil {
		return proposals.SetLinks{}, err
	}
	out := proposals.SetLinks{Order: slices.Clone(orderOf(st)), Branches: map[core.RepoID]string{}, URLs: map[core.RepoID]string{}}
	for _, m := range members {
		if m.Kind != core.ProposalMergeRequest {
			continue
		}
		rec, err := l.store.GetProposalRecord(ctx, m.ID)
		if err != nil {
			return proposals.SetLinks{}, fmt.Errorf("read proposal %s: %w", m.ID, err)
		}
		if mr, ok := rec.Proposal.Action.(coreprop.MergeRequest); ok {
			out.Branches[m.Repo] = mr.Branch
		}
		if m.Status == core.ProposalExecuted && m.MRURL != "" {
			out.URLs[m.Repo] = m.MRURL
		}
	}
	return out, nil
}

func orderOf(st cs.State) []core.RepoID {
	switch s := st.(type) {
	case cs.Open:
		return s.Set.Order
	case cs.Confirming:
		return s.Set.Order
	case cs.Shipped:
		return s.Set.Order
	case cs.Closed:
		return s.Set.Order
	}
	return nil
}

// proposalFanout is the proposals publisher: it passes every event on to the
// UI hub and queues the proposal for the change-set service (a new proposal
// reopens a settled set). Publish never blocks; one goroutine in the app
// errgroup (run) delivers the queued IDs, each at most once while waiting.
type proposalFanout struct {
	next proposals.Publisher
	svc  atomic.Pointer[changesets.Service]
	log  func(msg string, args ...any)

	mu      sync.Mutex
	pending []core.ProposalID
	wake    chan struct{}
}

func newProposalFanout(next proposals.Publisher, log func(msg string, args ...any)) *proposalFanout {
	return &proposalFanout{next: next, log: log, wake: make(chan struct{}, 1)}
}

func (f *proposalFanout) Publish(e proposals.Event) {
	if f.next != nil {
		f.next.Publish(e)
	}
	f.mu.Lock()
	if !slices.Contains(f.pending, e.ID) {
		f.pending = append(f.pending, e.ID)
	}
	f.mu.Unlock()
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

// run delivers queued proposal IDs to the change-set service until ctx is done.
func (f *proposalFanout) run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-f.wake:
		}
		for {
			f.mu.Lock()
			if len(f.pending) == 0 {
				f.mu.Unlock()
				break
			}
			id := f.pending[0]
			f.pending = f.pending[1:]
			f.mu.Unlock()
			svc := f.svc.Load()
			if svc == nil {
				continue
			}
			if err := svc.OnProposalChanged(ctx, id); err != nil && ctx.Err() == nil && f.log != nil {
				f.log("could not update the change set of a proposal", "proposal", id, "err", err)
			}
		}
	}
}

// repoNamer gives the display name of a tracked repo ("" when unknown).
func repoNamer(store *db.Store) api.RepoNamer {
	return func(ctx context.Context, id core.RepoID) string {
		r, err := store.GetRepo(ctx, id)
		if err != nil {
			return ""
		}
		return r.Name
	}
}
