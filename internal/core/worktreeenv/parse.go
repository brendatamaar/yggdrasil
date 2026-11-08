package worktreeenv

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Sentinel errors, one per rejection rule, so tests and callers can tell the
// rules apart. Messages are plain language and hold no user input.
var (
	errRangeLow   = errors.New("the port range must start at 1024 or higher")
	errRangeHigh  = errors.New("the port range must end at 65535 or lower")
	errRangeSmall = errors.New("the port range must hold at least 20 ports")
	errRangeForm  = errors.New("write the port range as low-high, for example 20000-29999")

	// ErrPathEmpty and the errors below name the rule a profile path broke.
	ErrPathEmpty    = errors.New("a profile path is empty")
	ErrPathAbsolute = errors.New("a profile path must be relative to the repo (no drive letter, share or leading slash)")
	ErrPathParent   = errors.New("a profile path must not contain '..'")
	ErrPathGit      = errors.New("a profile path must not point into .git")
	ErrPathControl  = errors.New("a profile path holds a control character")
	ErrPathGlob     = errors.New("a profile path must name one file; wildcards are not supported")
	ErrPathReserved = errors.New("a profile path holds a name Windows does not allow")
	ErrPathLong     = errors.New("a profile path is longer than 260 characters")
	ErrPathCount    = errors.New("a profile can list at most 50 files")

	// ErrBlockRange and the errors below name the rule a stored block broke.
	ErrBlockRange = errors.New("the stored port block is outside 1..65535")
	ErrBlockAlign = errors.New("the port block does not start on a block boundary of the range")
	ErrBlockSize  = errors.New("the port block is not 20 ports long")
	ErrBlockOutOf = errors.New("the port block lies outside the range")
)

// ParseRange reads "lo-hi" (spaces around the numbers allowed).
func ParseRange(s string) (Range, error) {
	lo, hi, ok := strings.Cut(s, "-")
	if !ok {
		return Range{}, errRangeForm
	}
	l, err1 := strconv.Atoi(strings.TrimSpace(lo))
	h, err2 := strconv.Atoi(strings.TrimSpace(hi))
	if err1 != nil || err2 != nil || strings.ContainsAny(lo+hi, "+-") {
		return Range{}, errRangeForm
	}
	r := Range{Lo: l, Hi: h}
	if err := r.Validate(); err != nil {
		return Range{}, err
	}
	return r, nil
}

// String formats the range as ParseRange reads it.
func (r Range) String() string { return fmt.Sprintf("%d-%d", r.Lo, r.Hi) }

// ParseProfile validates a list of profile paths (API or settings input) and
// returns them normalised: forward slashes, cleaned, duplicates (compared
// case-insensitively) merged with the first spelling kept. An empty list is
// valid; an empty entry is not. Any '..' component is rejected before
// cleaning, which is stricter than "escapes after cleaning" on purpose.
func ParseProfile(in []string) ([]RelPath, error) {
	if len(in) > MaxEntries {
		return nil, ErrPathCount
	}
	out := make([]RelPath, 0, len(in))
	seen := map[string]bool{}
	for _, s := range in {
		p, err := parseRelPath(s)
		if err != nil {
			return nil, err
		}
		key := strings.ToLower(string(p))
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, p)
	}
	return out, nil
}

// Validate reports whether p is in the normal form parseRelPath produces.
func (p RelPath) Validate() error {
	q, err := parseRelPath(string(p))
	if err != nil {
		return err
	}
	if q != p {
		return ErrPathEmpty
	}
	return nil
}

var reservedNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true, "conin$": true, "conout$": true,
}

func reserved(comp string) bool {
	base := comp
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	base = strings.ToLower(strings.TrimRight(base, " "))
	if reservedNames[base] {
		return true
	}
	return len(base) == 4 && (strings.HasPrefix(base, "com") || strings.HasPrefix(base, "lpt")) && base[3] >= '1' && base[3] <= '9'
}

// parseRelPath is the one place that decides what a repo-relative path is.
// The compose file parser uses it too.
func parseRelPath(s string) (RelPath, error) {
	if strings.TrimSpace(s) == "" {
		return "", ErrPathEmpty
	}
	if !utf8.ValidString(s) {
		return "", ErrPathControl
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return "", ErrPathControl
		}
	}
	if utf8.RuneCountInString(s) > 260 {
		return "", ErrPathLong
	}
	s = strings.ReplaceAll(s, `\`, "/")
	if strings.HasPrefix(s, "/") || strings.Contains(s, ":") {
		// leading slash, UNC ("//host"), drive letters and NTFS streams
		return "", ErrPathAbsolute
	}
	if strings.ContainsAny(s, "*?[") {
		return "", ErrPathGlob
	}
	var comps []string
	for _, c := range strings.Split(s, "/") {
		switch c {
		case "", ".":
			continue
		case "..":
			return "", ErrPathParent
		}
		comps = append(comps, c)
	}
	if len(comps) == 0 {
		return "", ErrPathEmpty
	}
	for _, c := range comps {
		lc := strings.ToLower(c)
		if lc == ".git" || lc == "git~1" {
			return "", ErrPathGit
		}
		if strings.HasSuffix(c, ".") || strings.HasSuffix(c, " ") || strings.ContainsAny(c, `<>"|`) || reserved(c) {
			return "", ErrPathReserved
		}
	}
	return RelPath(strings.Join(comps, "/")), nil
}

// BlockFromRow reads a stored block. It accepts blocks of any size and
// alignment inside 1..65535, because rows written under an earlier range stay
// where they are (E5).
func BlockFromRow(base, size int) (Block, error) {
	b := Block{Base: base, Size: size}
	if b.Validate() != nil {
		return Block{}, ErrBlockRange
	}
	return b, nil
}

// NewBlockFromRow is the strict form for rows being created: 20 ports, on a
// block boundary of r, inside r.
func NewBlockFromRow(base, size int, r Range) (Block, error) {
	b, err := BlockFromRow(base, size)
	if err != nil {
		return Block{}, err
	}
	if size != core.PortBlockSize {
		return Block{}, ErrBlockSize
	}
	if base < r.Lo || base+size-1 > r.Hi {
		return Block{}, ErrBlockOutOf
	}
	if (base-r.Lo)%core.PortBlockSize != 0 {
		return Block{}, ErrBlockAlign
	}
	return b, nil
}

// RowOf is the inverse of BlockFromRow.
func RowOf(b Block) (base, size int) { return b.Base, b.Size }
