// Package repostest holds the test helpers of internal/repos: the scripted
// StatusReader fake and the contract suite that the fake and the real reader
// both pass.
package repostest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/repos"
)

// StatusScenario is one situation the contract describes.
type StatusScenario string

const (
	// ScenarioClean: branch main, one commit "init", nothing changed.
	ScenarioClean StatusScenario = "clean"
	// ScenarioDirty: one staged, one unstaged and one untracked file.
	ScenarioDirty StatusScenario = "dirty"
	// ScenarioAheadBehind: tracks an upstream, one commit ahead and one behind.
	ScenarioAheadBehind StatusScenario = "ahead_behind"
	// ScenarioDetached: HEAD is detached.
	ScenarioDetached StatusScenario = "detached"
	// ScenarioNoUpstream: branch main without an upstream.
	ScenarioNoUpstream StatusScenario = "no_upstream"
	// ScenarioNoCommits: a fresh repo without a commit.
	ScenarioNoCommits StatusScenario = "no_commits"
	// ScenarioMissing: the folder does not exist.
	ScenarioMissing StatusScenario = "missing"
	// ScenarioNotARepo: a plain folder.
	ScenarioNotARepo StatusScenario = "not_a_repo"
	// ScenarioTimedOut: a repo read under an expired deadline.
	ScenarioTimedOut StatusScenario = "timed_out"
)

// StatusSetup prepares a scenario for the reader under test and returns the
// folder to read. The fake scripts a result; the real reader gets a git repo
// built in t.TempDir().
type StatusSetup interface {
	Prepare(t *testing.T, s StatusScenario) corerepos.CleanPath
}

// StatusReaderContract is the contract of repos.StatusReader. newReader makes
// a reader and its setup. It runs against the fake and against real git.
func StatusReaderContract(t *testing.T, newReader func(t *testing.T) (repos.StatusReader, StatusSetup)) {
	t.Helper()
	type check func(t *testing.T, snap corerepos.Snapshot, kind corerepos.ProblemKind, err error)
	ok := func(f func(t *testing.T, s corerepos.Snapshot)) check {
		return func(t *testing.T, snap corerepos.Snapshot, kind corerepos.ProblemKind, err error) {
			t.Helper()
			if err != nil || kind != "" {
				t.Fatalf("Read failed: kind %q, err %v", kind, err)
			}
			f(t, snap)
		}
	}
	fails := func(want corerepos.ProblemKind) check {
		return func(t *testing.T, _ corerepos.Snapshot, kind corerepos.ProblemKind, err error) {
			t.Helper()
			if err == nil || kind != want {
				t.Fatalf("Read = kind %q, err %v; want a %q failure", kind, err, want)
			}
		}
	}
	cases := []struct {
		s     StatusScenario
		check check
	}{
		{ScenarioClean, ok(func(t *testing.T, s corerepos.Snapshot) {
			if s.Branch != "main" || s.Detached || s.Ahead+s.Behind+s.Staged+s.Unstaged+s.Untracked+s.Conflicted != 0 {
				t.Errorf("clean repo = %+v", s)
			}
			if s.LastCommit == nil || s.LastCommit.Subject != "init" || s.LastCommit.OID == "" || s.LastCommit.Author == "" || s.LastCommit.At.IsZero() {
				t.Errorf("last commit = %+v", s.LastCommit)
			}
		})},
		{ScenarioDirty, ok(func(t *testing.T, s corerepos.Snapshot) {
			if s.Staged != 1 || s.Unstaged != 1 || s.Untracked != 1 || s.Conflicted != 0 {
				t.Errorf("dirty counts = %+v", s)
			}
		})},
		{ScenarioAheadBehind, ok(func(t *testing.T, s corerepos.Snapshot) {
			if !s.HasUpstream || s.Upstream == "" || s.Ahead != 1 || s.Behind != 1 {
				t.Errorf("ahead/behind = %+v", s)
			}
		})},
		{ScenarioDetached, ok(func(t *testing.T, s corerepos.Snapshot) {
			if !s.Detached || s.Branch != "" {
				t.Errorf("detached = %+v", s)
			}
		})},
		{ScenarioNoUpstream, ok(func(t *testing.T, s corerepos.Snapshot) {
			if s.HasUpstream || s.Branch != "main" {
				t.Errorf("no upstream = %+v", s)
			}
		})},
		{ScenarioNoCommits, ok(func(t *testing.T, s corerepos.Snapshot) {
			if s.LastCommit != nil {
				t.Errorf("a repo without commits has last commit %+v", s.LastCommit)
			}
		})},
		{ScenarioMissing, fails(corerepos.ProblemMissing)},
		{ScenarioNotARepo, fails(corerepos.ProblemNotARepo)},
		{ScenarioTimedOut, fails(corerepos.ProblemTimedOut)},
	}
	for _, c := range cases {
		t.Run(string(c.s), func(t *testing.T) {
			t.Parallel() // each scenario has its own reader and folders; git start-up dominates the time
			reader, setup := newReader(t)
			path := setup.Prepare(t, c.s)
			ctx := context.Background()
			if c.s == ScenarioTimedOut {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
			}
			snap, kind, err := reader.Read(ctx, path)
			c.check(t, snap, kind, err)
		})
	}
}

// FakeResult is what a FakeReader returns for one path.
type FakeResult struct {
	Snap corerepos.Snapshot
	// Kind and Err, when Err is not nil, make the read fail.
	Kind corerepos.ProblemKind
	Err  error
	// Delay is how long the read takes; it ends early when ctx ends.
	Delay time.Duration
}

// FakeReader is a scripted repos.StatusReader. It is safe for concurrent use.
type FakeReader struct {
	mu        sync.Mutex
	results   map[corerepos.CleanPath]FakeResult
	calls     map[corerepos.CleanPath]int
	active    int
	maxActive int
}

// NewFakeReader returns a reader with no scripted paths: every read of an
// unscripted path fails as "missing".
func NewFakeReader() *FakeReader {
	return &FakeReader{results: map[corerepos.CleanPath]FakeResult{}, calls: map[corerepos.CleanPath]int{}}
}

// Script sets the result for path.
func (f *FakeReader) Script(path corerepos.CleanPath, r FakeResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results[path] = r
}

// Calls returns how many reads path has had.
func (f *FakeReader) Calls(path corerepos.CleanPath) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[path]
}

// MaxActive returns the most reads that ran at the same time.
func (f *FakeReader) MaxActive() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxActive
}

// Read implements repos.StatusReader.
func (f *FakeReader) Read(ctx context.Context, path corerepos.CleanPath) (corerepos.Snapshot, corerepos.ProblemKind, error) {
	f.mu.Lock()
	res, scripted := f.results[path]
	f.calls[path]++
	f.active++
	f.maxActive = max(f.maxActive, f.active)
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}()
	if res.Delay > 0 {
		select {
		case <-time.After(res.Delay):
		case <-ctx.Done():
		}
	}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return corerepos.Snapshot{}, corerepos.ProblemTimedOut, fmt.Errorf("read %s: %w", path, ctx.Err())
	case ctx.Err() != nil:
		return corerepos.Snapshot{}, corerepos.ProblemGitFailed, fmt.Errorf("read %s: %w", path, ctx.Err())
	case !scripted:
		return corerepos.Snapshot{}, corerepos.ProblemMissing, fmt.Errorf("the folder %s does not exist", path)
	case res.Err != nil:
		return corerepos.Snapshot{}, res.Kind, res.Err
	}
	return res.Snap, "", nil
}

// FakeSetup scripts the contract scenarios on a FakeReader.
type FakeSetup struct{ Reader *FakeReader }

// Prepare implements StatusSetup.
func (s FakeSetup) Prepare(_ *testing.T, sc StatusScenario) corerepos.CleanPath {
	path := corerepos.CleanPath("/fake/" + string(sc))
	commit := &corerepos.CommitRef{OID: "0123456789abcdef", Subject: "init", Author: "Ada Test", At: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	snap := corerepos.Snapshot{Branch: "main", LastCommit: commit}
	res := FakeResult{}
	switch sc {
	case ScenarioClean:
	case ScenarioDirty:
		snap.Staged, snap.Unstaged, snap.Untracked = 1, 1, 1
	case ScenarioAheadBehind:
		snap.HasUpstream, snap.Upstream, snap.Ahead, snap.Behind = true, "origin/main", 1, 1
	case ScenarioDetached:
		snap.Branch, snap.Detached = "", true
	case ScenarioNoUpstream:
	case ScenarioNoCommits:
		snap.LastCommit = nil
	case ScenarioMissing:
		return path // unscripted
	case ScenarioNotARepo:
		res = FakeResult{Kind: corerepos.ProblemNotARepo, Err: errors.New("not a git repository")}
	case ScenarioTimedOut:
		res = FakeResult{Snap: snap, Delay: time.Minute}
	}
	res.Snap = snap
	s.Reader.Script(path, res)
	return path
}
