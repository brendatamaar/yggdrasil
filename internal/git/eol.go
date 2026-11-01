package git

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// NumstatEntry is one line of `git diff --numstat -z --no-renames`.
type NumstatEntry struct {
	Path    string
	Added   int
	Deleted int
	Binary  bool // git prints "-" for both counts
}

// ParseNumstat reads `git diff --numstat -z` output made with --no-renames:
// "<added> TAB <deleted> TAB <path> NUL" per file. The rename form (an empty
// path field) is refused.
func ParseNumstat(out string) ([]NumstatEntry, error) {
	var res []NumstatEntry
	for _, tok := range splitNUL(out) {
		f := strings.SplitN(tok, "\t", 3)
		if len(f) != 3 || f[2] == "" {
			return nil, fmt.Errorf("bad numstat entry %q", tok)
		}
		e := NumstatEntry{Path: f[2]}
		if f[0] == "-" && f[1] == "-" {
			e.Binary = true
		} else {
			a, aerr := strconv.Atoi(f[0])
			d, derr := strconv.Atoi(f[1])
			if aerr != nil || derr != nil || a < 0 || d < 0 {
				return nil, fmt.Errorf("bad numstat counts in %q", tok)
			}
			e.Added, e.Deleted = a, d
		}
		res = append(res, e)
	}
	return res, nil
}

// ClassifyEOL finds the files whose whole change is a line-ending change
// (PL-03). plain is the numstat of the diff, ignoringCR the numstat of the same
// diff made with --ignore-cr-at-eol. A file qualifies when its plain diff has
// changed lines, is not binary, and the ignoring diff has no changed lines for
// it (git either omits the file or prints 0 and 0). The result is sorted.
// It reads text only: no user config is read or changed and no file rewritten.
func ClassifyEOL(plain, ignoringCR string) ([]string, error) {
	p, err := ParseNumstat(plain)
	if err != nil {
		return nil, err
	}
	q, err := ParseNumstat(ignoringCR)
	if err != nil {
		return nil, err
	}
	left := make(map[string]NumstatEntry, len(q))
	for _, e := range q {
		left[e.Path] = e
	}
	var res []string
	for _, e := range p {
		if e.Binary || e.Added+e.Deleted == 0 {
			continue
		}
		if r, ok := left[e.Path]; ok && (r.Binary || r.Added+r.Deleted > 0) {
			continue
		}
		if len(res) == 0 || !slices.Contains(res, e.Path) {
			res = append(res, e.Path)
		}
	}
	sort.Strings(res)
	return res, nil
}
