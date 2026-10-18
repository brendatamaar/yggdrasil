// Package hosts is the functional core of git-host polling (GH-01, GH-02,
// GH-04, docs/design/git-hosts.md): which MR and pipeline data to keep fresh,
// with TTLs, per-instance concurrency, backoff and rate limits. Everything is
// read-only: the only effects it asks for are GET fetches and publishes.
// Nothing here does IO, reads the clock or logs; the shell in internal/hosts
// does. The types carry no JSON tags: wire DTOs live in internal/api. Tokens
// never enter the core.
package hosts

import (
	"fmt"
	"strings"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// InstanceID names a host instance: host[:port], lower case ("github.com",
// "git.acme.dev").
type InstanceID string

// Validate reports whether the ID is non-empty, lower case and holds no
// whitespace, slash or control character.
func (id InstanceID) Validate() error {
	s := string(id)
	if s == "" || s != strings.ToLower(s) || strings.ContainsFunc(s, func(r rune) bool {
		return r <= ' ' || r == 0x7f || r == '/' || r == '\\' || r == '@'
	}) {
		return fmt.Errorf("invalid host instance id %q", s)
	}
	return nil
}

// Kind is a host product.
type Kind string

const (
	KindGitLab Kind = "gitlab"
	KindGitHub Kind = "github"
	KindGitea  Kind = "gitea"
)

// Validate reports whether k is a known kind.
func (k Kind) Validate() error {
	switch k {
	case KindGitLab, KindGitHub, KindGitea:
		return nil
	}
	return fmt.Errorf("unknown host kind %q", string(k))
}

// ProjectPath is the repo's path on the host ("group/sub/repo",
// "owner/repo"), without slashes at either end.
type ProjectPath string

// Instance is a configured host. The token itself never enters the core.
type Instance struct {
	ID   InstanceID
	Kind Kind
	// Base is the instance's base URL, e.g. "https://git.acme.dev".
	Base string
	// Token: a token is configured.
	Token bool
}

// Key says what is polled. It is a closed set: RepoKey or InboxKey. Both are
// comparable, so a Key is a map key.
type Key interface{ isKey() }

// RepoKey is the MR and pipeline of one repo's branch. A detached HEAD
// (Branch "") is not polled.
type RepoKey struct {
	Repo   core.RepoID
	Branch string
}

// InboxKey is the MR inbox of one instance.
type InboxKey struct{ Instance InstanceID }

func (RepoKey) isKey()  {}
func (InboxKey) isKey() {}

// TTLs of cached payloads.
const (
	RepoTTL  = 60 * time.Second
	InboxTTL = 120 * time.Second
	// TickEvery is the interval of the shell's Tick ticker.
	TickEvery = 15 * time.Second
	// MaxInFlight is the concurrent fetches per instance.
	MaxInFlight = 4
	// DefaultRateLimitWait applies when a rate-limit response has no RetryAt.
	DefaultRateLimitWait = 60 * time.Second
	// MaxBackoff caps the transient-failure backoff.
	MaxBackoff = 10 * time.Minute
)

// Target is what should be polled now for a key.
type Target struct {
	Instance InstanceID
	Project  ProjectPath
	TTL      time.Duration
}

// Poller is the state of the host poller.
type Poller struct {
	Instances map[InstanceID]Instance
	// Targets is what should be polled now.
	Targets map[Key]Target
	Entries map[Key]Entry
	// Limited holds instance-wide rate limits: no fetch before the time.
	Limited map[InstanceID]time.Time
	// InFlight counts fetches per instance (at most MaxInFlight).
	InFlight map[InstanceID]int
}

// Entry is the polling state of one key.
type Entry struct {
	Attempt int
	Phase   Phase
}

// Phase is where a key is in its polling cycle. It is a closed set: Idle,
// Fetching, Waiting or Blocked.
type Phase interface{ isPhase() }

// Idle has data, or Data == nil before the first fetch.
type Idle struct {
	Data      Payload
	FetchedAt time.Time
	ETag      string
}

// Fetching: a request is in flight. The UI keeps showing Prev.
type Fetching struct {
	Attempt int
	Prev    Idle
}

// Waiting: backing off after a transient error, or because of a rate limit.
type Waiting struct {
	Until    time.Time
	Failures int
	Prev     Idle
	Why      WaitReason
}

// Blocked: polling stops until the config changes or the user asks again.
type Blocked struct {
	Why   BlockReason
	Since time.Time
	Prev  Idle
}

func (Idle) isPhase()     {}
func (Fetching) isPhase() {}
func (Waiting) isPhase()  {}
func (Blocked) isPhase()  {}

// WaitReason says why a key is Waiting.
type WaitReason string

const (
	WaitTransient   WaitReason = "transient"
	WaitRateLimited WaitReason = "rate_limited"
)

// Validate reports whether w is a known reason.
func (w WaitReason) Validate() error {
	switch w {
	case WaitTransient, WaitRateLimited:
		return nil
	}
	return fmt.Errorf("unknown wait reason %q", string(w))
}

// BlockReason says why a key is Blocked.
type BlockReason string

const (
	// BlockUnauthorized: bad or expired token.
	BlockUnauthorized BlockReason = "unauthorized"
	// BlockNotFound: private without a token, or a wrong path.
	BlockNotFound BlockReason = "not_found"
	// BlockUnsupported: an instance with no kind configured.
	BlockUnsupported BlockReason = "unsupported"
)

// Validate reports whether b is a known reason.
func (b BlockReason) Validate() error {
	switch b {
	case BlockUnauthorized, BlockNotFound, BlockUnsupported:
		return nil
	}
	return fmt.Errorf("unknown block reason %q", string(b))
}

// Payload is fetched data. It is a closed set: RepoState or Inbox.
type Payload interface{ isPayload() }

// RepoState is the MR and pipeline of the current branch.
type RepoState struct {
	MR       *MergeRequest
	Pipeline *Pipeline
}

// Inbox is the user's MRs on one instance.
type Inbox struct{ Items []InboxItem }

func (RepoState) isPayload() {}
func (Inbox) isPayload()     {}

// MergeRequest is an MR or PR.
type MergeRequest struct {
	Number    int
	Title     string
	URL       string
	Author    string
	Source    string
	Target    string
	State     MRState
	UpdatedAt time.Time
}

// MRState is the state of a merge request.
type MRState string

const (
	MROpen   MRState = "open"
	MRDraft  MRState = "draft"
	MRMerged MRState = "merged"
	MRClosed MRState = "closed"
)

// Validate reports whether s is a known state.
func (s MRState) Validate() error {
	switch s {
	case MROpen, MRDraft, MRMerged, MRClosed:
		return nil
	}
	return fmt.Errorf("unknown merge request state %q", string(s))
}

// Pipeline is the latest CI run for a branch.
type Pipeline struct {
	Status    PipelineStatus
	URL       string
	UpdatedAt time.Time
}

// PipelineStatus is the state of a pipeline.
type PipelineStatus string

const (
	PipelinePending  PipelineStatus = "pending"
	PipelineRunning  PipelineStatus = "running"
	PipelineSuccess  PipelineStatus = "success"
	PipelineFailed   PipelineStatus = "failed"
	PipelineCanceled PipelineStatus = "canceled"
	PipelineSkipped  PipelineStatus = "skipped"
	PipelineUnknown  PipelineStatus = "unknown"
)

// Validate reports whether s is a known status.
func (s PipelineStatus) Validate() error {
	switch s {
	case PipelinePending, PipelineRunning, PipelineSuccess, PipelineFailed,
		PipelineCanceled, PipelineSkipped, PipelineUnknown:
		return nil
	}
	return fmt.Errorf("unknown pipeline status %q", string(s))
}

// InboxItem is one MR in the inbox.
type InboxItem struct {
	MR MergeRequest
	// Repo is "group/repo" on the host.
	Repo string
	Role Role
}

// Role is how the user relates to an inbox MR.
type Role string

const (
	RoleReviewRequested Role = "review_requested"
	RoleAssigned        Role = "assigned"
	RoleAuthored        Role = "authored"
)

// Validate reports whether r is a known role.
func (r Role) Validate() error {
	switch r {
	case RoleReviewRequested, RoleAssigned, RoleAuthored:
		return nil
	}
	return fmt.Errorf("unknown role %q", string(r))
}

// FailKind classifies a failed fetch. The client translates HTTP statuses and
// network errors into it; the core never sees them.
type FailKind string

const (
	// FailTransient: timeout, connection error, 5xx.
	FailTransient FailKind = "transient"
	// FailRateLimited: 429, or 403 with a rate-limit header.
	FailRateLimited FailKind = "rate_limited"
	// FailUnauthorized: 401, or 403 without a rate-limit header.
	FailUnauthorized FailKind = "unauthorized"
	// FailNotFound: 404.
	FailNotFound FailKind = "not_found"
)

// Validate reports whether k is a known kind.
func (k FailKind) Validate() error {
	switch k {
	case FailTransient, FailRateLimited, FailUnauthorized, FailNotFound:
		return nil
	}
	return fmt.Errorf("unknown fetch failure kind %q", string(k))
}
