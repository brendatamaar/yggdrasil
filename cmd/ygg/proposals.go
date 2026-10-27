package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/builds/toolchains"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/hosts"
	"github.com/brendatamaar/yggdrasil/internal/mainlock"
	"github.com/brendatamaar/yggdrasil/internal/proposals"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// buildQueue is the part of the builds service the install proposal needs.
type buildQueue interface {
	Enqueue(ctx context.Context, spec corebuilds.Spec, actor corebuilds.Actor) (core.BuildID, error)
	Get(ctx context.Context, id core.BuildID) (corebuilds.State, error)
}

// proposalBuilds implements proposals.Builds over the builds service: it
// queues one install step of the repo's toolchain in the main checkout (the
// builds service takes the main-checkout lock itself) and polls the build to
// its end.
type proposalBuilds struct {
	queue      buildQueue
	store      *db.Store
	toolchains []builds.Toolchain
	poll       time.Duration
}

var _ proposals.Builds = proposalBuilds{}

// Enqueue finds the install step called step among the defaults of the
// toolchains detected at the repo root. Detection reads files only.
func (b proposalBuilds) Enqueue(ctx context.Context, repo core.RepoID, step, owner string) (core.BuildID, error) {
	r, err := b.store.GetRepo(ctx, repo)
	if err != nil {
		return "", fmt.Errorf("find repo %s: %w", repo, err)
	}
	fsys := os.DirFS(r.Path)
	for _, tc := range b.toolchains {
		d, ok := tc.Detect(fsys)
		if !ok || (d.Dir != "." && d.Dir != "") {
			continue
		}
		for _, st := range tc.Commands(d).Install {
			if st.Name != step {
				continue
			}
			spec := corebuilds.Spec{Target: corebuilds.MainCheckout{}, Steps: []corebuilds.StepSpec{st.Spec(repo)}}
			return b.queue.Enqueue(ctx, spec, corebuilds.Actor(owner))
		}
	}
	return "", fmt.Errorf("repo %s has no install step %q", repo, step)
}

// Wait polls the build until it is finished.
func (b proposalBuilds) Wait(ctx context.Context, id core.BuildID) (proposals.BuildOutcome, error) {
	poll := b.poll
	if poll <= 0 {
		poll = 500 * time.Millisecond
	}
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		st, err := b.queue.Get(ctx, id)
		if err != nil {
			return proposals.BuildOutcome{}, fmt.Errorf("read build %s: %w", id, err)
		}
		if f, ok := st.(corebuilds.Finished); ok {
			switch f.Result.(type) {
			case corebuilds.ResultSucceeded:
				return proposals.BuildOutcome{Succeeded: true}, nil
			case corebuilds.ResultCancelled:
				return proposals.BuildOutcome{Reason: "The build was cancelled."}, nil
			default:
				return proposals.BuildOutcome{Reason: "The install command failed."}, nil
			}
		}
		select {
		case <-ctx.Done():
			return proposals.BuildOutcome{}, ctx.Err()
		case <-t.C:
		}
	}
}

// instanceLister is the part of the hosts service the classifier needs.
type instanceLister interface {
	Instances(ctx context.Context) ([]corehosts.Instance, error)
}

// proposalHosts implements proposals.Hosts: a remote belongs to the configured
// instance whose host name it names. The URL may carry credentials and is
// never logged or returned.
type proposalHosts struct{ svc instanceLister }

var _ proposals.Hosts = proposalHosts{}

func (h proposalHosts) Kind(remoteURL string) proposals.HostKind {
	ref, err := hosts.ParseRemote(remoteURL)
	if err != nil {
		return proposals.HostUnknown
	}
	insts, err := h.svc.Instances(context.Background())
	if err != nil {
		return proposals.HostUnknown
	}
	for _, in := range insts {
		u, err := url.Parse(in.Base)
		if err != nil || !strings.EqualFold(u.Hostname(), ref.Host) {
			continue
		}
		switch in.Kind {
		case corehosts.KindGitLab:
			return proposals.HostGitLab
		case corehosts.KindGitHub:
			return proposals.HostGitHub
		case corehosts.KindGitea:
			return proposals.HostGitea
		}
	}
	return proposals.HostUnknown
}

// newProposals builds the proposals service. pub is bound to the server after
// the server exists; lock is the one main-checkout lock shared with tasks,
// builds and git actions.
func newProposals(store *db.Store, g *git.Git, lock *mainlock.Registry, buildSvc *builds.Service, hostSvc *hosts.Service, pub *api.ProposalsHub, redactor safety.Redactor) (*proposals.Service, error) {
	svc, err := proposals.New(proposals.Config{
		Store: store, Git: g, Resolver: proposals.StoreResolver{Store: store}, Lock: lock,
		Builds: proposalBuilds{queue: buildSvc, store: store, toolchains: toolchains.All()},
		Hosts:  proposalHosts{svc: hostSvc}, Publisher: pub, Redactor: redactor,
		Clock: func() time.Time { return time.Now().UTC() }, NewID: db.NewID,
	})
	if err != nil {
		return nil, fmt.Errorf("start proposals: %w", err)
	}
	return svc, nil
}
