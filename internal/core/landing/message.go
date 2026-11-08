package landing

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
)

const (
	maxBranchRunes = 200
	maxHolderRunes = 64
	maxPathRunes   = 300

	noteGitCleanupLater = "git will clean up its record later"
	noteAlreadyRemoved  = "already removed"
)

// MergeMessage builds the commit message `Merge branch '<branch>' (<title>)`.
// Control and format characters (newlines, escapes, bidi marks) become spaces,
// runs of white space collapse, and the branch and the title are capped, so a
// title can never add lines, trailers or terminal escapes to the commit.
func MergeMessage(branch, title string) string {
	b := capRunes(plainText(branch), maxBranchRunes)
	t := capRunes(plainText(title), MaxTitleRunes)
	if t == "" {
		return "Merge branch '" + b + "'"
	}
	return "Merge branch '" + b + "' (" + t + ")"
}

// plainText replaces control and format characters with spaces and collapses
// white space.
func plainText(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.IsSpace(r) || r == unicode.ReplacementChar {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

func capRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n]))
}

// cleanHolders sanitises process names for a user message: control characters
// become spaces, names are capped, empty and duplicate names (ignoring case)
// are dropped, and at most MaxHolders remain, in the order given.
func cleanHolders(in []string) []string {
	var out []string
	var seen []string
	for _, h := range in {
		h = capRunes(plainText(h), maxHolderRunes)
		key := strings.ToLower(h)
		if h == "" || slices.Contains(seen, key) {
			continue
		}
		seen = append(seen, key)
		out = append(out, h)
		if len(out) == MaxHolders {
			break
		}
	}
	return out
}

func okClass(c LockClass) LockClass {
	if c.Validate() != nil {
		return LockOther
	}
	return c
}

func holdersSentence(h []string) string {
	if len(h) == 0 {
		return ""
	}
	return " Programs that may be using it: " + strings.Join(h, ", ") + "."
}

func classSentence(c LockClass) string {
	switch c {
	case LockPathTooLong:
		return "Windows could not handle a file path in this folder because it may be too long."
	case LockAccessDenied:
		return "Windows denied access to a file in this folder. A program may be using it (for example an editor, a terminal or a dev server), or its permissions may block it."
	default:
		return "Something may be using files in this folder (for example an editor, a terminal or a dev server)."
	}
}

// Message is the plain-language text for the user. Nothing was changed.
func (l Locked) Message() string {
	return classSentence(okClass(l.Class)) + " Nothing was changed. Close whatever may be using it and retry." + holdersSentence(l.Holders)
}

// Message is the plain-language text for the user. The folder was moved aside
// and part of it is deleted.
func (s Stuck) Message() string {
	left := ""
	if s.Remaining > 0 {
		left = fmt.Sprintf(" About %d items are left.", s.Remaining)
	}
	return "Removal is not finished: some files could not be deleted yet." + left + " " +
		classSentence(okClass(s.Class)) + " Close whatever may be using it and retry; the rest will be deleted." + holdersSentence(s.Holders)
}
