package repos

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/git"
)

// ErrHistoryUnavailable means the source cannot safely supply history. Callers
// retain an unavailable label, never substitute a successful empty history.
var ErrHistoryUnavailable = errors.New("repo history is unavailable")

// HistoryGit is the read-only, resource-bounded git capability for standup.
type HistoryGit interface {
	LogRange(context.Context, string, time.Time, time.Time, int) ([]git.Commit, bool, error)
}

// HistoryRange is a validated, UTC half-open interval with a bounded count.
type HistoryRange struct {
	from, to time.Time
	max      int
}

func (r HistoryRange) From() time.Time { return r.from }
func (r HistoryRange) To() time.Time   { return r.to }
func (r HistoryRange) Max() int        { return r.max }

// ParseHistoryRange validates before scope lookup or git execution.
func ParseHistoryRange(from, to time.Time, max int) (HistoryRange, error) {
	from, to = from.UTC(), to.UTC()
	if from.IsZero() || to.IsZero() || !from.Before(to) || to.Sub(from) > 31*24*time.Hour || max < 1 || max > 200 {
		return HistoryRange{}, apperr.New(apperr.Invalid, "Choose an increasing date range of at most 31 days and between 1 and 200 commits.", nil)
	}
	return HistoryRange{from: from, to: to, max: max}, nil
}

// GitHistoryReader reads only tracked-ready repos in tracked projects. It has
// no cache, worker or mutation capability. LogRange owns bounded process capture;
// this reader also bounds scope lookups and rechecks scope before returning data.
type GitHistoryReader struct {
	src ActiveSource
	git HistoryGit
}

func NewHistoryReader(src ActiveSource, g HistoryGit) *GitHistoryReader {
	return &GitHistoryReader{src: src, git: g}
}

var _ HistoryReader = (*GitHistoryReader)(nil)

func (s *GitHistoryReader) tracked(ctx context.Context, id core.RepoID) (core.Repo, error) {
	projects, err := s.src.ListProjects(ctx)
	if err != nil {
		return core.Repo{}, err
	}
	for _, p := range projects {
		if err := p.ID.Validate(); err != nil {
			return core.Repo{}, err
		}
		rows, err := s.src.ListRepos(ctx, p.ID)
		if err != nil {
			return core.Repo{}, err
		}
		for _, r := range rows {
			if r.ID != id {
				continue
			}
			if r.ProjectID != p.ID || (r.Readiness != "" && r.Readiness != core.RepoReady) {
				return core.Repo{}, ErrHistoryUnavailable
			}
			if err := r.Validate(); err != nil {
				return core.Repo{}, err
			}
			if _, err := corerepos.NewCleanPath(r.Path); err != nil {
				return core.Repo{}, err
			}
			return r, nil
		}
	}
	return core.Repo{}, ErrHistoryUnavailable
}

// Commits preserves LogRange's partial flag even when there are no observed
// matches. A cutoff cannot promise the globally newest commits or completeness.
func (s *GitHistoryReader) Commits(ctx context.Context, id core.RepoID, from, to time.Time, max int) ([]HistoryCommit, bool, error) {
	if err := id.Validate(); err != nil {
		return nil, false, apperr.New(apperr.Invalid, "Choose a valid tracked repo.", err)
	}
	r, err := ParseHistoryRange(from, to, max)
	if err != nil {
		return nil, false, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	unavailable := func(err error) ([]HistoryCommit, bool, error) {
		return nil, false, fmt.Errorf("%w: %w", ErrHistoryUnavailable, err)
	}
	if err := ctx.Err(); err != nil {
		return unavailable(err)
	}
	repo, err := s.tracked(ctx, id)
	if err != nil {
		return unavailable(err)
	}
	cs, partial, err := s.git.LogRange(ctx, repo.Path, r.from, r.to, r.max)
	if err != nil {
		return unavailable(err)
	}
	if err := ctx.Err(); err != nil {
		return unavailable(err)
	}
	out, err := parseHistoryCommits(cs, r)
	if err != nil {
		return unavailable(err)
	}
	current, err := s.tracked(ctx, id)
	if err != nil {
		return unavailable(err)
	}
	if current.ProjectID != repo.ProjectID || current.Path != repo.Path {
		return unavailable(ErrHistoryUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return unavailable(err)
	}
	return out, partial, nil
}

func parseHistoryCommits(cs []git.Commit, r HistoryRange) ([]HistoryCommit, error) {
	if len(cs) > r.max {
		return nil, errors.New("git history exceeded the requested count")
	}
	out := make([]HistoryCommit, 0, len(cs))
	bytes := 0
	seen := map[string]bool{}
	var previous git.Commit
	for _, c := range cs {
		bytes += len(c.Hash) + len(c.Subject) + len(c.Author)
		if bytes > 1<<20 || seen[c.Hash] || !historyOID(c.Hash) || !historyText(c.Subject) || !historyText(c.Author) || c.Date.IsZero() || c.Date.Before(r.from) || !c.Date.Before(r.to) {
			return nil, errors.New("git history returned an invalid commit")
		}
		if len(out) > 0 {
			prev := previous
			if prev.Date.Before(c.Date) || (prev.Date.Equal(c.Date) && prev.Hash >= c.Hash) {
				return nil, errors.New("git history returned unordered or duplicate commits")
			}
		}
		seen[c.Hash] = true
		previous = c
		out = append(out, HistoryCommit{OID: c.Hash, Subject: c.Subject, Author: c.Author, At: c.Date.UTC()})
	}
	return out, nil
}

func historyText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func historyOID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
