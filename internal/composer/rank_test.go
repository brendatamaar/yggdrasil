package composer

import (
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
)

func items(paths ...string) []Item {
	out := make([]Item, len(paths))
	for i, p := range paths {
		out[i] = Item{Repo: "shop-api", Path: p}
	}
	return out
}

func paths(its []Item) []string {
	out := make([]string, len(its))
	for i, it := range its {
		out[i] = it.Path
	}
	return out
}

func TestRankBoostsPrefixAndBasename(t *testing.T) {
	got := Rank("main", items("docs/domain/maintenance.md", "cmd/app/main.go", "src/m/a/i/n.txt", "main.go"), 10)
	if len(got) != 4 {
		t.Fatalf("got %v", paths(got))
	}
	if got[0].Path != "main.go" || got[3].Path != "src/m/a/i/n.txt" {
		t.Fatalf("order %v", paths(got))
	}
}

func TestRankIsCaseInsensitiveAndUnicode(t *testing.T) {
	got := Rank("ÜBER", items("src/über cool.txt", "other.txt"), 10)
	if len(got) != 1 || got[0].Path != "src/über cool.txt" {
		t.Fatalf("got %v", paths(got))
	}
}

func TestRankDropsNonSubsequences(t *testing.T) {
	if got := Rank("zzq", items("a.go", "b.go"), 10); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestRankEmptyQueryKeepsInputOrderAndLimit(t *testing.T) {
	in := items("c", "a", "b")
	got := Rank("", in, 2)
	if !reflect.DeepEqual(paths(got), []string{"c", "a"}) {
		t.Fatalf("got %v", paths(got))
	}
	if Rank("", in, 0) != nil {
		t.Fatal("limit 0 must return nothing")
	}
}

func randPath(r *rand.Rand) string {
	alpha := "abcAB/._- é"
	n := 1 + r.IntN(12)
	var sb strings.Builder
	rs := []rune(alpha)
	for i := 0; i < n; i++ {
		sb.WriteRune(rs[r.IntN(len(rs))])
	}
	return sb.String()
}

func TestRankProperties(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for iter := 0; iter < 500; iter++ {
		var in []Item
		for i := 0; i < r.IntN(30); i++ {
			in = append(in, Item{Repo: "r", Path: randPath(r), Dir: r.IntN(2) == 0})
		}
		qr := []rune(randPath(r))
		q := string(qr[:min(len(qr), 1+r.IntN(2))])
		limit := 1 + r.IntN(10)
		out := Rank(q, in, limit)
		again := Rank(q, in, limit)
		if !reflect.DeepEqual(out, again) {
			t.Fatal("not deterministic")
		}
		if len(out) > limit {
			t.Fatalf("over the limit: %d > %d", len(out), limit)
		}
		// subset, each at most as often as in the input
		count := map[Item]int{}
		for _, it := range in {
			count[it]++
		}
		for _, it := range out {
			count[it]--
			if count[it] < 0 {
				t.Fatalf("result %+v not in input", it)
			}
		}
		// equal scores keep input order: with an empty query everything ties.
		all := Rank("", in, len(in)+1)
		if len(in) > 0 && !reflect.DeepEqual(all, in) {
			t.Fatal("ties must keep the input order")
		}
	}
}
