package search

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	coresearch "github.com/brendatamaar/yggdrasil/internal/core/search"
	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// Grepper searches one repo folder. It is read-only. Implementations stop at
// MaxPerRepo matches and say so in GrepResult.Truncated.
//
// Errors: ErrMissing (folder gone), ErrGitFailed (git exited with an error),
// or the context's error when ctx ended first.
type Grepper interface {
	Grep(ctx context.Context, dir string, q coresearch.Query) (GrepResult, error)
}

// GrepResult is what one repo returned. Matches carry no repo ID: the service
// stamps it. Paths are repo-relative with "/" and always inside the repo.
type GrepResult struct {
	Matches   []coresearch.Match
	Truncated bool
	Skipped   int // output lines that could not be parsed
}

var (
	// ErrMissing means the repo folder does not exist.
	ErrMissing = errors.New("the repo folder is missing")
	// ErrGitFailed means git could not run the search.
	ErrGitFailed = errors.New("git could not search the repo")
)

// maxLineBytes bounds the memory of one output line; the rest of a longer
// line is dropped (the text is trimmed to 300 runes anyway).
const maxLineBytes = 64 << 10

// maxTotalBytes bounds all stdout of one repo, including lines that fail to
// parse. Past it the search is stopped and the repo counts as truncated.
const maxTotalBytes = 8 << 20

// maxStderrBytes bounds the stderr kept for the log.
const maxStderrBytes = 4 << 10

// GitGrepper runs git grep through platform.Start. Create it with NewGitGrepper.
type GitGrepper struct {
	exe   string
	env   []string
	start platform.StartFunc
}

// NewGitGrepper finds git in env (the clean environment from internal/safety).
// start is platform.Start, or a fake in tests.
func NewGitGrepper(env []string, start platform.StartFunc) (*GitGrepper, error) {
	if env == nil {
		return nil, fmt.Errorf("git grep: %w", platform.ErrNoEnv)
	}
	exe, err := platform.Resolve("git", "", env)
	if err != nil {
		return nil, fmt.Errorf("find git: %w", err)
	}
	if start == nil {
		start = platform.Start
	}
	e := make([]string, 0, len(env)+2)
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(name) {
		case "LC_ALL", "GIT_TERMINAL_PROMPT":
			continue
		}
		e = append(e, kv)
	}
	return &GitGrepper{exe: exe, env: append(e, "GIT_TERMINAL_PROMPT=0", "LC_ALL=C"), start: start}, nil
}

// argv builds the command line. The pattern follows -e, so it is never read as
// an option; the pathspecs follow "--". Without path filters the pathspecs
// are literal; with them, git's glob pathspecs are on (the core already
// refused ":" magic, a leading "-" and "..").
func (g *GitGrepper) argv(q coresearch.Query) []string {
	a := []string{g.exe,
		"-c", "core.longpaths=true", "-c", "core.quotepath=false", "-c", "credential.helper=",
		// Repo config must not start helper processes.
		"-c", "core.fsmonitor=false", "-c", "core.hooksPath=", "-c", "protocol.allow=never",
		"--no-optional-locks"}
	if len(q.Paths) == 0 {
		a = append(a, "--literal-pathspecs")
	}
	a = append(a, "grep", "--no-color", "-n", "--column", "-I", "--null", "--no-recurse-submodules")
	if q.Mode == coresearch.ModeRegex {
		a = append(a, "-E")
	} else {
		a = append(a, "-F")
	}
	if !q.Case {
		a = append(a, "-i")
	}
	a = append(a, "-e", q.Pattern, "--")
	return append(a, q.Paths...)
}

// Grep implements Grepper.
func (g *GitGrepper) Grep(ctx context.Context, dir string, q coresearch.Query) (GrepResult, error) {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return GrepResult{}, ErrMissing
	}
	// A tracked repo is a repo root with its own .git. A subfolder of another
	// repo would make git search that repo's files, so it is refused before
	// git starts (outside any repo git fails on its own).
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return GrepResult{}, fmt.Errorf("%w: %s is not the top folder of a git repository", ErrGitFailed, dir)
	}
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sink := &lineSink{limit: coresearch.MaxPerRepo, stop: cancel}
	var stderr capBuf
	p, err := g.start(wctx, platform.Spec{Argv: g.argv(q), Dir: dir, Env: g.env, Stdout: sink, Stderr: &stderr})
	if err != nil {
		return GrepResult{}, fmt.Errorf("%w: %w", ErrGitFailed, err)
	}
	code, werr := p.Wait()
	sink.flush()
	res := GrepResult{Matches: sink.matches, Truncated: sink.full, Skipped: sink.skipped}
	switch {
	case sink.full:
		return res, nil // we stopped git ourselves; its exit code means nothing
	case ctx.Err() != nil:
		return GrepResult{}, ctx.Err()
	case werr != nil:
		return GrepResult{}, fmt.Errorf("%w: %w", ErrGitFailed, werr)
	case code == 0 || code == 1: // 1: no match
		return res, nil
	}
	return GrepResult{}, fmt.Errorf("%w: exit status %d: %s", ErrGitFailed, code, strings.TrimSpace(stderr.String()))
}

// capBuf keeps the first maxStderrBytes bytes written to it.
type capBuf struct{ b bytes.Buffer }

func (c *capBuf) Write(p []byte) (int, error) {
	if room := maxStderrBytes - c.b.Len(); room > 0 {
		c.b.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

func (c *capBuf) String() string { return c.b.String() }

// lineSink parses git's output while it streams: one match per line, no
// unbounded buffer. When a match beyond limit arrives it calls stop (which
// kills git) and drops the rest.
type lineSink struct {
	limit    int
	stop     func()
	cur      []byte
	overflow bool // the current line is longer than maxLineBytes; drop its tail
	matches  []coresearch.Match
	skipped  int
	full     bool
	total    int // bytes seen so far
}

func (s *lineSink) Write(p []byte) (int, error) {
	n := len(p)
	s.total += n
	if s.total > maxTotalBytes && !s.full {
		s.full = true
		s.stop()
	}
	for len(p) > 0 && !s.full {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			s.add(p)
			break
		}
		s.add(p[:i])
		s.endLine()
		p = p[i+1:]
	}
	return n, nil
}

func (s *lineSink) add(b []byte) {
	if s.overflow {
		return
	}
	if room := maxLineBytes - len(s.cur); len(b) > room {
		b = b[:room]
		s.overflow = true
	}
	s.cur = append(s.cur, b...)
}

func (s *lineSink) endLine() {
	line := s.cur
	s.cur, s.overflow = s.cur[:0], false
	if len(line) == 0 {
		return
	}
	m, ok := ParseGrepLine(string(line))
	if !ok {
		s.skipped++
		return
	}
	if len(s.matches) >= s.limit {
		s.full = true
		s.stop()
		return
	}
	s.matches = append(s.matches, m)
}

// flush handles a last line without a newline.
func (s *lineSink) flush() {
	if len(s.cur) > 0 && !s.full {
		s.endLine()
	}
}

// ParseGrepLine parses one line of `git grep -n --column --null` output:
// "<path>\x00<line>\x00<col>\x00<text>". It rejects lines without the NUL, with a
// non-numeric line or column, or whose path is empty, absolute or not inside
// the repo. The text is trimmed to the core's cap and loses a trailing CR.
func ParseGrepLine(line string) (coresearch.Match, bool) {
	path, rest, ok := strings.Cut(line, "\x00")
	if !ok || !insideRepo(path) {
		return coresearch.Match{}, false
	}
	ln, rest, ok := cutNumber(rest)
	if !ok {
		return coresearch.Match{}, false
	}
	col, rest, ok := cutNumber(rest)
	if !ok {
		return coresearch.Match{}, false
	}
	rest = strings.TrimSuffix(rest, "\r")
	return coresearch.Match{Path: path, Line: ln, Col: col, Text: coresearch.TrimText(rest)}, true
}

func cutNumber(s string) (n int, rest string, ok bool) {
	num, rest, found := strings.Cut(s, "\x00")
	if !found || num == "" || len(num) > 9 {
		return 0, "", false
	}
	n, err := strconv.Atoi(num)
	if err != nil || n < 0 {
		return 0, "", false
	}
	return n, rest, true
}

// insideRepo reports whether a git-reported path ("/" separators) stays inside
// the repo: relative, no "..", no drive or reserved names.
func insideRepo(p string) bool {
	if p == "" || strings.ContainsAny(p, "\x00\\") || strings.HasPrefix(p, "/") {
		return false
	}
	return filepath.IsLocal(filepath.FromSlash(p))
}

var _ Grepper = (*GitGrepper)(nil)
