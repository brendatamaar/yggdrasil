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

// statusPublisher sends repo-status changes to the browser and gives the
// hosts service the current branch of every tracked repo (git-hosts design:
// targets come from repo status). SetRepos is non-blocking and coalescing.
type statusPublisher struct {
	srv    *api.Server
	status *repos.StatusService
	hosts  *hosts.Service
	log    *slog.Logger
}

func (p statusPublisher) RepoChanged(id core.RepoID) {
	p.srv.PublishRepoStatus(id)
	p.feedHosts()
}

func (p statusPublisher) CountsChanged() {
	p.srv.PublishRepoCounts()
	p.feedHosts()
}

func (p statusPublisher) feedHosts() {
	if p.hosts == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	snap, err := p.status.Snapshot(ctx)
	if err != nil {
		p.log.Debug("repo status not ready for hosts", "err", err)
		return
	}
	p.hosts.SetRepos(hostTargets(snap))
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
