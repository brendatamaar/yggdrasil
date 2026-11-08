package changesets

import (
	"slices"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// kindRank is the order inside one repo: an install or file change lands
// before the commit that includes it, a push before its merge request. Landing
// kinds (merge_local, remove_worktree) and publish/subtasks are not here: they
// are never part of a run and are confirmed one at a time.
var kindRank = map[core.ProposalKind]int{
	core.ProposalInstall:      0,
	core.ProposalAgentFile:    1,
	core.ProposalDeleteFiles:  2,
	core.ProposalCommit:       3,
	core.ProposalPush:         4,
	core.ProposalMergeRequest: 5,
}

func inScope(scope Scope, repo core.RepoID) bool {
	switch sc := scope.(type) {
	case ScopeAll:
		return true
	case ScopeRepo:
		return sc.Repo == repo
	}
	return false
}

func compareStr(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// planMembers returns the pending members a run of scope would confirm, in
// run order.
func planMembers(o Open, scope Scope) []Member {
	if scope == nil {
		return nil
	}
	pos := make(map[core.RepoID]int, len(o.Set.Order))
	for i, r := range o.Set.Order {
		pos[r] = i
	}
	at := func(r core.RepoID) int {
		if i, ok := pos[r]; ok {
			return i
		}
		return len(o.Set.Order)
	}
	var out []Member
	for _, m := range o.Members {
		if _, ranked := kindRank[m.Kind]; ranked && m.Status == core.ProposalPending && inScope(scope, m.Repo) {
			out = append(out, m)
		}
	}
	slices.SortStableFunc(out, func(a, b Member) int {
		if d := at(a.Repo) - at(b.Repo); d != 0 {
			return d
		}
		if a.Repo != b.Repo {
			return compareStr(string(a.Repo), string(b.Repo))
		}
		if d := kindRank[a.Kind] - kindRank[b.Kind]; d != 0 {
			return d
		}
		return compareStr(string(a.ID), string(b.ID))
	})
	return out
}

// Plan lists what a run of scope confirms: only pending members of repos in
// scope, never a landing kind, ordered by the repo's position in the merge
// order (repos missing from it last, by ID) and then by kind rank. Pure.
func Plan(s State, scope Scope) []Planned {
	if s == nil {
		return nil
	}
	ms := planMembers(open(s), scope)
	out := make([]Planned, len(ms))
	for i, m := range ms {
		out[i] = Planned{ID: m.ID, Hash: m.Hash}
	}
	return out
}

// Preflight returns the advisory warnings of running scope with the current
// order and dependency edges: dependent_before_dependency when, in the plan, a
// member of repo R runs before a member of a repo D that R depends on, or when
// D has pending members of the same kind that are not in the plan (so R's
// would run first). It never blocks. Pure.
func Preflight(s State, scope Scope, deps []Edge) []Warning {
	if s == nil {
		return nil
	}
	o := open(s)
	plan := planMembers(o, scope)
	inPlan := make(map[core.ProposalID]bool, len(plan))
	for _, m := range plan {
		inPlan[m.ID] = true
	}
	var out []Warning
	seen := map[Edge]bool{}
	for _, e := range deps {
		if e.Dependent == e.Dependency || seen[e] ||
			!slices.Contains(o.Set.Repos, e.Dependent) || !slices.Contains(o.Set.Repos, e.Dependency) {
			continue
		}
		seen[e] = true
		firstR, lastD := -1, -1
		for i, m := range plan {
			if m.Repo == e.Dependent && firstR < 0 {
				firstR = i
			}
			if m.Repo == e.Dependency {
				lastD = i
			}
		}
		hit := firstR >= 0 && lastD > firstR
		for _, m := range plan {
			if hit || m.Repo != e.Dependent {
				continue
			}
			hit = slices.ContainsFunc(o.Members, func(x Member) bool {
				_, ranked := kindRank[x.Kind]
				return ranked && x.Repo == e.Dependency && x.Kind == m.Kind && x.Status == core.ProposalPending && !inPlan[x.ID]
			})
		}
		if hit {
			out = append(out, Warning{Code: WarningDependentBeforeDependency, Dependent: e.Dependent, Dependency: e.Dependency})
		}
	}
	slices.SortFunc(out, func(a, b Warning) int {
		if d := compareStr(string(a.Dependent), string(b.Dependent)); d != 0 {
			return d
		}
		return compareStr(string(a.Dependency), string(b.Dependency))
	})
	return out
}
