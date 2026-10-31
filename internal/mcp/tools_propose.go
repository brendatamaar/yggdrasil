package mcp

import (
	"context"
	"fmt"
	"strings"

	cmcp "github.com/brendatamaar/yggdrasil/internal/core/mcp"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
)

// runPropose answers the propose_* tools (MCP-05). Each one creates a pending
// proposal through the proposals service and returns; nothing is confirmed or
// executed here, and no tool can do either (M4). propose_publish and
// propose_subtasks are not registered in this phase.
func (rc *reqCtx) runPropose(ctx context.Context, c cmcp.Call) (result, error) {
	var (
		tool    cmcp.Tool
		ref     cmcp.RepoRef
		summary string
	)
	switch v := c.(type) {
	case cmcp.ProposeCommit:
		tool, ref, summary = cmcp.ToolProposeCommit, v.Repo, v.Summary
	case cmcp.ProposePush:
		tool, ref, summary = cmcp.ToolProposePush, v.Repo, v.Summary
	case cmcp.ProposeMergeRequest:
		tool, ref, summary = cmcp.ToolProposeMR, v.Repo, v.Summary
	case cmcp.ProposeInstall:
		tool, ref, summary = cmcp.ToolProposeInstall, v.Repo, v.Summary
	case cmcp.ProposeAgentFile:
		tool, ref, summary = cmcp.ToolProposeAgentFile, v.Repo, v.Summary
	case cmcp.ProposeDeleteFiles:
		tool, ref, summary = cmcp.ToolProposeDelete, v.Repo, v.Summary
	default:
		return result{}, fmt.Errorf("runPropose: unhandled %T", c)
	}
	r, err := rc.repo(tool, ref)
	if err != nil {
		return result{}, err
	}
	where := coreprop.Target(coreprop.MainCheckout{Repo: r.ID})
	if r.Worktree != "" {
		where = coreprop.InWorktree{ID: r.Worktree}
	}

	var action coreprop.Action
	switch v := c.(type) {
	case cmcp.ProposeCommit:
		action = coreprop.Commit{Where: where, Paths: relPaths(v.Paths), Message: v.Message}
	case cmcp.ProposePush:
		action = coreprop.Push{Where: where, Branch: v.Branch, Remote: v.Remote}
	case cmcp.ProposeMergeRequest:
		ri, err := rc.s.cfg.Hosts.Remote(ctx, r.ID)
		if err != nil || !ri.GitLab {
			return result{}, userErr("Merge requests are only supported for GitLab remotes.")
		}
		action = coreprop.MergeRequest{Where: where, Branch: v.Branch, Remote: v.Remote,
			TargetBranch: v.Target, Title: v.Title, Description: v.Description}
	case cmcp.ProposeInstall:
		step, err := rc.s.cfg.Builds.InstallStep(ctx, r.ID)
		if err != nil {
			return result{}, fmt.Errorf("find install step %s: %w", r.ID, err)
		}
		if step == "" {
			return result{}, userErr(r.Name + " has no install command Yggdrasil knows.")
		}
		action = coreprop.Install{Repo: r.ID, Step: step}
	case cmcp.ProposeAgentFile:
		action = coreprop.AgentFile{Where: where, Path: coreprop.RelPath(v.Path), Content: v.Content}
	case cmcp.ProposeDeleteFiles:
		action = coreprop.DeleteFiles{Where: where, Paths: relPaths(v.Paths)}
	}
	// The audit entry names the tool and repo only, never file content or a
	// commit message.
	if err := rc.audit(ctx, tool, "repo:"+string(r.ID), map[string]any{"repo": r.Name, "kind": string(action.Kind())}); err != nil {
		return result{}, err
	}
	if err := rc.live(); err != nil {
		return result{}, err
	}
	ref2, err := rc.s.cfg.Proposer.Create(ctx, rc.act.Task, action, summary)
	if err != nil {
		return result{}, fmt.Errorf("create proposal: %w", err)
	}
	return result{text: proposalText(ref2)}, nil
}

func relPaths(in []string) []coreprop.RelPath {
	out := make([]coreprop.RelPath, len(in))
	for i, p := range in {
		out[i] = coreprop.RelPath(p)
	}
	return out
}

func proposalText(p ProposalRef) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Proposal %s created, status %s. Nothing has been done yet: the user must confirm it in Yggdrasil.", p.ID, p.Status)
	if len(p.Lines) > 0 {
		b.WriteString("\nThe user will be asked to confirm:\n")
		b.WriteString(strings.Join(p.Lines, "\n"))
	}
	return b.String()
}
