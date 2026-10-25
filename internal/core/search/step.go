package search

import (
	"cmp"
	"maps"
	"slices"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Step is the search state machine: (state2, commands) = Step(state1, input).
// An input that is illegal for the state returns the state unchanged and no
// commands (docs/design/code-search.md, transition table).
func Step(s Search, in Input) (Search, []Command) {
	switch x := in.(type) {
	case Begin:
		if s.Begun {
			return s, nil
		}
		return begin(x)
	case RepoResult:
		if !s.Searching() || !s.Pending[x.Repo] {
			return s, nil
		}
		return merge(s, x), nil
	case RepoFailed:
		if !s.Searching() || !s.Pending[x.Repo] {
			return s, nil
		}
		kind := x.Kind
		if kind.Validate() != nil {
			kind = ProblemGitFailed // an unknown reason still ends the repo's wait
		}
		n := clone(s)
		delete(n.Pending, x.Repo)
		n.Problems[x.Repo] = kind
		return n, nil
	case Deadline:
		if !s.Searching() {
			return s, nil
		}
		n := clone(s)
		for id := range n.Pending {
			n.Problems[id] = ProblemTimedOut
		}
		n.Pending = map[core.RepoID]bool{}
		return n, []Command{CancelAll{}}
	}
	return s, nil
}

// Run feeds inputs to Step in order and returns the final state and every
// command emitted. The property tests drive Step through it.
func Run(s Search, ins ...Input) (Search, []Command) {
	var all []Command
	for _, in := range ins {
		var cs []Command
		s, cs = Step(s, in)
		all = append(all, cs...)
	}
	return s, all
}

func begin(b Begin) (Search, []Command) {
	s := Search{
		Begun:    true,
		Query:    b.Query,
		Pending:  map[core.RepoID]bool{},
		PerRepo:  map[core.RepoID]int{},
		Problems: map[core.RepoID]ProblemKind{},
	}
	var cmds []Command
	for _, r := range b.Repos {
		if s.Pending[r.ID] {
			continue
		}
		s.Pending[r.ID] = true
		s.Repos = append(s.Repos, r)
		cmds = append(cmds, Grep{Repo: r, Query: b.Query})
	}
	return s, cmds
}

func clone(s Search) Search {
	n := s
	n.Pending = maps.Clone(s.Pending)
	n.PerRepo = maps.Clone(s.PerRepo)
	n.Problems = maps.Clone(s.Problems)
	n.Results = slices.Clone(s.Results)
	return n
}

// TrimText cuts a line to MaxTextRunes runes.
func TrimText(t string) string {
	if utf8.RuneCountInString(t) <= MaxTextRunes {
		return t
	}
	r := []rune(t)
	return string(r[:MaxTextRunes])
}

func merge(s Search, r RepoResult) Search {
	n := clone(s)
	delete(n.Pending, r.Repo)
	if r.Truncated {
		n.Truncated = true
	}
	names := make(map[core.RepoID]string, len(n.Repos))
	for _, rr := range n.Repos {
		names[rr.ID] = rr.Name
	}
	// The repo's own matches: stamped with the repo, trimmed, ordered,
	// deduplicated, then cut to the per-repo cap.
	own := make([]Match, 0, len(r.Matches))
	for _, m := range r.Matches {
		m.Repo = r.Repo
		m.Text = TrimText(m.Text)
		own = append(own, m)
	}
	sortMatches(own, names)
	own = dedup(own)
	if len(own) > MaxPerRepo {
		own = own[:MaxPerRepo]
		n.Truncated = true
	}
	all := append(n.Results, own...)
	sortMatches(all, names)
	all = dedup(all)
	if len(all) > MaxTotal {
		all = all[:MaxTotal]
		n.Truncated = true
	}
	n.Results = all
	n.PerRepo = map[core.RepoID]int{}
	for _, m := range all {
		n.PerRepo[m.Repo]++
	}
	return n
}

func compare(a, b Match, names map[core.RepoID]string) int {
	return cmp.Or(
		cmp.Compare(names[a.Repo], names[b.Repo]),
		cmp.Compare(a.Repo, b.Repo),
		cmp.Compare(a.Path, b.Path),
		cmp.Compare(a.Line, b.Line),
		cmp.Compare(a.Col, b.Col),
		cmp.Compare(a.Text, b.Text),
	)
}

func sortMatches(ms []Match, names map[core.RepoID]string) {
	slices.SortStableFunc(ms, func(a, b Match) int { return compare(a, b, names) })
}

// dedup drops matches with the same (repo, path, line, col); the input is
// sorted, so the kept one (the smallest text) does not depend on arrival order.
func dedup(ms []Match) []Match {
	return slices.CompactFunc(ms, func(a, b Match) bool {
		return a.Repo == b.Repo && a.Path == b.Path && a.Line == b.Line && a.Col == b.Col
	})
}
