package standup

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corestandup "github.com/brendatamaar/yggdrasil/internal/core/standup"
	"github.com/brendatamaar/yggdrasil/internal/repos"
)

// readConcurrency bounds the source reads that run at once.
const readConcurrency = 8

// ErrSuperseded means a newer refresh of the same project replaced this one.
var ErrSuperseded = errors.New("standup request superseded")

// errInvalidData marks a source result that failed the boundary checks.
var errInvalidData = errors.New("standup source returned invalid data")

// Service is the standup shell. A refresh reads the sources, runs
// core/standup.Step and returns the result; it never calls the Polisher and
// never touches the network (MRSource serves cached host data). Polish is the
// only path to the Polisher and needs an explicit user request.
type Service struct {
	d Deps

	mu       sync.Mutex
	projects map[core.ProjectID]*projectRead
	inflight map[core.RequestID]*polishCall
}

// projectRead is the shared standup state of one project: the latest refresh
// wins and cancels the one before it.
type projectRead struct {
	mu     sync.Mutex
	s      corestandup.Standup
	cancel context.CancelFunc
}

type polishCall struct {
	key  string
	done chan struct{}
	res  api.ActionResultDTO
	err  error
}

var _ api.StandupService = (*Service)(nil)

// New builds the service. Hub may be nil (nothing is published); every other
// dependency is required.
func New(d Deps) (*Service, error) {
	switch {
	case d.Scope == nil:
		return nil, errors.New("standup: Deps.Scope is required")
	case d.Commits == nil:
		return nil, errors.New("standup: Deps.Commits is required")
	case d.MRs == nil:
		return nil, errors.New("standup: Deps.MRs is required")
	case d.Builds == nil:
		return nil, errors.New("standup: Deps.Builds is required")
	case d.Polish == nil:
		return nil, errors.New("standup: Deps.Polish is required")
	case d.Clock == nil:
		d.Clock = time.Now
	}
	return &Service{d: d, projects: map[core.ProjectID]*projectRead{}, inflight: map[core.RequestID]*polishCall{}}, nil
}

// Result is what a refresh returns: the range and the state after the read,
// which is Ready, Partial or Failed.
type Result struct {
	Range corestandup.Range
	State corestandup.State
}

func invalidRange(err error) error {
	return apperr.New(apperr.Invalid, "Choose a start before the end, at most 31 days apart.", err)
}

// Refresh reads the sources of the tracked, ready repos of project in
// [from, to) and returns the deterministic summary. It starts no agent and
// makes no network or model call. A newer Refresh of the same project
// supersedes this one (ErrSuperseded).
func (s *Service) Refresh(ctx context.Context, project core.ProjectID, from, to time.Time) (Result, error) {
	rng, err := corestandup.ParseRange(project, from, to)
	if err != nil {
		return Result{}, invalidRange(err)
	}
	scope, scopeCut, err := s.scope(ctx, project)
	if err != nil {
		return Result{}, err
	}

	ps := s.project(project)
	ps.mu.Lock()
	next, cmds := corestandup.Step(ps.s, corestandup.Requested{Range: rng, Now: s.d.Clock()})
	ps.s = next
	var attempt int
	readCtx, cancel := context.WithTimeout(ctx, corestandup.ReadTimeout)
	defer cancel()
	for _, c := range cmds {
		switch c := c.(type) {
		case corestandup.CancelRead:
			if ps.cancel != nil {
				ps.cancel()
			}
		case corestandup.ReadSources:
			attempt = c.Attempt
		}
	}
	if attempt == 0 {
		ps.mu.Unlock()
		return Result{}, invalidRange(corestandup.ErrInvalidRange)
	}
	ps.cancel = cancel
	ps.mu.Unlock()

	in := s.collect(readCtx, ctx, rng, scope, scopeCut, attempt, project)

	ps.mu.Lock()
	defer ps.mu.Unlock()
	next, cmds = corestandup.Step(ps.s, in)
	ps.s = next
	if _, cancelled := in.(corestandup.Cancelled); cancelled {
		return Result{}, fmt.Errorf("standup read cancelled: %w", ctx.Err())
	}
	if len(cmds) == 0 {
		return Result{}, apperr.New(apperr.Conflict, "A newer standup request replaced this one.", ErrSuperseded)
	}
	return Result{Range: rng, State: next.State}, nil
}

func (s *Service) project(id core.ProjectID) *projectRead {
	s.mu.Lock()
	defer s.mu.Unlock()
	ps, ok := s.projects[id]
	if !ok {
		ps = &projectRead{s: corestandup.Standup{State: corestandup.Empty{}}}
		s.projects[id] = ps
	}
	return ps
}

// scope returns the repos in scope, sorted by name then ID, at most
// MaxRepos. cut reports that more were in scope than the cap. Only what
// ScopeReader returns is ever read: a reserved or untracked repo is not in it.
func (s *Service) scope(ctx context.Context, project core.ProjectID) (refs []RepoRef, cut bool, err error) {
	rows, err := s.d.Scope.Repos(ctx, project)
	if err != nil {
		return nil, false, fmt.Errorf("list standup repos of %s: %w", project, err)
	}
	seen := map[core.RepoID]bool{}
	for _, r := range rows {
		if r.ID.Validate() != nil || strings.TrimSpace(r.Name) == "" || seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		refs = append(refs, r)
	}
	slices.SortFunc(refs, func(a, b RepoRef) int {
		return strings.Compare(a.Name+"\x00"+string(a.ID), b.Name+"\x00"+string(b.ID))
	})
	if len(refs) > corestandup.MaxRepos {
		refs, cut = refs[:corestandup.MaxRepos], true
	}
	return refs, cut, nil
}

type job struct {
	kind corestandup.SourceKind
	repo RepoRef
}

type jobResult struct {
	commits   []corestandup.Commit
	mrs       []corestandup.MR
	builds    []corestandup.Build
	truncated bool
	err       error
}

// collect reads every source of every repo, at most readConcurrency at once,
// under readCtx (a timeout and the supersede signal). parent is the caller's
// context: when it is done the result is Cancelled. A source error becomes a
// labelled Unavailable entry; nothing is invented for it.
func (s *Service) collect(readCtx, parent context.Context, rng corestandup.Range, scope []RepoRef, scopeCut bool, attempt int, project core.ProjectID) corestandup.Input {
	jobs := make([]job, 0, len(scope)*3)
	for _, r := range scope {
		for _, k := range []corestandup.SourceKind{corestandup.SourceCommits, corestandup.SourceMRs, corestandup.SourceBuilds} {
			jobs = append(jobs, job{kind: k, repo: r})
		}
	}
	results := make([]jobResult, len(jobs))
	var g errgroup.Group
	g.SetLimit(readConcurrency)
	for i := range jobs {
		if err := readCtx.Err(); err != nil {
			results[i].err = err
			continue
		}
		g.Go(func() error {
			results[i] = s.readOne(readCtx, rng, jobs[i])
			return nil
		})
	}
	_ = g.Wait() // the goroutines never return an error; failures live in results

	if parent.Err() != nil {
		return corestandup.Cancelled{Attempt: attempt}
	}

	// Revalidate the scope: a repo that stopped being tracked and ready during
	// the read must not appear.
	current, _, err := s.scope(parent, project)
	if err != nil {
		if parent.Err() != nil {
			return corestandup.Cancelled{Attempt: attempt}
		}
		return corestandup.ReadFailed{Attempt: attempt, Reason: "The tracked repos could not be checked, so the standup was not built."}
	}
	still := map[core.RepoID]bool{}
	for _, r := range current {
		still[r.ID] = true
	}

	out := corestandup.SourcesRead{Attempt: attempt}
	if scopeCut {
		out.Truncated = []corestandup.SourceKind{corestandup.SourceCommits, corestandup.SourceMRs, corestandup.SourceBuilds}
	}
	type tally struct{ ok, failed int }
	tallies := map[corestandup.SourceKind]*tally{corestandup.SourceCommits: {}, corestandup.SourceMRs: {}, corestandup.SourceBuilds: {}}
	var labels []corestandup.Unavailable
	truncated := map[corestandup.SourceKind]bool{}
	for i, j := range jobs {
		if !still[j.repo.ID] {
			continue
		}
		r := results[i]
		if r.err != nil {
			tallies[j.kind].failed++
			labels = append(labels, corestandup.Unavailable{Source: j.kind, Repo: j.repo.ID, RepoName: j.repo.Name, Reason: reasonOf(r.err)})
			continue
		}
		tallies[j.kind].ok++
		out.Commits = append(out.Commits, r.commits...)
		out.MRs = append(out.MRs, r.mrs...)
		out.Builds = append(out.Builds, r.builds...)
		if r.truncated {
			truncated[j.kind] = true
		}
	}
	for _, k := range []corestandup.SourceKind{corestandup.SourceCommits, corestandup.SourceMRs, corestandup.SourceBuilds} {
		t := tallies[k]
		if t.ok == 0 && t.failed > 0 {
			// Every repo failed: the whole source is unavailable.
			var reason string
			for _, l := range labels {
				if l.Source == k {
					reason = l.Reason
					break
				}
			}
			labels = slices.DeleteFunc(labels, func(l corestandup.Unavailable) bool { return l.Source == k })
			labels = append(labels, corestandup.Unavailable{Source: k, Reason: reason})
			continue
		}
		out.Readable++
		if truncated[k] {
			out.Truncated = append(out.Truncated, k)
		}
	}
	out.Unavailable = labels
	return out
}

// readOne reads one source of one repo and parses the result at the boundary.
func (s *Service) readOne(ctx context.Context, rng corestandup.Range, j job) jobResult {
	if err := ctx.Err(); err != nil {
		return jobResult{err: err}
	}
	const max = corestandup.MaxRecordsPerSource
	inRange := func(t time.Time) bool { return !t.IsZero() && !t.Before(rng.From) && t.Before(rng.To) }
	invalid := func(what string) jobResult {
		return jobResult{err: fmt.Errorf("%w: %s", errInvalidData, what)}
	}
	switch j.kind {
	case corestandup.SourceCommits:
		cs, trunc, err := s.d.Commits.Commits(ctx, j.repo.ID, rng.From, rng.To, max)
		if err != nil {
			return jobResult{err: err}
		}
		if len(cs) > max {
			return invalid("too many commits")
		}
		out := make([]corestandup.Commit, 0, len(cs))
		for _, c := range cs {
			if c.OID == "" || !inRange(c.At) {
				return invalid("commit")
			}
			out = append(out, corestandup.Commit{Repo: j.repo.ID, RepoName: j.repo.Name, OID: c.OID, Subject: c.Subject, Author: c.Author, At: c.At.UTC()})
		}
		return jobResult{commits: out, truncated: trunc}
	case corestandup.SourceMRs:
		ms, trunc, err := s.d.MRs.MRs(ctx, j.repo.ID, rng.From, rng.To, max)
		if err != nil {
			return jobResult{err: err}
		}
		if len(ms) > max {
			return invalid("too many merge requests")
		}
		out := make([]corestandup.MR, 0, len(ms))
		for _, m := range ms {
			if m.Repo != j.repo.ID || m.Number < 1 || strings.TrimSpace(m.State) == "" || !inRange(m.At) {
				return invalid("merge request")
			}
			m.RepoName, m.At = j.repo.Name, m.At.UTC()
			out = append(out, m)
		}
		return jobResult{mrs: out, truncated: trunc}
	default:
		bs, trunc, err := s.d.Builds.Builds(ctx, j.repo.ID, rng.From, rng.To, max)
		if err != nil {
			return jobResult{err: err}
		}
		if len(bs) > max {
			return invalid("too many builds")
		}
		out := make([]corestandup.Build, 0, len(bs))
		for _, b := range bs {
			if b.Repo != j.repo.ID || b.ID.Validate() != nil || b.Status.Validate() != nil || !inRange(b.At) {
				return invalid("build")
			}
			b.RepoName, b.At = j.repo.Name, b.At.UTC()
			out = append(out, b)
		}
		return jobResult{builds: out, truncated: trunc}
	}
}

// reasonOf is the plain-language label of a source error. It never repeats the
// error text: that can hold paths or host details.
func reasonOf(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "the read timed out"
	case errors.Is(err, repos.ErrHistoryUnavailable):
		return "the repo history could not be read"
	case errors.Is(err, errInvalidData):
		return "the source returned data that could not be used"
	}
	return "the source could not be read"
}
