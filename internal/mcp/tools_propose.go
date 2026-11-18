package mcp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	cmcp "github.com/brendatamaar/yggdrasil/internal/core/mcp"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
)

// runPropose answers the propose_* tools (MCP-05). Each one creates a pending
// proposal through the proposals service and returns; nothing is confirmed or
// executed here, and no tool can do either (M4). propose_subtasks is not
// registered in this phase, and no tool proposes creating a repo: that starts
// only from the user's new repo dialog (MCP-05).
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
	case cmcp.ProposeIgnore:
		tool, ref, summary = cmcp.ToolProposeIgnore, v.Repo, v.Summary
	case cmcp.ProposeReviewComment:
		tool, ref, summary = cmcp.ToolProposeReviewComment, v.Repo, v.Summary
	case cmcp.ProposePublish:
		tool, ref, summary = cmcp.ToolProposePublish, v.Repo, v.Summary
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

	var (
		action coreprop.Action
		plan   *PublishPlan // set for propose_publish: bound to the proposal after it exists
		detail = map[string]any{}
	)
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
	case cmcp.ProposeIgnore:
		// Always the main checkout's top-level .gitignore: the proposals service
		// pins the file's current checksum when it creates the proposal.
		action = coreprop.IgnoreEntries{Where: coreprop.MainCheckout{Repo: r.ID}, Entries: slices.Clone(v.Entries)}
	case cmcp.ProposeReviewComment:
		marker, err := newMarker()
		if err != nil {
			return result{}, fmt.Errorf("make comment marker: %w", err)
		}
		// The comment is posted to a host: scrub secrets and machine paths first.
		// What the user confirms is the scrubbed text.
		action = coreprop.ReviewComment{Repo: r.ID, MRNumber: v.MR, ExpectedHead: v.ExpectedHead,
			Body: rc.sanitize(v.Body), Marker: marker}
		detail["mr"] = v.MR
	case cmcp.ProposePublish:
		p, err := rc.publishPlan(ctx, r, v)
		if err != nil {
			return result{}, err
		}
		plan = &p
		action = coreprop.PublishRepo{Operation: p.Operation, Repo: r.ID, HostInstance: p.Host, Owner: p.Owner,
			OwnerKind: p.OwnerKind, Name: p.Name, Visibility: p.Visibility, Branch: p.Branch,
			ExpectedHead: p.ExpectedHead, Marker: p.Marker}
	}
	// The audit entry names the tool and repo only, never file content or a
	// commit message.
	detail["repo"], detail["kind"] = r.Name, string(action.Kind())
	if err := rc.audit(ctx, tool, "repo:"+string(r.ID), detail); err != nil {
		return result{}, err
	}
	if err := rc.live(); err != nil {
		return result{}, err
	}
	ref2, err := rc.s.cfg.Proposer.Create(ctx, rc.act.Task, action, summary)
	if err != nil {
		return result{}, fmt.Errorf("create proposal: %w", err)
	}
	if plan != nil {
		// The confirmation of a publish finds its destination through this
		// binding. If it fails the proposal exists but could not be confirmed,
		// so say so: the user rejects it and the agent asks again.
		if err := rc.s.cfg.Publish.Bind(ctx, *plan, ref2.ID); err != nil {
			rc.s.cfg.Logger.Error("mcp publish bind failed", "call", rc.id, "task", rc.act.Task, "proposal", ref2.ID, "err", rc.sanitize(err.Error()))
			return result{}, userErr(fmt.Sprintf("Proposal %s was created but could not be prepared for confirmation. Nothing has been done. Ask the user to reject it, then propose again.", ref2.ID))
		}
	}
	return result{text: proposalText(ref2)}, nil
}

// publishPlan asks the publishing service for the immutable values of a publish
// proposal: the new repo operation of this repo, the commit that would be
// pushed and the operation marker. The agent supplies only the destination; it
// can name no operation, branch or commit, so it can publish nothing but the
// first commit of a repo made with the new repo dialog. It writes nothing.
func (rc *reqCtx) publishPlan(ctx context.Context, r RepoView, v cmcp.ProposePublish) (PublishPlan, error) {
	pub := rc.s.cfg.Publish
	if pub == nil {
		return PublishPlan{}, userErr("Publishing is not available.")
	}
	p, err := pub.Plan(ctx, r.ID, PublishRequest{Host: v.Host, Owner: v.Owner, OwnerKind: v.OwnerKind, Name: v.Name, Visibility: v.Visibility})
	switch {
	case errors.Is(err, ErrNotPublishable):
		return PublishPlan{}, userErr(r.Name + " cannot be published from here. Only a repo made with Yggdrasil's new repo dialog, that is ready and not published yet, can be.")
	case err != nil:
		return PublishPlan{}, fmt.Errorf("plan publish %s: %w", r.ID, err)
	}
	if p.Repo != r.ID {
		// A plan for another repo is never used.
		return PublishPlan{}, userErr(msgNoRepo)
	}
	if len(p.Problems) > 0 {
		return PublishPlan{}, userErr("This cannot be published yet: " + strings.Join(p.Problems, " "))
	}
	return p, nil
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
