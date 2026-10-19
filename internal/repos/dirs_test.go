package repos_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/repos"
	"github.com/brendatamaar/yggdrasil/internal/repos/repostest"
)

func TestDirsContractFake(t *testing.T) {
	repostest.DirsContract(t, repostest.Caps{Links: true},
		func(_ *testing.T, tree repostest.Tree) (repos.DirLister, corerepos.CleanPath) {
			return repostest.NewFakeDirs(tree), repostest.FakeRoot
		})
}

// liveDirsFactory writes tree to a temp folder. A Link key becomes a link to
// a folder outside the root (which holds a folder, to tempt a follower).
func liveDirsFactory(t *testing.T, tree repostest.Tree) (repos.DirLister, corerepos.CleanPath) {
	t.Helper()
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(outside, "escaped"), 0o750); err != nil {
		t.Fatal(err)
	}
	for rel, kind := range tree {
		full := filepath.Join(root, filepath.FromSlash(rel))
		switch kind {
		case repostest.File:
			if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte("x\r\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		case repostest.Link:
			if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
				t.Fatal(err)
			}
			if !makeLink(t, full, outside) {
				t.Skip("can't create links here")
			}
		case repostest.Repo:
			if err := os.MkdirAll(filepath.Join(full, ".git"), 0o750); err != nil {
				t.Fatal(err)
			}
		case repostest.RepoFile:
			if err := os.MkdirAll(full, 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(full, ".git"), []byte("gitdir: x\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		default:
			if err := os.MkdirAll(full, 0o750); err != nil {
				t.Fatal(err)
			}
		}
	}
	return repos.FSDirs{}, corerepos.CleanPath(root)
}

func TestDirsContractLive(t *testing.T) {
	repostest.DirsContract(t, repostest.Caps{Links: true}, liveDirsFactory)
}

func TestFSDirsDoesNotChangeTheTree(t *testing.T) {
	d, root := liveDirsFactory(t, repostest.Tree{"a": repostest.Repo, "b c": repostest.Dir, "f.txt": repostest.File})
	before := snapshot(t, string(root))
	if _, err := d.ListDirs(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	if after := snapshot(t, string(root)); !equalSnapshot(before, after) {
		t.Errorf("listing changed the tree: %v -> %v", before, after)
	}
}

// A path past the classic 260 character limit still lists (docs/AGENTS.md 9).
func TestFSDirsLongPath(t *testing.T) {
	deep := t.TempDir()
	for len(deep) < 300 {
		deep = filepath.Join(deep, strings.Repeat("d", 40))
	}
	if err := os.MkdirAll(filepath.Join(deep, "shop-api", ".git"), 0o750); err != nil {
		t.Skipf("this machine can't create long paths: %v", err)
	}
	got, err := repos.FSDirs{}.ListDirs(t.Context(), corerepos.CleanPath(deep))
	if err != nil || len(got) != 1 || got[0].Name != "shop-api" || !got[0].IsRepo {
		t.Fatalf("ListDirs(long path) = %+v, %v", got, err)
	}
}
