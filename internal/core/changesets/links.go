package changesets

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Description limits. MaxRenderedLinks is the most the marker can expand to;
// ValidateLinkMarker counts the marker as that many bytes.
const (
	MaxDescriptionBytes = 8192
	MaxRenderedLinks    = 2048
	linkSeparator       = " · "
)

func isLineBreak(r rune) bool {
	return r == '\n' || r == '\r' || r == '\u0085' || r == ' ' || r == ' ' || r == '\v' || r == '\f'
}

// ValidateLinkMarker rejects a description with more than one LinkMarker
// (including one that appears only after the first is removed), a line break
// (push-option descriptions are one line), or a length over
// MaxDescriptionBytes with the marker counted as its rendered maximum. Pure.
func ValidateLinkMarker(description string) error {
	if strings.ContainsFunc(description, isLineBreak) {
		return errors.New("the description must be a single line")
	}
	n := strings.Count(description, LinkMarker)
	if n > 1 || (n == 1 && strings.Contains(strings.Replace(description, LinkMarker, "", 1), LinkMarker)) {
		return fmt.Errorf("the description may contain %s only once", LinkMarker)
	}
	size := len(description)
	if n == 1 {
		size += MaxRenderedLinks - len(LinkMarker)
	}
	if size > MaxDescriptionBytes {
		return fmt.Errorf("the description is too long (at most %d bytes, counting the links as %d)", MaxDescriptionBytes, MaxRenderedLinks)
	}
	return nil
}

// plainName makes a repo or branch name safe for a one-line description:
// control characters, line separators and angle brackets become "_".
func plainName(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || isLineBreak(r) || r == '<' || r == '>' {
			return '_'
		}
		return r
	}, s)
}

// RenderLinks replaces the one LinkMarker in an MR description template with
// the link text, on one line: the set's repos in merge order, each as
// "repo (branch)" and, when links holds the MR of an earlier, executed member
// and the URL is a safe web URL on host, ": URL"; entries are joined by " · ".
// links must hold only executed members' MRs (the shell builds it from
// executed results). The text is cut at whole entries to MaxRenderedLinks
// bytes. A template without exactly one marker, or one that would contain a
// marker after the replacement, is returned unchanged. Pure.
func RenderLinks(template string, order []core.RepoID, branches map[core.RepoID]string, links map[core.RepoID]string, host string) string {
	if strings.Count(template, LinkMarker) != 1 {
		return template
	}
	var entries []string
	seen := map[core.RepoID]bool{}
	size := 0
	for _, r := range order {
		if seen[r] {
			continue
		}
		seen[r] = true
		e := plainName(string(r))
		if b := branches[r]; b != "" {
			e += " (" + plainName(b) + ")"
		}
		if u, ok := links[r]; ok && linkURLOK(u, host) {
			e += ": " + u
		}
		add := len(e)
		if len(entries) > 0 {
			add += len(linkSeparator)
		}
		if size+add > MaxRenderedLinks {
			continue
		}
		size += add
		entries = append(entries, e)
	}
	out := strings.Replace(template, LinkMarker, strings.Join(entries, linkSeparator), 1)
	if strings.Contains(out, LinkMarker) {
		return template
	}
	return out
}
