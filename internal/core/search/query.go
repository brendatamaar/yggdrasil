package search

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// ErrInvalidQuery is wrapped by every ParseQuery rejection. The message after
// it says which rule failed, in plain language.
var ErrInvalidQuery = errors.New("invalid search")

// RawQuery is a query as it arrives from outside (the HTTP body).
type RawQuery struct {
	Pattern string
	Mode    string
	Case    bool
	Repos   []string
	Paths   []string
}

func reject(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidQuery, fmt.Sprintf(format, a...))
}

// ParseQuery turns outside data into a Query or rejects it. It is the only way
// to build a Query. The pattern is data and is kept exactly as typed; the
// shell passes it after -e, so a pattern like "--output=x" is only text.
// The ERE shape check (regexp/syntax) is the shell's job: the core may not
// import it (docs/AGENTS.md 5.11).
func ParseQuery(r RawQuery) (Query, error) {
	if strings.TrimSpace(r.Pattern) == "" {
		return Query{}, reject("type something to search for")
	}
	if len(r.Pattern) > MaxPatternBytes {
		return Query{}, reject("the search text is longer than %d bytes", MaxPatternBytes)
	}
	if !utf8.ValidString(r.Pattern) {
		return Query{}, reject("the search text is not valid UTF-8")
	}
	if strings.ContainsAny(r.Pattern, "\x00\r\n") {
		return Query{}, reject("the search text must be a single line")
	}
	mode := Mode(r.Mode)
	if r.Mode == "" {
		mode = ModeLiteral
	}
	if err := mode.Validate(); err != nil {
		return Query{}, reject("%s", err.Error())
	}
	if len(r.Repos) > MaxRepoFilter {
		return Query{}, reject("too many repos selected (at most %d)", MaxRepoFilter)
	}
	var repos []core.RepoID
	for _, s := range r.Repos {
		id := core.RepoID(s)
		if err := id.Validate(); err != nil {
			return Query{}, reject("a selected repo id is not valid")
		}
		if !slices.Contains(repos, id) {
			repos = append(repos, id)
		}
	}
	if len(r.Paths) > MaxPaths {
		return Query{}, reject("too many path filters (at most %d)", MaxPaths)
	}
	var paths []string
	for _, p := range r.Paths {
		if err := checkPathGlob(p); err != nil {
			return Query{}, err
		}
		if !slices.Contains(paths, p) {
			paths = append(paths, p)
		}
	}
	return Query{Pattern: r.Pattern, Mode: mode, Case: r.Case, Repos: repos, Paths: paths}, nil
}

// Raw is the inverse of ParseQuery: ParseQuery(q.Raw()) == q for a parsed q.
func (q Query) Raw() RawQuery {
	r := RawQuery{Pattern: q.Pattern, Mode: string(q.Mode), Case: q.Case}
	for _, id := range q.Repos {
		r.Repos = append(r.Repos, string(id))
	}
	r.Paths = slices.Clone(q.Paths)
	return r
}

// Validate reports whether q could have come from ParseQuery.
func (q Query) Validate() error {
	p, err := ParseQuery(q.Raw())
	if err != nil {
		return err
	}
	if p.Mode != q.Mode {
		return reject("mode is empty")
	}
	return nil
}

// checkPathGlob rejects pathspecs that could leave the repo or that git would
// read as an option or as pathspec magic (":(...)" and ":!").
func checkPathGlob(p string) error {
	switch {
	case strings.TrimSpace(p) == "":
		return reject("a path filter is empty")
	case len(p) > MaxPathBytes:
		return reject("a path filter is longer than %d bytes", MaxPathBytes)
	case !utf8.ValidString(p) || strings.ContainsAny(p, "\x00\r\n"):
		return reject("a path filter has characters that are not allowed")
	case strings.HasPrefix(p, "-"):
		return reject("a path filter must not start with \"-\"")
	case strings.Contains(p, ":"):
		return reject("a path filter must not contain \":\" (it would be read as pathspec magic or a drive letter)")
	case strings.HasPrefix(p, "/") || strings.HasPrefix(p, "\\"):
		return reject("a path filter must be relative to the repo")
	}
	for _, seg := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return reject("a path filter must not leave the repo (\"..\")")
		}
	}
	return nil
}
