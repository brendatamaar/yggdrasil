package mcp

import (
	"context"
	"errors"

	"github.com/brendatamaar/yggdrasil/internal/core"
	cmcp "github.com/brendatamaar/yggdrasil/internal/core/mcp"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
)

// The interfaces here are what the MCP server needs from the rest of
// Yggdrasil, defined where they are used. cmd/ygg adapts the real services to
// them; internal/mcp/mcptest holds the fakes. Nothing that crosses these
// interfaces carries an absolute path of the machine: locations are repo and
// worktree IDs, and the adapter resolves them.

// ErrNoTask: the task does not exist (any more).
var ErrNoTask = errors.New("mcp: no such task")

// Loc is where a read or action happens: a repo's main checkout, or the task
// worktree of that repo when Worktree is set.
type Loc struct {
	Repo     core.RepoID
	Worktree core.WorktreeID
}

// RepoView is one tracked repo of the task's project, as the task sees it.
type RepoView struct {
	ID            core.RepoID
	Name          string
	DefaultBranch string
	// InScope: the task may read the content of this repo and act on it.
	InScope bool
	// Worktree is the task's worktree of this repo, "" when it has none.
	Worktree       core.WorktreeID
	WorktreeBranch string
	// Paths are the absolute folders of this repo (main checkout, task
	// worktree). They are never shown to the agent: sanitize scrubs them.
	Paths []string
}

// Snapshot is the task's view of the workspace at one moment. Repos holds only
// repos that are tracked and not untracked (SF-02); the adapter builds it on
// every call, so a repo untracked mid-run disappears at the next call.
type Snapshot struct {
	Project   core.ProjectID
	Mode      core.TaskMode
	Repos     []RepoView
	AgentName string
}

// Workspace builds a task's Snapshot.
type Workspace interface {
	Snapshot(ctx context.Context, task core.TaskID) (Snapshot, error)
}

// RepoStatus is the branch and counts of a checkout.
type RepoStatus struct {
	Branch                string
	Detached              bool
	Upstream              string
	Ahead, Behind         int
	Staged, Unstaged      int
	Untracked, Conflicted int
	Head                  string
}

// Commit is one line of history.
type Commit struct {
	ID      string
	Author  string
	When    string
	Subject string
}

// Change is one changed path (relative, forward slashes).
type Change struct {
	Path   string
	Status string
}

// Reader reads git state. The adapter picks the worktree or main checkout from
// Loc.
type Reader interface {
	Status(ctx context.Context, at Loc) (RepoStatus, error)
	Commits(ctx context.Context, at Loc, limit int) ([]Commit, error)
	Changes(ctx context.Context, at Loc) ([]Change, error)
	// Diff returns at most maxBytes of diff text for path ("" = everything)
	// and whether it was cut.
	Diff(ctx context.Context, at Loc, path string, maxBytes int) (text string, truncated bool, err error)
}

// BuildPreset is a saved build, with the repos it touches.
type BuildPreset struct {
	ID    string
	Name  string
	Repos []core.RepoID
	Steps []string
}

// BuildSummary is one row of the build list.
type BuildSummary struct {
	ID     core.BuildID
	Status string
	Repos  []core.RepoID
}

// Builds is the builds service as the MCP tools use it.
type Builds interface {
	// Info lists, in plain lines, the detected toolchains and their commands.
	Info(ctx context.Context, repo core.RepoID) ([]string, error)
	// Order lists the project's repos in build order.
	Order(ctx context.Context, project core.ProjectID) ([]core.RepoID, error)
	Presets(ctx context.Context, project core.ProjectID) ([]BuildPreset, error)
	List(ctx context.Context, project core.ProjectID, limit int) ([]BuildSummary, error)
	// Repos are the repos a build touches (for the scope check of its log).
	Repos(ctx context.Context, id core.BuildID) ([]core.RepoID, error)
	Log(ctx context.Context, id core.BuildID, step, maxBytes int) (text string, truncated bool, err error)
	// Start queues the named steps (for example "verify") for one repo, in the
	// task's worktree when at.Worktree is set. It returns at once.
	Start(ctx context.Context, task core.TaskID, at Loc, steps []string) (core.BuildID, error)
	RunPreset(ctx context.Context, task core.TaskID, presetID string) (core.BuildID, error)
	// InstallStep is the ID of the repo's install step ("" when it has none).
	InstallStep(ctx context.Context, repo core.RepoID) (string, error)
}

// GitOutcome is the result of a fetch or pull.
type GitOutcome struct {
	// Text is a plain sentence for the agent.
	Text string
	// From and To are the commits before and after a pull.
	From, To string
}

// GitActions runs the safe git actions of the repos service (fast-forward-only
// pull, clean tree, no build or editor on the main checkout; audited).
type GitActions interface {
	Fetch(ctx context.Context, task core.TaskID, repo core.RepoID) (GitOutcome, error)
	Pull(ctx context.Context, task core.TaskID, repo core.RepoID) (GitOutcome, error)
}

// RemoteInfo is cached host data for a repo. No network call is made.
type RemoteInfo struct {
	// GitLab says the remote is a GitLab instance (merge requests supported).
	GitLab bool
	Lines  []string
}

// Hosts reads the hosts cache.
type Hosts interface {
	Remote(ctx context.Context, repo core.RepoID) (RemoteInfo, error)
}

// DevServer is a running dev server.
type DevServer struct {
	Name   string
	Repo   core.RepoID
	Status string
}

// DevServers answers the dev-server tools. Until dev servers exist the real
// implementation reports none.
type DevServers interface {
	List(ctx context.Context, project core.ProjectID) ([]DevServer, error)
	// Log returns the log of a dev server; ErrNoDevServer when there is none.
	Log(ctx context.Context, project core.ProjectID, name string, maxBytes int) (string, error)
}

// ErrNoDevServer: there is no dev server with that name.
var ErrNoDevServer = errors.New("mcp: no such dev server")

// ProposalRef is what a created proposal looks like to the agent.
type ProposalRef struct {
	ID     core.ProposalID
	Status string
	// Lines are the exact lines the user will be asked to confirm.
	Lines []string
}

// Proposer creates pending proposals. It is the only way an agent changes
// anything; it has no confirm, reject or execute.
type Proposer interface {
	Create(ctx context.Context, task core.TaskID, action coreprop.Action, summary string) (ProposalRef, error)
}

// AuditRecord is one audit entry the MCP server asks for. The adapter makes it
// with audit.Prepare and commits it.
type AuditRecord struct {
	Task    core.TaskID
	Tool    cmcp.Tool
	Subject string
	// Detail is JSON-encodable, small, and holds no file content.
	Detail map[string]any
}

// Auditor writes the audit entry of a state-changing call (invariant 8). A
// call whose entry cannot be written does not run.
type Auditor interface {
	Record(ctx context.Context, r AuditRecord) error
}

// userMessager is implemented by errors that carry a plain-language message
// for people (apperr.Error, proposals.Refusal, git errors).
type userMessager interface{ UserMessage() string }
