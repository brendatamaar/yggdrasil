package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/hosts"
	"github.com/brendatamaar/yggdrasil/internal/repos"
)

// lateHostsPublisher forwards host changes to the server, which is built
// after the hosts service. srv is set once, before run starts.
type lateHostsPublisher struct{ srv *api.Server }

func (p *lateHostsPublisher) PublishHostsChanged(msg api.HostsChangedMessage) {
	if p.srv != nil {
		p.srv.PublishHostsChanged(msg)
	}
}

// statusPublisher sends repo-status changes to the browser and nudges the
// hosts feeder. Neither call blocks the status publish loop.
type statusPublisher struct {
	srv    *api.Server
	feeder *hostsFeeder
}

func (p statusPublisher) RepoChanged(id core.RepoID) {
	p.srv.PublishRepoStatus(id)
	p.feeder.mark()
}

func (p statusPublisher) CountsChanged() {
	p.srv.PublishRepoCounts()
	p.feeder.mark()
}

// hostsFeeder coalesces "status changed" nudges: one goroutine, owned by the
// app's errgroup, reads the snapshot and calls SetRepos. Any number of marks
// while it works produce one more read.
type hostsFeeder struct {
	status *repos.StatusService
	hosts  *hosts.Service
	log    *slog.Logger
	dirty  chan struct{}
}

func newHostsFeeder(status *repos.StatusService, h *hosts.Service, log *slog.Logger) *hostsFeeder {
	return &hostsFeeder{status: status, hosts: h, log: log, dirty: make(chan struct{}, 1)}
}

// mark records that the repo set may have changed. It never blocks.
func (f *hostsFeeder) mark() {
	if f == nil || f.hosts == nil {
		return
	}
	select {
	case f.dirty <- struct{}{}:
	default:
	}
}

// run feeds until ctx ends.
func (f *hostsFeeder) run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-f.dirty:
		}
		sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		snap, err := f.status.Snapshot(sctx)
		cancel()
		if err != nil {
			f.log.Debug("repo status not ready for hosts", "err", err)
			continue
		}
		f.hosts.SetRepos(hostTargets(snap))
	}
}

// hostTargets maps the status read model to hosts targets: every tracked repo
// with its current branch ("" when detached or never read).
func hostTargets(r corerepos.Refresher) []hosts.RepoRef {
	out := make([]hosts.RepoRef, 0, len(r.Entries))
	for id, e := range r.Entries {
		out = append(out, hosts.RepoRef{ID: id, Path: string(e.Path), Branch: branchOf(e.View)})
	}
	return out
}

func branchOf(v corerepos.View) string {
	switch v := v.(type) {
	case corerepos.Fresh:
		return v.Snap.Branch
	case corerepos.Problem:
		if v.Last != nil {
			return v.Last.Branch
		}
	}
	return ""
}
