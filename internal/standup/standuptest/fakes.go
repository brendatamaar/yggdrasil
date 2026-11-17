// Package standuptest holds the fakes of the standup sources, with scripted
// failures, and the contract suites that every implementation of a source must
// pass: time filtering over [from, to), bounds, a missing source, and
// cancellation (AU-02). The fakes answer exactly like the contract says, so a
// test of the service can rely on them.
package standuptest

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corestandup "github.com/brendatamaar/yggdrasil/internal/core/standup"
	"github.com/brendatamaar/yggdrasil/internal/repos"
	"github.com/brendatamaar/yggdrasil/internal/standup"
)

// ErrMissing is what a fake returns for a repo it does not have, or one that was
// marked broken.
var ErrMissing = errors.New("standuptest: source missing")

// script holds the scripted failures shared by the source fakes.
type script struct {
	mu     sync.Mutex
	broken map[core.RepoID]error
	block  map[core.RepoID]bool
	calls  map[core.RepoID]int
}

func (s *script) init() {
	if s.broken == nil {
		s.broken = map[core.RepoID]error{}
		s.block = map[core.RepoID]bool{}
		s.calls = map[core.RepoID]int{}
	}
}

// Fail makes every read of repo return err.
func (s *script) Fail(repo core.RepoID, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	s.broken[repo] = err
}

// Block makes every read of repo wait until its context is done.
func (s *script) Block(repo core.RepoID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	s.block[repo] = true
}

// Calls is how many reads of repo started.
func (s *script) Calls(repo core.RepoID) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	return s.calls[repo]
}

// TotalCalls is how many reads of any repo started.
func (s *script) TotalCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.calls {
		n += c
	}
	return n
}

// enter applies the scripted failures of one read.
func (s *script) enter(ctx context.Context, repo core.RepoID, known bool) error {
	s.mu.Lock()
	s.init()
	s.calls[repo]++
	err, broken := s.broken[repo]
	block := s.block[repo]
	s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	if broken {
		if err == nil {
			err = ErrMissing
		}
		return err
	}
	if !known {
		return ErrMissing
	}
	return nil
}

func window[T any](in []T, at func(T) time.Time, from, to time.Time, max int, newestFirst func(a, b T) int) ([]T, bool) {
	var out []T
	for _, v := range in {
		if t := at(v); !t.Before(from) && t.Before(to) {
			out = append(out, v)
		}
	}
	slices.SortFunc(out, newestFirst)
	if len(out) > max {
		return out[:max], true
	}
	return out, false
}

// Commits is a fake repos.HistoryReader.
type Commits struct {
	script
	mu   sync.Mutex
	recs map[core.RepoID][]repos.HistoryCommit
}

var _ standup.CommitSource = (*Commits)(nil)

// Seed sets the commits of a repo (and makes the repo known).
func (f *Commits) Seed(repo core.RepoID, cs []repos.HistoryCommit) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recs == nil {
		f.recs = map[core.RepoID][]repos.HistoryCommit{}
	}
	f.recs[repo] = slices.Clone(cs)
}

func (f *Commits) Commits(ctx context.Context, repo core.RepoID, from, to time.Time, max int) ([]repos.HistoryCommit, bool, error) {
	f.mu.Lock()
	recs, known := f.recs[repo]
	f.mu.Unlock()
	if err := f.enter(ctx, repo, known); err != nil {
		return nil, false, fmt.Errorf("%w: %w", repos.ErrHistoryUnavailable, err)
	}
	out, trunc := window(recs, func(c repos.HistoryCommit) time.Time { return c.At }, from, to, max,
		func(a, b repos.HistoryCommit) int { return cmp.Or(b.At.Compare(a.At), cmp.Compare(a.OID, b.OID)) })
	return out, trunc, nil
}

// MRs is a fake standup.MRSource over cached data.
type MRs struct {
	script
	mu   sync.Mutex
	recs map[core.RepoID][]corestandup.MR
}

var _ standup.MRSource = (*MRs)(nil)

// Seed sets the merge requests of a repo (and makes the repo known).
func (f *MRs) Seed(repo core.RepoID, ms []corestandup.MR) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recs == nil {
		f.recs = map[core.RepoID][]corestandup.MR{}
	}
	f.recs[repo] = slices.Clone(ms)
}

func (f *MRs) MRs(ctx context.Context, repo core.RepoID, from, to time.Time, max int) ([]corestandup.MR, bool, error) {
	f.mu.Lock()
	recs, known := f.recs[repo]
	f.mu.Unlock()
	if err := f.enter(ctx, repo, known); err != nil {
		return nil, false, err
	}
	out, trunc := window(recs, func(m corestandup.MR) time.Time { return m.At }, from, to, max,
		func(a, b corestandup.MR) int { return cmp.Or(b.At.Compare(a.At), cmp.Compare(a.Number, b.Number)) })
	return out, trunc, nil
}

// Builds is a fake standup.BuildSource over store data.
type Builds struct {
	script
	mu   sync.Mutex
	recs map[core.RepoID][]corestandup.Build
}

var _ standup.BuildSource = (*Builds)(nil)

// Seed sets the builds of a repo (and makes the repo known).
func (f *Builds) Seed(repo core.RepoID, bs []corestandup.Build) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recs == nil {
		f.recs = map[core.RepoID][]corestandup.Build{}
	}
	f.recs[repo] = slices.Clone(bs)
}

func (f *Builds) Builds(ctx context.Context, repo core.RepoID, from, to time.Time, max int) ([]corestandup.Build, bool, error) {
	f.mu.Lock()
	recs, known := f.recs[repo]
	f.mu.Unlock()
	if err := f.enter(ctx, repo, known); err != nil {
		return nil, false, err
	}
	out, trunc := window(recs, func(b corestandup.Build) time.Time { return b.At }, from, to, max,
		func(a, b corestandup.Build) int { return cmp.Or(b.At.Compare(a.At), cmp.Compare(a.ID, b.ID)) })
	return out, trunc, nil
}

// Scope is a fake standup.ScopeReader. Ready lists the tracked, ready repos;
// a repo that is reserved or untracked is simply not in it.
type Scope struct {
	mu       sync.Mutex
	projects map[core.ProjectID][]standup.RepoRef
	err      error
	calls    int
	// OnCall, when set, runs at the start of every Repos call with its 1-based
	// number, so a test can change the scope between the first and second read.
	OnCall func(n int)
}

var _ standup.ScopeReader = (*Scope)(nil)

// Set replaces the ready repos of a project.
func (f *Scope) Set(project core.ProjectID, refs ...standup.RepoRef) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.projects == nil {
		f.projects = map[core.ProjectID][]standup.RepoRef{}
	}
	f.projects[project] = slices.Clone(refs)
}

// Fail makes Repos return err (nil restores it).
func (f *Scope) Fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *Scope) Repos(ctx context.Context, project core.ProjectID) ([]standup.RepoRef, error) {
	f.mu.Lock()
	f.calls++
	n, hook := f.calls, f.OnCall
	f.mu.Unlock()
	if hook != nil {
		hook(n)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.err != nil {
		return nil, f.err
	}
	refs, ok := f.projects[project]
	if !ok {
		return nil, ErrNoProject
	}
	return slices.Clone(refs), nil
}

// ErrNoProject is the fake's untracked-project error. Wrap or replace it with
// db.ErrNotFound in a test that needs the real class.
var ErrNoProject = errors.New("standuptest: project not tracked")

// Polisher is a fake standup.Polisher. It is idempotent per request ID, like the
// ordinary action path: a repeat returns the first task.
type Polisher struct {
	mu    sync.Mutex
	tasks map[core.RequestID]core.TaskID
	// Calls records every call, repeats included.
	Calls []PolishCall
	Err   error
	// Gate, when set, is waited on inside SubmitPolish (to hold a first call
	// while a second arrives).
	Gate chan struct{}
}

// PolishCall is one SubmitPolish call.
type PolishCall struct {
	ID           core.RequestID
	Project      core.ProjectID
	Text         string
	Agent, Model string
}

var _ standup.Polisher = (*Polisher)(nil)

func (f *Polisher) SubmitPolish(ctx context.Context, id core.RequestID, project core.ProjectID, text, agent, model string) (core.TaskID, error) {
	f.mu.Lock()
	f.Calls = append(f.Calls, PolishCall{ID: id, Project: project, Text: text, Agent: agent, Model: model})
	gate, err := f.Gate, f.Err
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tasks == nil {
		f.tasks = map[core.RequestID]core.TaskID{}
	}
	if t, ok := f.tasks[id]; ok {
		return t, nil
	}
	t := core.TaskID(fmt.Sprintf("tsk_%d", len(f.tasks)+1))
	f.tasks[id] = t
	return t, nil
}

// CallCount is the number of SubmitPolish calls so far.
func (f *Polisher) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Calls)
}

// Hub records what the service publishes. It implements api.Phase6Hub.
type Hub struct {
	mu      sync.Mutex
	Actions []api.ActionChangedMessage
}

var _ api.Phase6Hub = (*Hub)(nil)

func (h *Hub) PublishNotice(api.NoticeChangedMessage)   {}
func (h *Hub) PublishNewRepo(api.NewRepoChangedMessage) {}
func (h *Hub) PublishAction(m api.ActionChangedMessage) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.Actions = append(h.Actions, m)
}

// Published returns a copy of the recorded action messages.
func (h *Hub) Published() []api.ActionChangedMessage {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.Actions)
}
