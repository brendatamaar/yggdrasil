package mcp

import (
	"context"
	"fmt"

	cmcp "github.com/brendatamaar/yggdrasil/internal/core/mcp"
)

// runGit answers fetch and pull (MCP-04). Both go through the repos actions
// service, which applies the same guards as the UI: pull is fast-forward-only,
// needs a clean tree and no build or editing task holding the main checkout,
// and records the old and new commit in the audit log. The one reading of
// invariant 2 this allows is recorded in docs/threat-model.md.
func (rc *reqCtx) runGit(ctx context.Context, c cmcp.Call) (result, error) {
	g := rc.s.cfg.Git
	switch v := c.(type) {
	case cmcp.Fetch:
		r, err := rc.repo(cmcp.ToolFetch, v.Repo)
		if err != nil {
			return result{}, err
		}
		if err := rc.audit(ctx, cmcp.ToolFetch, "repo:"+string(r.ID), map[string]any{"repo": r.Name}); err != nil {
			return result{}, err
		}
		if err := rc.live(); err != nil {
			return result{}, err
		}
		o, err := g.Fetch(ctx, rc.act.Task, r.ID)
		if err != nil {
			return result{}, fmt.Errorf("fetch %s: %w", r.ID, err)
		}
		return result{text: r.Name + ": " + o.Text}, nil

	case cmcp.Pull:
		r, err := rc.repo(cmcp.ToolPull, v.Repo)
		if err != nil {
			return result{}, err
		}
		if err := rc.audit(ctx, cmcp.ToolPull, "repo:"+string(r.ID), map[string]any{"repo": r.Name}); err != nil {
			return result{}, err
		}
		if err := rc.live(); err != nil {
			return result{}, err
		}
		o, err := g.Pull(ctx, rc.act.Task, r.ID)
		if err != nil {
			return result{}, fmt.Errorf("pull %s: %w", r.ID, err)
		}
		return result{text: r.Name + ": " + o.Text}, nil
	}
	return result{}, fmt.Errorf("runGit: unhandled %T", c)
}
