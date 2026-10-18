package repos

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// Limits bound a folder scan.
type Limits struct {
	// MaxDepth is how many folder levels below the scanned folder are read.
	MaxDepth int
	// MaxDirs is how many folders one scan visits.
	MaxDirs int
	// MaxRepos is how many repos one scan reports.
	MaxRepos int
}

// DefaultLimits are the limits of the design: depth 3, 5,000 folders, 200 repos.
var DefaultLimits = Limits{MaxDepth: 3, MaxDirs: 5000, MaxRepos: MaxPicks}

// Scanner finds the git repos under a folder. It only reads.
type Scanner interface {
	// Scan walks path. It finds the folder itself as a repo (RelPath "."),
	// nested repos, stops at a repo root (no submodules), skips node_modules
	// and hidden folders, never follows a symlink or junction, honours the
	// limits with Truncated, and counts unreadable folders in Skipped.
	Scan(ctx context.Context, path corerepos.CleanPath, lim Limits) (corerepos.ScanResult, error)
	// CheckRepo reports why rel under path is not a repo that may be
	// tracked: missing, not a folder, behind a link, or without .git. It
	// returns nil for a usable repo folder.
	CheckRepo(ctx context.Context, path corerepos.CleanPath, rel corerepos.RelPath) error
}

// Walker is the filesystem view ScanWith needs. Paths are slash-separated and
// relative to the scanned folder; "." is the folder itself.
type Walker interface {
	ReadDir(rel string) ([]fs.DirEntry, error)
	// HasGit reports whether rel holds a .git folder or file.
	HasGit(rel string) bool
	// IsLink reports whether rel is a symlink, junction or other reparse
	// point. A path that can't be examined counts as a link.
	IsLink(rel string) bool
}

func skippedName(name string) bool {
	return name == "node_modules" || strings.HasPrefix(name, ".")
}

func join(rel, name string) string {
	if rel == "." {
		return name
	}
	return rel + "/" + name
}

// ScanWith is the scan algorithm over a Walker. The live scanner and the test
// fake share it; their contract suite checks the parts that touch the disk.
func ScanWith(ctx context.Context, w Walker, lim Limits) (corerepos.ScanResult, error) {
	var res corerepos.ScanResult
	dirs := 0
	// visit returns true when the whole scan must stop.
	var visit func(rel string, depth int) (bool, error)
	visit = func(rel string, depth int) (bool, error) {
		if err := ctx.Err(); err != nil {
			return true, fmt.Errorf("scan stopped: %w", err)
		}
		dirs++
		if dirs > lim.MaxDirs {
			res.Truncated = true
			return true, nil
		}
		if w.HasGit(rel) {
			if len(res.Repos) >= lim.MaxRepos {
				res.Truncated = true
				return true, nil
			}
			res.Repos = append(res.Repos, corerepos.RelPath(rel))
			return false, nil
		}
		entries, err := w.ReadDir(rel)
		if err != nil {
			res.Skipped++
			return false, nil
		}
		slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
		var subs []string
		for _, e := range entries {
			child := join(rel, e.Name())
			if !e.IsDir() || skippedName(e.Name()) || w.IsLink(child) {
				continue
			}
			subs = append(subs, child)
		}
		if depth >= lim.MaxDepth {
			if len(subs) > 0 {
				res.Truncated = true // there is more below, unread
			}
			return false, nil
		}
		for _, c := range subs {
			if stop, err := visit(c, depth+1); stop || err != nil {
				return stop, err
			}
		}
		return false, nil
	}
	if _, err := visit(".", 0); err != nil {
		return corerepos.ScanResult{}, err
	}
	slices.Sort(res.Repos)
	return res, nil
}

// FSScanner is the live Scanner on the local disk.
type FSScanner struct{}

var _ Scanner = FSScanner{}

type osWalker struct{ root string }

func (w osWalker) abs(rel string) string {
	if rel == "." {
		return w.root
	}
	return filepath.Join(w.root, filepath.FromSlash(rel))
}

func (w osWalker) ReadDir(rel string) ([]fs.DirEntry, error) {
	return os.ReadDir(w.abs(rel)) //nolint:gosec // a tracked folder chosen by the user; read-only
}

func (w osWalker) HasGit(rel string) bool {
	_, err := os.Stat(filepath.Join(w.abs(rel), ".git")) //nolint:gosec // read-only
	return err == nil
}

func (w osWalker) IsLink(rel string) bool {
	link, err := platform.IsReparsePoint(w.abs(rel))
	return err != nil || link
}

// Scan implements Scanner.
func (FSScanner) Scan(ctx context.Context, path corerepos.CleanPath, lim Limits) (corerepos.ScanResult, error) {
	return ScanWith(ctx, osWalker{root: string(path)}, lim)
}

// CheckRepo implements Scanner. Every folder from path down to rel must be a
// real folder, not a link: a hand-made request can't reach outside the
// project folder through a junction.
func (FSScanner) CheckRepo(_ context.Context, path corerepos.CleanPath, rel corerepos.RelPath) error {
	return CheckRepoWith(osWalker{root: string(path)}, string(rel))
}

// CheckRepoWith is CheckRepo over a Walker.
func CheckRepoWith(w Walker, rel string) error {
	cur := "."
	if rel != "." {
		for _, seg := range strings.Split(rel, "/") {
			cur = join(cur, seg)
			if w.IsLink(cur) {
				return apperr.New(apperr.Invalid, fmt.Sprintf("The folder %q is a link to somewhere else. Choose the real folder.", rel), nil)
			}
		}
	}
	if !w.HasGit(rel) {
		return apperr.New(apperr.Invalid, fmt.Sprintf("The folder %q isn't a git repo (no .git inside). Choose a repo's top folder.", rel), nil)
	}
	return nil
}
