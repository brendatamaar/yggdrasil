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

// UpdateComposeSettings implements worktreeenv.ComposeStore.
func (s *Store) UpdateComposeSettings(_ context.Context, repo core.RepoID, _ time.Time, audit core.AuditEntry,
	mut func(worktreeenv.ComposeSettings) (worktreeenv.ComposeSettings, bool, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.repos[repo] {
		return worktreeenv.ErrNotFound
	}
	next, write, err := mut(s.composes[repo])
	if err != nil || !write {
		return err
	}
	if s.AuditErr != nil {
		if err := s.AuditErr(audit); err != nil {
			return err
		}
	}
	if s.composes == nil {
		s.composes = map[core.RepoID]worktreeenv.ComposeSettings{}
	}
	s.composes[repo] = next
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
	if _, err := env.Argv(c.Op, c.Project, c.File, c.HasEnvFile); err != nil {
		return worktreeenv.ComposeResult{Failure: env.FailComposeError, Summary: "That compose operation is not allowed."}
	}
	r.mu.Lock()
	r.calls = append(r.calls, c)
	f, gate := r.Result, r.Gate
	r.mu.Unlock()
	if gate != nil && c.Op != env.OpPs {
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
	reads map[string]int
	// Hook, if set, is called before every read with the path and how many
	// times that path has been read (1 for the first). It may call Put.
	Hook func(rel env.RelPath, n int)
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

// Reads is how many times rel has been read below root.
func (f *ComposeFiles) Reads(root string, rel env.RelPath) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads[root+"|"+string(rel)]
}

// Read implements worktreeenv.ComposeFiles.
func (f *ComposeFiles) Read(root string, rel env.RelPath) ([]byte, error) {
	b, ok, err := f.ReadOptional(root, rel)
	if err == nil && !ok {
		return nil, worktreeenv.ErrComposeFile
	}
	return b, err
}

// ReadOptional implements worktreeenv.ComposeFiles.
func (f *ComposeFiles) ReadOptional(root string, rel env.RelPath) ([]byte, bool, error) {
	f.mu.Lock()
	if f.reads == nil {
		f.reads = map[string]int{}
	}
	key := root + "|" + string(rel)
	f.reads[key]++
	n, hook := f.reads[key], f.Hook
	f.mu.Unlock()
	if hook != nil {
		hook(rel, n)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if rel.Validate() != nil {
		return nil, false, worktreeenv.ErrComposeFile
	}
	b, ok := f.files[key]
	if !ok {
		return nil, false, nil
	}
	if len(b) > worktreeenv.MaxComposeFile {
		return nil, false, worktreeenv.ErrComposeFile
	}
	return append([]byte(nil), b...), true, nil
}
