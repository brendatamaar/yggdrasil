package api

import (
	"context"
	"net/http"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

// This file is owned by the git hosts task (GH-01, GH-02, GH-04). The DTOs are
// in types_hosts.go. Everything is read-only: no endpoint writes to a host.
// The file is excluded from tygo (tygo.yaml): it holds Go-only wiring.

// HostsService is implemented by internal/hosts (docs/design/git-hosts.md).
// It never returns a token. Every method returns errors classified by
// internal/apperr.
type HostsService interface {
	// Instances returns the configured hosts and which have a token.
	Instances(ctx context.Context) ([]corehosts.Instance, error)
	// RepoHost returns the MR, pipeline, links and polling phase of a repo's
	// current branch, or why the repo is not linked to a host.
	RepoHost(ctx context.Context, repo core.RepoID) (RepoHostView, error)
	// Inbox returns the merged MR inbox across instances.
	Inbox(ctx context.Context) (InboxView, error)
	// Refresh asks to refetch keys; empty means all.
	Refresh(ctx context.Context, keys []corehosts.Key) error
	// FailedPipelines returns the repos whose current branch's pipeline
	// failed, for the repo-status "Failed pipeline" filter.
	FailedPipelines(ctx context.Context) (map[core.RepoID]struct{}, error)
}

// RepoHostView is what HostsService knows about one repo.
type RepoHostView struct {
	// Linked is false when the repo has no remote, an unknown host or a
	// detached HEAD; NotLinked then says why, in plain language.
	Linked    bool
	NotLinked string
	Instance  corehosts.Instance
	Project   corehosts.ProjectPath
	Branch    string
	// Entry holds the data and the polling phase of the repo's key.
	Entry corehosts.Entry
	Links corehosts.LinkSet
	// CredentialsInRemote: the remote URL holds credentials (a health flag).
	CredentialsInRemote bool
}

// InboxView is the MR inbox across instances.
type InboxView struct {
	Items []InboxEntry
	// Instances holds each instance's polling phase.
	Instances []InboxInstance
}

// InboxEntry is one inbox MR and the instance it came from.
type InboxEntry struct {
	Instance corehosts.InstanceID
	Item     corehosts.InboxItem
}

// InboxInstance is the phase of one instance's inbox key.
type InboxInstance struct {
	Instance corehosts.InstanceID
	Entry    corehosts.Entry
}

// routesHosts registers the host endpoints. It registers nothing when the
// service is not wired.
func (s *Server) routesHosts(mux *http.ServeMux) {
	if s.hosts == nil {
		return
	}
	_ = mux // the git hosts task adds its routes here
}
