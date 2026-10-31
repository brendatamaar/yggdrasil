// Package mcptest holds the fakes and contract suites for internal/mcp. The
// fakes are in-memory, record their calls, and never touch the machine.
package mcptest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/mcp"
)

// FakeWorkspace is an in-memory mcp.Workspace. Untrack removes a repo from
// every snapshot from the next call on.
type FakeWorkspace struct {
	mu    sync.Mutex
	snaps map[core.TaskID]mcp.Snapshot
}

var _ mcp.Workspace = (*FakeWorkspace)(nil)

// NewFakeWorkspace returns an empty workspace.
func NewFakeWorkspace() *FakeWorkspace { return &FakeWorkspace{snaps: map[core.TaskID]mcp.Snapshot{}} }

// Set stores the snapshot of a task.
func (w *FakeWorkspace) Set(task core.TaskID, s mcp.Snapshot) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.snaps[task] = s
}

// Untrack removes a repo from every task's snapshot.
func (w *FakeWorkspace) Untrack(repo core.RepoID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for t, s := range w.snaps {
		s.Repos = slices.DeleteFunc(slices.Clone(s.Repos), func(r mcp.RepoView) bool { return r.ID == repo })
		w.snaps[t] = s
	}
}

// Snapshot implements mcp.Workspace.
func (w *FakeWorkspace) Snapshot(_ context.Context, task core.TaskID) (mcp.Snapshot, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	s, ok := w.snaps[task]
	if !ok {
		return mcp.Snapshot{}, mcp.ErrNoTask
	}
	s.Repos = slices.Clone(s.Repos)
	return s, nil
}

// Calls records the calls a fake received, as "name arg ..." strings.
type Calls struct {
	mu   sync.Mutex
	list []string
}

// Add records one call.
func (c *Calls) Add(format string, a ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.list = append(c.list, fmt.Sprintf(format, a...))
}

// All returns the recorded calls.
func (c *Calls) All() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.list)
}

// FakeReader answers git reads from maps keyed by repo ID. The text includes
// the location so tests can tell worktree from main checkout.
type FakeReader struct {
	Calls
	// Diffs is the diff text per repo.
	Diffs map[core.RepoID]string
	// SecretInDiff, when set, is added to every diff (to test redaction).
	Extra string
}

var _ mcp.Reader = (*FakeReader)(nil)

// Status implements mcp.Reader.
func (r *FakeReader) Status(_ context.Context, at mcp.Loc) (mcp.RepoStatus, error) {
	r.Add("status %s wt=%s", at.Repo, at.Worktree)
	return mcp.RepoStatus{Branch: "main", Upstream: "origin/main", Ahead: 1, Unstaged: 2, Head: "abc1234"}, nil
}

// Commits implements mcp.Reader.
func (r *FakeReader) Commits(_ context.Context, at mcp.Loc, limit int) ([]mcp.Commit, error) {
	r.Add("commits %s wt=%s limit=%d", at.Repo, at.Worktree, limit)
	return []mcp.Commit{{ID: "abc1234", Author: "Ada", When: "2026-01-02", Subject: "Add cart" + r.Extra}}, nil
}

// Changes implements mcp.Reader.
func (r *FakeReader) Changes(_ context.Context, at mcp.Loc) ([]mcp.Change, error) {
	r.Add("changes %s wt=%s", at.Repo, at.Worktree)
	return []mcp.Change{{Path: "src/cart.go", Status: "M"}}, nil
}

// Diff implements mcp.Reader.
func (r *FakeReader) Diff(_ context.Context, at mcp.Loc, path string, maxBytes int) (string, bool, error) {
	r.Add("diff %s wt=%s path=%s max=%d", at.Repo, at.Worktree, path, maxBytes)
	d := r.Diffs[at.Repo] + r.Extra
	if len(d) > maxBytes {
		return d[:maxBytes], true, nil
	}
	return d, false, nil
}

// FakeBuilds is an in-memory mcp.Builds.
type FakeBuilds struct {
	Calls
	ProjectRepos []core.RepoID
	PresetList   []mcp.BuildPreset
	BuildList    []mcp.BuildSummary
	Logs         map[core.BuildID]string
	Install      string
	// StartErr, when set, is returned by Start.
	StartErr error
	next     int
}

var _ mcp.Builds = (*FakeBuilds)(nil)

// Info implements mcp.Builds.
func (b *FakeBuilds) Info(_ context.Context, repo core.RepoID) ([]string, error) {
	return []string{"node: verify (lint, test)"}, nil
}

// Order implements mcp.Builds.
func (b *FakeBuilds) Order(context.Context, core.ProjectID) ([]core.RepoID, error) {
	return slices.Clone(b.ProjectRepos), nil
}

// Presets implements mcp.Builds.
func (b *FakeBuilds) Presets(context.Context, core.ProjectID) ([]mcp.BuildPreset, error) {
	return slices.Clone(b.PresetList), nil
}

// List implements mcp.Builds.
func (b *FakeBuilds) List(context.Context, core.ProjectID, int) ([]mcp.BuildSummary, error) {
	return slices.Clone(b.BuildList), nil
}

// Repos implements mcp.Builds.
func (b *FakeBuilds) Repos(_ context.Context, id core.BuildID) ([]core.RepoID, error) {
	for _, x := range b.BuildList {
		if x.ID == id {
			return slices.Clone(x.Repos), nil
		}
	}
	return nil, errors.New("no such build")
}

// Log implements mcp.Builds.
func (b *FakeBuilds) Log(_ context.Context, id core.BuildID, _ int, maxBytes int) (string, bool, error) {
	l := b.Logs[id]
	if len(l) > maxBytes {
		return l[:maxBytes], true, nil
	}
	return l, false, nil
}

// Start implements mcp.Builds.
func (b *FakeBuilds) Start(_ context.Context, task core.TaskID, at mcp.Loc, steps []string) (core.BuildID, error) {
	b.Add("start task=%s repo=%s wt=%s steps=%v", task, at.Repo, at.Worktree, steps)
	if b.StartErr != nil {
		return "", b.StartErr
	}
	b.next++
	return core.BuildID(fmt.Sprintf("bld_%d", b.next)), nil
}

// RunPreset implements mcp.Builds.
func (b *FakeBuilds) RunPreset(_ context.Context, task core.TaskID, preset string) (core.BuildID, error) {
	b.Add("preset task=%s id=%s", task, preset)
	b.next++
	return core.BuildID(fmt.Sprintf("bld_%d", b.next)), nil
}

// InstallStep implements mcp.Builds.
func (b *FakeBuilds) InstallStep(context.Context, core.RepoID) (string, error) { return b.Install, nil }

// FakeGit records fetch and pull.
type FakeGit struct {
	Calls
	PullErr error
}

var _ mcp.GitActions = (*FakeGit)(nil)

// Fetch implements mcp.GitActions.
func (g *FakeGit) Fetch(_ context.Context, task core.TaskID, repo core.RepoID) (mcp.GitOutcome, error) {
	g.Add("fetch task=%s repo=%s", task, repo)
	return mcp.GitOutcome{Text: "fetched"}, nil
}

// Pull implements mcp.GitActions.
func (g *FakeGit) Pull(_ context.Context, task core.TaskID, repo core.RepoID) (mcp.GitOutcome, error) {
	g.Add("pull task=%s repo=%s", task, repo)
	if g.PullErr != nil {
		return mcp.GitOutcome{}, g.PullErr
	}
	return mcp.GitOutcome{Text: "pulled", From: "a1", To: "b2"}, nil
}

// FakeHosts answers host data from a map.
type FakeHosts struct {
	Remotes map[core.RepoID]mcp.RemoteInfo
}

var _ mcp.Hosts = (*FakeHosts)(nil)

// Remote implements mcp.Hosts.
func (h *FakeHosts) Remote(_ context.Context, repo core.RepoID) (mcp.RemoteInfo, error) {
	return h.Remotes[repo], nil
}

// FakeProposer records created proposals, all pending. It has no way to
// confirm or execute one.
type FakeProposer struct {
	mu      sync.Mutex
	Created []Proposed
	Err     error
}

// Proposed is one created proposal.
type Proposed struct {
	Task    core.TaskID
	Action  coreprop.Action
	Summary string
}

var _ mcp.Proposer = (*FakeProposer)(nil)

// Create implements mcp.Proposer.
func (p *FakeProposer) Create(_ context.Context, task core.TaskID, a coreprop.Action, summary string) (mcp.ProposalRef, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.Err != nil {
		return mcp.ProposalRef{}, p.Err
	}
	p.Created = append(p.Created, Proposed{Task: task, Action: a, Summary: summary})
	ref := mcp.ProposalRef{
		ID: core.ProposalID(fmt.Sprintf("prp_%d", len(p.Created))), Status: string(core.ProposalPending),
	}
	for _, l := range coreprop.Describe(a) {
		ref.Lines = append(ref.Lines, l.Text)
	}
	return ref, nil
}

// FakeAudit records audit requests; Fail makes Record fail.
type FakeAudit struct {
	mu      sync.Mutex
	Records []mcp.AuditRecord
	Fail    bool
}

var _ mcp.Auditor = (*FakeAudit)(nil)

// Record implements mcp.Auditor.
func (a *FakeAudit) Record(_ context.Context, r mcp.AuditRecord) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.Fail {
		return errors.New("audit store is down")
	}
	a.Records = append(a.Records, r)
	return nil
}

// Count is the number of records.
func (a *FakeAudit) Count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.Records)
}

// FakeDevServers lists the servers it is given.
type FakeDevServers struct{ Servers []mcp.DevServer }

var _ mcp.DevServers = (*FakeDevServers)(nil)

// List implements mcp.DevServers.
func (d *FakeDevServers) List(context.Context, core.ProjectID) ([]mcp.DevServer, error) {
	return slices.Clone(d.Servers), nil
}

// Log implements mcp.DevServers.
func (d *FakeDevServers) Log(_ context.Context, _ core.ProjectID, name string, _ int) (string, error) {
	for _, s := range d.Servers {
		if s.Name == name {
			return "log of " + name, nil
		}
	}
	return "", mcp.ErrNoDevServer
}
