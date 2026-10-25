package searchtest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coresearch "github.com/brendatamaar/yggdrasil/internal/core/search"
	"github.com/brendatamaar/yggdrasil/internal/search"
)

// Harness is the implementation under test: a Grepper and a way to make a
// repo holding the given files (tracked and committed).
type Harness struct {
	Grepper search.Grepper
	// Repo returns the folder of a new repo with these files.
	Repo func(files map[string]string) string
	// Missing is a folder that does not exist.
	Missing string
	// NotRepo returns an existing folder that is not a git repository.
	NotRepo func() string
	// Block returns a repo whose search stays running until its context ends,
	// and a channel that is closed once the search runs. Optional: when nil,
	// the mid-flight clause only checks that cancelling never yields a
	// failure other than the context's error.
	Block func() (dir string, started <-chan struct{})
}

// GrepperContract is the contract of search.Grepper. It runs against the fake
// and against real git on temp repos.
func GrepperContract(t *testing.T, newHarness func(t *testing.T) Harness) {
	q := func(pattern string) coresearch.Query {
		return coresearch.Query{Pattern: pattern, Mode: coresearch.ModeLiteral, Case: true}
	}
	run := func(t *testing.T, h Harness, dir string, query coresearch.Query) search.GrepResult {
		t.Helper()
		res, err := h.Grepper.Grep(context.Background(), dir, query)
		if err != nil {
			t.Fatalf("Grep: %v", err)
		}
		return res
	}
	// Git starts slowly on some machines, so the clauses run in parallel.
	sub := func(name string, fn func(t *testing.T)) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fn(t)
		})
	}
	files := map[string]string{
		"src/App.java":     "class App {\n  int needle = 1;\n}\n",
		"src/util/Aid.go":  "package util\nfunc Needle() {}\n",
		"README.md":        "hello a.c and abc\r\nsecond Needle line\r\n",
		"data/blob.bin":    "needle\x00\x01\x02 binary",
		"docs/notes.txt":   "nothing here\n",
		"regex.txt":        "abc only\n",
		"-dash/file.txt":   "needle in a dash dir\n",
		"with space/a.txt": "needle with space\n",
	}

	sub("literal hit has path line column and text", func(t *testing.T) {
		h := newHarness(t)
		res := run(t, h, h.Repo(files), q("needle = 1"))
		if len(res.Matches) != 1 {
			t.Fatalf("matches = %+v", res.Matches)
		}
		m := res.Matches[0]
		if m.Path != "src/App.java" || m.Line != 2 || m.Col != 7 || m.Text != "  int needle = 1;" {
			t.Fatalf("match = %+v", m)
		}
	})
	sub("literal mode does not read regex characters", func(t *testing.T) {
		h := newHarness(t)
		res := run(t, h, h.Repo(files), q("a.c"))
		if len(res.Matches) != 1 || res.Matches[0].Path != "README.md" || res.Matches[0].Col != 7 {
			t.Fatalf("matches = %+v", res.Matches)
		}
	})
	sub("regex hit", func(t *testing.T) {
		h := newHarness(t)
		query := q("a.c")
		query.Mode = coresearch.ModeRegex
		dir := h.Repo(files)
		res := run(t, h, dir, query)
		// README.md ("a.c" at column 7) and regex.txt ("abc" at column 1), nothing else.
		if len(res.Matches) != 2 {
			t.Fatalf("matches = %+v", res.Matches)
		}
		if a, b := res.Matches[0], res.Matches[1]; a.Path != "README.md" || a.Line != 1 || a.Col != 7 ||
			b.Path != "regex.txt" || b.Line != 1 || b.Col != 1 {
			t.Fatalf("matches = %+v", res.Matches)
		}
		query.Pattern = "ne+dle = [0-9]"
		if res := run(t, h, dir, query); len(res.Matches) != 1 || res.Matches[0].Path != "src/App.java" {
			t.Fatalf("matches = %+v", res.Matches)
		}
	})
	sub("case flag", func(t *testing.T) {
		h := newHarness(t)
		dir := h.Repo(files)
		sensitive := run(t, h, dir, q("Needle"))
		if len(sensitive.Matches) != 2 {
			t.Fatalf("case-sensitive matches = %+v", sensitive.Matches)
		}
		insensitive := q("Needle")
		insensitive.Case = false
		// App.java, Aid.go, README.md, -dash/file.txt, with space/a.txt (the binary is skipped).
		if res := run(t, h, dir, insensitive); len(res.Matches) != 5 {
			t.Fatalf("case-insensitive matches = %+v", res.Matches)
		}
		lower := run(t, h, dir, q("needle"))
		if len(lower.Matches) != 3 { // App.java, -dash/file.txt, with space/a.txt
			t.Fatalf("case-sensitive lower-case matches = %+v", lower.Matches)
		}
	})
	sub("pathspec filter", func(t *testing.T) {
		h := newHarness(t)
		query := q("needle")
		query.Paths = []string{"*.java"}
		res := run(t, h, h.Repo(files), query)
		if len(res.Matches) != 1 || res.Matches[0].Path != "src/App.java" {
			t.Fatalf("matches = %+v", res.Matches)
		}
	})
	sub("binary files are skipped", func(t *testing.T) {
		h := newHarness(t)
		for _, m := range run(t, h, h.Repo(files), q("needle")).Matches {
			if strings.HasSuffix(m.Path, ".bin") {
				t.Fatalf("binary file in results: %+v", m)
			}
		}
	})
	sub("CRLF is not part of the text", func(t *testing.T) {
		h := newHarness(t)
		res := run(t, h, h.Repo(files), q("second"))
		if len(res.Matches) != 1 || res.Matches[0].Text != "second Needle line" {
			t.Fatalf("matches = %+v", res.Matches)
		}
	})
	sub("paths with spaces and a leading dash", func(t *testing.T) {
		h := newHarness(t)
		got := map[string]bool{}
		for _, m := range run(t, h, h.Repo(files), q("needle")).Matches {
			got[m.Path] = true
		}
		if !got["with space/a.txt"] || !got["-dash/file.txt"] {
			t.Fatalf("paths = %v", got)
		}
	})
	sub("no match is empty, not an error", func(t *testing.T) {
		h := newHarness(t)
		res := run(t, h, h.Repo(files), q("zebra"))
		if len(res.Matches) != 0 || res.Truncated {
			t.Fatalf("result = %+v", res)
		}
	})
	sub("a cap stops early", func(t *testing.T) {
		h := newHarness(t)
		var b strings.Builder
		for i := 0; i < 5000; i++ {
			fmt.Fprintf(&b, "line %d needle\n", i)
		}
		res := run(t, h, h.Repo(map[string]string{"big.txt": b.String()}), q("needle"))
		if len(res.Matches) != coresearch.MaxPerRepo || !res.Truncated {
			t.Fatalf("got %d matches, truncated=%v", len(res.Matches), res.Truncated)
		}
	})
	sub("exactly the cap is not truncated", func(t *testing.T) {
		h := newHarness(t)
		var b strings.Builder
		for i := 0; i < coresearch.MaxPerRepo; i++ {
			fmt.Fprintf(&b, "line %d needle\n", i)
		}
		res := run(t, h, h.Repo(map[string]string{"edge.txt": b.String()}), q("needle"))
		if len(res.Matches) != coresearch.MaxPerRepo || res.Truncated {
			t.Fatalf("got %d matches, truncated=%v", len(res.Matches), res.Truncated)
		}
	})
	sub("long lines are trimmed", func(t *testing.T) {
		h := newHarness(t)
		res := run(t, h, h.Repo(map[string]string{"long.txt": "needle " + strings.Repeat("x", 5000) + "\n"}), q("needle"))
		if len(res.Matches) != 1 || len([]rune(res.Matches[0].Text)) != coresearch.MaxTextRunes {
			t.Fatalf("matches = %+v", res.Matches)
		}
	})
	sub("a cancelled context stops the search", func(t *testing.T) {
		h := newHarness(t)
		dir := h.Repo(files)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := h.Grepper.Grep(ctx, dir, q("needle"))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	})
	sub("cancelling mid-flight returns the context error", func(t *testing.T) {
		h := newHarness(t)
		if h.Block == nil {
			// Real git: the search may finish before the cancel lands, so only
			// the failure mode is pinned: never a git failure.
			dir := h.Repo(map[string]string{"a.txt": strings.Repeat("needle\n", 3000)})
			ctx, cancel := context.WithCancel(context.Background())
			go func() { time.Sleep(10 * time.Millisecond); cancel() }()
			_, err := h.Grepper.Grep(ctx, dir, q("needle"))
			cancel()
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v, want nil or context.Canceled", err)
			}
			return
		}
		dir, started := h.Block()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, err := h.Grepper.Grep(ctx, dir, q("needle"))
			done <- err
		}()
		select {
		case <-started:
		case <-time.After(10 * time.Second):
			t.Fatal("the search never started")
		}
		select {
		case err := <-done:
			t.Fatalf("the search ended before the cancel: %v", err)
		case <-time.After(20 * time.Millisecond):
		}
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v, want context.Canceled", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the search did not stop after the cancel")
		}
	})
	sub("a folder that is not a repo is ErrGitFailed", func(t *testing.T) {
		h := newHarness(t)
		res, err := h.Grepper.Grep(context.Background(), h.NotRepo(), q("needle"))
		if !errors.Is(err, search.ErrGitFailed) || len(res.Matches) != 0 {
			t.Fatalf("matches=%v err=%v, want ErrGitFailed", res.Matches, err)
		}
	})
	sub("a regex both engines refuse is ErrGitFailed", func(t *testing.T) {
		h := newHarness(t)
		dir := h.Repo(files)
		for _, p := range []string{"(ab", "[a"} {
			query := q(p)
			query.Mode = coresearch.ModeRegex
			if _, err := h.Grepper.Grep(context.Background(), dir, query); !errors.Is(err, search.ErrGitFailed) {
				t.Errorf("pattern %q: err = %v, want ErrGitFailed", p, err)
			}
		}
		// The same text as a literal is fine.
		if res := run(t, h, dir, q("(ab")); len(res.Matches) != 0 {
			t.Errorf("literal %q: %+v", "(ab", res.Matches)
		}
	})
	sub("a missing folder is ErrMissing", func(t *testing.T) {
		h := newHarness(t)
		if _, err := h.Grepper.Grep(context.Background(), h.Missing, q("needle")); !errors.Is(err, search.ErrMissing) {
			t.Fatalf("err = %v, want ErrMissing", err)
		}
	})
	sub("an option-looking pattern is only text", func(t *testing.T) {
		h := newHarness(t)
		dir := h.Repo(map[string]string{"a.txt": "see --open-files-in-pager=touch x and -e here\n"})
		for _, p := range []string{"--open-files-in-pager=touch x", "-e", "--help", "--no-index", "-O"} {
			res := run(t, h, dir, q(p))
			wantHit := p == "--open-files-in-pager=touch x" || p == "-e"
			if wantHit != (len(res.Matches) == 1) {
				t.Errorf("pattern %q: matches = %+v", p, res.Matches)
			}
		}
		if _, err := os.Stat(filepath.Join(dir, "x")); err == nil {
			t.Fatal("the pattern was run as a pager command")
		}
	})
}
