package repos

import (
	"fmt"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
)

// Boundary parsers of the repo page reads (WS-05). Each turns a query or
// path value into a type that cannot hold an invalid value, and rejects the
// rest with a plain-language message.

// Limits of the history read.
const (
	DefaultLogLimit = 100
	MaxLogLimit     = 500
)

func detailInvalid(msg, detail string) error {
	return apperr.New(apperr.Invalid, msg, fmt.Errorf("%s", detail))
}

// RelPath is a slash-separated path inside a repo: clean, relative, without
// "..". The empty RelPath means "the whole repo".
type RelPath string

// String returns the path.
func (p RelPath) String() string { return string(p) }

// ParseRelPath validates a path given by a client. Backslashes are read as
// separators. "." and "" give the empty RelPath.
func ParseRelPath(s string) (RelPath, error) {
	const msg = "That file path isn't valid. Pick a file from the list."
	for _, r := range s {
		if r < 0x20 && r != '\t' {
			return "", detailInvalid(msg, "path holds a control character")
		}
	}
	s = strings.ReplaceAll(s, `\`, "/")
	if strings.HasPrefix(s, "/") || filepath.IsAbs(s) || (len(s) >= 2 && s[1] == ':') {
		return "", detailInvalid(msg, "path is absolute")
	}
	for seg := range strings.SplitSeq(s, "/") {
		if seg == ".." {
			return "", detailInvalid(msg, "path leaves the repo")
		}
	}
	c := path.Clean(s)
	if c == "." {
		return "", nil
	}
	if strings.HasPrefix(c, "-") || strings.HasPrefix(c, ":") {
		return "", detailInvalid(msg, "path starts with - or :")
	}
	return RelPath(c), nil
}

// DiffSide says which changes of the working tree a diff shows.
type DiffSide string

// The closed set of diff sides.
const (
	Unstaged DiffSide = "unstaged"
	Staged   DiffSide = "staged"
)

// ParseDiffSide accepts "unstaged", "staged" and "" (unstaged).
func ParseDiffSide(s string) (DiffSide, error) {
	switch DiffSide(s) {
	case "", Unstaged:
		return Unstaged, nil
	case Staged:
		return Staged, nil
	}
	return "", detailInvalid(`The diff mode must be "unstaged" or "staged".`, "mode "+strconv.Quote(s))
}

// Limit is a number of commits, 1 to MaxLogLimit.
type Limit int

// ParseLimit accepts "" (DefaultLogLimit) or an integer from 1 to 500.
func ParseLimit(s string) (Limit, error) {
	if s == "" {
		return DefaultLogLimit, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > MaxLogLimit {
		return 0, detailInvalid(fmt.Sprintf("The limit must be a number from 1 to %d.", MaxLogLimit), "limit "+strconv.Quote(s))
	}
	return Limit(n), nil
}

// Ref is a branch, tag or "HEAD" name that git would accept and that cannot
// be read as an option. The empty Ref means HEAD.
type Ref string

// String returns the name.
func (r Ref) String() string { return string(r) }

// ParseRef validates a ref name by git's naming rules (a strict subset: no
// revision syntax such as ~, ^, @{ or :).
func ParseRef(s string) (Ref, error) {
	const msg = "That branch or tag name isn't valid."
	if s == "" {
		return "", nil
	}
	if len(s) > 255 || strings.HasPrefix(s, "-") || strings.HasPrefix(s, "/") ||
		strings.HasSuffix(s, "/") || strings.HasSuffix(s, ".") || strings.HasSuffix(s, ".lock") ||
		strings.Contains(s, "..") || strings.Contains(s, "//") || strings.Contains(s, "@{") || s == "@" {
		return "", detailInvalid(msg, "ref "+strconv.Quote(s))
	}
	for _, r := range s {
		if r <= 0x20 || r == 0x7f || strings.ContainsRune(`~^:?*[\`, r) {
			return "", detailInvalid(msg, "ref holds a forbidden character")
		}
	}
	for seg := range strings.SplitSeq(s, "/") {
		if strings.HasPrefix(seg, ".") || strings.HasSuffix(seg, ".lock") {
			return "", detailInvalid(msg, "ref segment starts with . or ends with .lock")
		}
	}
	return Ref(s), nil
}

// OID is a commit ID: 4 to 64 lower-case hex digits.
type OID string

// String returns the ID.
func (o OID) String() string { return string(o) }

// ParseOID accepts 4 to 64 hex digits (any case) and lower-cases them.
func ParseOID(s string) (OID, error) {
	const msg = "That commit ID isn't valid."
	if len(s) < 4 || len(s) > 64 {
		return "", detailInvalid(msg, "commit ID length")
	}
	s = strings.ToLower(s)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", detailInvalid(msg, "commit ID is not hex")
		}
	}
	return OID(s), nil
}
