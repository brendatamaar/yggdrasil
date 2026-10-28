package composer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// maxListBytes bounds the memory of one file listing.
const maxListBytes = 64 << 20

// Mentions fuzzy-searches the files and folders of the project's tracked repos
// (invariant 6). The list of a repo comes from `git ls-files`, so nothing
// outside a tracked repo is ever read, and `.git` entries are dropped.
func (s *Service) Mentions(ctx context.Context, project core.ProjectID, query string, limit int) (api.MentionsResponse, error) {
	limit = min(max(limit, 1), 50)
	if _, err := s.c.Store.GetProject(ctx, project); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return api.MentionsResponse{}, apperr.New(apperr.NotFound, "That project doesn't exist (any more).", err)
		}
		return api.MentionsResponse{}, fmt.Errorf("read project: %w", err)
	}
	repos, err := s.c.Store.ListRepos(ctx, project) // tracked repos only
	if err != nil {
		return api.MentionsResponse{}, fmt.Errorf("list repos: %w", err)
	}
	env, err := s.c.Env.Build(safety.EnvOptions{})
	if err != nil {
		return api.MentionsResponse{}, fmt.Errorf("prepare the environment: %w", err)
	}
	var all []Item
	ids := map[string]core.Repo{}
	truncated := false
	for _, r := range repos {
		if ctx.Err() != nil {
			return api.MentionsResponse{}, ctx.Err()
		}
		ids[r.Name] = r
		files, cut, err := s.listFiles(ctx, env, r.Path)
		if err != nil {
			continue // a repo that can't be listed (moved, broken) adds nothing
		}
		truncated = truncated || cut
		all = append(all, itemsOf(r.Name, files)...)
	}
	out := api.MentionsResponse{Mentions: []api.MentionDTO{}, Truncated: truncated}
	for _, it := range Rank(query, all, limit) {
		kind := "file"
		if it.Dir {
			kind = "dir"
		}
		out.Mentions = append(out.Mentions, api.MentionDTO{RepoID: ids[it.Repo].ID, RepoName: it.Repo, Path: it.Path, Kind: kind})
	}
	return out, nil
}

// itemsOf turns the files of one repo into picker items: every file and every
// folder that holds one. Shallow paths come first, then alphabetical.
func itemsOf(repo string, files []string) []Item {
	dirs := map[string]bool{}
	for _, f := range files {
		for d := f; ; {
			i := strings.LastIndexByte(d, '/')
			if i < 0 {
				break
			}
			d = d[:i]
			if dirs[d] {
				break
			}
			dirs[d] = true
		}
	}
	out := make([]Item, 0, len(files)+len(dirs))
	for _, f := range files {
		out = append(out, Item{Repo: repo, Path: f})
	}
	for d := range dirs {
		out = append(out, Item{Repo: repo, Path: d, Dir: true})
	}
	sort.Slice(out, func(i, j int) bool {
		di, dj := strings.Count(out[i].Path, "/"), strings.Count(out[j].Path, "/")
		if di != dj {
			return di < dj
		}
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Dir && !out[j].Dir
	})
	return out
}

// safeEntry reports whether a listed path may be offered: relative, no ".."
// part and nothing inside .git.
func safeEntry(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || len(p) > 1 && p[1] == ':' {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." || seg == "" || strings.EqualFold(seg, ".git") {
			return false
		}
	}
	return true
}

// countingWriter keeps stdout up to a size and stops git once max entries
// (NUL-separated) arrived.
type countingWriter struct {
	buf    bytes.Buffer
	nuls   int
	max    int
	stop   context.CancelFunc
	capped bool
}

func (w *countingWriter) Write(p []byte) (int, error) {
	if w.capped {
		return len(p), nil
	}
	w.nuls += bytes.Count(p, []byte{0})
	if w.buf.Len()+len(p) > maxListBytes || w.nuls >= w.max {
		w.capped = true
		w.stop()
		if w.buf.Len()+len(p) <= maxListBytes {
			w.buf.Write(p)
		}
		return len(p), nil
	}
	w.buf.Write(p)
	return len(p), nil
}

// listFiles runs git ls-files in a repo: tracked files plus new files that
// are not ignored. It stops at MaxScanFiles entries or ScanTimeout and says so.
func (s *Service) listFiles(ctx context.Context, env []string, dir string) (files []string, truncated bool, err error) {
	exe, err := platform.Resolve("git", "", env)
	if err != nil {
		return nil, false, fmt.Errorf("find git: %w", err)
	}
	runCtx, cancel := context.WithTimeout(ctx, s.c.ScanTimeout)
	defer cancel()
	w := &countingWriter{max: s.c.MaxScanFiles, stop: cancel}
	var stderr bytes.Buffer
	p, err := platform.Start(runCtx, platform.Spec{
		Argv: []string{exe, "-c", "core.quotepath=false", "-c", "core.longpaths=true", "-c", "core.fsmonitor=false", "--no-optional-locks",
			"ls-files", "-z", "--cached", "--others", "--exclude-standard"},
		Dir: dir, Env: append(append([]string(nil), env...), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C"),
		Stdout: w, Stderr: &stderr,
	})
	if err != nil {
		return nil, false, fmt.Errorf("start git: %w", err)
	}
	code, werr := p.Wait()
	if ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	timedOut := runCtx.Err() != nil
	if !timedOut && (werr != nil || code != 0) {
		return nil, false, fmt.Errorf("git ls-files failed (exit %d): %w", code, werr)
	}
	truncated = timedOut || w.capped
	seen := map[string]bool{}
	parts := bytes.Split(w.buf.Bytes(), []byte{0})
	if truncated && len(parts) > 0 {
		parts = parts[:len(parts)-1] // the last entry may be cut off
	}
	for _, part := range parts {
		f := string(part)
		if !safeEntry(f) || seen[f] {
			continue
		}
		seen[f] = true
		files = append(files, f)
		if len(files) >= s.c.MaxScanFiles {
			truncated = true
			break
		}
	}
	return files, truncated, nil
}
