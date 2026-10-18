package builds

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

func genModules(r *rand.Rand) []Module {
	n := r.Intn(8)
	mods := make([]Module, n)
	coord := func(i int) Coord { return Coord{Group: "g", Artifact: fmt.Sprintf("a%d", i), Version: "1"} }
	perm := r.Perm(n)
	for i := range mods {
		mods[i] = Module{RepoID: core.RepoID(fmt.Sprintf("repo%d", perm[i])), Coord: coord(perm[i])}
		for k := r.Intn(3); k > 0; k-- {
			// dependencies on known modules, sometimes unknown (external) ones
			d := coord(r.Intn(n + 2))
			if r.Intn(4) == 0 {
				d.Version = "2"
			}
			mods[i].Deps = append(mods[i].Deps, d)
		}
	}
	return mods
}

// O1
func TestPropOrderTopological(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		mods := genModules(r)
		order, cycles := OrderModules(mods)
		order2, cycles2 := OrderModules(mods)
		if !reflect.DeepEqual(order, order2) || !reflect.DeepEqual(cycles, cycles2) {
			t.Fatal("not deterministic")
		}
		var in, out []string
		for _, m := range mods {
			in = append(in, string(m.RepoID))
		}
		for _, id := range order {
			out = append(out, string(id))
		}
		sort.Strings(in)
		sort.Strings(out)
		if len(in) != len(out) || (len(in) > 0 && !reflect.DeepEqual(in, out)) {
			t.Fatalf("not a permutation: in %v out %v", in, out)
		}
		pos := map[core.RepoID]int{}
		for i, id := range order {
			pos[id] = i
		}
		// edges by Group:Artifact (repo IDs are unique in this generator)
		by := map[string]core.RepoID{}
		for _, m := range mods {
			by[m.Coord.Group+":"+m.Coord.Artifact] = m.RepoID
		}
		adj := map[core.RepoID][]core.RepoID{}
		for _, m := range mods {
			for _, d := range m.Deps {
				if to, ok := by[d.Group+":"+d.Artifact]; ok && to != m.RepoID {
					adj[m.RepoID] = append(adj[m.RepoID], to)
				}
			}
		}
		reach := func(a, b core.RepoID) bool {
			seen := map[core.RepoID]bool{a: true}
			stack := []core.RepoID{a}
			for len(stack) > 0 {
				x := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				for _, y := range adj[x] {
					if y == b {
						return true
					}
					if !seen[y] {
						seen[y] = true
						stack = append(stack, y)
					}
				}
			}
			return false
		}
		for a, tos := range adj {
			for _, b := range tos {
				if reach(b, a) {
					continue // inside a cycle
				}
				if pos[b] >= pos[a] {
					t.Fatalf("%s depends on %s but comes first: %v", a, b, order)
				}
			}
		}
		// cycles: exactly the mutual-reachability classes of size > 1, once each
		wantCycle := map[core.RepoID]string{}
		for _, m := range mods {
			var cls []string
			for _, o := range mods {
				if m.RepoID == o.RepoID || (reach(m.RepoID, o.RepoID) && reach(o.RepoID, m.RepoID)) {
					cls = append(cls, string(o.RepoID))
				}
			}
			if len(cls) > 1 {
				sort.Strings(cls)
				wantCycle[m.RepoID] = fmt.Sprint(cls)
			}
		}
		got := map[core.RepoID]bool{}
		for _, c := range cycles {
			var ids []string
			for _, id := range c {
				if got[id] {
					t.Fatalf("%s reported twice", id)
				}
				got[id] = true
				ids = append(ids, string(id))
			}
			for i := 1; i < len(c); i++ {
				if pos[c[i]] != pos[c[i-1]]+1 {
					t.Fatalf("cycle %v not contiguous in %v", c, order)
				}
			}
			sort.Strings(ids)
			if want := wantCycle[c[0]]; want != fmt.Sprint(ids) {
				t.Fatalf("cycle %v, want class %s", ids, want)
			}
		}
		if len(got) != len(wantCycle) {
			t.Fatalf("reported %d cycle members, want %d", len(got), len(wantCycle))
		}
	})
}
