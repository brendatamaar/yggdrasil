package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/audit"
	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/builds/toolchains"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/hosts"
	"github.com/brendatamaar/yggdrasil/internal/mcp"
	"github.com/brendatamaar/yggdrasil/internal/proposals"
	"github.com/brendatamaar/yggdrasil/internal/repos"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// This file wires the MCP server (MCP-01 to MCP-06) to the real services: each
// adapter turns one service into the small interface internal/mcp defines.
// Locations are repo and worktree IDs; paths are resolved here and never leave.

// mcpService is the api.MCPService: the server plus its public address. It is
// also where a run gets and gives back its MCP session.
type mcpService struct {
	*mcp.Server
	mu   sync.Mutex
	addr string
}

var _ api.MCPService = (*mcpService)(nil)

// Status implements api.MCPService.
func (m *mcpService) Status(context.Context) api.McpStatusDTO {
	return api.McpStatusDTO{Enabled: true, Url: m.url(), ActiveSessions: m.ActiveSessions()}
}

// setAddr records the listener address once the server is bound.
func (m *mcpService) setAddr(a net.Addr) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.addr = a.String()
}

func (m *mcpService) url() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.addr == "" {
		return ""
	}
	return "http://" + m.addr + "/mcp"
}

// BeginRun gives a run its MCP session: a new token, written into the agent's
// config file in dir (the run's own folder; never a command line). The path
// goes to agents.StartOptions.MCPConfigPath. The tasks service calls it before
// it starts the agent and calls EndRun when the run ends or is recovered.
func (m *mcpService) BeginRun(a agents.Adapter, task core.TaskID, attempt int, dir string) (string, error) {
	cfg, ok := a.(agents.MCPConfigurer)
	if !ok {
		return "", nil // this agent cannot use Yggdrasil's tools
	}
	url := m.url()
	if url == "" {
		return "", errors.New("the MCP server has no address yet")
	}
	token, err := m.Issue(task, attempt)
	if err != nil {
		return "", err
	}
	path, err := cfg.WriteMCPConfig(dir, url, token)
	if err != nil {
		m.Revoke(task, attempt)
		return "", fmt.Errorf("write the MCP config for task %s: %w", task, err)
	}
	return path, nil
}

// EndRun revokes the task's session.
func (m *mcpService) EndRun(task core.TaskID, attempt int) { m.Revoke(task, attempt) }

// newMCP builds the MCP server over the real services.
func newMCP(store *db.Store, g *git.Git, buildSvc *builds.Service, hostSvc *hosts.Service, actions *repos.ActionsService,
	propSvc *proposals.Service, redactor *safety.SecretRedactor, version string) (*mcpService, error) {
	srv, err := mcp.New(mcp.Config{
		Workspace:  mcpWorkspace{store: store},
		Reader:     mcpReader{store: store, git: g},
		Builds:     mcpBuilds{svc: buildSvc, store: store, toolchains: builtinToolchains()},
		Git:        &mcpGit{actions: actions},
		Hosts:      mcpHosts{svc: hostSvc},
		DevServers: mcp.NoDevServers{},
		Proposer:   mcpProposer{svc: propSvc},
		Audit:      mcpAudit{store: store, redactor: redactor},
		Redactor:   redactor,
		Version:    version,
	})
	if err != nil {
		return nil, fmt.Errorf("start the MCP server: %w", err)
	}
	return &mcpService{Server: srv}, nil
}

func mcpActor(task core.TaskID) string { return "agent:" + string(task) }

// mcpWorkspace builds a task's snapshot from the database on every call, so a
// repo untracked mid-run disappears at the next call (SF-02).
type mcpWorkspace struct{ store *db.Store }

func (w mcpWorkspace) Snapshot(ctx context.Context, id core.TaskID) (mcp.Snapshot, error) {
	t, err := w.store.GetTask(ctx, id)
	if errors.Is(err, db.ErrNotFound) {
		return mcp.Snapshot{}, mcp.ErrNoTask
	}
	if err != nil {
		return mcp.Snapshot{}, fmt.Errorf("read task %s: %w", id, err)
	}
	rs, err := w.store.ListRepos(ctx, t.ProjectID) // tracked repos only
	if err != nil {
		return mcp.Snapshot{}, err
	}
	wts, err := w.store.ListWorktrees(ctx, id)
	if err != nil {
		return mcp.Snapshot{}, err
	}
	snap := mcp.Snapshot{Project: t.ProjectID, Mode: t.Mode, AgentName: t.Agent}
	for _, r := range rs {
		v := mcp.RepoView{ID: r.ID, Name: r.Name, DefaultBranch: r.DefaultBranch, InScope: slices.Contains(t.RepoIDs, r.ID)}
		for _, wt := range wts {
			if wt.RepoID == r.ID && wt.Status == core.WorktreeActive {
				v.Worktree, v.WorktreeBranch = wt.ID, wt.Branch
			}
		}
		snap.Repos = append(snap.Repos, v)
	}
	return snap, nil
}

// mcpReader reads git state in the main checkout or a task worktree.
type mcpReader struct {
	store *db.Store
	git   *git.Git
}

// dir resolves a location to a folder: the repo must be tracked and a worktree
// must belong to that repo.
func (r mcpReader) dir(ctx context.Context, at mcp.Loc) (string, error) {
	repo, err := r.store.GetRepo(ctx, at.Repo)
	if err != nil {
		return "", fmt.Errorf("read repo %s: %w", at.Repo, err)
	}
	if at.Worktree == "" {
		return repo.Path, nil
	}
	wt, err := r.store.GetWorktree(ctx, at.Worktree)
	if err != nil {
		return "", fmt.Errorf("read worktree %s: %w", at.Worktree, err)
	}
	if wt.RepoID != repo.ID {
		return "", fmt.Errorf("worktree %s does not belong to repo %s", wt.ID, repo.ID)
	}
	return wt.Path, nil
}

func (r mcpReader) Status(ctx context.Context, at mcp.Loc) (mcp.RepoStatus, error) {
	dir, err := r.dir(ctx, at)
	if err != nil {
		return mcp.RepoStatus{}, err
	}
	st, err := r.git.Status(ctx, dir)
	if err != nil {
		return mcp.RepoStatus{}, err
	}
	return mcp.RepoStatus{
		Branch: st.Branch, Detached: st.Detached, Upstream: st.Upstream, Ahead: st.Ahead, Behind: st.Behind,
		Staged: st.Staged, Unstaged: st.Unstaged, Untracked: st.Untracked, Conflicted: st.Conflicted, Head: shortOID(st.OID),
	}, nil
}

func shortOID(s string) string {
	if len(s) > 10 {
		return s[:10]
	}
	return s
}

func (r mcpReader) Commits(ctx context.Context, at mcp.Loc, limit int) ([]mcp.Commit, error) {
	dir, err := r.dir(ctx, at)
	if err != nil {
		return nil, err
	}
	cs, err := r.git.Log(ctx, dir, limit)
	if err != nil {
		return nil, err
	}
	out := make([]mcp.Commit, len(cs))
	for i, c := range cs {
		out[i] = mcp.Commit{ID: c.Short, Author: c.Author, When: c.Date.UTC().Format("2006-01-02"), Subject: c.Subject}
	}
	return out, nil
}

func (r mcpReader) Changes(ctx context.Context, at mcp.Loc) ([]mcp.Change, error) {
	dir, err := r.dir(ctx, at)
	if err != nil {
		return nil, err
	}
	st, err := r.git.Status(ctx, dir)
	if err != nil {
		return nil, err
	}
	out := make([]mcp.Change, len(st.Files))
	for i, f := range st.Files {
		out[i] = mcp.Change{Path: f.Path, Status: strings.TrimSpace(strings.ReplaceAll(f.Index+f.Worktree, ".", " "))}
	}
	return out, nil
}

func (r mcpReader) Diff(ctx context.Context, at mcp.Loc, path string, maxBytes int) (string, bool, error) {
	dir, err := r.dir(ctx, at)
	if err != nil {
		return "", false, err
	}
	text, err := r.git.Diff(ctx, dir, git.DiffOptions{Mode: git.DiffWorktree, Path: path, Limit: maxBytes})
	if err != nil {
		return "", false, err
	}
	if len(text) > maxBytes {
		return text[:maxBytes], true, nil
	}
	return text, false, nil
}

// builtinToolchains is the toolchain set the build tools detect with.
func builtinToolchains() []builds.Toolchain { return toolchains.All() }

// mcpBuilds adapts the builds service.
type mcpBuilds struct {
	svc        *builds.Service
	store      *db.Store
	toolchains []builds.Toolchain
}

// detect returns the commands of every toolchain found at the repo root.
func (b mcpBuilds) detect(ctx context.Context, repo core.RepoID) ([]builds.Commands, []builds.Toolchain, error) {
	r, err := b.store.GetRepo(ctx, repo)
	if err != nil {
		return nil, nil, fmt.Errorf("read repo %s: %w", repo, err)
	}
	fsys := os.DirFS(r.Path)
	var cmds []builds.Commands
	var tcs []builds.Toolchain
	for _, tc := range b.toolchains {
		d, ok := tc.Detect(fsys)
		if !ok || (d.Dir != "." && d.Dir != "") {
			continue
		}
		cmds = append(cmds, tc.Commands(d))
		tcs = append(tcs, tc)
	}
	return cmds, tcs, nil
}

func stepNames(ss []builds.Step) string {
	n := make([]string, len(ss))
	for i, s := range ss {
		n[i] = s.Name
	}
	return strings.Join(n, ", ")
}

func (b mcpBuilds) Info(ctx context.Context, repo core.RepoID) ([]string, error) {
	cmds, tcs, err := b.detect(ctx, repo)
	if err != nil {
		return nil, err
	}
	var out []string
	for i, c := range cmds {
		line := tcs[i].Name() + ":"
		for _, g := range []struct {
			name  string
			steps []builds.Step
		}{{"install", c.Install}, {"verify", c.Verify}, {"full_tests", c.FullTests}, {"build", c.Build}, {"dev server", c.DevServer}} {
			if len(g.steps) > 0 {
				line += fmt.Sprintf(" %s [%s];", g.name, stepNames(g.steps))
			}
		}
		out = append(out, line)
	}
	return out, nil
}

func (b mcpBuilds) Order(ctx context.Context, project core.ProjectID) ([]core.RepoID, error) {
	rs, err := b.store.ListRepos(ctx, project)
	if err != nil {
		return nil, err
	}
	var mods []corebuilds.Module
	inOrder := map[core.RepoID]bool{}
	for _, r := range rs {
		fsys := os.DirFS(r.Path)
		for _, tc := range b.toolchains {
			o, ok := tc.(builds.Orderer)
			if !ok {
				continue
			}
			d, found := tc.Detect(fsys)
			if !found {
				continue
			}
			ms, err := o.Modules(fsys, d)
			if err != nil {
				continue // an unreadable manifest only leaves the repo unordered
			}
			for _, m := range ms {
				m.RepoID = r.ID
				mods = append(mods, m)
				inOrder[r.ID] = true
			}
		}
	}
	order, _ := corebuilds.OrderModules(mods)
	seen := map[core.RepoID]bool{}
	var out []core.RepoID
	for _, id := range order {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, r := range rs {
		if !seen[r.ID] {
			out = append(out, r.ID)
		}
	}
	return out, nil
}

func repoIDsOf(steps []corebuilds.StepSpec) []core.RepoID {
	var out []core.RepoID
	for _, s := range steps {
		if !slices.Contains(out, s.RepoID) {
			out = append(out, s.RepoID)
		}
	}
	return out
}

func (b mcpBuilds) Presets(ctx context.Context, project core.ProjectID) ([]mcp.BuildPreset, error) {
	ps, err := b.svc.ListPresets(ctx, project)
	if err != nil {
		return nil, err
	}
	out := make([]mcp.BuildPreset, len(ps))
	for i, p := range ps {
		out[i] = mcp.BuildPreset{ID: string(p.ID), Name: p.Name, Repos: repoIDsOf(p.Steps)}
		for _, s := range p.Steps {
			out[i].Steps = append(out[i].Steps, s.Name)
		}
	}
	return out, nil
}

// buildFacts reads the common fields of a build state.
func buildFacts(st corebuilds.State) (id core.BuildID, spec corebuilds.Spec, status string, ok bool) {
	switch s := st.(type) {
	case corebuilds.Queued:
		return s.ID, s.Spec, "queued", true
	case corebuilds.Running:
		return s.ID, s.Spec, "running", true
	case corebuilds.Finished:
		switch s.Result.(type) {
		case corebuilds.ResultSucceeded:
			return s.ID, s.Spec, "succeeded", true
		case corebuilds.ResultCancelled:
			return s.ID, s.Spec, "cancelled", true
		}
		return s.ID, s.Spec, "failed", true
	}
	return "", corebuilds.Spec{}, "", false
}

func (b mcpBuilds) List(ctx context.Context, project core.ProjectID, limit int) ([]mcp.BuildSummary, error) {
	states, err := b.svc.List(ctx, db.BuildFilter{Project: project, Limit: limit})
	if err != nil {
		return nil, err
	}
	var out []mcp.BuildSummary
	for _, st := range states {
		if id, spec, status, ok := buildFacts(st); ok {
			out = append(out, mcp.BuildSummary{ID: id, Status: status, Repos: repoIDsOf(spec.Steps)})
		}
	}
	return out, nil
}

func (b mcpBuilds) Repos(ctx context.Context, id core.BuildID) ([]core.RepoID, error) {
	st, err := b.svc.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	_, spec, _, ok := buildFacts(st)
	if !ok {
		return nil, fmt.Errorf("build %s has no state", id)
	}
	return repoIDsOf(spec.Steps), nil
}

// Log returns the end of the step log: the part an agent needs is the failure.
func (b mcpBuilds) Log(ctx context.Context, id core.BuildID, step, maxBytes int) (string, bool, error) {
	data, _, err := b.svc.StepLog(ctx, id, step, 0)
	if err != nil {
		return "", false, err
	}
	if len(data) > maxBytes {
		return string(data[len(data)-maxBytes:]), true, nil
	}
	return string(data), false, nil
}

// Start queues the named step groups of the repo's detected toolchains.
func (b mcpBuilds) Start(ctx context.Context, task core.TaskID, at mcp.Loc, groups []string) (core.BuildID, error) {
	cmds, _, err := b.detect(ctx, at.Repo)
	if err != nil {
		return "", err
	}
	var steps []corebuilds.StepSpec
	for _, g := range groups {
		for _, c := range cmds {
			var src []builds.Step
			switch g {
			case "verify":
				src = c.Verify
			case "build":
				src = c.Build
			case "full_tests":
				src = c.FullTests
			}
			for _, s := range src {
				steps = append(steps, s.Spec(at.Repo))
			}
		}
	}
	if len(steps) == 0 {
		return "", apperr.New(apperr.Invalid, "This repo has no "+strings.Join(groups, "/")+" command configured.", nil)
	}
	spec := corebuilds.Spec{Target: corebuilds.MainCheckout{}, Steps: steps}
	if at.Worktree != "" {
		spec.Target = corebuilds.InWorktree{WorktreeID: at.Worktree}
	}
	return b.svc.Enqueue(ctx, spec, corebuilds.Actor(mcpActor(task)))
}

func (b mcpBuilds) RunPreset(ctx context.Context, task core.TaskID, presetID string) (core.BuildID, error) {
	return b.svc.RunPreset(ctx, corebuilds.PresetID(presetID), corebuilds.Actor(mcpActor(task)))
}

func (b mcpBuilds) InstallStep(ctx context.Context, repo core.RepoID) (string, error) {
	cmds, _, err := b.detect(ctx, repo)
	if err != nil {
		return "", err
	}
	for _, c := range cmds {
		if len(c.Install) > 0 {
			return c.Install[0].Name, nil
		}
	}
	return "", nil
}

// mcpGit runs fetch and pull through the repos actions service, with the same
// guards as the UI: pull is fast-forward-only, needs a clean tree, and is
// skipped while a build or an editing task holds the main checkout.
type mcpGit struct{ actions *repos.ActionsService }

func (g *mcpGit) run(ctx context.Context, kind corerepos.ActionKind, task core.TaskID, repo core.RepoID) (mcp.GitOutcome, error) {
	req, err := repos.ParseBatchRequest(string(kind), []string{string(repo)}, "")
	if err != nil {
		return mcp.GitOutcome{}, err
	}
	id, err := g.actions.Start(ctx, req, corerepos.Actor(mcpActor(task)))
	if err != nil {
		return mcp.GitOutcome{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		v, ok := g.actions.Get(id)
		if ok && v.Finished && len(v.Items) == 1 {
			return describeItem(v.Items[0]), nil
		}
		select {
		case <-ctx.Done():
			return mcp.GitOutcome{}, apperr.New(apperr.Unavailable, "The git action is taking too long. Check the repo in Yggdrasil.", ctx.Err())
		case <-t.C:
		}
	}
}

func describeItem(it repos.ItemView) mcp.GitOutcome {
	switch it.Outcome {
	case "fetched":
		return mcp.GitOutcome{Text: "fetched."}
	case "up_to_date":
		return mcp.GitOutcome{Text: "already up to date."}
	case "pulled":
		return mcp.GitOutcome{Text: "fast-forwarded.", From: it.From, To: it.To}
	}
	if it.Message != "" {
		return mcp.GitOutcome{Text: "not done. " + it.Message}
	}
	return mcp.GitOutcome{Text: "not done (" + it.State + " " + it.Reason + ")."}
}

func (g *mcpGit) Fetch(ctx context.Context, task core.TaskID, repo core.RepoID) (mcp.GitOutcome, error) {
	return g.run(ctx, corerepos.KindFetch, task, repo)
}

func (g *mcpGit) Pull(ctx context.Context, task core.TaskID, repo core.RepoID) (mcp.GitOutcome, error) {
	return g.run(ctx, corerepos.KindPull, task, repo)
}

// repoHoster is the part of the hosts service the tools read: cached data.
type repoHoster interface {
	RepoHost(ctx context.Context, repo core.RepoID) (api.RepoHostView, error)
}

// mcpHosts reads the hosts cache; it makes no network call.
type mcpHosts struct{ svc repoHoster }

func (h mcpHosts) Remote(ctx context.Context, repo core.RepoID) (mcp.RemoteInfo, error) {
	v, err := h.svc.RepoHost(ctx, repo)
	if err != nil {
		return mcp.RemoteInfo{}, err
	}
	if !v.Linked {
		return mcp.RemoteInfo{Lines: []string{v.NotLinked}}, nil
	}
	info := mcp.RemoteInfo{GitLab: v.Instance.Kind == corehosts.KindGitLab}
	var prev corehosts.Idle
	switch p := v.Entry.Phase.(type) {
	case corehosts.Idle:
		prev = p
	case corehosts.Fetching:
		prev = p.Prev
	case corehosts.Waiting:
		prev = p.Prev
	case corehosts.Blocked:
		prev = p.Prev
		info.Lines = append(info.Lines, "The host did not answer the last time (check the token in Yggdrasil).")
	}
	if rs, ok := prev.Data.(corehosts.RepoState); ok {
		if rs.MR != nil {
			info.Lines = append(info.Lines, fmt.Sprintf("Merge request !%d %q (%s): %s into %s", rs.MR.Number, rs.MR.Title, rs.MR.State, rs.MR.Source, rs.MR.Target))
		} else {
			info.Lines = append(info.Lines, "No merge request for this branch.")
		}
		if rs.Pipeline != nil {
			info.Lines = append(info.Lines, "Pipeline: "+string(rs.Pipeline.Status))
		}
	}
	return info, nil
}

// mcpProposer creates proposals with the agent as their source. It can only
// create: the proposals service decides and executes on the user's confirm.
type mcpProposer struct{ svc *proposals.Service }

func (p mcpProposer) Create(ctx context.Context, task core.TaskID, a coreprop.Action, summary string) (mcp.ProposalRef, error) {
	v, err := p.svc.Create(ctx, proposals.CreateRequest{
		Task: task, Action: a, Summary: summary, Source: coreprop.AgentSource{Task: task},
	})
	if err != nil {
		return mcp.ProposalRef{}, err
	}
	return mcp.ProposalRef{ID: v.ID, Status: string(v.Status), Lines: v.Lines}, nil
}

// mcpAudit writes the audit entry of a state-changing tool call, made with
// audit.Prepare so it is redacted (invariants 4 and 8).
type mcpAudit struct {
	store    *db.Store
	redactor safety.Redactor
}

func (a mcpAudit) Record(ctx context.Context, r mcp.AuditRecord) error {
	detail, err := json.Marshal(r.Detail)
	if err != nil {
		return fmt.Errorf("encode audit detail: %w", err)
	}
	e, err := audit.Prepare(core.AuditEntry{
		Actor: mcpActor(r.Task), Action: "mcp." + string(r.Tool), Subject: r.Subject, Detail: detail,
	}, a.redactor, core.AuditID(db.NewID("aud")), time.Now())
	if err != nil {
		return err
	}
	return a.store.Commit(ctx, func(tx *db.Tx) error { return tx.AppendAudit(ctx, e) })
}
