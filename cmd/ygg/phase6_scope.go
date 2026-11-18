package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/actions"
	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/core"
	coreactions "github.com/brendatamaar/yggdrasil/internal/core/actions"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/hosts"
	"github.com/brendatamaar/yggdrasil/internal/repos"
	"github.com/brendatamaar/yggdrasil/internal/safety"
	"github.com/brendatamaar/yggdrasil/internal/standup"
)

// Adapters of the Phase 6 composition root that read the tracked scope. Every
// read starts from the store's tracked, READY repos (an untracked or reserved
// repo is db.ErrNotFound) and then the bounded status snapshot, the cached host
// links and the safe git wrapper. Nothing here writes anything.

// scopeStore is the part of *db.Store the scope reader needs.
type scopeStore interface {
	GetRepo(ctx context.Context, id core.RepoID) (core.Repo, error)
	GetProject(ctx context.Context, id core.ProjectID) (core.Project, error)
	ListRepos(ctx context.Context, project core.ProjectID) ([]core.Repo, error)
	ListBuildStates(ctx context.Context, f db.BuildFilter) ([]corebuilds.State, error)
}

// statusReader is the part of the status service the scope reader needs.
type statusReader interface {
	Snapshot(ctx context.Context) (corerepos.Refresher, error)
	NoiseFacts(ctx context.Context, id core.RepoID) (corerepos.NoiseFacts, error)
}

// failedPipelines is the part of the host service that says which repos have a
// failed pipeline (the same read the dashboard uses).
type failedPipelines interface {
	FailedPipelines(ctx context.Context) (map[core.RepoID]struct{}, error)
}

type hostCache interface {
	repoHostReader
	failedPipelines
}

// scopeReader implements actions.ScopeReader and standup.ScopeReader.
type scopeReader struct {
	store  scopeStore
	status statusReader
	hosts  hostCache
	git    *git.Git
	// log and redact report a failed read; both are optional (a nil log is silent).
	log    *slog.Logger
	redact safety.Redactor
}

// logRead records a failed read without its secrets.
func (s *scopeReader) logRead(what string, err error) {
	if s.log == nil {
		return
	}
	msg := err.Error()
	if s.redact != nil {
		msg = s.redact.Redact(msg)
	}
	s.log.Warn("scope read failed", "what", what, "error", msg)
}

var (
	_ actions.ScopeReader  = (*scopeReader)(nil)
	_ standup.ScopeReader  = (*scopeReader)(nil)
	_ actions.ScopeReader  = (*scopeReader)(nil)
	_ failedPipelines      = (*hosts.Service)(nil)
	_ statusReader         = (*repos.StatusService)(nil)
	_ scopeStore           = (*db.Store)(nil)
	_ standup.BuildStore   = (*db.Store)(nil)
	_ repos.ActiveSource   = (*db.Store)(nil)
	_ actions.RequestStore = (*db.Store)(nil)
)

// tracked returns a tracked READY repo or db.ErrNotFound.
func (s *scopeReader) tracked(ctx context.Context, id core.RepoID) (core.Repo, error) {
	r, err := s.store.GetRepo(ctx, id)
	if err != nil {
		return core.Repo{}, err
	}
	if r.Readiness != "" && r.Readiness != core.RepoReady {
		return core.Repo{}, fmt.Errorf("repo %s is not ready: %w", id, db.ErrNotFound)
	}
	return r, nil
}

func (s *scopeReader) StillTracked(ctx context.Context, id core.RepoID) error {
	_, err := s.tracked(ctx, id)
	return err
}

func (s *scopeReader) entry(ctx context.Context, r core.Repo) (corerepos.View, error) {
	snap, err := s.status.Snapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the repo status: %w", err)
	}
	e, ok := snap.Entries[r.ID]
	if !ok {
		return corerepos.Unknown{}, nil
	}
	return e.View, nil
}

// Facts computes the catalogue facts of a repo from the same sources the
// Needs-attention list reads (status, failed pipeline, newest build), so the
// reasons offered here are the reasons shown there.
func (s *scopeReader) Facts(ctx context.Context, id core.RepoID) (coreactions.Facts, error) {
	r, err := s.tracked(ctx, id)
	if err != nil {
		return coreactions.Facts{}, err
	}
	view, err := s.entry(ctx, r)
	if err != nil {
		return coreactions.Facts{}, err
	}
	f := coreactions.Facts{Repo: id}
	_, problem := view.(corerepos.Problem)
	f.Readable = !problem
	snap, haveSnap := corerepos.SnapshotOf(view)
	if problem {
		f.Reasons = append(f.Reasons, coreactions.ReasonUnreadable)
	}
	for _, fl := range corerepos.Flags(view) {
		switch fl {
		case corerepos.FlagConflicts:
			f.Reasons = append(f.Reasons, coreactions.ReasonConflicts)
		case corerepos.FlagDiverged:
			f.Reasons = append(f.Reasons, coreactions.ReasonDiverged)
		}
	}
	if haveSnap && snap.Conflicted > 0 && !hasReason(f.Reasons, coreactions.ReasonConflicts) {
		f.Reasons = append(f.Reasons, coreactions.ReasonConflicts)
	}
	// A host read that fails removes only the reasons and offerings that need
	// it; it never invents one. The failure is logged, redacted.
	view2, herr := s.hosts.RepoHost(ctx, id)
	if herr != nil {
		s.logRead("read the host link of repo "+string(id), herr)
	}
	failed, ferr := s.hosts.FailedPipelines(ctx)
	if ferr != nil {
		s.logRead("read the failed pipelines", ferr)
	}
	if _, ok := failed[id]; ok {
		f.Reasons = append(f.Reasons, coreactions.ReasonFailedCI)
		f.CIRunID = pipelineRunID(view2)
	}
	if bid, ok := s.failedBuild(ctx, id); ok {
		f.Reasons = append(f.Reasons, coreactions.ReasonFailedBuild)
		f.BuildID, f.Caps.BuildLog = bid, true
	}
	if haveSnap {
		if snap.Behind > 0 {
			f.Reasons = append(f.Reasons, coreactions.ReasonBehind)
		}
		if snap.Staged+snap.Unstaged+snap.Untracked > 0 {
			f.Reasons = append(f.Reasons, coreactions.ReasonDirty)
		}
		f.UntrackedCount = snap.Untracked
		if snap.LastCommit != nil {
			f.Head = snap.LastCommit.OID
		}
	}
	// HEAD is read live: a stale offering is refused against what the repo is
	// now, not against the last status read. When the live read fails, the last
	// status read is not a pin either: the pin stays empty and the repo is
	// marked unreadable, so a pull or review is never pinned to a stale head.
	if f.Readable && s.git != nil {
		h, herr := s.git.Head(ctx, r.Path)
		if herr != nil || h == "" {
			if herr != nil {
				s.logRead("read HEAD of repo "+string(id), herr)
			}
			f.Head, f.Readable = "", false
			if !hasReason(f.Reasons, coreactions.ReasonUnreadable) {
				f.Reasons = append(f.Reasons, coreactions.ReasonUnreadable)
			}
		} else {
			f.Head = h
		}
	}
	if view2.Linked && view2.Instance.Token && hostReadsContext(view2.Instance.Kind) {
		f.Caps.HostLogs, f.Caps.HostIssues, f.Caps.HostMR, f.Caps.CommentPost = true, true, true, true
	}
	if herr == nil {
		if n, head, ok := openMR(view2.Entry); ok {
			f.MRNumber, f.MRHead = n, head
		}
	}
	return f, nil
}

// hostReadsContext: the host kinds whose clients can read context (logs,
// issues, merge request diffs).
func hostReadsContext(k corehosts.Kind) bool {
	return k == corehosts.KindGitHub || k == corehosts.KindGitLab
}

func hasReason(rs []coreactions.Reason, r coreactions.Reason) bool {
	for _, x := range rs {
		if x == r {
			return true
		}
	}
	return false
}

// pipelineRunID reads the provider run ID from the cached pipeline's address
// (".../actions/runs/123", ".../-/pipelines/123"). An address that has no
// numeric last segment gives no run ID, and the catalogue falls back to a repo
// diagnosis; the ID is never taken from the browser.
func pipelineRunID(v api.RepoHostView) string {
	p := cachedPipeline(v.Entry)
	if p == nil {
		return ""
	}
	u := strings.TrimRight(strings.SplitN(p.URL, "?", 2)[0], "/")
	i := strings.LastIndex(u, "/")
	if i < 0 {
		return ""
	}
	seg := u[i+1:]
	if n, err := strconv.ParseInt(seg, 10, 64); err != nil || n <= 0 || strconv.FormatInt(n, 10) != seg || len(seg) > 18 {
		return ""
	}
	return seg
}

func cachedPipeline(e corehosts.Entry) *corehosts.Pipeline {
	if rd, ok := corehosts.Previous(e).Data.(corehosts.RepoState); ok {
		return rd.Pipeline
	}
	return nil
}

// openMR is the number and head commit of the cached open merge request of the
// repo's branch. The head is empty when the host did not report one; the
// catalogue then does not offer a review (it needs a pinned head).
func openMR(e corehosts.Entry) (int, string, bool) {
	if rd, ok := corehosts.Previous(e).Data.(corehosts.RepoState); ok && rd.MR != nil && rd.MR.State == corehosts.MROpen {
		return rd.MR.Number, rd.MR.Head, true
	}
	return 0, "", false
}

// failedBuild is the newest build of the repo when it failed.
func (s *scopeReader) failedBuild(ctx context.Context, id core.RepoID) (string, bool) {
	states, err := s.store.ListBuildStates(ctx, db.BuildFilter{Repo: id, Limit: 1})
	if err != nil || len(states) == 0 {
		return "", false
	}
	fin, ok := states[0].(corebuilds.Finished)
	if !ok || corebuilds.Status(states[0]) != core.BuildFailed {
		return "", false
	}
	return string(fin.ID), true
}

func (s *scopeReader) NoiseFacts(ctx context.Context, id core.RepoID) (corerepos.NoiseFacts, error) {
	if _, err := s.tracked(ctx, id); err != nil {
		return corerepos.NoiseFacts{}, err
	}
	return s.status.NoiseFacts(ctx, id)
}

func (s *scopeReader) Target(ctx context.Context, id core.RepoID) (actions.Target, error) {
	r, err := s.tracked(ctx, id)
	if err != nil {
		return actions.Target{}, err
	}
	t := actions.Target{Project: r.ProjectID}
	if v, herr := s.hosts.RepoHost(ctx, id); herr == nil && v.Linked {
		t.HasHost, t.Instance, t.HostProject = true, string(v.Instance.ID), string(v.Project)
	}
	return t, nil
}

func (s *scopeReader) ReposOf(ctx context.Context, project core.ProjectID) ([]core.RepoID, error) {
	if _, err := s.store.GetProject(ctx, project); err != nil {
		return nil, err
	}
	rows, err := s.store.ListRepos(ctx, project)
	if err != nil {
		return nil, err
	}
	out := make([]core.RepoID, 0, len(rows))
	for _, r := range rows {
		if r.Readiness == "" || r.Readiness == core.RepoReady {
			out = append(out, r.ID)
		}
	}
	return out, nil
}

func (s *scopeReader) PullTarget(ctx context.Context, id core.RepoID) (actions.PullTarget, error) {
	r, err := s.tracked(ctx, id)
	if err != nil {
		return actions.PullTarget{}, err
	}
	view, err := s.entry(ctx, r)
	if err != nil {
		return actions.PullTarget{}, err
	}
	fresh, ok := view.(corerepos.Fresh)
	if !ok || fresh.Snap.Branch == "" || !fresh.Snap.HasUpstream {
		return actions.PullTarget{}, errors.New("the repo has no readable branch with an upstream")
	}
	head, err := s.git.Head(ctx, r.Path)
	if err != nil {
		return actions.PullTarget{}, fmt.Errorf("read HEAD of repo %s: %w", id, err)
	}
	return actions.PullTarget{Branch: fresh.Snap.Branch, Upstream: fresh.Snap.Upstream, Head: head}, nil
}

// Repos implements standup.ScopeReader: the tracked READY repos of a project
// with their names (db.ErrNotFound for an unknown project).
func (s *scopeReader) Repos(ctx context.Context, project core.ProjectID) ([]standup.RepoRef, error) {
	if _, err := s.store.GetProject(ctx, project); err != nil {
		return nil, err
	}
	rows, err := s.store.ListRepos(ctx, project)
	if err != nil {
		return nil, err
	}
	out := make([]standup.RepoRef, 0, len(rows))
	for _, r := range rows {
		if r.Readiness == "" || r.Readiness == core.RepoReady {
			out = append(out, standup.RepoRef{ID: r.ID, Name: r.Name})
		}
	}
	return out, nil
}

// contextReader implements actions.ContextReader: host reads through the
// bounded host reader, the redacted local build log and the exact diff.
type contextReader struct {
	scope  *scopeReader
	host   hosts.ContextReader
	store  scopeStore
	logs   stepLogs
	git    *git.Git
	redact safety.Redactor
}

var _ actions.ContextReader = (*contextReader)(nil)

type stepLogs interface {
	StepLog(ctx context.Context, id core.BuildID, step int, after int64) ([]byte, int64, error)
}

func (c *contextReader) Host(ctx context.Context, key corehosts.ContextKey) (hosts.ContextOutcome, error) {
	return c.host.Read(ctx, key)
}

// maxStepLogRead bounds how much of one build's logs are read in total.
const maxStepLogRead = 4 * coreactions.MaxContextBytes

// BuildLog returns the tail of the build's redacted logs, at most
// coreactions.MaxContextBytes. The build must belong to the repo.
func (c *contextReader) BuildLog(ctx context.Context, repo core.RepoID, build core.BuildID) (string, bool, error) {
	if _, err := c.scope.tracked(ctx, repo); err != nil {
		return "", false, err
	}
	states, err := c.store.ListBuildStates(ctx, db.BuildFilter{Repo: repo, Limit: 20})
	if err != nil {
		return "", false, fmt.Errorf("list builds of repo %s: %w", repo, err)
	}
	var steps int
	found := false
	for _, st := range states {
		if fin, ok := st.(corebuilds.Finished); ok && fin.ID == build {
			steps, found = len(fin.Steps), true
		}
	}
	if !found {
		return "", false, fmt.Errorf("build %s of repo %s: %w", build, repo, db.ErrNotFound)
	}
	var b []byte
	for step := 0; step < steps && len(b) < maxStepLogRead; step++ {
		var after int64
		for len(b) < maxStepLogRead {
			data, next, rerr := c.logs.StepLog(ctx, build, step, after)
			if rerr != nil {
				return "", false, fmt.Errorf("read the log of build %s: %w", build, rerr)
			}
			if len(data) == 0 {
				break
			}
			b = append(b, data...)
			after = next
		}
	}
	truncated := false
	if len(b) > coreactions.MaxContextBytes {
		b, truncated = b[len(b)-coreactions.MaxContextBytes:], true
	}
	return c.redact.Redact(strings.ToValidUTF8(string(b), "")), truncated, nil
}

// Diff returns the exact current changes of the repo (staged, then unstaged),
// the HEAD and the hash a commit proposal pins, and the changed paths.
func (c *contextReader) Diff(ctx context.Context, repo core.RepoID) (actions.DiffContext, error) {
	r, err := c.scope.tracked(ctx, repo)
	if err != nil {
		return actions.DiffContext{}, err
	}
	head, err := c.git.Head(ctx, r.Path)
	if err != nil {
		return actions.DiffContext{}, fmt.Errorf("read HEAD of repo %s: %w", repo, err)
	}
	st, err := c.git.Status(ctx, r.Path)
	if err != nil {
		return actions.DiffContext{}, fmt.Errorf("read the status of repo %s: %w", repo, err)
	}
	seen := map[string]bool{}
	var paths []string
	for _, f := range st.Files {
		if !seen[f.Path] {
			seen[f.Path] = true
			paths = append(paths, f.Path)
		}
	}
	hash, err := c.git.DiffHash(ctx, r.Path, paths)
	if err != nil {
		return actions.DiffContext{}, fmt.Errorf("fingerprint the changes of repo %s: %w", repo, err)
	}
	var text strings.Builder
	for _, mode := range []git.DiffMode{git.DiffStaged, git.DiffWorktree} {
		part, derr := c.git.Diff(ctx, r.Path, git.DiffOptions{Mode: mode, Limit: coreactions.MaxContextBytes})
		if derr != nil {
			return actions.DiffContext{}, fmt.Errorf("read the diff of repo %s: %w", repo, derr)
		}
		text.WriteString(part)
	}
	out := text.String()
	truncated := st.Truncated
	if len(out) > coreactions.MaxContextBytes {
		out, truncated = out[:coreactions.MaxContextBytes], true
	}
	return actions.DiffContext{
		Head: head, DiffHash: hash, Text: c.redact.Redact(strings.ToValidUTF8(out, "")), Truncated: truncated, Paths: paths,
	}, nil
}
