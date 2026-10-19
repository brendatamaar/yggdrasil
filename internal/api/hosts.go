package api

import (
	"context"
	"net/http"
	"time"

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
	// ConfigInfo says which file the config is read from and which lines were
	// skipped. The problems never hold the value of a line.
	ConfigInfo(ctx context.Context) HostsConfigInfo
	// RepoHost returns the MR, pipeline, links and polling phase of a repo's
	// current branch, or why the repo is not linked to a host.
	RepoHost(ctx context.Context, repo core.RepoID) (RepoHostView, error)
	// Inbox returns the merged MR inbox across instances.
	Inbox(ctx context.Context) (InboxView, error)
	// Refresh asks to refetch keys; empty means all. A RepoKey with an empty
	// Branch names every branch of that repo. A key that matches nothing is an
	// apperr Invalid error.
	Refresh(ctx context.Context, keys []corehosts.Key) error
	// FailedPipelines returns the repos whose current branch's pipeline
	// failed, for the repo-status "Failed pipeline" filter.
	FailedPipelines(ctx context.Context) (map[core.RepoID]struct{}, error)
}

// HostsConfigInfo describes where the config came from.
type HostsConfigInfo struct {
	EnvPath  string
	Problems []string
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
	mux.HandleFunc("GET /api/hosts", s.listHosts)
	mux.HandleFunc("POST /api/hosts/refresh", s.refreshHosts)
	mux.HandleFunc("GET /api/mrs", s.listMRs)
	mux.HandleFunc("GET /api/repos/{id}/host", s.repoHost)
}

// PublishHostsChanged tells subscribers that host data changed. The hosts
// service calls it (hosts.Publisher).
func (s *Server) PublishHostsChanged(msg HostsChangedMessage) {
	s.hub.publishNext(hostsTopic{}, msg)
}

func (s *Server) listHosts(w http.ResponseWriter, r *http.Request) {
	insts, err := s.hosts.Instances(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	info := s.hosts.ConfigInfo(r.Context())
	resp := HostsResponse{
		Instances:      make([]HostInstanceDTO, 0, len(insts)),
		ConfigProblems: append([]string{}, info.Problems...),
		EnvPath:        info.EnvPath,
	}
	for _, in := range insts {
		resp.Instances = append(resp.Instances, HostInstanceDTO{
			ID: string(in.ID), Kind: HostKind(in.Kind), Base: in.Base, TokenConfigured: in.Token,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) repoHost(w http.ResponseWriter, r *http.Request) {
	id := core.RepoID(r.PathValue("id"))
	if err := id.Validate(); err != nil {
		writeError(w, http.StatusNotFound, ErrNotFound, "That repo isn't tracked.", "Refresh the page and try again.")
		return
	}
	v, err := s.hosts.RepoHost(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	resp := RepoHostResponse{
		Linked: v.Linked, NotLinked: v.NotLinked, CredentialsInRemote: v.CredentialsInRemote,
		Links: HostLinksDTO{Repo: v.Links.Repo, Branch: v.Links.Branch, MRList: v.Links.MRList, Pipelines: v.Links.Pipelines},
		Phase: HostPhaseIdle,
	}
	if v.Linked {
		resp.Instance, resp.Kind = string(v.Instance.ID), HostKind(v.Instance.Kind)
		resp.Project, resp.Branch = string(v.Project), v.Branch
		ph := phaseOf(v.Entry)
		resp.Phase, resp.BlockReason, resp.RetryAt, resp.FetchedAt = ph.phase, ph.reason, ph.retryAt, ph.fetchedAt
		if rs, ok := ph.data.(corehosts.RepoState); ok {
			resp.MR, resp.Pipeline = mrDTO(rs.MR), pipelineDTO(rs.Pipeline)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) listMRs(w http.ResponseWriter, r *http.Request) {
	v, err := s.hosts.Inbox(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	resp := InboxResponse{Items: make([]InboxItemDTO, 0, len(v.Items)), Instances: make([]InboxInstanceDTO, 0, len(v.Instances))}
	for _, e := range v.Items {
		resp.Items = append(resp.Items, InboxItemDTO{MR: *mrDTO(&e.Item.MR), Instance: string(e.Instance), Repo: e.Item.Repo, Role: string(e.Item.Role)})
	}
	for _, in := range v.Instances {
		ph := phaseOf(in.Entry)
		resp.Instances = append(resp.Instances, InboxInstanceDTO{
			Instance: string(in.Instance), Phase: ph.phase, BlockReason: ph.reason, RetryAt: ph.retryAt, FetchedAt: ph.fetchedAt,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// ParseRefresh turns a refresh request into keys (boundary parser). Repos
// become a RepoKey with an empty Branch (every branch of that repo) and
// instances become their inbox key. Malformed IDs are rejected here; IDs of
// things that are not configured are rejected by the service. Empty lists mean
// everything.
func ParseRefresh(req RefreshHostsRequest) ([]corehosts.Key, error) {
	var keys []corehosts.Key
	for _, id := range req.Repos {
		if err := id.Validate(); err != nil {
			return nil, err
		}
		keys = append(keys, corehosts.RepoKey{Repo: id})
	}
	for _, s := range req.Instances {
		id := corehosts.InstanceID(s)
		if err := id.Validate(); err != nil {
			return nil, err
		}
		keys = append(keys, corehosts.InboxKey{Instance: id})
	}
	return keys, nil
}

func (s *Server) refreshHosts(w http.ResponseWriter, r *http.Request) {
	var req RefreshHostsRequest
	if !decodeBody(w, r, &req) {
		return
	}
	keys, err := ParseRefresh(req)
	if err != nil {
		badRequest(w, "One of the repos or hosts in the request isn't valid.", "Refresh the page and try again.")
		return
	}
	if err := s.hosts.Refresh(r.Context(), keys); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type phaseInfo struct {
	phase     HostPhase
	reason    string
	retryAt   *time.Time
	fetchedAt *time.Time
	data      corehosts.Payload
}

// phaseOf maps a core entry to what the UI shows. Every phase keeps the last
// known data.
func phaseOf(e corehosts.Entry) phaseInfo {
	var prev corehosts.Idle
	out := phaseInfo{phase: HostPhaseIdle}
	switch v := e.Phase.(type) {
	case corehosts.Idle:
		prev = v
	case corehosts.Fetching:
		prev, out.phase = v.Prev, HostPhaseFetching
	case corehosts.Waiting:
		until := v.Until
		prev, out.phase, out.retryAt = v.Prev, HostPhaseRetrying, &until
	case corehosts.Blocked:
		prev, out.phase, out.reason = v.Prev, HostPhaseBlocked, string(v.Why)
	}
	out.data = prev.Data
	if !prev.FetchedAt.IsZero() {
		t := prev.FetchedAt
		out.fetchedAt = &t
	}
	return out
}

func mrDTO(m *corehosts.MergeRequest) *MergeRequestDTO {
	if m == nil {
		return nil
	}
	return &MergeRequestDTO{Number: m.Number, Title: m.Title, URL: m.URL, Author: m.Author, Source: m.Source, Target: m.Target, State: string(m.State), UpdatedAt: m.UpdatedAt}
}

func pipelineDTO(p *corehosts.Pipeline) *PipelineDTO {
	if p == nil {
		return nil
	}
	return &PipelineDTO{Status: string(p.Status), URL: p.URL, UpdatedAt: p.UpdatedAt}
}
