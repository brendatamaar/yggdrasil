package search_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	coresearch "github.com/brendatamaar/yggdrasil/internal/core/search"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/search"
	"github.com/brendatamaar/yggdrasil/internal/search/searchtest"
)

func formatLine(m coresearch.Match) string {
	return fmt.Sprintf("%s\x00%d\x00%d\x00%s", m.Path, m.Line, m.Col, m.Text)
}

func TestParseGrepLineRoundTrip(t *testing.T) {
	for _, m := range []coresearch.Match{
		{Path: "src/App.java", Line: 2, Col: 7, Text: "  int needle = 1;"},
		{Path: "with space/a b.txt", Line: 1, Col: 1, Text: "text: with : colons"},
		{Path: "-dash/f.txt", Line: 99999, Col: 12, Text: ""},
		{Path: "ünï/ç.go", Line: 3, Col: 2, Text: "héllo"},
	} {
		got, ok := search.ParseGrepLine(formatLine(m))
		if !ok || got != m {
			t.Errorf("parse(format(%+v)) = %+v, %v", m, got, ok)
		}
	}
}

func TestParseGrepLineRejects(t *testing.T) {
	cases := map[string]string{
		"no separators":      "just some text",
		"no NUL after path":  "a.txt:1:2:text",
		"line not a number":  "a.txt\x00x\x002\x00t",
		"column not number":  "a.txt\x001\x00y\x00t",
		"negative line":      "a.txt\x00-1\x002\x00t",
		"empty line number":  "a.txt\x00\x002\x00t",
		"missing text field": "a.txt\x001\x002",
		"empty path":         "\x001\x002\x00t",
		"absolute path":      "/etc/passwd\x001\x002\x00t",
		"parent path":        "../x/a.txt\x001\x002\x00t",
		"inner parent":       "a/../../x\x001\x002\x00t",
		"backslash path":     "a\\b.txt\x001\x002\x00t",
		"huge number":        "a.txt\x009999999999\x002\x00t",
	}
	for name, line := range cases {
		if m, ok := search.ParseGrepLine(line); ok {
			t.Errorf("%s: accepted %+v", name, m)
		}
	}
}

func TestParseGrepLineTrimsAndDropsCR(t *testing.T) {
	m, ok := search.ParseGrepLine("a.txt\x001\x001\x00" + strings.Repeat("é", 500) + "\r")
	if !ok || utf8.RuneCountInString(m.Text) != coresearch.MaxTextRunes || strings.Contains(m.Text, "\r") {
		t.Fatalf("got %d runes, ok=%v", utf8.RuneCountInString(m.Text), ok)
	}
}

func FuzzParseGrepLine(f *testing.F) {
	f.Add("a.txt\x001\x002\x00text")
	f.Add("../a\x001\x002\x00t")
	f.Add("\x00\x00\x00")
	f.Fuzz(func(t *testing.T, line string) {
		m, ok := search.ParseGrepLine(line)
		if !ok {
			return
		}
		// accepted => valid
		if m.Path == "" || !filepath.IsLocal(filepath.FromSlash(m.Path)) || strings.HasPrefix(m.Path, "/") ||
			m.Line < 0 || m.Col < 0 || utf8.RuneCountInString(m.Text) > coresearch.MaxTextRunes {
			t.Fatalf("accepted an invalid match %+v from %q", m, line)
		}
	})
}

// recorder wraps platform.Start and remembers every process.
type recorder struct {
	mu    sync.Mutex
	procs []platform.Process
	specs []platform.Spec
}

func (r *recorder) start(ctx context.Context, spec platform.Spec) (platform.Process, error) {
	p, err := platform.Start(ctx, spec)
	if err == nil {
		r.mu.Lock()
		r.procs = append(r.procs, p)
		r.specs = append(r.specs, spec)
		r.mu.Unlock()
	}
	return p, err
}

func (r *recorder) allExited(t *testing.T) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.procs {
		done := make(chan struct{})
		go func() { _, _ = p.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("git process %d is still running", p.Pid())
		}
	}
}

func TestArgvKeepsPatternAndPathsAsData(t *testing.T) {
	env := searchtest.GitEnv(t)
	g, err := search.NewGitGrepper(env, platform.Start)
	if err != nil {
		t.Fatal(err)
	}
	q := coresearch.Query{Pattern: "--open-files-in-pager=cmd", Mode: coresearch.ModeLiteral, Case: false,
		Paths: []string{"*.java", "src/-x"}}
	argv := g.Argv(q)
	e := slices.Index(argv, "-e")
	dd := slices.Index(argv, "--")
	if e < 0 || argv[e+1] != q.Pattern || dd != e+2 {
		t.Fatalf("pattern is not passed as -e <pattern> --: %q", argv)
	}
	if !slices.Equal(argv[dd+1:], q.Paths) {
		t.Fatalf("pathspecs are not exactly the args after --: %q", argv)
	}
	for _, must := range []string{"-I", "-F", "-i", "--no-color", "--null", "--no-optional-locks"} {
		if !slices.Contains(argv, must) {
			t.Errorf("missing %s in %q", must, argv)
		}
	}
	for _, never := range []string{"--no-index", "--untracked", "--open-files-in-pager", "-O", "--recurse-submodules"} {
		if slices.Contains(argv[:e], never) {
			t.Errorf("%s must never be passed: %q", never, argv)
		}
	}
	if slices.Contains(argv, "--literal-pathspecs") {
		t.Errorf("a glob was requested, pathspecs cannot be literal: %q", argv)
	}
	q.Paths = nil
	q.Mode, q.Case = coresearch.ModeRegex, true
	argv = g.Argv(q)
	if !slices.Contains(argv, "--literal-pathspecs") || !slices.Contains(argv, "-E") || slices.Contains(argv, "-i") || slices.Contains(argv, "-F") {
		t.Errorf("argv = %q", argv)
	}
}

func TestGlobsCannotLeaveTheRepoOrBecomeOptions(t *testing.T) {
	env := searchtest.GitEnv(t)
	rec := &recorder{}
	g, err := search.NewGitGrepper(env, rec.start)
	if err != nil {
		t.Fatal(err)
	}
	// The parser refuses these before any process starts.
	for _, p := range []string{"../x", "-x", "..\\x", ":(top)*.go", "/abs/path", "a/../../b"} {
		_, err := search.ParseRequest(coresearch.RawQuery{Pattern: "needle", Paths: []string{p}})
		if err == nil {
			t.Errorf("path filter %q was accepted", p)
		}
	}
	// Defence in depth: even if one reached git, it is a pathspec after "--",
	// never an option and never a path outside the repo.
	dir := searchtest.InitRepo(t, env, map[string]string{"a.txt": "needle\n"})
	outside := searchtest.InitRepo(t, env, map[string]string{"secret.txt": "needle\n"})
	rel, err := filepath.Rel(dir, outside)
	if err != nil {
		t.Skip("no relative path to the other folder")
	}
	for _, p := range []string{"-x", "--open-files-in-pager=touch pwned", filepath.ToSlash(rel) + "/*"} {
		q := coresearch.Query{Pattern: "needle", Mode: coresearch.ModeLiteral, Case: true, Paths: []string{p}}
		res, err := g.Grep(context.Background(), dir, q)
		if err != nil && !errors.Is(err, search.ErrGitFailed) {
			t.Fatalf("%q: %v", p, err)
		}
		for _, m := range res.Matches {
			t.Errorf("%q: found %+v", p, m)
		}
	}
	if _, err := search.NewGitGrepper(nil, nil); err == nil {
		t.Error("a nil environment must be refused")
	}
}

func TestCancelLeavesNoProcess(t *testing.T) {
	env := searchtest.GitEnv(t)
	rec := &recorder{}
	g, err := search.NewGitGrepper(env, rec.start)
	if err != nil {
		t.Fatal(err)
	}
	dir := searchtest.InitRepo(t, env, map[string]string{"a.txt": strings.Repeat("needle\n", 2000)})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	_, _ = g.Grep(ctx, dir, coresearch.Query{Pattern: "needle", Mode: coresearch.ModeLiteral, Case: true})
	cancel()
	rec.allExited(t)
	if len(rec.procs) == 0 {
		t.Fatal("git was not started")
	}
}

func TestCapsOnALargeRepoStopGit(t *testing.T) {
	env := searchtest.GitEnv(t)
	rec := &recorder{}
	g, err := search.NewGitGrepper(env, rec.start)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for i := 0; i < 200000; i++ {
		b.WriteString("a needle on every line of this big file\n")
	}
	files := map[string]string{"big/one.txt": b.String(), "big/two.txt": b.String()}
	dir := searchtest.InitRepo(t, env, files)
	res, err := g.Grep(context.Background(), dir, coresearch.Query{Pattern: "needle", Mode: coresearch.ModeLiteral, Case: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Matches) != coresearch.MaxPerRepo || !res.Truncated {
		t.Fatalf("got %d matches, truncated=%v", len(res.Matches), res.Truncated)
	}
	rec.allExited(t)
}

func TestGitFailureIsReported(t *testing.T) {
	env := searchtest.GitEnv(t)
	g, err := search.NewGitGrepper(env, platform.Start)
	if err != nil {
		t.Fatal(err)
	}
	// A subfolder of a repo is not a tracked repo root: refuse it, so git never
	// searches the parent repo's files through a wrong path.
	parent := searchtest.InitRepo(t, env, map[string]string{"sub/loose.txt": "x\n"})
	res, err := g.Grep(context.Background(), filepath.Join(parent, "sub"), coresearch.Query{Pattern: "x", Mode: coresearch.ModeLiteral, Case: true})
	if !errors.Is(err, search.ErrGitFailed) || len(res.Matches) != 0 {
		t.Fatalf("a folder that is not a repo: matches=%v err=%v, want ErrGitFailed", res.Matches, err)
	}
	dir := searchtest.InitRepo(t, env, map[string]string{"a.txt": "x\n"})
	q := coresearch.Query{Pattern: "(", Mode: coresearch.ModeRegex, Case: true}
	if _, err := g.Grep(context.Background(), dir, q); !errors.Is(err, search.ErrGitFailed) {
		t.Fatalf("an invalid regex that reaches git: err = %v, want ErrGitFailed", err)
	}
}
