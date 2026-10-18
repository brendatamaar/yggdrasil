package api

import (
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// DTOs of the git hosts area (docs/design/git-hosts.md). Everything is
// read-only, and none of them can hold a token. They are plain; the hosts task
// may refine them in this file. The core types have no JSON tags, so every
// conversion happens in the handlers.

// HostKind is a host product.
type HostKind string

const (
	HostKindGitLab HostKind = "gitlab"
	HostKindGitHub HostKind = "github"
	HostKindGitea  HostKind = "gitea"
)

// HostPhase is where a host query stands, as the UI shows it.
type HostPhase string

const (
	// HostPhaseIdle: data is cached, or nothing was fetched yet.
	HostPhaseIdle HostPhase = "idle"
	// HostPhaseFetching: a request is in flight; the data shown is the last known.
	HostPhaseFetching HostPhase = "fetching"
	// HostPhaseRetrying: a transient error or a rate limit; retried at RetryAt.
	HostPhaseRetrying HostPhase = "retrying"
	// HostPhaseBlocked: polling stopped until the config changes or the user
	// refreshes. BlockReason says why.
	HostPhaseBlocked HostPhase = "blocked"
)

// HostInstanceDTO is a configured host (GET /api/hosts).
type HostInstanceDTO struct {
	// ID is host[:port], e.g. "git.acme.dev".
	ID   string   `json:"id"`
	Kind HostKind `json:"kind"`
	Base string   `json:"base"`
	// TokenConfigured is all the API says about a token.
	TokenConfigured bool `json:"tokenConfigured"`
}

// HostsResponse is the body of GET /api/hosts.
type HostsResponse struct {
	Instances []HostInstanceDTO `json:"instances"`
	// ConfigProblems lists skipped config lines, e.g. "line 3: unknown kind".
	// They never hold the value of a line.
	ConfigProblems []string `json:"configProblems"`
	// EnvPath is the file the config is read from.
	EnvPath string `json:"envPath"`
}

// MergeRequestDTO is an MR or PR.
type MergeRequestDTO struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	Author string `json:"author"`
	// Source and Target are branch names.
	Source string `json:"source"`
	Target string `json:"target"`
	// State is open, draft, merged or closed.
	State     string    `json:"state"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// PipelineDTO is the latest CI run for a branch.
type PipelineDTO struct {
	// Status is pending, running, success, failed, canceled, skipped or unknown.
	Status    string    `json:"status"`
	URL       string    `json:"url"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// HostLinksDTO are links that always work, even without a token or data.
type HostLinksDTO struct {
	Repo      string `json:"repo"`
	Branch    string `json:"branch,omitempty"`
	MRList    string `json:"mrList"`
	Pipelines string `json:"pipelines"`
}

// RepoHostResponse is the body of GET /api/repos/{id}/host.
type RepoHostResponse struct {
	Linked bool `json:"linked"`
	// NotLinked says why, in plain language, when Linked is false.
	NotLinked string   `json:"notLinked,omitempty"`
	Instance  string   `json:"instance,omitempty"`
	Kind      HostKind `json:"kind,omitempty"`
	// Project is the repo's path on the host, e.g. "acme-shop/shop-api".
	Project string `json:"project,omitempty"`
	Branch  string `json:"branch,omitempty"`
	// MR and Pipeline are for the current branch; nil when none was found.
	MR       *MergeRequestDTO `json:"mr,omitempty"`
	Pipeline *PipelineDTO     `json:"pipeline,omitempty"`
	Links    HostLinksDTO     `json:"links"`
	Phase    HostPhase        `json:"phase"`
	// BlockReason is unauthorized, not_found or unsupported when Phase is
	// "blocked". RetryAt is set when Phase is "retrying".
	BlockReason string     `json:"blockReason,omitempty"`
	RetryAt     *time.Time `json:"retryAt,omitempty"`
	FetchedAt   *time.Time `json:"fetchedAt,omitempty"`
	// CredentialsInRemote is a health flag: the remote URL holds credentials
	// (use a credential helper). The value is never sent.
	CredentialsInRemote bool `json:"credentialsInRemote"`
}

// InboxItemDTO is one MR in the inbox.
type InboxItemDTO struct {
	MR MergeRequestDTO `json:"mr"`
	// Instance is the host the MR is on.
	Instance string `json:"instance"`
	// Repo is "group/repo" on the host.
	Repo string `json:"repo"`
	// Role is review_requested, assigned or authored.
	Role string `json:"role"`
}

// InboxInstanceDTO is the polling state of one instance's inbox.
type InboxInstanceDTO struct {
	Instance    string     `json:"instance"`
	Phase       HostPhase  `json:"phase"`
	BlockReason string     `json:"blockReason,omitempty"`
	RetryAt     *time.Time `json:"retryAt,omitempty"`
	FetchedAt   *time.Time `json:"fetchedAt,omitempty"`
}

// InboxResponse is the body of GET /api/mrs: the inbox across instances,
// deduplicated by URL.
type InboxResponse struct {
	Items     []InboxItemDTO     `json:"items"`
	Instances []InboxInstanceDTO `json:"instances"`
}

// RefreshHostsRequest is the body of POST /api/hosts/refresh. Empty lists
// mean everything.
type RefreshHostsRequest struct {
	Repos     []core.RepoID `json:"repos"`
	Instances []string      `json:"instances"`
}

// HostsChangedMessage is the data of a message on the "hosts" topic. Repo is
// set when one repo's data changed, Instance when one instance's inbox or
// state changed; with neither, everything may have changed.
type HostsChangedMessage struct {
	Repo     *core.RepoID `json:"repo,omitempty"`
	Instance string       `json:"instance,omitempty"`
}
