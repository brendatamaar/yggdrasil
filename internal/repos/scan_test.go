package repos_test

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/repos"
	"github.com/brendatamaar/yggdrasil/internal/repos/repostest"
)

func TestScannerContractFake(t *testing.T) {
	repostest.ScannerContract(t, repostest.Caps{Links: true, Locked: true},
		func(_ *testing.T, tree repostest.Tree) (repos.Scanner, corerepos.CleanPath) {
			return repostest.NewFake(tree), "/fake"
		})
}

// liveFactory writes tree to a temp folder. A Link key becomes a link to a
// folder outside the root that holds the keys below it. Locked is not
// supported.
func liveFactory(t *testing.T, tree repostest.Tree) (repos.Scanner, corerepos.CleanPath) {
	t.Helper()
	root := t.TempDir()
	outside := t.TempDir()
	keys := make([]string, 0, len(tree))
	for k := range tree {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	linkOf := func(k string) (link, rest string, ok bool) {
		for l, kind := range tree {
			if kind == repostest.Link && len(k) > len(l) && k[:len(l)+1] == l+"/" {
				return l, k[len(l)+1:], true
			}
		}
		return "", "", false
	}
	targets := map[string]string{}
	mk := func(base, rel string, kind repostest.Kind) {
		dir := base
		if rel != "." {
			dir = filepath.Join(base, filepath.FromSlash(rel))
		}
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		switch kind {
		case repostest.Repo:
			if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o750); err != nil {
				t.Fatal(err)
			}
		case repostest.RepoFile:
			if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: x\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, k := range keys {
		kind := tree[k]
		if kind == repostest.Locked {
			t.Skip("the live scanner contract can't make unreadable folders")
		}
		if l, rest, ok := linkOf(k); ok {
			if targets[l] == "" {
				targets[l] = filepath.Join(outside, filepath.FromSlash(l))
				if err := os.MkdirAll(targets[l], 0o750); err != nil {
					t.Fatal(err)
				}
			}
			mk(targets[l], rest, kind)
			continue
		}
		if kind == repostest.Link {
			continue
		}
		mk(root, k, kind)
	}
	for l := range targets {
		if !makeLink(t, filepath.Join(root, filepath.FromSlash(l)), targets[l]) {
			t.Skip("can't create links here")
		}
	}
	return repos.FSScanner{}, corerepos.CleanPath(root)
}

func TestScannerContractLive(t *testing.T) {
	repostest.ScannerContract(t, repostest.Caps{Links: true}, liveFactory)
}

func TestFSScannerDoesNotChangeTheTree(t *testing.T) {
	s, root := liveFactory(t, repostest.Tree{"a": repostest.Repo, "b/c": repostest.Repo, "d": repostest.Dir})
	before := snapshot(t, string(root))
	if _, err := s.Scan(t.Context(), root, repos.DefaultLimits); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckRepo(t.Context(), root, "a"); err != nil {
		t.Fatal(err)
	}
	if after := snapshot(t, string(root)); !equalSnapshot(before, after) {
		t.Errorf("the scan changed the tree: %v -> %v", before, after)
	}
}

func snapshot(t *testing.T, root string) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			out[p] = -1 // directory times are updated lazily on NTFS
			return nil
		}
		out[p] = info.Size()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func equalSnapshot(a, b map[string]int64) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
