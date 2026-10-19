// Package repostest has the scanner fake and the contract suite that both the
// fake and the live scanner of internal/repos must pass. Import it from
// _test.go files only.
package repostest

import (
	"context"
	"errors"
	"io/fs"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/repos"
)

// Kind says what a path in a Tree is.
type Kind int

const (
	// Dir is a plain folder.
	Dir Kind = iota + 1
	// Repo is a folder with a .git folder.
	Repo
	// RepoFile is a folder with a .git file (a linked worktree).
	RepoFile
	// Link is a symlink or junction to a folder. The keys below it describe
	// the folder it points to.
	Link
	// Locked is a folder that can't be read.
	Locked
	// File is a plain file. Only the DirLister contract uses it.
	File
)

// Tree describes a folder tree with slash paths relative to its root. Parent
// folders are implied.
type Tree map[string]Kind

// Fake is a Scanner over a Tree, with no disk behind it. It runs the same
// ScanWith algorithm as the live scanner.
type Fake struct {
	fsys   fstest.MapFS
	links  map[string]bool
	locked map[string]bool
}

var _ repos.Scanner = (*Fake)(nil)

// NewFake builds the fake for tree.
func NewFake(tree Tree) *Fake {
	f := &Fake{fsys: fstest.MapFS{}, links: map[string]bool{}, locked: map[string]bool{}}
	for p, k := range tree {
		if p != "." {
			f.fsys[p] = &fstest.MapFile{Mode: fs.ModeDir | 0o755}
		}
		switch k {
		case Repo:
			f.fsys[join(p, ".git/HEAD")] = &fstest.MapFile{Data: []byte("ref: refs/heads/main\n")}
		case RepoFile:
			f.fsys[join(p, ".git")] = &fstest.MapFile{Data: []byte("gitdir: ../.git/worktrees/x\n")}
		case Link:
			f.links[p] = true
		case Locked:
			f.locked[p] = true
		}
	}
	return f
}

type fakeWalker struct{ f *Fake }

func (w fakeWalker) ReadDir(rel string) ([]fs.DirEntry, error) {
	if w.f.locked[rel] {
		return nil, fs.ErrPermission
	}
	return fs.ReadDir(w.f.fsys, rel)
}

func (w fakeWalker) HasGit(rel string) bool {
	_, err := fs.Stat(w.f.fsys, join(rel, ".git"))
	return err == nil
}

func (w fakeWalker) IsLink(rel string) bool { return w.f.links[rel] }

func join(rel, name string) string {
	if rel == "." {
		return name
	}
	return rel + "/" + name
}

// Scan implements repos.Scanner.
func (f *Fake) Scan(ctx context.Context, _ corerepos.CleanPath, lim repos.Limits) (corerepos.ScanResult, error) {
	return repos.ScanWith(ctx, fakeWalker{f}, lim)
}

// CheckRepo implements repos.Scanner.
func (f *Fake) CheckRepo(_ context.Context, _ corerepos.CleanPath, rel corerepos.RelPath) (corerepos.RelPath, error) {
	return rel, repos.CheckRepoWith(fakeWalker{f}, string(rel))
}

// Caps says what a scanner under test can do.
type Caps struct {
	// Links: the scanner's tree can hold links.
	Links bool
	// Locked: the tree can hold folders that can't be read.
	Locked bool
	// CaseInsensitive: the scanner's disk ignores case, so CheckRepo must
	// answer with the on-disk spelling.
	CaseInsensitive bool
}

// Factory builds a scanner over tree and returns the folder to scan. The
// live factory writes the tree to a temp folder; it may skip Link and Locked
// entries when Caps says it can't hold them.
type Factory func(t *testing.T, tree Tree) (s repos.Scanner, root corerepos.CleanPath)

func rels(r corerepos.ScanResult) []string {
	out := make([]string, 0, len(r.Repos))
	for _, p := range r.Repos {
		out = append(out, string(p))
	}
	sort.Strings(out)
	return out
}

func same(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// ScannerContract is the contract of repos.Scanner, run against the fake and
// the live scanner.
func ScannerContract(t *testing.T, caps Caps, newScanner Factory) {
	ctx := context.Background()
	lim := repos.DefaultLimits
	scan := func(t *testing.T, tree Tree, l repos.Limits) corerepos.ScanResult {
		t.Helper()
		s, root := newScanner(t, tree)
		res, err := s.Scan(ctx, root, l)
		if err != nil {
			t.Fatalf("Scan: %v", err)
		}
		return res
	}
	check := func(t *testing.T, res corerepos.ScanResult, want ...string) {
		t.Helper()
		if got := rels(res); !same(got, want) {
			t.Errorf("repos = %v, want %v", got, want)
		}
	}

	t.Run("the folder itself is a repo", func(t *testing.T) {
		res := scan(t, Tree{".": Repo, "sub": Dir}, lim)
		check(t, res, ".")
		if res.Truncated {
			t.Error("Truncated")
		}
	})
	t.Run("nested repos", func(t *testing.T) {
		check(t, scan(t, Tree{"shop-api": Repo, "shop-web": Repo, "libs/common": Repo, "docs": Dir}, lim),
			"libs/common", "shop-api", "shop-web")
	})
	t.Run("a .git file marks a repo too", func(t *testing.T) {
		check(t, scan(t, Tree{"wt": RepoFile}, lim), "wt")
	})
	t.Run("stops at a repo root", func(t *testing.T) {
		check(t, scan(t, Tree{"a": Repo, "a/vendor/sub": Repo}, lim), "a")
	})
	t.Run("skips node_modules and hidden folders", func(t *testing.T) {
		check(t, scan(t, Tree{"node_modules/x": Repo, ".cache/y": Repo, "ok": Repo}, lim), "ok")
	})
	t.Run("no repos is an empty, complete result", func(t *testing.T) {
		res := scan(t, Tree{"a": Dir, "a/b": Dir}, lim)
		check(t, res)
		if res.Truncated || res.Skipped != 0 {
			t.Errorf("result = %+v", res)
		}
	})
	t.Run("depth limit", func(t *testing.T) {
		res := scan(t, Tree{"a/b/c": Repo}, lim) // depth 3: found
		check(t, res, "a/b/c")
		if res.Truncated {
			t.Error("a repo at the depth limit is found without truncation")
		}
		res = scan(t, Tree{"a/b/c/d": Repo}, lim) // depth 4: not read
		check(t, res)
		if !res.Truncated {
			t.Error("a tree deeper than the limit must be Truncated")
		}
	})
	t.Run("repo limit", func(t *testing.T) {
		tree := Tree{}
		for _, n := range []string{"a", "b", "c", "d", "e"} {
			tree[n] = Repo
		}
		l := lim
		l.MaxRepos = 3
		res := scan(t, tree, l)
		if len(res.Repos) != 3 || !res.Truncated {
			t.Errorf("result = %+v, want 3 repos and Truncated", res)
		}
	})
	t.Run("folder limit", func(t *testing.T) {
		tree := Tree{}
		for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
			tree[n] = Dir
		}
		l := lim
		l.MaxDirs = 4
		if res := scan(t, tree, l); !res.Truncated {
			t.Errorf("result = %+v, want Truncated", res)
		}
	})
	t.Run("results are sorted and use forward slashes", func(t *testing.T) {
		res := scan(t, Tree{"z": Repo, "m/n": Repo, "a": Repo}, lim)
		if !sort.SliceIsSorted(res.Repos, func(i, j int) bool { return res.Repos[i] < res.Repos[j] }) {
			t.Errorf("repos not sorted: %v", res.Repos)
		}
		for _, r := range res.Repos {
			if strings.Contains(string(r), `\`) {
				t.Errorf("RelPath %q has a backslash", r)
			}
		}
	})
	t.Run("a cancelled context stops the scan", func(t *testing.T) {
		s, root := newScanner(t, Tree{"a": Repo})
		c, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := s.Scan(c, root, lim); !errors.Is(err, context.Canceled) {
			t.Errorf("Scan = %v, want context.Canceled", err)
		}
	})
	if caps.Links {
		t.Run("never follows a link", func(t *testing.T) {
			res := scan(t, Tree{"lnk": Link, "lnk/outside": Repo, "real": Repo}, lim)
			check(t, res, "real")
		})
		t.Run("CheckRepo refuses a path through a link", func(t *testing.T) {
			s, root := newScanner(t, Tree{"lnk": Link, "lnk/outside": Repo})
			if _, err := s.CheckRepo(ctx, root, "lnk/outside"); err == nil {
				t.Error("CheckRepo accepted a repo behind a link")
			}
		})
	}
	if caps.Locked {
		t.Run("an unreadable folder is skipped and counted", func(t *testing.T) {
			res := scan(t, Tree{"locked": Locked, "ok": Repo}, lim)
			check(t, res, "ok")
			if res.Skipped != 1 {
				t.Errorf("Skipped = %d, want 1", res.Skipped)
			}
		})
	}
	if caps.CaseInsensitive {
		t.Run("CheckRepo answers with the on-disk spelling", func(t *testing.T) {
			s, root := newScanner(t, Tree{"Shop-Api": Repo})
			got, err := s.CheckRepo(ctx, root, "shop-API")
			if err != nil || got != "Shop-Api" {
				t.Errorf("CheckRepo(shop-API) = %q, %v; want Shop-Api", got, err)
			}
		})
	}
	t.Run("CheckRepo", func(t *testing.T) {
		s, root := newScanner(t, Tree{"api": Repo, "docs": Dir, "wt": RepoFile})
		for _, ok := range []corerepos.RelPath{"api", "wt"} {
			if _, err := s.CheckRepo(ctx, root, ok); err != nil {
				t.Errorf("CheckRepo(%s) = %v", ok, err)
			}
		}
		for _, bad := range []corerepos.RelPath{"docs", "missing", "api/nope"} {
			if _, err := s.CheckRepo(ctx, root, bad); err == nil {
				t.Errorf("CheckRepo(%s) accepted", bad)
			}
		}
	})
}
