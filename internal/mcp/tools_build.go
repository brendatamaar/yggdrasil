package mcp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/core"
	cmcp "github.com/brendatamaar/yggdrasil/internal/core/mcp"
)

// startableSteps are the step groups start_build accepts. "install" is not one
// of them: installing changes the checkout and is a proposal (invariant 2).
var startableSteps = []string{"verify", "build", "full_tests"}

const (
	msgNoBuild  = "No such build."
	msgNoPreset = "No such preset."
)

// runBuild answers the build and dev-server tools (MCP-02, MCP-03).
func (rc *reqCtx) runBuild(ctx context.Context, c cmcp.Call) (result, error) {
	bs := rc.s.cfg.Builds
	switch v := c.(type) {
	case cmcp.BuildInfo:
		r, err := rc.repo(cmcp.ToolBuildInfo, v.Repo)
		if err != nil {
			return result{}, err
		}
		lines, err := bs.Info(ctx, r.ID)
		if err != nil {
			return result{}, fmt.Errorf("build info %s: %w", r.ID, err)
		}
		if len(lines) == 0 {
			return result{text: r.Name + ": no build toolchain was detected."}, nil
		}
		return result{text: r.Name + "\n" + strings.Join(lines, "\n")}, nil

	case cmcp.BuildOrder:
		order, err := bs.Order(ctx, rc.snap.Project)
		if err != nil {
			return result{}, fmt.Errorf("build order: %w", err)
		}
		var names []string
		for _, id := range order {
			if n, ok := rc.name(id); ok {
				names = append(names, n)
			}
		}
		if len(names) == 0 {
			return result{text: "No repos to order."}, nil
		}
		return result{text: strings.Join(names, "\n")}, nil

	case cmcp.ListBuildPresets:
		ps, err := bs.Presets(ctx, rc.snap.Project)
		if err != nil {
			return result{}, fmt.Errorf("list presets: %w", err)
		}
		var b strings.Builder
		for _, p := range ps {
			names, ok := rc.names(p.Repos)
			if !ok {
				continue
			}
			fmt.Fprintf(&b, "%s (id %s): %s; steps %s\n", p.Name, p.ID, strings.Join(names, ", "), strings.Join(p.Steps, ", "))
		}
		if b.Len() == 0 {
			return result{text: "No build presets."}, nil
		}
		return result{text: b.String()}, nil

	case cmcp.ListBuilds:
		list, err := bs.List(ctx, rc.snap.Project, v.Limit)
		if err != nil {
			return result{}, fmt.Errorf("list builds: %w", err)
		}
		var b strings.Builder
		for _, x := range list {
			names, ok := rc.names(x.Repos)
			if !ok {
				continue
			}
			fmt.Fprintf(&b, "%s %s %s\n", x.ID, x.Status, strings.Join(names, ", "))
		}
		if b.Len() == 0 {
			return result{text: "No builds yet."}, nil
		}
		return result{text: b.String()}, nil

	case cmcp.BuildLog:
		repos, err := bs.Repos(ctx, v.Build)
		if err != nil || len(repos) == 0 || !rc.allInScope(repos) {
			// Unknown and out-of-scope builds answer alike.
			return result{}, userErr(msgNoBuild)
		}
		text, cut, err := bs.Log(ctx, v.Build, v.Step, v.MaxBytes)
		if err != nil {
			return result{}, fmt.Errorf("build log %s: %w", v.Build, err)
		}
		if text == "" {
			return result{text: "The log is empty."}, nil
		}
		return result{text: text, truncated: cut}, nil

	case cmcp.StartBuild:
		return rc.startBuild(ctx, v)

	case cmcp.RunBuildPreset:
		return rc.runPreset(ctx, v)

	case cmcp.ListDevServers:
		ds, err := rc.s.cfg.DevServers.List(ctx, rc.snap.Project)
		if err != nil {
			return result{}, fmt.Errorf("list dev servers: %w", err)
		}
		var b strings.Builder
		for _, d := range ds {
			if n, ok := rc.name(d.Repo); ok {
				fmt.Fprintf(&b, "%s (%s) %s\n", d.Name, n, d.Status)
			}
		}
		if b.Len() == 0 {
			return result{text: "No dev servers are running."}, nil
		}
		return result{text: b.String()}, nil

	case cmcp.DevServerLog:
		text, err := rc.s.cfg.DevServers.Log(ctx, rc.snap.Project, v.Name, cmcp.MaxLogBytes)
		if errors.Is(err, ErrNoDevServer) {
			return result{}, userErr("No such dev server.")
		}
		if err != nil {
			return result{}, fmt.Errorf("dev server log: %w", err)
		}
		return result{text: text}, nil
	}
	return result{}, fmt.Errorf("runBuild: unhandled %T", c)
}

// names maps repo IDs to names; ok is false unless every repo is visible.
func (rc *reqCtx) names(ids []core.RepoID) ([]string, bool) {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		n, ok := rc.name(id)
		if !ok {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

func (rc *reqCtx) allInScope(ids []core.RepoID) bool {
	for _, id := range ids {
		if !slices.Contains(rc.cctx.Scope, id) || !slices.Contains(rc.cctx.Project, id) {
			return false
		}
	}
	return true
}

func (rc *reqCtx) startBuild(ctx context.Context, v cmcp.StartBuild) (result, error) {
	steps := v.Steps
	if len(steps) == 0 {
		steps = []string{"verify"}
	}
	for _, s := range steps {
		if !slices.Contains(startableSteps, s) {
			return result{}, userErr(fmt.Sprintf("Unknown build step %q. Use: %s. Installing is a proposal: use propose_install.",
				s, strings.Join(startableSteps, ", ")))
		}
	}
	var repos []RepoView
	for _, ref := range v.Repos {
		r, err := rc.repo(cmcp.ToolStartBuild, ref)
		if err != nil {
			return result{}, err
		}
		if !slices.ContainsFunc(repos, func(x RepoView) bool { return x.ID == r.ID }) {
			repos = append(repos, r)
		}
	}
	names := make([]string, len(repos))
	for i, r := range repos {
		names[i] = r.Name
	}
	if err := rc.audit(ctx, cmcp.ToolStartBuild, "task:"+string(rc.act.Task),
		map[string]any{"repos": names, "steps": steps}); err != nil {
		return result{}, err
	}
	var b strings.Builder
	for _, r := range repos {
		id, err := rc.s.cfg.Builds.Start(ctx, rc.act.Task, r.loc(), steps)
		if err != nil {
			m, ok := userMessage(err)
			if !ok {
				rc.s.cfg.Logger.Error("mcp start build failed", "call", rc.id, "repo", r.ID, "err", rc.s.sanitize(err.Error()))
				m = genericFailure(rc.id)
			}
			fmt.Fprintf(&b, "%s: not started. %s\n", r.Name, m)
			continue
		}
		where := "main checkout"
		if r.Worktree != "" {
			where = "task worktree"
		}
		fmt.Fprintf(&b, "%s: build %s queued (%s). Read it with build_log.\n", r.Name, id, where)
	}
	return result{text: b.String()}, nil
}

func (rc *reqCtx) runPreset(ctx context.Context, v cmcp.RunBuildPreset) (result, error) {
	ps, err := rc.s.cfg.Builds.Presets(ctx, rc.snap.Project)
	if err != nil {
		return result{}, fmt.Errorf("list presets: %w", err)
	}
	for _, p := range ps {
		if p.ID != v.Preset && !strings.EqualFold(p.Name, v.Preset) {
			continue
		}
		ids := make([]core.RepoID, len(p.Repos))
		copy(ids, p.Repos)
		// Every repo of the preset must be one the task works in; otherwise
		// the preset does not exist as far as the agent can tell.
		if len(ids) == 0 || !cmcp.Authorize(rc.cctx, cmcp.ToolRunBuildPreset, ids...).Allowed {
			return result{}, userErr(msgNoPreset)
		}
		if err := rc.audit(ctx, cmcp.ToolRunBuildPreset, "task:"+string(rc.act.Task),
			map[string]any{"preset": p.Name}); err != nil {
			return result{}, err
		}
		id, err := rc.s.cfg.Builds.RunPreset(ctx, rc.act.Task, p.ID)
		if err != nil {
			return result{}, fmt.Errorf("run preset %s: %w", p.ID, err)
		}
		return result{text: fmt.Sprintf("Preset %s: build %s queued. Read it with build_log.", p.Name, id)}, nil
	}
	return result{}, userErr(msgNoPreset)
}
