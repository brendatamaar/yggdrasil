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

// isReservedName reports Windows device names (CON, NUL, COM1, ...), with or
// without an extension.
func isReservedName(s string) bool {
	base, _, _ := strings.Cut(strings.ToUpper(s), ".")
	switch base {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	return len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) &&
		base[3] >= '1' && base[3] <= '9'
}
