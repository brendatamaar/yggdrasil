// Package searchtest holds the fake Grepper and the contract suite that runs
// against the fake and against real git (docs/design/code-search.md,
// Requirements).
package searchtest

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	coresearch "github.com/brendatamaar/yggdrasil/internal/core/search"
	"github.com/brendatamaar/yggdrasil/internal/search"
)

// Fake is an in-memory Grepper. A "repo" is a fake folder name with files;
// Script replaces the answer of one folder. It is safe for concurrent use.
//
// Known differences from git grep, so a green contract run is read correctly:
//   - The pattern engine is Go RE2, not POSIX ERE. Patterns that both accept
//     behave alike for the contract's cases; back-references, "\d", "(?i)" and
//     leftmost-longest matching differ. The contract only uses patterns on
//     which both agree (and "(ab" / "[a" as patterns both refuse).
//   - There is no tracked/untracked concept: every registered file is treated
//     as tracked and committed, and there is no .gitignore, symlink or
//     submodule handling.
//   - A folder made by NotRepo behaves like a folder without .git.
type Fake struct {
	mu      sync.Mutex
	repos   map[string]map[string]string
	notRepo map[string]bool
	scripts map[string]func(ctx context.Context) (search.GrepResult, error)
	n       int

	// Calls counts Grep calls; Active and MaxActive track concurrency.
	Calls     atomic.Int64
	Active    atomic.Int64
	MaxActive atomic.Int64
	Dirs      []string // every dir ever asked for, in call order
}

// NewFake returns an empty fake.
func NewFake() *Fake {
	return &Fake{repos: map[string]map[string]string{}, notRepo: map[string]bool{}, scripts: map[string]func(context.Context) (search.GrepResult, error){}}
}

// Repo registers a repo with files (path to content, "/" separators) and
// returns its fake folder name.
func (f *Fake) Repo(files map[string]string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	dir := fmt.Sprintf("fake-repo-%d", f.n)
	f.repos[dir] = files
	return dir
}

// NotRepo registers an existing folder that is not a git repository; Grep on
// it fails with ErrGitFailed, like git does outside a repo.
func (f *Fake) NotRepo() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	dir := fmt.Sprintf("fake-folder-%d", f.n)
	f.repos[dir] = nil
	f.notRepo[dir] = true
	return dir
}

// Block registers a repo whose Grep blocks until its context ends and then
// returns the context's error. started is closed once Grep is running, so a
// test can cancel while the search is in flight.
func (f *Fake) Block() (dir string, started <-chan struct{}) {
	dir = f.Repo(map[string]string{"a.txt": "needle\n"})
	ch := make(chan struct{})
	var once sync.Once
	f.Script(dir, func(ctx context.Context) (search.GrepResult, error) {
		once.Do(func() { close(ch) })
		<-ctx.Done()
		return search.GrepResult{}, ctx.Err()
	})
	return dir, ch
}

// Script makes Grep on dir run fn instead of searching files.
func (f *Fake) Script(dir string, fn func(ctx context.Context) (search.GrepResult, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scripts[dir] = fn
	if _, ok := f.repos[dir]; !ok {
		f.repos[dir] = nil
	}
}

// Grep implements search.Grepper.
func (f *Fake) Grep(ctx context.Context, dir string, q coresearch.Query) (search.GrepResult, error) {
	f.Calls.Add(1)
	cur := f.Active.Add(1)
	defer f.Active.Add(-1)
	for {
		m := f.MaxActive.Load()
		if cur <= m || f.MaxActive.CompareAndSwap(m, cur) {
			break
		}
	}
	f.mu.Lock()
	f.Dirs = append(f.Dirs, dir)
	files, ok := f.repos[dir]
	script := f.scripts[dir]
	isFolder := f.notRepo[dir]
	f.mu.Unlock()
	if !ok {
		return search.GrepResult{}, search.ErrMissing
	}
	if isFolder {
		return search.GrepResult{}, fmt.Errorf("%w: %s is not the top folder of a git repository", search.ErrGitFailed, dir)
	}
	if err := ctx.Err(); err != nil {
		return search.GrepResult{}, err
	}
	if script != nil {
		return script(ctx)
	}
	re, err := compile(q)
	if err != nil {
		return search.GrepResult{}, fmt.Errorf("%w: %w", search.ErrGitFailed, err)
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var res search.GrepResult
	for _, p := range paths {
		body := files[p]
		if strings.ContainsRune(body, 0) || !pathMatches(q.Paths, p) { // -I: binary files are skipped
			continue
		}
		for i, line := range strings.Split(body, "\n") {
			loc := re.FindStringIndex(line)
			if loc == nil {
				continue
			}
			if len(res.Matches) >= coresearch.MaxPerRepo {
				res.Truncated = true
				return res, nil
			}
			res.Matches = append(res.Matches, coresearch.Match{
				Path: p, Line: i + 1, Col: loc[0] + 1, Text: coresearch.TrimText(strings.TrimSuffix(line, "\r")),
			})
		}
	}
	return res, nil
}

func compile(q coresearch.Query) (*regexp.Regexp, error) {
	p := q.Pattern
	if q.Mode != coresearch.ModeRegex {
		p = regexp.QuoteMeta(p)
	}
	if !q.Case {
		p = "(?i)" + p
	}
	return regexp.Compile(p)
}

// pathMatches is git's pathspec for plain globs: "*" also crosses "/".
func pathMatches(specs []string, p string) bool {
	if len(specs) == 0 {
		return true
	}
	for _, s := range specs {
		var b strings.Builder
		b.WriteString("^")
		for _, r := range s {
			switch r {
			case '*':
				b.WriteString(".*")
			case '?':
				b.WriteString(".")
			default:
				b.WriteString(regexp.QuoteMeta(string(r)))
			}
		}
		b.WriteString("$")
		if regexp.MustCompile(b.String()).MatchString(p) {
			return true
		}
	}
	return false
}

var _ search.Grepper = (*Fake)(nil)
