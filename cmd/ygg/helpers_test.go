package main

import (
	"os"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/safety"
	"github.com/brendatamaar/yggdrasil/internal/search/searchtest"
)

func writeFile(path string) error { return os.WriteFile(path, nil, 0o600) }

func redactorForTest() safety.Redactor { return safety.NewRedactor() }

// setRepoAuthor gives one temporary repository a fictional identity in its own
// .git/config (shared by its worktrees). The app commits and merges through a
// clean environment that never reads the test's author variables, so a machine
// without a global identity (CI, a fresh Linux box) would otherwise refuse the
// commit. It never touches HOME or the global git config.
func setRepoAuthor(t *testing.T, env []string, repoDir string) {
	t.Helper()
	searchtest.RunGit(t, env, repoDir, "config", "user.name", "Ada Test")
	searchtest.RunGit(t, env, repoDir, "config", "user.email", "ada@example.test")
	searchtest.RunGit(t, env, repoDir, "config", "commit.gpgsign", "false")
}
