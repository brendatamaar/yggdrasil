package worktreeenvtest

import (
	"context"
	"sync"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	env "github.com/brendatamaar/yggdrasil/internal/core/worktreeenv"
	"github.com/brendatamaar/yggdrasil/internal/worktreeenv"
)

// ComposeSettings implements worktreeenv.ComposeStore.
func (s *Store) ComposeSettings(_ context.Context, repo core.RepoID) (worktreeenv.ComposeSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.repos[repo] {
		return worktreeenv.ComposeSettings{}, worktreeenv.ErrNotFound
	}
	return s.composes[repo], nil
}

// PutComposeSettings implements worktreeenv.ComposeStore.
func (s *Store) PutComposeSettings(_ context.Context, repo core.RepoID, c worktreeenv.ComposeSettings, _ time.Time, audit core.AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.repos[repo] {
		return worktreeenv.ErrNotFound
	}
	if s.composes == nil {
		s.composes = map[core.RepoID]worktreeenv.ComposeSettings{}
	}
	s.composes[repo] = c
	s.audits = append(s.audits, audit)
	return nil
}

// ComposeRunner is a worktreeenv.ComposeRunner that runs nothing: it records
// the calls and answers from a script.
type ComposeRunner struct {
	mu    sync.Mutex
	calls []worktreeenv.ComposeCall
	// Result answers a call; nil means success with no output.
	Result func(worktreeenv.ComposeCall) worktreeenv.ComposeResult
	// Gate, if set, is received from before a call returns, so tests can hold
	// an operation open.
	Gate chan struct{}
}

// Run implements worktreeenv.ComposeRunner.
func (r *ComposeRunner) Run(ctx context.Context, c worktreeenv.ComposeCall) worktreeenv.ComposeResult {
	if _, err := env.Argv(c.Op, c.Project, c.File); err != nil {
		return worktreeenv.ComposeResult{Failure: env.FailComposeError, Summary: "That compose operation is not allowed."}
	}
	r.mu.Lock()
	r.calls = append(r.calls, c)
	f, gate := r.Result, r.Gate
	r.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return worktreeenv.ComposeResult{Failure: env.FailComposeError, Summary: "Docker compose took too long or was cancelled."}
		}
	}
	if f != nil {
		return f(c)
	}
	return worktreeenv.ComposeResult{}
}

// Calls returns the calls made so far.
func (r *ComposeRunner) Calls() []worktreeenv.ComposeCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]worktreeenv.ComposeCall(nil), r.calls...)
}

// ComposeFiles is an in-memory worktreeenv.ComposeFiles keyed by root and path.
type ComposeFiles struct {
	mu    sync.Mutex
	files map[string][]byte
}

// Put sets a file.
func (f *ComposeFiles) Put(root string, rel env.RelPath, data string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.files == nil {
		f.files = map[string][]byte{}
	}
	f.files[root+"|"+string(rel)] = []byte(data)
}

// Read implements worktreeenv.ComposeFiles.
func (f *ComposeFiles) Read(root string, rel env.RelPath) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if rel.Validate() != nil {
		return nil, worktreeenv.ErrComposeFile
	}
	b, ok := f.files[root+"|"+string(rel)]
	if !ok || len(b) > worktreeenv.MaxComposeFile {
		return nil, worktreeenv.ErrComposeFile
	}
	return append([]byte(nil), b...), nil
}
