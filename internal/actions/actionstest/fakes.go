package actionstest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/actions"
	"github.com/brendatamaar/yggdrasil/internal/core"
	coreactions "github.com/brendatamaar/yggdrasil/internal/core/actions"
	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/hosts"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

const fakeHead = "0123456789abcdef0123456789abcdef01234567"

type fakeRepo struct {
	project  core.ProjectID
	upstream bool
	tracked  bool
	ready    bool
	dirty    bool
	broken   bool
}

// FakeScope is an in-memory actions.ScopeReader that obeys ScopeReaderContract.
type FakeScope struct {
	mu    sync.Mutex
	repos map[core.RepoID]*fakeRepo
	seq   int
}

var _ actions.ScopeReader = (*FakeScope)(nil)

// NewFakeScope returns an empty fake.
func NewFakeScope() *FakeScope { return &FakeScope{repos: map[core.RepoID]*fakeRepo{}} }

func (s *FakeScope) next(prefix string) string {
	s.seq++
	return fmt.Sprintf("%s_%016d", prefix, s.seq)
}

func (s *FakeScope) get(ctx context.Context, id core.RepoID) (*fakeRepo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r, ok := s.repos[id]
	if !ok || !r.tracked || !r.ready {
		return nil, fmt.Errorf("repo %s: %w", id, db.ErrNotFound)
	}
	return r, nil
}

func (s *FakeScope) Facts(ctx context.Context, id core.RepoID) (coreactions.Facts, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.get(ctx, id)
	if err != nil {
		return coreactions.Facts{}, err
	}
	f := coreactions.Facts{Repo: id, Readable: !r.broken}
	if r.broken {
		f.Reasons = []coreactions.Reason{coreactions.ReasonUnreadable}
		return f, nil
	}
	f.Head = fakeHead
	if r.dirty {
		f.Reasons, f.UntrackedCount = []coreactions.Reason{coreactions.ReasonDirty}, 1
	}
	return f, nil
}

func (s *FakeScope) StillTracked(ctx context.Context, id core.RepoID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.get(ctx, id)
	return err
}

func (s *FakeScope) NoiseFacts(ctx context.Context, id core.RepoID) (corerepos.NoiseFacts, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.get(ctx, id)
	return corerepos.NoiseFacts{}, err
}

func (s *FakeScope) Target(ctx context.Context, id core.RepoID) (actions.Target, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.get(ctx, id)
	return actions.Target{Project: r.projectOrEmpty()}, err
}

func (r *fakeRepo) projectOrEmpty() core.ProjectID {
	if r == nil {
		return ""
	}
	return r.project
}

func (s *FakeScope) ReposOf(ctx context.Context, project core.ProjectID) ([]core.RepoID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	known := false
	var out []core.RepoID
	for id, r := range s.repos {
		if r.project != project {
			continue
		}
		known = true
		if r.tracked && r.ready {
			out = append(out, id)
		}
	}
	if !known {
		return nil, fmt.Errorf("project %s: %w", project, db.ErrNotFound)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func (s *FakeScope) PullTarget(ctx context.Context, id core.RepoID) (actions.PullTarget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.get(ctx, id)
	if err != nil {
		return actions.PullTarget{}, err
	}
	if !r.upstream || r.broken {
		return actions.PullTarget{}, errors.New("the repo has no readable branch with an upstream")
	}
	return actions.PullTarget{Branch: "main", Upstream: "origin/main", Head: fakeHead}, nil
}

// Rig returns the contract rig over this fake.
func (s *FakeScope) Rig() ScopeRig {
	add := func(project core.ProjectID, up, ready bool) core.RepoID {
		s.mu.Lock()
		defer s.mu.Unlock()
		id := core.RepoID(s.next("rep"))
		s.repos[id] = &fakeRepo{project: project, upstream: up, tracked: true, ready: ready}
		return id
	}
	mut := func(id core.RepoID, f func(*fakeRepo)) {
		s.mu.Lock()
		defer s.mu.Unlock()
		f(s.repos[id])
	}
	return ScopeRig{
		Reader: s,
		Repo: func(_ *testing.T, up bool) Tracked {
			s.mu.Lock()
			p := core.ProjectID(s.next("prj"))
			s.mu.Unlock()
			return Tracked{ID: add(p, up, true), Project: p}
		},
		Sibling:  func(_ *testing.T, p core.ProjectID) core.RepoID { return add(p, true, true) },
		Reserved: func(_ *testing.T, p core.ProjectID) core.RepoID { return add(p, false, false) },
		Untrack:  func(_ *testing.T, id core.RepoID) { mut(id, func(r *fakeRepo) { r.tracked = false }) },
		Dirty:    func(_ *testing.T, id core.RepoID) { mut(id, func(r *fakeRepo) { r.dirty = true }) },
		Break:    func(_ *testing.T, id core.RepoID) { mut(id, func(r *fakeRepo) { r.broken = true }) },
	}
}

// FakeContext is an in-memory actions.ContextReader that obeys
// ContextReaderContract. Its repo is "rep_main"; "rep_other" is a second one.
type FakeContext struct {
	mu      sync.Mutex
	tracked bool
	edits   map[string]string
	builds  map[core.BuildID]fakeBuild
	redact  safety.Redactor
	seq     int
}

type fakeBuild struct {
	repo core.RepoID
	log  string
}

var _ actions.ContextReader = (*FakeContext)(nil)

const (
	fakeMain  = core.RepoID("rep_main")
	fakeOther = core.RepoID("rep_other")
)

// NewFakeContext returns a fake with rep_main tracked and clean.
func NewFakeContext() *FakeContext {
	return &FakeContext{tracked: true, edits: map[string]string{}, builds: map[core.BuildID]fakeBuild{}, redact: safety.NewRedactor()}
}

func (c *FakeContext) Host(ctx context.Context, _ corehosts.ContextKey) (hosts.ContextOutcome, error) {
	if err := ctx.Err(); err != nil {
		return hosts.ContextOutcome{}, err
	}
	return hosts.ContextOutcome{Blocked: &corehosts.CtxBlocked{Reason: corehosts.ContextNoToken}}, nil
}

func (c *FakeContext) BuildLog(ctx context.Context, repo core.RepoID, id core.BuildID) (string, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	b, ok := c.builds[id]
	if repo != fakeMain || !c.tracked || !ok || b.repo != repo {
		return "", false, fmt.Errorf("build %s: %w", id, db.ErrNotFound)
	}
	log, trunc := b.log, false
	if len(log) > coreactions.MaxContextBytes {
		log, trunc = log[len(log)-coreactions.MaxContextBytes:], true
	}
	return c.redact.Redact(log), trunc, nil
}

func (c *FakeContext) Diff(ctx context.Context, repo core.RepoID) (actions.DiffContext, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return actions.DiffContext{}, err
	}
	if repo != fakeMain || !c.tracked {
		return actions.DiffContext{}, fmt.Errorf("repo %s: %w", repo, db.ErrNotFound)
	}
	paths := make([]string, 0, len(c.edits))
	for p := range c.edits {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	var text strings.Builder
	h := sha256.New()
	for _, p := range paths {
		fmt.Fprintf(h, "%s\x00%s\x00", p, c.edits[p])
		fmt.Fprintf(&text, "+++ %s\n+%s\n", p, c.edits[p])
	}
	out, trunc := text.String(), false
	if len(out) > coreactions.MaxContextBytes {
		out, trunc = out[:coreactions.MaxContextBytes], true
	}
	return actions.DiffContext{Head: fakeHead, DiffHash: hex.EncodeToString(h.Sum(nil)), Text: c.redact.Redact(out), Truncated: trunc, Paths: paths}, nil
}

// Rig returns the contract rig over this fake.
func (c *FakeContext) Rig() ContextRig {
	return ContextRig{
		Reader: c, Repo: fakeMain, OtherRepo: fakeOther,
		Head: func(*testing.T) string { return fakeHead },
		Edit: func(_ *testing.T, name string) []string {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.edits["README.md"] = "changed " + name + " " + SecretToken
			c.edits["notes.txt"] = "notes " + name + " " + SecretToken
			return []string{"README.md", "notes.txt"}
		},
		Build: func(_ *testing.T, repo core.RepoID, n int) core.BuildID {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.seq++
			id := core.BuildID(fmt.Sprintf("bld_%016d", c.seq))
			body := "token=" + SecretToken + "\n" + strings.Repeat("x", max(0, n-30)) + "\nLAST-LINE\n"
			c.builds[id] = fakeBuild{repo: repo, log: body}
			return id
		},
		Untrack: func(*testing.T) {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.tracked = false
		},
		NoTokenKey: corehosts.ContextKey{},
	}
}
