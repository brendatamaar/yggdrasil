package builds

import (
	"sort"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// OrderModules orders repos so that a module comes after the modules it
// depends on (BL-04). Members of a dependency cycle are kept together in
// input order and reported once each in cycles; the order is deterministic.
//
// A dependency matches the module with the same Group and Artifact (the
// version is ignored: the reactor builds what is checked out). Dependencies
// on unknown coordinates and on the module itself are ignored.
func OrderModules(mods []Module) (order []core.RepoID, cycles [][]core.RepoID) {
	n := len(mods)
	if n == 0 {
		return nil, nil
	}
	type key struct{ g, a string }
	byCoord := map[key][]int{}
	for i, m := range mods {
		k := key{m.Coord.Group, m.Coord.Artifact}
		byCoord[k] = append(byCoord[k], i)
	}
	deps := make([][]int, n) // deps[i]: modules i depends on
	for i, m := range mods {
		seen := map[int]bool{}
		for _, d := range m.Deps {
			for _, j := range byCoord[key{d.Group, d.Artifact}] {
				if j != i && !seen[j] {
					seen[j] = true
					deps[i] = append(deps[i], j)
				}
			}
		}
		sort.Ints(deps[i])
	}

	// Tarjan's strongly connected components.
	index := make([]int, n)
	low := make([]int, n)
	on := make([]bool, n)
	for i := range index {
		index[i] = -1
	}
	comp := make([]int, n)
	var stack []int
	var comps [][]int
	next := 0
	var visit func(v int)
	visit = func(v int) {
		index[v], low[v] = next, next
		next++
		stack = append(stack, v)
		on[v] = true
		for _, w := range deps[v] {
			if index[w] < 0 {
				visit(w)
				low[v] = min(low[v], low[w])
			} else if on[w] {
				low[v] = min(low[v], index[w])
			}
		}
		if low[v] == index[v] {
			var members []int
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				on[w] = false
				members = append(members, w)
				if w == v {
					break
				}
			}
			sort.Ints(members)
			for _, w := range members {
				comp[w] = len(comps)
			}
			comps = append(comps, members)
		}
	}
	for v := 0; v < n; v++ {
		if index[v] < 0 {
			visit(v)
		}
	}

	// Kahn over the components, always taking the ready one whose first
	// member comes first in the input.
	nc := len(comps)
	waiting := make([]int, nc) // unfinished dependency components
	dependents := make([][]int, nc)
	type edge struct{ from, to int }
	seenEdge := map[edge]bool{}
	for v := 0; v < n; v++ {
		for _, w := range deps[v] {
			cv, cw := comp[v], comp[w]
			if cv != cw && !seenEdge[edge{cv, cw}] {
				seenEdge[edge{cv, cw}] = true
				waiting[cv]++
				dependents[cw] = append(dependents[cw], cv)
			}
		}
	}
	done := make([]bool, nc)
	var cycleFirst []int
	for range comps {
		pick := -1
		for c := 0; c < nc; c++ {
			if !done[c] && waiting[c] == 0 && (pick < 0 || comps[c][0] < comps[pick][0]) {
				pick = c
			}
		}
		done[pick] = true
		for _, d := range dependents[pick] {
			waiting[d]--
		}
		var group []core.RepoID
		for _, v := range comps[pick] {
			group = append(group, mods[v].RepoID)
		}
		order = append(order, group...)
		if len(group) > 1 {
			cycles = append(cycles, group)
			cycleFirst = append(cycleFirst, comps[pick][0])
		}
	}
	// Report cycles in input order of their first member.
	idx := make([]int, len(cycles))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return cycleFirst[idx[a]] < cycleFirst[idx[b]] })
	sorted := make([][]core.RepoID, len(cycles))
	for i, j := range idx {
		sorted[i] = cycles[j]
	}
	return order, sorted
}
