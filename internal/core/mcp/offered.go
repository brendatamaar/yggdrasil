package mcp

import (
	"slices"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// ClassOf is the repo class of a tool: which repos its arguments may name.
// An unknown tool has ClassNone.
func ClassOf(t Tool) Class {
	switch t {
	case ToolListRepos, ToolRepoStatus, ToolRemoteStatus, ToolBuildInfo, ToolBuildOrder,
		ToolListBuildPresets, ToolListBuilds, ToolListDevServers, ToolDevServerLog:
		return ClassMetadata
	case ToolRecentCommits, ToolChangedFiles, ToolGetDiff, ToolBuildLog:
		return ClassContent
	case ToolStartBuild, ToolRunBuildPreset, ToolFetch, ToolPull, ToolProposeCommit, ToolProposePush,
		ToolProposeMR, ToolProposeInstall, ToolProposeAgentFile, ToolProposeDelete:
		return ClassAction
	}
	return ClassNone
}

// needsWorktree: the tools that act on a task branch (commit, push, MR).
func needsWorktree(t Tool) bool {
	return t == ToolProposeCommit || t == ToolProposePush || t == ToolProposeMR
}

// Offered lists the tools a task is offered in a context (the Offered table of
// docs/design/mcp.md), in AllTools order. A dead session is offered nothing.
// The "remote is GitLab" condition of propose_merge_request needs host data
// and is checked by the shell.
func Offered(c Context) []Tool {
	var out []Tool
	for _, t := range AllTools() {
		if Authorize(c, t).Allowed {
			out = append(out, t)
		}
	}
	return out
}

// Authorize decides whether a call of tool t is allowed in the context. repos
// are the repos the call names, already resolved by the shell against the
// visible set by name or ID; a reference that did not resolve is passed as the
// empty RepoID. With no repos it answers the tool-level question and agrees
// with Offered for every (context, tool) (M1).
//
// Every repo must be visible (in c.Project) and, for content and action tools,
// in c.Scope; otherwise the answer is DenyNoSuchRepo, the same for an
// untracked, an unknown and an out-of-scope repo (M3). The worktree check runs
// only after the repo checks, so it cannot reveal anything about a hidden repo.
func Authorize(c Context, t Tool, repos ...core.RepoID) Decision {
	class := ClassOf(t)
	if class == ClassNone {
		return Decision{Reason: DenyUnknown}
	}
	if !c.Live {
		return Decision{Reason: DenyNoSession}
	}
	if c.Mode.Validate() != nil {
		return Decision{Reason: DenyUnknown}
	}
	for _, r := range repos {
		if r == "" || !slices.Contains(c.Project, r) || (class != ClassMetadata && !slices.Contains(c.Scope, r)) {
			return Decision{Reason: DenyNoSuchRepo}
		}
	}
	if needsWorktree(t) && c.Mode != core.ModeCurrentBranch {
		if len(repos) == 0 {
			if !slices.ContainsFunc(c.Scope, func(r core.RepoID) bool { return c.HasWorktree[r] }) {
				return Decision{Reason: DenyNoWorktree}
			}
		}
		for _, r := range repos {
			if !c.HasWorktree[r] {
				return Decision{Reason: DenyNoWorktree}
			}
		}
	}
	return Decision{Allowed: true}
}
