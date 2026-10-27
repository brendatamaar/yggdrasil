package composer

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// Item is one candidate of the @ picker: a file or folder of a tracked repo.
// Path uses forward slashes and is relative to the repo.
type Item struct {
	Repo string
	Path string
	Dir  bool
}

// Rank filters items by the fuzzy query and returns at most limit of them,
// best first. It is pure and deterministic: the result is a subset of items,
// and items with equal scores keep their input order. An empty query matches
// everything with the same score, so the input order decides.
//
// Matching is case-insensitive and needs the query's characters to appear in
// the path in order (a subsequence). Prefix matches of the path or of the file
// name, whole-substring matches and runs of consecutive characters score more.
func Rank(query string, items []Item, limit int) []Item {
	if limit <= 0 {
		return nil
	}
	q := []rune(strings.ToLower(strings.TrimSpace(query)))
	type scored struct {
		it    Item
		score int
		idx   int
	}
	var hits []scored
	for i, it := range items {
		if s, ok := score(q, it.Path); ok {
			hits = append(hits, scored{it, s, i})
		}
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].score > hits[b].score })
	if len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]Item, len(hits))
	for i, h := range hits {
		out[i] = h.it
	}
	return out
}

// score reports whether q is a subsequence of path and how well it fits.
func score(q []rune, path string) (int, bool) {
	if len(q) == 0 {
		return 0, true
	}
	p := strings.ToLower(path)
	pr := []rune(p)
	run, best, total := 0, 0, 0
	qi := 0
	for _, c := range pr {
		if qi < len(q) && c == q[qi] {
			qi++
			run++
			total += run // consecutive characters grow the bonus
			best = max(best, run)
		} else {
			run = 0
		}
	}
	if qi < len(q) {
		return 0, false
	}
	s := 100 + total + best
	qs := string(q)
	base := p
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		base = p[i+1:]
	}
	switch {
	case strings.HasPrefix(base, qs):
		s += 200
	case strings.Contains(base, qs):
		s += 120
	}
	if strings.HasPrefix(p, qs) {
		s += 100
	} else if strings.Contains(p, qs) {
		s += 40
	}
	// Shorter paths first among otherwise similar matches.
	s -= min(utf8.RuneCountInString(p)/8, 50)
	return s, true
}
