package search_test

import (
	"path/filepath"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/search"
	"github.com/brendatamaar/yggdrasil/internal/search/searchtest"
)

func TestGrepperContractFake(t *testing.T) {
	searchtest.GrepperContract(t, func(t *testing.T) searchtest.Harness {
		f := searchtest.NewFake()
		return searchtest.Harness{Grepper: f, Repo: f.Repo, Missing: "no-such-repo", NotRepo: f.NotRepo, Block: f.Block}
	})
}

func TestGrepperContractRealGit(t *testing.T) {
	env := searchtest.GitEnv(t)
	g, err := search.NewGitGrepper(env, platform.Start)
	if err != nil {
		t.Fatal(err)
	}
	searchtest.GrepperContract(t, func(t *testing.T) searchtest.Harness {
		return searchtest.Harness{
			Grepper: g,
			Repo:    func(files map[string]string) string { return searchtest.InitRepo(t, env, files) },
			Missing: filepath.Join(t.TempDir(), "gone"),
			// A subfolder of a repo: git would search the parent's files.
			NotRepo: func() string {
				return filepath.Join(searchtest.InitRepo(t, env, map[string]string{"sub/loose.txt": "needle\n"}), "sub")
			},
		}
	})
}
