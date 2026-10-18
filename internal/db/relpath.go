package db

import (
	"strconv"
	"strings"
	"unicode"

	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
)

// derivedRel derives repos.rel_path from the paths: the repo's folder under
// the project's folder, with forward slashes, "." for the project folder
// itself. Paths are compared without case, as Windows does. ok is false for a
// repo outside the project folder (which tracking never produces) or whose
// derived folder is not one the core accepts.
func derivedRel(projectPath, repoPath string) (rel string, ok bool) {
	norm := func(p string) string { return strings.TrimRight(strings.ReplaceAll(p, `\`, "/"), "/") }
	pp, rp := norm(projectPath), norm(repoPath)
	switch {
	case strings.EqualFold(pp, rp):
		return ".", true
	case len(rp) > len(pp) && strings.EqualFold(rp[:len(pp)], pp) && rp[len(pp)] == '/':
		rel = rp[len(pp)+1:]
	default:
		return "", false
	}
	if _, err := corerepos.NewRelPath(rel); err != nil {
		return "", false
	}
	return rel, true
}

// slugOf turns a name into one safe path segment: letters, digits, space,
// '.', '_' and '-' stay, everything else becomes '-'. A name that is empty or
// only dots after that becomes "repo".
func slugOf(name string) string {
	s := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' || r == '.' || r == '_' || r == '-' {
			return r
		}
		return '-'
	}, name)
	s = strings.TrimSpace(s)
	if strings.Trim(s, ".") == "" {
		return "repo"
	}
	return s
}

// fallbackRel is the rel_path of a repo outside its project folder: a slug of
// its name, with the repo's ID appended when that is already taken in the
// project. The result always passes corerepos.NewRelPath. taken holds the
// rel_paths in use in the project and is updated.
func fallbackRel(name, repoID string, taken map[string]bool) string {
	base := slugOf(name)
	rel := base
	if taken[rel] {
		rel = base + "-" + slugOf(repoID)
		for n := 2; taken[rel]; n++ {
			rel = base + "-" + slugOf(repoID) + "-" + strconv.Itoa(n)
		}
	}
	if _, err := corerepos.NewRelPath(rel); err != nil {
		rel = "repo-" + slugOf(repoID)
	}
	taken[rel] = true
	return rel
}
