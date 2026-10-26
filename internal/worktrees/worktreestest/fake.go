// Package worktreestest has fakes for the capabilities of internal/worktrees
// and the contract suite that runs against the fake and the real
// implementation (docs/AGENTS.md 5.11).
package worktreestest

import (
	"context"
	"errors"
	"os"
	"sync"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/git"
)

// Rand is a scripted worktrees.Rand: it counts up, so the first name drawn
// from a fresh Rand is always the same and later draws differ.
type Rand struct {
	mu sync.Mutex
	n  int
}

// IntN implements worktrees.Rand.
func (r *Rand) IntN(n int) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	v := r.n % n
	r.n++
	return v
}

// Git is an in-memory worktrees.Git. WorktreeAdd makes the folder, like git.
type Git struct {
	mu       sync.Mutex
	branches map[string]map[string]bool // repo path -> branch
	bad      map[string]bool
	// Adds counts successful WorktreeAdd calls.
	Adds int
}

// NewGit returns an empty fake.
func NewGit() *Git {
	return &Git{branches: map[string]map[string]bool{}, bad: map[string]bool{}}
}

// MarkBad makes every call for the repo path fail, like a broken repo.
func (g *Git) MarkBad(repo string) { g.mu.Lock(); g.bad[repo] = true; g.mu.Unlock() }

// AddBranch makes the branch exist in the repo.
func (g *Git) AddBranch(repo, branch string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.branches[repo] == nil {
		g.branches[repo] = map[string]bool{}
	}
	g.branches[repo][branch] = true
}

// HasBranch reports whether the branch exists in the repo.
func (g *Git) HasBranch(repo, branch string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.branches[repo][branch]
}

var errBad = errors.New("fake git: repo is broken")

// WorktreeAdd implements worktrees.Git.
func (g *Git) WorktreeAdd(_ context.Context, repo, path, branch, start string) (git.Worktree, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.bad[repo] {
		return git.Worktree{}, errBad
	}
	if g.branches[repo][branch] {
		return git.Worktree{}, errors.New("fake git: branch exists")
	}
	if start == "" {
		return git.Worktree{}, errors.New("fake git: no start point")
	}
	if err := os.Mkdir(path, 0o750); err != nil {
		return git.Worktree{}, err
	}
	if g.branches[repo] == nil {
		g.branches[repo] = map[string]bool{}
	}
	g.branches[repo][branch] = true
	g.Adds++
	return git.Worktree{Path: path, Branch: branch}, nil
}

// FakeCommit is the commit every fake repo resolves to.
const FakeCommit = "0123456789abcdef0123456789abcdef01234567"

// ResolveCommit implements worktrees.Git.
func (g *Git) ResolveCommit(_ context.Context, repo, _ string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.bad[repo] {
		return "", errBad
	}
	return FakeCommit, nil
}

// BranchExists implements worktrees.Git.
func (g *Git) BranchExists(_ context.Context, repo, name string) (bool, bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.bad[repo] {
		return false, false, errBad
	}
	return g.branches[repo][name], false, nil
}

// Store is an in-memory worktrees.Store.
type Store struct {
	mu     sync.Mutex
	rows   []core.Worktree
	bases  map[core.WorktreeID]string
	Audits []core.AuditEntry
}

// NewStore returns an empty fake.
func NewStore() *Store { return &Store{bases: map[core.WorktreeID]string{}} }

// List implements worktrees.Store.
func (s *Store) List(_ context.Context, task core.TaskID) ([]core.Worktree, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []core.Worktree
	for _, w := range s.rows {
		if w.TaskID == task {
			out = append(out, w)
		}
	}
	return out, nil
}

// Record implements worktrees.Store.
func (s *Store) Record(_ context.Context, w core.Worktree, base string, audit core.AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, w)
	s.bases[w.ID] = base
	s.Audits = append(s.Audits, audit)
	return nil
}

// Base returns the recorded base commit.
func (s *Store) Base(id core.WorktreeID) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bases[id]
}
