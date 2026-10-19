package repostest

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/repos"
)

// FakeDirs is a repos.DirLister over a Tree, with no disk behind it. Its root
// is FakeRoot.
type FakeDirs struct {
	kinds map[string]Kind
}

// FakeRoot is the folder a FakeDirs lists.
const FakeRoot = corerepos.CleanPath("/fake")

var _ repos.DirLister = (*FakeDirs)(nil)

// NewFakeDirs builds the fake for tree. Parent folders are implied. Keys below
// a Link are ignored: a link is never followed.
func NewFakeDirs(tree Tree) *FakeDirs {
	f := &FakeDirs{kinds: map[string]Kind{}}
	for p, k := range tree {
		for dir := path.Dir(p); dir != "."; dir = path.Dir(dir) {
			if _, ok := f.kinds[dir]; !ok {
				f.kinds[dir] = Dir
			}
		}
		f.kinds[p] = k
	}
	return f
}

// Roots implements repos.DirLister.
func (f *FakeDirs) Roots() ([]string, error) { return []string{string(FakeRoot)}, nil }

// ListDirs implements repos.DirLister.
func (f *FakeDirs) ListDirs(ctx context.Context, p corerepos.CleanPath) ([]repos.Dir, error) {
	rel, ok := f.relOf(p)
	if !ok {
		return nil, apperr.New(apperr.Invalid, fmt.Sprintf("The folder %q can't be read. Choose another folder.", string(p)), errors.New("no such folder"))
	}
	var out []repos.Dir
	for key, kind := range f.kinds {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("browse stopped: %w", err)
		}
		if path.Dir(key) != rel || kind == File || kind == Link || f.behindLink(key) {
			continue
		}
		out = append(out, repos.Dir{
			Name: path.Base(key), Path: filepath.Join(string(p), path.Base(key)),
			IsRepo: kind == Repo || kind == RepoFile,
		})
	}
	slices.SortFunc(out, func(a, b repos.Dir) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return out, nil
}

// relOf maps a listed path to its slash path under the fake root ("." for the
// root). A path that is not a folder of the tree is not found.
func (f *FakeDirs) relOf(p corerepos.CleanPath) (string, bool) {
	root := filepath.FromSlash(string(FakeRoot))
	s := filepath.Clean(string(p))
	if s == root {
		return ".", true
	}
	rest, ok := strings.CutPrefix(s, root+string(filepath.Separator))
	if !ok {
		return "", false
	}
	rel := filepath.ToSlash(rest)
	k, found := f.kinds[rel]
	if !found || k == File || k == Link || f.behindLink(rel) {
		return "", false
	}
	return rel, true
}

func (f *FakeDirs) behindLink(key string) bool {
	for dir := path.Dir(key); dir != "."; dir = path.Dir(dir) {
		if f.kinds[dir] == Link {
			return true
		}
	}
	return false
}

// DirsFactory builds a DirLister over tree and returns the folder to list.
// The live factory writes the tree to a temp folder.
type DirsFactory func(t *testing.T, tree Tree) (d repos.DirLister, root corerepos.CleanPath)

// DirsContract is the contract of repos.DirLister, run against the fake and
// the live lister. caps.Links says the tree can hold links.
func DirsContract(t *testing.T, caps Caps, newDirs DirsFactory) {
	ctx := context.Background()
	list := func(t *testing.T, tree Tree) ([]repos.Dir, corerepos.CleanPath) {
		t.Helper()
		d, root := newDirs(t, tree)
		got, err := d.ListDirs(ctx, root)
		if err != nil {
			t.Fatalf("ListDirs: %v", err)
		}
		return got, root
	}
	names := func(ds []repos.Dir) []string {
		out := make([]string, 0, len(ds))
		for _, d := range ds {
			out = append(out, d.Name)
		}
		return out
	}

	t.Run("Roots are not empty", func(t *testing.T) {
		d, _ := newDirs(t, Tree{"a": Dir})
		roots, err := d.Roots()
		if err != nil || len(roots) == 0 {
			t.Fatalf("Roots = %v, %v", roots, err)
		}
		for _, r := range roots {
			if r == "" {
				t.Error("an empty root")
			}
		}
	})
	t.Run("lists only folders", func(t *testing.T) {
		got, _ := list(t, Tree{"docs": Dir, "notes.txt": File, "shop-api": Repo})
		if want := []string{"docs", "shop-api"}; !slices.Equal(names(got), want) {
			t.Errorf("entries = %v, want %v", names(got), want)
		}
	})
	t.Run("flags repos with a git folder or file", func(t *testing.T) {
		got, _ := list(t, Tree{"plain": Dir, "repo": Repo, "wt": RepoFile})
		flags := map[string]bool{}
		for _, d := range got {
			flags[d.Name] = d.IsRepo
		}
		if len(flags) != 3 || flags["plain"] || !flags["repo"] || !flags["wt"] {
			t.Errorf("IsRepo = %v, want plain=false repo=true wt=true", flags)
		}
	})
	t.Run("sorted by name without case", func(t *testing.T) {
		got, _ := list(t, Tree{"b": Dir, "A": Dir, "c": Dir, "B2": Dir})
		if want := []string{"A", "b", "B2", "c"}; !slices.Equal(names(got), want) {
			t.Errorf("entries = %v, want %v", names(got), want)
		}
	})
	t.Run("paths are the listed folder plus the name", func(t *testing.T) {
		got, root := list(t, Tree{"with space": Dir, "deep/er": Dir})
		for _, d := range got {
			if want := filepath.Join(string(root), d.Name); d.Path != want {
				t.Errorf("Path = %q, want %q", d.Path, want)
			}
		}
		if want := []string{"deep", "with space"}; !slices.Equal(names(got), want) {
			t.Errorf("entries = %v, want %v (direct children only)", names(got), want)
		}
	})
	t.Run("hidden and dependency folders are listed", func(t *testing.T) {
		got, _ := list(t, Tree{".cache": Dir, "node_modules": Dir})
		if want := []string{".cache", "node_modules"}; !slices.Equal(names(got), want) {
			t.Errorf("entries = %v, want %v", names(got), want)
		}
	})
	t.Run("a folder with only files lists nothing", func(t *testing.T) {
		if got, _ := list(t, Tree{"only.txt": File}); len(got) != 0 {
			t.Errorf("entries = %v", names(got))
		}
	})
	t.Run("lists a subfolder", func(t *testing.T) {
		d, root := newDirs(t, Tree{"libs/common": Repo, "libs/notes.txt": File, "other": Dir})
		got, err := d.ListDirs(ctx, corerepos.CleanPath(filepath.Join(string(root), "libs")))
		if err != nil || len(got) != 1 || got[0].Name != "common" || !got[0].IsRepo {
			t.Errorf("ListDirs(libs) = %+v, %v", got, err)
		}
	})
	t.Run("a missing folder or a file is an Invalid error", func(t *testing.T) {
		d, root := newDirs(t, Tree{"f.txt": File})
		for _, bad := range []string{"nope", "f.txt"} {
			_, err := d.ListDirs(ctx, corerepos.CleanPath(filepath.Join(string(root), bad)))
			if c, ok := apperr.ClassOf(err); !ok || c != apperr.Invalid {
				t.Errorf("ListDirs(%s) = %v, want an apperr.Invalid", bad, err)
			}
		}
	})
	t.Run("a cancelled context stops the listing", func(t *testing.T) {
		d, root := newDirs(t, Tree{"a": Dir, "b": Dir})
		c, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := d.ListDirs(c, root); !errors.Is(err, context.Canceled) {
			t.Errorf("ListDirs = %v, want context.Canceled", err)
		}
	})
	if caps.Links {
		t.Run("leaves out links", func(t *testing.T) {
			got, _ := list(t, Tree{"lnk": Link, "real": Dir})
			if want := []string{"real"}; !slices.Equal(names(got), want) {
				t.Errorf("entries = %v, want %v (a link must not be listed)", names(got), want)
			}
		})
	}
}
