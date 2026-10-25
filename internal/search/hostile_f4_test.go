package search_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	coresearch "github.com/brendatamaar/yggdrasil/internal/core/search"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/search"
	"github.com/brendatamaar/yggdrasil/internal/search/searchtest"
)

// A repo's own .git/config is hostile input: core.fsmonitor and
// core.hooksPath name programs that git would run. The search must start none
// of them (invariants 3 and 5). Git runs these commands through its shell on
// every OS (Git for Windows ships sh), so one command string works everywhere
// and no OS switch is needed.
//
// core.pager and textconv are not tested: git grep without a terminal never
// starts a pager, and textconv only applies with --textconv, which is not
// passed, so both are inert for this search.
//
// A control run proves the planted config is live: plain git, without the
// hardening flags, does run it. Without the control a green test could mean
// "the config never worked".
func TestHostileRepoConfigStartsNoProgram(t *testing.T) {
	env := searchtest.GitEnv(t)
	g, err := search.NewGitGrepper(env, platform.Start)
	if err != nil {
		t.Fatal(err)
	}
	dir := searchtest.InitRepo(t, env, map[string]string{"a.txt": "needle\n"})
	markers := t.TempDir()
	fsMarker := filepath.Join(markers, "fsmonitor ran")
	hookMarker := filepath.Join(markers, "hook ran")

	// The trailing "#" comments out the arguments git appends to fsmonitor.
	searchtest.RunGit(t, env, dir, "config", "core.fsmonitor", "echo hit > '"+filepath.ToSlash(fsMarker)+"' #")
	hooks := filepath.Join(t.TempDir(), "hooks dir")
	if err := os.MkdirAll(hooks, 0o750); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho hit > '" + filepath.ToSlash(hookMarker) + "'\n"
	for _, h := range []string{"pre-commit", "post-checkout", "post-index-change"} {
		if err := os.WriteFile(filepath.Join(hooks, h), []byte(script), 0o755); err != nil { //nolint:gosec // a test hook must be executable
			t.Fatal(err)
		}
	}
	searchtest.RunGit(t, env, dir, "config", "core.hooksPath", filepath.ToSlash(hooks))

	// Make the index stale, so anything that refreshes it has a reason to.
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("needle\nneedle again\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []coresearch.Mode{coresearch.ModeLiteral, coresearch.ModeRegex} {
		res, err := g.Grep(context.Background(), dir, coresearch.Query{Pattern: "needle", Mode: mode, Case: true})
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if len(res.Matches) == 0 {
			t.Fatalf("%s: the search found nothing", mode)
		}
	}
	for name, m := range map[string]string{"core.fsmonitor": fsMarker, "core.hooksPath": hookMarker} {
		if _, err := os.Stat(m); err == nil {
			t.Errorf("%s from the repo config ran during the search", name)
		}
	}

	// Control: git without the flags runs both.
	searchtest.RunGit(t, env, dir, "status", "--porcelain")
	searchtest.RunGit(t, env, dir, "-c", "commit.gpgsign=false", "commit", "-q", "-a", "-m", "control")
	for name, m := range map[string]string{"core.fsmonitor": fsMarker, "core.hooksPath": hookMarker} {
		if _, err := os.Stat(m); err != nil {
			t.Skipf("control: %s did not run with plain git (%v); the planted config is not live on this git, so the absence above proves nothing", name, err)
		}
	}
}

// A tracked symlink whose target is outside the repo, created without any OS
// symlink privilege: the index entry (mode 120000) is written with
// update-index. The search must not read the target and no match may lead out
// of the repo (invariant 6). The OS-symlink variant is
// TestTrackedSymlinksDoNotLeakOutsideFiles (it skips without the privilege).
func TestIndexedSymlinkDoesNotLeakOutsideFile(t *testing.T) {
	env := searchtest.GitEnv(t)
	g, err := search.NewGitGrepper(env, platform.Start)
	if err != nil {
		t.Fatal(err)
	}
	const secret = "OUTSIDE_SECRET_acme_4c1"
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := searchtest.InitRepo(t, env, map[string]string{"a.txt": "inside\n"})
	rel, err := filepath.Rel(dir, outside)
	if err != nil {
		t.Skip("no relative path to the other folder")
	}
	target := filepath.ToSlash(filepath.Join(rel, "secret.txt"))
	blob := searchtest.GitOutput(t, env, dir, target, "hash-object", "-w", "--stdin")
	searchtest.RunGit(t, env, dir, "update-index", "--add", "--cacheinfo", "120000,"+blob+",link")
	searchtest.RunGit(t, env, dir, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "link")
	// Put the entry on disk the way git does here: a real symlink where the
	// OS allows it, else (core.symlinks=false) a plain file holding the target.
	searchtest.RunGit(t, env, dir, "checkout-index", "-f", "--", "link")

	for _, mode := range []coresearch.Mode{coresearch.ModeLiteral, coresearch.ModeRegex} {
		// "secret" may find the link's own text (its target path); the secret
		// content must never be found.
		for _, pat := range []string{secret, "secret"} {
			res, err := g.Grep(context.Background(), dir, coresearch.Query{Pattern: pat, Mode: mode, Case: true})
			if err != nil {
				t.Fatalf("%s %q: %v", mode, pat, err)
			}
			for _, m := range res.Matches {
				if strings.Contains(m.Text, secret) || !filepath.IsLocal(filepath.FromSlash(m.Path)) {
					t.Errorf("%s %q: a match leads out of the repo: %+v", mode, pat, m)
				}
			}
		}
	}
}
