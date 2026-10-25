package proposals

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// ErrInvalidAction wraps every rejection of an action's arguments. The text
// after it is a plain sentence that can be shown to the agent or the user.
var ErrInvalidAction = errors.New("invalid proposal")

// Limits of the boundary.
const (
	MaxPaths       = 200
	MaxMessageSize = 4 << 10
	MaxTitleLen    = 200
	MaxContentSize = 256 << 10
	maxRefLen      = 255
	maxRemoteLen   = 100
	maxStepLen     = 100
)

func invalid(f string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidAction, fmt.Sprintf(f, args...))
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// windowsDevice reports whether a path segment names a Windows device, with
// or without an extension ("NUL", "con.txt", "COM1").
func windowsDevice(seg string) bool {
	stem := seg
	if i := strings.IndexByte(stem, '.'); i >= 0 {
		stem = stem[:i]
	}
	stem = strings.ToUpper(strings.TrimRight(stem, " "))
	switch stem {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$", "CLOCK$":
		return true
	}
	if len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) {
		return stem[3] >= '0' && stem[3] <= '9'
	}
	return false
}

// CheckRelPath validates one path: relative, forward slashes, inside the root,
// no drive, stream, device name, control character or ".git" part.
func CheckRelPath(p string) error {
	switch {
	case p == "":
		return invalid("a path is empty")
	case !utf8.ValidString(p) || hasControl(p):
		return invalid("path %q has characters that are not allowed", p)
	case strings.Contains(p, `\`):
		return invalid("path %q uses a backslash; write paths with forward slashes", p)
	case strings.HasPrefix(p, "/"):
		return invalid("path %q is absolute; use a path inside the worktree", p)
	case strings.Contains(p, ":"):
		return invalid("path %q has a drive or stream marker (:)", p)
	case strings.ContainsAny(p, `<>"|?*`):
		return invalid("path %q has characters Windows does not allow", p)
	}
	for _, seg := range strings.Split(p, "/") {
		switch {
		case seg == "":
			return invalid("path %q has an empty part", p)
		case seg == "." || seg == "..":
			return invalid("path %q leaves the worktree or repeats itself (%s)", p, seg)
		case strings.HasSuffix(seg, ".") || strings.HasSuffix(seg, " "):
			return invalid("path %q has a part ending in a dot or space, which Windows rewrites", p)
		case hasShortNameAlias(seg):
			return invalid("path %q has a part like NAME~1, which Windows can use as a short name for another file", p)
		case asciiFold(seg, ".git"):
			return invalid("path %q is inside .git", p)
		case windowsDevice(seg):
			return invalid("path %q uses a reserved Windows device name", p)
		}
	}
	return nil
}

func checkPaths(ps []string) ([]RelPath, error) {
	if len(ps) == 0 {
		return nil, invalid("no paths given")
	}
	if len(ps) > MaxPaths {
		return nil, invalid("%d paths given; at most %d are allowed", len(ps), MaxPaths)
	}
	seen := make(map[string]bool, len(ps))
	out := make([]RelPath, 0, len(ps))
	for _, p := range ps {
		if err := CheckRelPath(p); err != nil {
			return nil, err
		}
		k := strings.ToLower(p)
		if seen[k] {
			return nil, invalid("path %q is listed twice", p)
		}
		seen[k] = true
		out = append(out, RelPath(p))
	}
	return out, nil
}

// hasShortNameAlias reports a "~" followed by a digit: the form of a Windows
// 8.3 short name (GIT~1, CLAUDE~1) that could reach .git or agent config.
func hasShortNameAlias(seg string) bool {
	for i := 0; i+1 < len(seg); i++ {
		if seg[i] == '~' && seg[i+1] >= '0' && seg[i+1] <= '9' {
			return true
		}
	}
	return false
}

// asciiFold compares a and b ignoring ASCII case only; other bytes must match
// exactly (no Kelvin sign or long s look-alikes).
func asciiFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if x >= 'A' && x <= 'Z' {
			x += 'a' - 'A'
		}
		if y >= 'A' && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

func isProtectedDirName(s string) bool {
	for _, d := range protectedDirNames {
		if asciiFold(s, d) {
			return true
		}
	}
	return false
}

func lastSegIsProtectedDir(p string) bool {
	return isProtectedDirName(p[strings.LastIndexByte(p, '/')+1:])
}

// IsProtectedPath reports whether p names agent config at any depth: a
// protected folder (.claude, .opencode, .codex, .gemini, .cursor, .husky,
// .githooks) as any part, including a bare folder name (committing it would
// stage everything below), or a protected file name (.mcp.json, CLAUDE.md,
// CLAUDE.local.md, AGENTS.md, opencode.json, GEMINI.md, .gitmodules) as the
// last part. ASCII case-insensitive. The list matches internal/core/guard.
// The shell uses it for the Facts.PathsOK of every other kind.
func IsProtectedPath(p RelPath) bool {
	segs := strings.Split(string(p), "/")
	for i, s := range segs {
		if isProtectedDirName(s) {
			return true
		}
		if i == len(segs)-1 {
			for _, n := range protectedFileNames {
				if asciiFold(s, n) {
					return true
				}
			}
		}
	}
	return false
}

// CheckBranch applies the git ref-name rules, and refuses a leading "-" so a
// branch can never be read as an option.
func CheckBranch(b string) error {
	switch {
	case b == "" || len(b) > maxRefLen:
		return invalid("branch name must be 1 to %d characters", maxRefLen)
	case !utf8.ValidString(b) || hasControl(b) || strings.ContainsAny(b, " ~^:?*[\\"):
		return invalid("branch name %q has characters git does not allow", b)
	case b == "@" || strings.HasPrefix(b, "-") || strings.HasPrefix(b, "+") || strings.HasPrefix(b, "/") || strings.HasSuffix(b, "/") || strings.HasSuffix(b, "."):
		return invalid("branch name %q is not a valid git branch name", b)
	case strings.Contains(b, "..") || strings.Contains(b, "//") || strings.Contains(b, "@{"):
		return invalid("branch name %q is not a valid git branch name", b)
	}
	for _, part := range strings.Split(b, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return invalid("branch name %q is not a valid git branch name", b)
		}
	}
	return nil
}

// CheckRemoteName accepts [A-Za-z0-9._-]+ that does not start with "-".
func CheckRemoteName(r string) error {
	if r == "" || len(r) > maxRemoteLen || strings.HasPrefix(r, "-") {
		return invalid("remote name %q is not valid", r)
	}
	for _, c := range r {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-'
		if !ok {
			return invalid("remote name %q may only use letters, digits, dot, dash and underscore", r)
		}
	}
	return nil
}

func checkMessage(m string) error {
	switch {
	case strings.TrimSpace(m) == "":
		return invalid("the commit message is empty")
	case len(m) > MaxMessageSize:
		return invalid("the commit message is over %d KiB", MaxMessageSize>>10)
	case !utf8.ValidString(m) || strings.ContainsRune(m, 0):
		return invalid("the commit message has characters that are not allowed")
	}
	return nil
}

func checkTitle(t string) error {
	switch {
	case strings.TrimSpace(t) == "":
		return invalid("the merge request title is empty")
	case len(t) > MaxTitleLen:
		return invalid("the merge request title is over %d characters", MaxTitleLen)
	case !utf8.ValidString(t) || hasControl(t):
		return invalid("the merge request title must be one line of plain text")
	}
	return nil
}

func checkDescription(d string) error {
	if strings.ContainsAny(d, "\r\n") {
		return invalid("the merge request description must be one line; git push options cannot carry a line break")
	}
	if !utf8.ValidString(d) || hasControl(d) {
		return invalid("the merge request description has characters that are not allowed")
	}
	if len(d) > MaxMessageSize {
		return invalid("the merge request description is over %d KiB", MaxMessageSize>>10)
	}
	return nil
}

func isHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// checkHexField accepts lowercase hex of 7 to 64 characters; "" is accepted
// only when optional.
func checkHexField(name, v string, optional bool) error {
	if v == "" && optional {
		return nil
	}
	if len(v) < 7 || len(v) > 64 || !isHex(v) {
		return invalid("%s must be a lowercase hex checksum", name)
	}
	return nil
}

func checkStep(s string) error {
	if s == "" || len(s) > maxStepLen || strings.HasPrefix(s, "-") || hasControl(s) || strings.ContainsAny(s, " \t") {
		return invalid("install step %q is not valid", s)
	}
	return nil
}
