package platform

import (
	"fmt"
	"hash/fnv"
	"strings"
	"unicode/utf8"
)

// ShortDirName turns name into a directory name that is valid on Windows and
// at most maxLen characters (at least 8), for worktree folders: long paths
// break tools (docs/AGENTS.md 9). A name that had to change ends in a hash of
// the original, so two different names stay different.
func ShortDirName(name string, maxLen int) string {
	maxLen = max(maxLen, 8)
	changed := false
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || strings.ContainsRune(`<>:"/\|?*`, r) {
			r = '-'
			changed = true
		}
		b.WriteRune(r)
	}
	s := strings.TrimRight(b.String(), " .")
	if s == "" {
		s, changed = "wt", true
	}
	if s != b.String() {
		changed = true
	}
	if isReservedName(s) {
		s, changed = "_"+s, true
	}
	if !changed && utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	suffix := fmt.Sprintf("-%06x", h.Sum32()&0xffffff)
	runes := []rune(s)
	if keep := maxLen - len(suffix); len(runes) > keep {
		runes = runes[:keep]
	}
	return strings.TrimRight(string(runes), " .") + suffix
}

// isReservedName reports Windows device names (CON, NUL, COM1, COM¹, ...),
// with or without an extension. Win32 drops trailing spaces and dots from the
// base name, so "NUL .txt" is NUL too.
func isReservedName(s string) bool {
	base, _, _ := strings.Cut(strings.ToUpper(s), ".")
	base = strings.TrimRight(base, " ")
	switch base {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	if !strings.HasPrefix(base, "COM") && !strings.HasPrefix(base, "LPT") {
		return false
	}
	switch base[3:] {
	case "1", "2", "3", "4", "5", "6", "7", "8", "9", "¹", "²", "³":
		return true
	}
	return false
}

// The guard shell must call HasShortNameSegment on proposal and tool paths: the pure core cannot.
//
// HasShortNameSegment reports whether a segment of the relative path looks
// like an 8.3 alias: a name ending in ~digits, before an optional extension
// (PROGRA~1, LONGDI~1.TXT). Both / and \ separate segments. Such a spelling can
// name a file whose long name the caller's rules would judge differently, so
// callers on Windows refuse or resolve it before deciding.
func HasShortNameSegment(rel string) bool {
	for _, seg := range strings.FieldsFunc(rel, func(r rune) bool { return r == '/' || r == '\\' }) {
		if i := strings.IndexByte(seg, '.'); i >= 0 && endsInTildeDigits(seg[:i]) {
			return true
		}
		if i := strings.LastIndexByte(seg, '.'); i >= 0 && endsInTildeDigits(seg[:i]) {
			return true
		}
		if endsInTildeDigits(seg) {
			return true
		}
	}
	return false
}

func endsInTildeDigits(name string) bool {
	n := len(name)
	for n > 0 && name[n-1] >= '0' && name[n-1] <= '9' {
		n--
	}
	return n < len(name) && n > 1 && name[n-1] == '~'
}
