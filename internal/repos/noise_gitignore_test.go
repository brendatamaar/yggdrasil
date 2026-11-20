package repos_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// The suggested pattern, written to a real .gitignore, hides exactly the
// generated directory at its exact repo-relative path: files outside the prefix,
// look-alikes that an unescaped metacharacter would also match, and sibling
// names that merely share a prefix all stay visible. Checked with real git
// status on a temporary repo, for LF, CRLF and no trailing newline.
func TestNoisePatternsHideExactlyTheDirectoryInRealGit(t *testing.T) {
	for _, tc := range []struct {
		dir string
		// lookalike is a path an unescaped pattern would also hide.
		lookalike string
	}{
		{"web/node_modules", "web/node_modulesX/f"},
		{"dist", "distX/f"},
		{"a b/build", "a  b/build/f"},
		{"[x]/target", "x/target/f"},
		{"a*b/dist", "aXXb/dist/f"},
		{"q?/obj", "qz/obj/f"},
		{"#hash/dist", "hash/dist/f"},
		{"!bang/bin", "bang/bin/f"},
		{"日本語/.venv", "日本/.venv/f"},
		{"ünï/__pycache__", "uni/__pycache__/f"},
		{"a{b,c}/dist", "ab/dist/f"},
		{"$HOME/dist", "home/dist/f"},
		{"~/dist", "x/dist/f"},
		{"-x/dist", "x/dist/f"},
		{"a'b/dist", "ab/dist/f"},
		{"a\"b/dist", "ab/dist/f"},
		{"a`b/dist", "ab/dist/f"},
		{"a%b/dist", "ab/dist/f"},
		{"a;b/dist", "ab/dist/f"},
		{"a&b|c/dist", "ab/dist/f"},
		{"a(b)/dist", "ab/dist/f"},
		{"a=b+c@d^e/dist", "abcde/dist/f"},
		{"deep/er/than/web/node_modules", "deep/er/than/web/node_modules_old/f"},
	} {
		for _, eol := range []struct{ name, nl string }{{"LF", "\n"}, {"CRLF", "\r\n"}, {"none", ""}} {
			t.Run(fmt.Sprintf("%s/%s", tc.dir, eol.name), func(t *testing.T) {
				facts := corerepos.NoiseFacts{Repo: "rep_1", UntrackedCount: 100}
				for i := 0; i < 60; i++ {
					facts.Sample = append(facts.Sample, fmt.Sprintf("%s/f%d", tc.dir, i))
				}
				n, _ := corerepos.NoiseStep(corerepos.Noise{Repo: "rep_1", State: corerepos.NoSuggestion{}}, corerepos.NoiseObserved{Facts: facts})
				s, ok := n.State.(corerepos.Suggested)
				if !ok || len(s.Candidates) != 1 {
					t.Fatalf("no single candidate: %+v", n.State)
				}
				pattern := s.Candidates[0].Pattern

				fx := newGitFx(t)
				repo := fx.repo("shop-web")
				skipIfOSCannotCreate(t, repo, tc.dir)
				inside := []string{tc.dir + "/f1", tc.dir + "/sub/dir/f2", tc.dir + "/.hidden"}
				outside := []string{"other/" + tc.dir + "/f", tc.lookalike, "src/main.js", "readme.txt", "x" + tc.dir + "/f"}
				for _, f := range append(slices.Clone(inside), outside...) {
					writeUnder(t, repo, f, "generated\r\n")
				}
				writeUnder(t, repo, ".gitignore", "# keep my own lines\r\n"+pattern+eol.nl)
				st, err := fx.g.Status(context.Background(), repo)
				if err != nil {
					t.Fatal(err)
				}
				var visible []string
				for _, f := range st.Files {
					visible = append(visible, f.Path)
				}
				want := append(slices.Clone(outside), ".gitignore")
				sort.Strings(visible)
				sort.Strings(want)
				if !slices.Equal(visible, want) {
					t.Fatalf("pattern %q\nvisible: %q\nwant:    %q", pattern, visible, want)
				}
			})
		}
	}
}

// skipIfOSCannotCreate skips the test when the folder cannot be created because
// the OS forbids one of its names (Windows rejects `*`, `?`, `"`, `|` and more).
// It skips only if the mkdir fails AND platform.ValidateChildName also rejects a
// component, so a real failure for a valid name still fails the test. The
// escaping of these names is covered as text in internal/core/repos.
func skipIfOSCannotCreate(t *testing.T, repo, dir string) {
	t.Helper()
	err := os.MkdirAll(filepath.Join(repo, filepath.FromSlash(dir)), 0o750)
	if err == nil {
		return
	}
	for _, c := range strings.Split(dir, "/") {
		if platform.ValidateChildName(c) != nil {
			t.Skipf("this OS cannot create a folder named %q: %v", c, err)
		}
	}
	t.Fatal(err)
}

// A pattern is only ever a directory: a file with the generated name is not
// hidden by the suggested line.
func TestNoisePatternDoesNotHideAFileOfThatName(t *testing.T) {
	facts := corerepos.NoiseFacts{Repo: "rep_1", UntrackedCount: 100}
	for i := 0; i < 60; i++ {
		facts.Sample = append(facts.Sample, fmt.Sprintf("dist/f%d", i))
	}
	n, _ := corerepos.NoiseStep(corerepos.Noise{Repo: "rep_1", State: corerepos.NoSuggestion{}}, corerepos.NoiseObserved{Facts: facts})
	pattern := n.State.(corerepos.Suggested).Candidates[0].Pattern
	fx := newGitFx(t)
	repo := fx.repo("shop-web")
	writeUnder(t, repo, "dist", "I am a file named dist\r\n") // the file, not a folder
	writeUnder(t, repo, ".gitignore", pattern+"\r\n")
	st, err := fx.g.Status(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	var visible []string
	for _, f := range st.Files {
		visible = append(visible, f.Path)
	}
	sort.Strings(visible)
	if !slices.Equal(visible, []string{".gitignore", "dist"}) {
		t.Fatalf("visible %q", visible)
	}
}
