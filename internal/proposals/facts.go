package proposals

import (
	"context"
	"errors"
	"fmt"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// subject is what an action acts on: where, which repo, which paths.
type subject struct {
	where  coreprop.Target // nil for Install
	repo   core.RepoID
	paths  []coreprop.RelPath
	agent  bool // paths are agent-config files (the AgentFile kind)
	remote string
}

func subjectOf(a coreprop.Action) (subject, error) {
	switch v := a.(type) {
	case coreprop.Commit:
		return subject{where: v.Where, paths: v.Paths}, nil
	case coreprop.Push:
		return subject{where: v.Where, remote: v.Remote}, nil
	case coreprop.MergeRequest:
		return subject{where: v.Where, remote: v.Remote}, nil
	case coreprop.AgentFile:
		return subject{where: v.Where, paths: []coreprop.RelPath{v.Path}, agent: true}, nil
	case coreprop.DeleteFiles:
		return subject{where: v.Where, paths: v.Paths}, nil
	case coreprop.Install:
		return subject{repo: v.Repo}, nil
	}
	return subject{}, fmt.Errorf("unsupported proposal kind %T", a)
}

// checkPaths applies the path rules of docs/design/proposals.md to paths under
// dir: no 8.3 short-name segment, protected agent-config paths only for the
// AgentFile kind, and the resolved path (links and junctions followed) stays
// inside dir. It returns a plain reason, or "".
func checkPaths(dir string, paths []coreprop.RelPath, agentFile bool) string {
	for _, p := range paths {
		if platform.HasShortNameSegment(string(p)) {
			return "A file path uses a short Windows name (such as PROJEC~1), which could name another file. Use the full file name."
		}
		if !agentFile && coreprop.IsProtectedPath(p) {
			return "This change touches agent settings or hook files. Those can only change through a separate agent-file proposal."
		}
		if dir == "" {
			continue
		}
		if _, err := insideRoot(dir, string(p)); err != nil {
			return "A file path is not inside the folder of this task. Use paths inside the task's worktree."
		}
	}
	return ""
}

// gatherFacts reads what Step needs to judge a Created input. A thing that is
// missing is a refusal (nothing is stored); only an unexpected error is an
// error.
func (s *Service) gatherFacts(ctx context.Context, req CreateRequest) (coreprop.Facts, core.RepoID, string, error) {
	sub, err := subjectOf(req.Action)
	if err != nil {
		return coreprop.Facts{}, "", "", refusal(coreprop.RefuseUnsupported, "")
	}
	bad := func(msg string) (coreprop.Facts, core.RepoID, string, error) {
		return coreprop.Facts{}, "", "", refusal(coreprop.RefuseBadFacts, msg)
	}
	task, err := s.cfg.Resolver.Task(ctx, req.Task)
	if errors.Is(err, db.ErrNotFound) {
		return bad("The task for this proposal does not exist.")
	}
	if err != nil {
		return coreprop.Facts{}, "", "", fmt.Errorf("read task %s: %w", req.Task, err)
	}
	dir := ""
	repo := sub.repo
	switch w := sub.where.(type) {
	case coreprop.InWorktree:
		wt, err := s.cfg.Resolver.Worktree(ctx, w.ID)
		if errors.Is(err, db.ErrNotFound) {
			return bad("The worktree for this proposal does not exist.")
		}
		if err != nil {
			return coreprop.Facts{}, "", "", fmt.Errorf("read worktree %s: %w", w.ID, err)
		}
		if wt.Task != req.Task {
			return bad("That worktree belongs to another task.")
		}
		repo, dir = wt.Repo, wt.Path
	case coreprop.MainCheckout:
		repo = w.Repo
		info, err := s.cfg.Resolver.Repo(ctx, repo)
		if err != nil && !errors.Is(err, db.ErrNotFound) {
			return coreprop.Facts{}, "", "", fmt.Errorf("read repo %s: %w", repo, err)
		}
		dir = info.Path
	}
	found := false
	for _, r := range task.Repos {
		found = found || r == repo
	}
	if !found {
		return bad("That repo is not part of this task.")
	}
	info, err := s.cfg.Resolver.Repo(ctx, repo)
	tracked := err == nil && info.Tracked
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return coreprop.Facts{}, "", "", fmt.Errorf("read repo %s: %w", repo, err)
	}
	f := coreprop.Facts{RepoTracked: tracked, PathsOK: true, RemoteSupported: true}
	detail := ""
	if tracked {
		if why := checkPaths(dir, sub.paths, sub.agent); why != "" {
			f.PathsOK, detail = false, why
		}
		if mr, ok := req.Action.(coreprop.MergeRequest); ok {
			url, err := s.cfg.Git.RemoteURL(ctx, dir, mr.Remote)
			if err != nil || s.cfg.Hosts.Kind(url) != HostGitLab {
				f.RemoteSupported = false
				detail = "Merge requests are only supported on GitLab for now. Push the branch and open the merge request on the host."
			}
		}
	}
	return f, repo, detail, nil
}
