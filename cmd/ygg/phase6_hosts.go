package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/hosts"
	"github.com/brendatamaar/yggdrasil/internal/hosts/gitea"
	"github.com/brendatamaar/yggdrasil/internal/hosts/github"
	"github.com/brendatamaar/yggdrasil/internal/hosts/gitlab"
)

// Wiring notes for host context (GH-05, GH-06, docs/design/phase-6-host-context.md).
//
// Failed-job logs: GitHub answers a job-log request with a redirect to a
// short-lived signed URL on another origin. The Getter follows such a redirect
// only to an origin listed in hostLogOrigins; any other origin is refused, the
// job's log is labelled unavailable and the rest of the run is still listed (a
// partial result, never a silent gap). The list is explicit and is never
// extended from a response. A log larger than its cap is cut and labelled
// truncated by the reader; an aggregate overflow is labelled partial.
var hostLogOrigins = func() []string {
	o := []string{"https://pipelines.actions.githubusercontent.com"}
	for i := 0; i < 20; i++ {
		o = append(o, "https://productionresultssa"+strconv.Itoa(i)+".blob.core.windows.net")
	}
	return o
}()

// newHostClients returns the live host clients, built over one Getter that
// carries the explicit signed-log origin allowlist. It replaces
// hostclients.New, which allows no external log redirect.
func newHostClients() (map[corehosts.Kind]hosts.HostClient, error) {
	g, err := hosts.NewGetterWithLogOrigins(nil, nil, hostLogOrigins)
	if err != nil {
		return nil, fmt.Errorf("prepare the git host clients: %w", err)
	}
	return map[corehosts.Kind]hosts.HostClient{
		corehosts.KindGitLab: gitlab.New(g),
		corehosts.KindGitHub: github.New(g),
		corehosts.KindGitea:  gitea.New(g),
	}, nil
}

// repoHostReader is the part of the host service the resolver and the facts use.
type repoHostReader interface {
	RepoHost(ctx context.Context, repo core.RepoID) (api.RepoHostView, error)
}

// contextResolver resolves a context key to the tracked, ready repo's host
// instance and project, on the server, from storage and the host links the
// poller already holds. It never resolves a reserved, untracked or foreign repo.
type contextResolver struct {
	store *db.Store
	hosts repoHostReader
}

var _ hosts.ContextResolver = contextResolver{}

func (r contextResolver) ResolveContext(ctx context.Context, key corehosts.ContextKey) (hosts.ContextTarget, error) {
	repo, err := r.store.GetRepo(ctx, key.Repo)
	if errors.Is(err, db.ErrNotFound) {
		return hosts.ContextTarget{}, fmt.Errorf("repo %s: %w", key.Repo, hosts.ErrContextScope)
	}
	if err != nil {
		return hosts.ContextTarget{}, fmt.Errorf("read repo %s: %w", key.Repo, err)
	}
	if repo.Readiness != "" && repo.Readiness != core.RepoReady {
		return hosts.ContextTarget{}, fmt.Errorf("repo %s: %w", key.Repo, hosts.ErrContextScope)
	}
	view, err := r.hosts.RepoHost(ctx, key.Repo)
	if err != nil {
		return hosts.ContextTarget{}, fmt.Errorf("read the host of repo %s: %w", key.Repo, err)
	}
	if !view.Linked {
		return hosts.ContextTarget{}, fmt.Errorf("repo %s has no linked host: %w", key.Repo, hosts.ErrContextScope)
	}
	// The revision changes when the repo moves folder or is linked elsewhere, so
	// a cached read of the old target is never served for the new one.
	sum := sha256.Sum256([]byte(repo.Path + "\x00" + string(view.Instance.ID) + "\x00" + string(view.Project)))
	return hosts.ContextTarget{Instance: view.Instance.ID, Project: view.Project, Revision: hex.EncodeToString(sum[:8])}, nil
}

// hostContextAPI is api.HostContextService over the bounded host reader. It
// holds no host writer: only a hosts.ContextReader.
type hostContextAPI struct {
	reader hosts.ContextReader
	// tracked is db.ErrNotFound for an untracked or reserved repo.
	tracked func(context.Context, core.RepoID) error
}

var _ api.HostContextService = hostContextAPI{}

func (h hostContextAPI) read(ctx context.Context, key corehosts.ContextKey) (hosts.ContextOutcome, error) {
	out, err := h.reader.Read(ctx, key)
	switch {
	case errors.Is(err, hosts.ErrContextSuperseded):
		return hosts.ContextOutcome{}, api.TryAgain(err)
	case errors.Is(err, hosts.ErrContextScope):
		return hosts.ContextOutcome{}, fmt.Errorf("host context of repo %s: %w", key.Repo, db.ErrNotFound)
	case err != nil:
		return hosts.ContextOutcome{}, fmt.Errorf("read host context of repo %s: %w", key.Repo, err)
	}
	return out, nil
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	t = t.UTC()
	return &t
}

// statusOf turns an outcome into the shared status of a response. A blocked or
// waiting read is a normal answer with a plain message; Blocked.Until becomes
// ExpiresAt.
func statusOf(out hosts.ContextOutcome) api.HostContextStatusDTO {
	switch {
	case out.Result != nil:
		st := api.HostContextStatusDTO{Phase: api.HostContextReady, ExpiresAt: timePtr(out.Result.Expires)}
		for _, u := range out.Result.Unavailable {
			st.Unavailable = append(st.Unavailable, api.HostContextUnavailableDTO{Source: u.Source, Reason: api.HostContextReason(u.Reason)})
		}
		if len(st.Unavailable) > 0 {
			st.Phase = api.HostContextPartial
			st.Message = "Part of this could not be read. The missing parts are listed."
		}
		return st
	case out.Blocked != nil:
		return api.HostContextStatusDTO{
			Phase: api.HostContextBlocked, Reason: api.HostContextReason(out.Blocked.Reason),
			ExpiresAt: timePtr(out.Blocked.Until), Message: blockedMessage(out.Blocked.Reason),
		}
	}
	return api.HostContextStatusDTO{Phase: api.HostContextWaiting, RetryAt: timePtr(out.RetryAt), Message: "The host is busy. Yggdrasil can read this again shortly."}
}

func blockedMessage(r corehosts.ContextBlockReason) string {
	switch r {
	case corehosts.ContextNoToken:
		return "No access token is set for this host. Add one in the host settings to read this."
	case corehosts.ContextForbidden:
		return "The host refused the token. Check that it is valid and has read access."
	case corehosts.ContextNotFound:
		return "The host has nothing at that address, or the token cannot see it."
	case corehosts.ContextUnsupported:
		return "This host cannot give that."
	case corehosts.ContextRetryExhausted:
		return "The host kept failing. Try again later."
	case corehosts.ContextUntracked:
		return "This repo is not tracked or not linked to a git host."
	case corehosts.ContextTooLarge:
		return "That is too large to read in full."
	}
	return "This could not be read."
}

func (h hostContextAPI) FailedJobs(ctx context.Context, repo core.RepoID, runID string) (api.FailedJobsResponse, error) {
	out, err := h.read(ctx, corehosts.ContextKey{Repo: repo, Kind: corehosts.ContextFailedJobs, ObjectID: runID})
	if err != nil {
		return api.FailedJobsResponse{}, err
	}
	resp := api.FailedJobsResponse{RepoID: repo, RunID: runID, Jobs: []api.FailedJobDTO{}, Status: statusOf(out)}
	if out.Result != nil {
		if v, ok := out.Result.Value.(corehosts.JobLogs); ok {
			resp.Head, resp.Truncated = v.Head, v.Truncated
			for _, j := range v.Jobs {
				resp.Jobs = append(resp.Jobs, api.FailedJobDTO{JobID: j.JobID, Name: j.Name, Stage: j.Stage, Log: j.Log, LogTruncated: j.LogTruncated, LogUnavailable: j.LogUnavailable})
			}
			if v.Truncated && resp.Status.Phase == api.HostContextReady {
				resp.Status.Phase = api.HostContextPartial
				resp.Status.Message = "More failed jobs exist than are listed here."
			}
		}
	}
	return resp, nil
}

func issueDTO(s corehosts.IssueSummary) api.IssueDTO {
	return api.IssueDTO{Number: s.Number, Title: s.Title, Author: s.Author, Labels: s.Labels, UpdatedAt: s.UpdatedAt}
}

func (h hostContextAPI) Issues(ctx context.Context, repo core.RepoID) (api.IssuesResponse, error) {
	out, err := h.read(ctx, corehosts.ContextKey{Repo: repo, Kind: corehosts.ContextIssues})
	if err != nil {
		return api.IssuesResponse{}, err
	}
	resp := api.IssuesResponse{RepoID: repo, Issues: []api.IssueDTO{}, Status: statusOf(out)}
	if out.Result != nil {
		if v, ok := out.Result.Value.(corehosts.IssueList); ok {
			resp.Truncated = v.Truncated
			for _, i := range v.Items {
				resp.Issues = append(resp.Issues, issueDTO(i))
			}
		}
	}
	return resp, nil
}

func (h hostContextAPI) Issue(ctx context.Context, repo core.RepoID, number int) (api.IssueResponse, error) {
	out, err := h.read(ctx, corehosts.ContextKey{Repo: repo, Kind: corehosts.ContextIssue, ObjectID: strconv.Itoa(number)})
	if err != nil {
		return api.IssueResponse{}, err
	}
	resp := api.IssueResponse{RepoID: repo, Status: statusOf(out)}
	if out.Result != nil {
		if v, ok := out.Result.Value.(corehosts.IssueDetail); ok {
			d := issueDTO(v.Summary)
			d.Body, d.BodyTruncated, d.URL = v.Body, v.BodyTruncated, v.URL
			resp.Issue = &d
		}
	}
	return resp, nil
}

// MRContext: a merge request diff is read for one immutable head, and the
// cached host data holds no head for a merge request, so there is nothing the
// server can pin the key to. The answer says so instead of guessing a head.
// Open gap for the hosts model (see the W3a report).
func (h hostContextAPI) MRContext(ctx context.Context, repo core.RepoID, number int) (api.MRContextResponse, error) {
	if err := h.tracked(ctx, repo); err != nil {
		return api.MRContextResponse{}, err // an untracked repo is not found, as for every read
	}
	return api.MRContextResponse{RepoID: repo, Status: api.HostContextStatusDTO{
		Phase: api.HostContextBlocked, Reason: api.HostContextUnsupported,
		Message: "Yggdrasil does not know this merge request's latest commit yet, so it cannot read its diff.",
	}}, nil
}
