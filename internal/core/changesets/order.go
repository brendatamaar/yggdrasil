package changesets

import (
	"slices"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// SuggestOrder returns a merge order of repos (duplicates dropped) with every
// dependency before its dependents; ties follow the position in fallback, then
// the repo ID, so the result is deterministic. Edges that name a repo outside
// repos, and self edges, are ignored. When the edges form a cycle it returns
// fallback's order of repos (repos missing from fallback last, by ID) and the
// repos that lie on a cycle. Pure.
func SuggestOrder(repos []core.RepoID, deps []Edge, fallback []core.RepoID) (order []core.RepoID, cycle []core.RepoID) {
	var nodes []core.RepoID
	in := map[core.RepoID]bool{}
	for _, r := range repos {
		if !in[r] {
			in[r] = true
			nodes = append(nodes, r)
		}
	}
	pos := map[core.RepoID]int{}
	for i, r := range fallback {
		if _, ok := pos[r]; !ok {
			pos[r] = i
		}
	}
	less := func(a, b core.RepoID) int {
		pa, oka := pos[a]
		pb, okb := pos[b]
		switch {
		case oka && okb && pa != pb:
			return pa - pb
		case oka != okb:
			if oka {
				return -1
			}
			return 1
		}
		return compareStr(string(a), string(b))
	}
	slices.SortFunc(nodes, less)

	// dependents[d] are the repos that wait for d.
	dependents := map[core.RepoID][]core.RepoID{}
	waiting := map[core.RepoID]int{}
	seen := map[Edge]bool{}
	for _, e := range deps {
		if e.Dependent == e.Dependency || !in[e.Dependent] || !in[e.Dependency] || seen[e] {
			continue
		}
		seen[e] = true
		dependents[e.Dependency] = append(dependents[e.Dependency], e.Dependent)
		waiting[e.Dependent]++
	}
	var ready []core.RepoID
	for _, n := range nodes {
		if waiting[n] == 0 {
			ready = append(ready, n)
		}
	}
	done := map[core.RepoID]bool{}
	for len(ready) > 0 {
		slices.SortFunc(ready, less)
		n := ready[0]
		ready = ready[1:]
		order = append(order, n)
		done[n] = true
		for _, d := range dependents[n] {
			if waiting[d]--; waiting[d] == 0 {
				ready = append(ready, d)
			}
		}
	}
	if len(order) == len(nodes) {
		return order, nil
	}
	// A leftover node is on a cycle when it can reach itself through
	// leftover nodes.
	for _, n := range nodes {
		if done[n] {
			continue
		}
		reach := map[core.RepoID]bool{}
		stack := slices.Clone(dependents[n])
		for len(stack) > 0 {
			x := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if reach[x] || done[x] {
				continue
			}
			reach[x] = true
			stack = append(stack, dependents[x]...)
		}
		if reach[n] {
			cycle = append(cycle, n)
		}
	}
	return slices.Clone(nodes), cycle // nodes are sorted by fallback position, then ID
}
