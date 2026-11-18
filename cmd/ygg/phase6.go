package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/actions"
	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/hosts"
	"github.com/brendatamaar/yggdrasil/internal/notify"
	"github.com/brendatamaar/yggdrasil/internal/proposals"
	"github.com/brendatamaar/yggdrasil/internal/repos"
	"github.com/brendatamaar/yggdrasil/internal/safety"
	"github.com/brendatamaar/yggdrasil/internal/standup"
	"github.com/brendatamaar/yggdrasil/internal/tasks"
)

// Phase 6 composition (workspace actions, host context, drafts, noise,
// notifications, standup). This file builds the services and the adapters of
// phase6_*.go and binds them to the server; app.go calls newPhase6 once the
// server exists. New repos (phase6_newrepos.go) are built earlier, because the
// proposals service takes them as its lifecycle; newPhase6 binds them.
//
// Shutdown order (see app.run): the server stops, then stopActions (the action
// service calls tasks and proposals, so it stops first), then stopNewRepos (the
// workers stop and the last outcomes reach proposals), then proposals and
// tasks, then closeNotices (so the statuses tasks announced while stopping are
// handed to the producer), then the hosts service and the store.

type phase6Deps struct {
	store     *db.Store
	status    *repos.StatusService
	hosts     *hosts.Service
	git       *git.Git
	builds    *builds.Service
	tasks     *tasks.Service
	proposals *proposals.Service
	srv       *api.Server
	redactor  safety.Redactor
	log       *slog.Logger
	feeder    *noticeFeeder
	nr        *newRepoParts
}

// phase6 holds what run starts and stops.
type phase6 struct {
	log      *slog.Logger
	actions  *actions.Service
	standup  *standup.Service
	producer *notify.Producer
	feeder   *noticeFeeder
	nr       *newRepoParts
	store    *db.Store
	services api.Phase6Services
}

func newPhase6(ctx context.Context, d phase6Deps) (*phase6, error) {
	now := func() time.Time { return time.Now().UTC() }

	// Host context reads: the bounded reader only (never a host writer).
	hostCtx := hosts.NewContextService(d.hosts, contextResolver{store: d.store, hosts: d.hosts})
	scope := &scopeReader{store: d.store, status: d.status, hosts: d.hosts, git: d.git, log: d.log, redact: d.redactor}
	ctxReader := &contextReader{scope: scope, host: hostCtx, store: d.store, logs: d.builds, git: d.git, redact: d.redactor}
	creator := &proposalCreator{svc: d.proposals, drafts: d.tasks, store: d.store}

	act, err := actions.New(actions.Deps{
		Scope: scope, Context: ctxReader, Tasks: d.tasks, Proposals: creator, Requests: d.store,
		Hub: d.srv, Drafts: folderDrafts{svc: d.tasks}, Redactor: d.redactor, Clock: now,
	})
	if err != nil {
		return nil, fmt.Errorf("start workspace actions: %w", err)
	}

	mrs, err := standup.NewMRSource(d.hosts, d.redactor)
	if err != nil {
		return nil, fmt.Errorf("start the standup merge request source: %w", err)
	}
	bs, err := standup.NewBuildSource(d.store)
	if err != nil {
		return nil, fmt.Errorf("start the standup build source: %w", err)
	}
	stand, err := standup.New(standup.Deps{
		Scope: scope, Commits: repos.NewHistoryReader(d.store, d.git), MRs: mrs, Builds: bs,
		Polish: polisher{scope: scope, tasks: d.tasks}, Hub: d.srv, Redactor: d.redactor, Clock: now,
	})
	if err != nil {
		_ = act.Close()
		return nil, fmt.Errorf("start the standup: %w", err)
	}

	// The producer outlives the run context on purpose: closeNotices stops it
	// after tasks closed, in the documented order.
	prod, err := notify.New(context.WithoutCancel(ctx), notify.Deps{Store: d.store, Publisher: d.srv, Clock: now, Redactor: d.redactor})
	if err != nil {
		_ = act.Close()
		return nil, fmt.Errorf("start notifications: %w", err)
	}
	d.feeder.bind(prod)

	// New repos were built before the server and the proposals service; bind
	// them now: the hub publishes to the server and every state change of an
	// operation reaches the proposals service (OperationChanged).
	d.nr.hub.srv.Store(d.srv)
	d.nr.notifier.follow = d.proposals.OperationChanged

	return &phase6{
		log: d.log, actions: act, standup: stand, producer: prod, feeder: d.feeder, nr: d.nr, store: d.store,
		services: api.Phase6Services{
			NewRepos:    newRepoAPI{svc: d.nr.svc, props: d.proposals, store: d.store, hub: d.nr.hub},
			Actions:     actionsAPI{svc: act},
			HostContext: hostContextAPI{reader: hostCtx, repoHost: d.hosts, tracked: scope.StillTracked},
			Drafts:      draftAPI{actions: act, tasks: d.tasks, store: d.store, now: now},
			Noise:       noiseAPI{scope: scope, proposals: d.proposals, creator: creator},
			Notices:     noticeAPI{prod: prod},
			Standup:     standupAPI{svc: stand},
		},
	}, nil
}

// start hands over announcements queued so far and reconciles the requests a
// crash left submitting (read-only toward their effects).
func (p *phase6) start(ctx context.Context) {
	p.feeder.start()
	if err := p.actions.Recover(ctx); err != nil {
		p.log.Warn("could not reconcile the workspace actions of the last run", "err", err)
	}
	// New repos: the follow-up loop hands the proposals service what recovery
	// settled (queued since recoverNewRepos), then the proposals are settled.
	p.nr.notifier.start(ctx)
	settleNewRepoProposals(ctx, p.log, p.nr.svc, p.store)
}

// recoverNewRepos reconciles the operations a crash or stop left running. It
// runs BEFORE the proposals service starts, so a Confirmed proposal is judged
// against an operation that recovery already settled, not one that is still
// being checked (an operation still unsettled is "keep": it stays Confirmed and
// its outcome arrives through the follow-up loop). Recovery reconciles
// read-only and repeats no stage. The wait is bounded and ends early when the
// operations stop changing (an operation parked for a human retry stays
// unsettled by design).
func (p *phase6) recoverNewRepos(ctx context.Context) {
	if err := p.nr.svc.Recover(ctx); err != nil {
		p.log.Warn("could not reconcile the new repos of the last run", "err", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	last, stable := "", 0
	for time.Now().Before(deadline) && stable < 5 {
		recs, err := p.store.ListUnsettledReservations(ctx)
		if err != nil || len(recs) == 0 {
			return
		}
		var sb strings.Builder
		for _, r := range recs {
			fmt.Fprintf(&sb, "%s:%d;", r.Operation.ID, r.Operation.Version)
		}
		if sb.String() == last {
			stable++
		} else {
			last, stable = sb.String(), 0
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// stopActions stops the action service, which calls tasks and proposals.
func (p *phase6) stopActions() {
	if p != nil {
		_ = p.actions.Close()
	}
}

// stopNewRepos stops the new-repo workers (a stage that was running is recorded
// Uncertain, never failed) and hands the last outcomes to the proposals service.
// It runs before proposals stop and before the store closes.
func (p *phase6) stopNewRepos() {
	if p != nil {
		_ = p.nr.svc.Close()
		p.nr.notifier.close()
	}
}

// closeNotices hands over the last announcements and stops the producer. It
// runs after tasks closed and before the store closes.
func (p *phase6) closeNotices() {
	if p != nil {
		p.feeder.close()
		_ = p.producer.Close()
	}
}
