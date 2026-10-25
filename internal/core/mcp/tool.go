package mcp

import (
	"fmt"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Tool is the name of an MCP tool. The set is closed: AllTools lists it.
type Tool string

const (
	ToolListRepos        Tool = "list_repos"
	ToolRepoStatus       Tool = "repo_status"
	ToolRemoteStatus     Tool = "remote_status"
	ToolRecentCommits    Tool = "recent_commits"
	ToolChangedFiles     Tool = "changed_files"
	ToolGetDiff          Tool = "get_diff"
	ToolBuildInfo        Tool = "build_info"
	ToolBuildOrder       Tool = "build_order"
	ToolListBuildPresets Tool = "list_build_presets"
	ToolListBuilds       Tool = "list_builds"
	ToolBuildLog         Tool = "build_log"
	ToolStartBuild       Tool = "start_build"
	ToolRunBuildPreset   Tool = "run_build_preset"
	ToolListDevServers   Tool = "list_dev_servers"
	ToolDevServerLog     Tool = "dev_server_log"
	ToolFetch            Tool = "fetch"
	ToolPull             Tool = "pull"
	ToolProposeCommit    Tool = "propose_commit"
	ToolProposePush      Tool = "propose_push"
	ToolProposeMR        Tool = "propose_merge_request"
	ToolProposeInstall   Tool = "propose_install"
	ToolProposeAgentFile Tool = "propose_agent_file"
	ToolProposeDelete    Tool = "propose_delete_files"
)

// AllTools is the closed set, in the order of docs/design/mcp.md.
func AllTools() []Tool {
	return []Tool{
		ToolListRepos, ToolRepoStatus, ToolRemoteStatus, ToolRecentCommits, ToolChangedFiles,
		ToolGetDiff, ToolBuildInfo, ToolBuildOrder, ToolListBuildPresets, ToolListBuilds,
		ToolBuildLog, ToolStartBuild, ToolRunBuildPreset, ToolListDevServers, ToolDevServerLog,
		ToolFetch, ToolPull, ToolProposeCommit, ToolProposePush, ToolProposeMR,
		ToolProposeInstall, ToolProposeAgentFile, ToolProposeDelete,
	}
}

// Validate reports whether t is in the closed set.
func (t Tool) Validate() error {
	for _, k := range AllTools() {
		if k == t {
			return nil
		}
	}
	return fmt.Errorf("unknown tool %q", string(t))
}

// Class is how a tool treats repos: Metadata tools may name any visible
// repo; Content and Action tools only repos in the task's scope (SF-02).
type Class string

const (
	ClassNone     Class = "none"
	ClassMetadata Class = "metadata"
	ClassContent  Class = "content"
	ClassAction   Class = "action"
)

// Caps is the core-side copy of the adapter capabilities the MCP rules need.
type Caps struct {
	Resume          bool
	PermissionHooks bool
	Enforcement Enforcement
}

// Enforcement is the core-side copy of the adapter's enforcement level
// (core packages cannot import each other or internal/agents).
type Enforcement string

const (
	// EnforcementHook: a pre-tool hook can block calls and fails closed.
	EnforcementHook Enforcement = "hook"
	// EnforcementLimited: Yggdrasil's limits hold only through flags.
	EnforcementLimited Enforcement = "limited"
)

// Validate reports whether e is one of the two known levels.
func (e Enforcement) Validate() error {
	switch e {
	case EnforcementHook, EnforcementLimited:
		return nil
	}
	return fmt.Errorf("unknown enforcement level %q", string(e))
}

// Context is what Offered and Authorize know about the calling task. The
// shell builds it from the session, the task and the visible repos.
type Context struct {
	Task core.TaskID
	// Live is false when the task has no live run (its session was revoked).
	Live bool
	// Mode is the mode of the current turn.
	Mode core.TaskMode
	// Scope are the repos the task may act on.
	Scope []core.RepoID
	// Project are the tracked, not untracked repos of the task's project.
	Project []core.RepoID
	// HasWorktree says which repos have a task worktree.
	HasWorktree map[core.RepoID]bool
	Caps        Caps
}

// Limits of the server (decision 5 of the MCP design).
const (
	Burst           = 30
	RatePerSecond   = 10.0
	MaxInFlight     = 4
	MaxResultBytes  = 64 * 1024
	MaxRequestBytes = 1 << 20
	MaxBuildSteps   = 50
)

// DenyReason says why a tool call is not allowed.
type DenyReason string

const (
	DenyNone       DenyReason = ""
	DenyUnknown    DenyReason = "unknown"
	DenyNoSession  DenyReason = "no_session"
	DenyNoWorktree DenyReason = "no_worktree"
	// DenyNoSuchRepo is the one answer for an untracked, an unknown and an
	// out-of-scope repo, so an agent cannot probe (M3).
	DenyNoSuchRepo DenyReason = "no_such_repo"
)

// Decision is the answer of Authorize.
type Decision struct {
	Allowed bool
	Reason  DenyReason
}
