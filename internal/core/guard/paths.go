package guard

import "strings"

// pathParts is a path split for containment checks. kind is 0 for a drive
// path (`C:\x`) or a relative path, 1 for a rooted path (`/x`, `\x`) and 2 for
// a UNC-style path (`\\host\x`). ok is false for anything we cannot reason
// about safely: a ".." part, a NUL, a stream or drive part in the wrong
// place, or (when folding) a part Windows would trim.
type pathParts struct {
	kind int
	segs []string
	abs  bool
	ok   bool
	// trims is set when folding and a part ends in "." or " ", which Windows
	// trims: the name is not what it looks like. Only containment cares.
	trims bool
}

func isSep(c rune) bool { return c == '/' || c == '\\' }

func isDriveSeg(s string) bool {
	return len(s) == 2 && s[1] == ':' && (s[0] >= 'A' && s[0] <= 'Z' || s[0] >= 'a' && s[0] <= 'z')
}

// splitPath splits p on both separators. A path is never trusted to use one
// kind of separator.
func splitPath(p string, fold bool) pathParts {
	out := pathParts{ok: true}
	if strings.ContainsRune(p, 0) {
		out.ok = false
		return out
	}
	lead := 0
	for lead < len(p) && isSep(rune(p[lead])) {
		lead++
	}
	if lead > 2 {
		lead = 2
	}
	out.kind = lead
	for _, s := range strings.FieldsFunc(p, isSep) {
		if s == "." {
			continue
		}
		out.segs = append(out.segs, s)
	}
	out.abs = lead > 0 || (len(out.segs) > 0 && isDriveSeg(out.segs[0]))
	for i, s := range out.segs {
		if s == ".." {
			out.ok = false
		}
		if strings.ContainsRune(s, ':') && (i != 0 || lead != 0 || !isDriveSeg(s)) {
			out.ok = false // alternate data stream, device or drive-relative form
		}
		if fold && (strings.HasSuffix(s, ".") || strings.HasSuffix(s, " ")) {
			out.trims = true
		}
	}
	return out
}

func segEq(a, b string, fold bool) bool {
	if fold {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// insideRoot reports whether canon equals root or lies below it, comparing
// whole path parts (so `C:\work\repo-evil` is not inside `C:\work\repo`).
func insideRoot(root, canon string, fold bool) bool {
	r, c := splitPath(root, fold), splitPath(canon, fold)
	if !r.ok || !c.ok || r.trims || c.trims || !r.abs || !c.abs || r.kind != c.kind || len(r.segs) > len(c.segs) {
		return false
	}
	for i, s := range r.segs {
		if !segEq(s, c.segs[i], fold) {
			return false
		}
	}
	return true
}

func insideAny(roots []string, canon string, fold bool) bool {
	for _, r := range roots {
		if insideRoot(r, canon, fold) {
			return true
		}
	}
	return false
}

// protectedNames are file names (last part) that hold agent config;
// protectedDirs are folders whose whole content is protected. Writes to them
// are proposals (invariant 2).
var protectedNames = []string{".mcp.json", "CLAUDE.md", "AGENTS.md", "opencode.json", "GEMINI.md"}
var protectedDirs = []string{".git", ".claude", ".opencode"}

// isProtected reports whether a path has a .git or agent-config part. It
// compares without regard to case on every platform: refusing a harmless
// look-alike is safe, missing a real one is not.
func isProtected(canon string) bool {
	segs := strings.FieldsFunc(canon, isSep)
	for i, s := range segs {
		for _, d := range protectedDirs {
			if strings.EqualFold(s, d) {
				return true
			}
		}
		if i == len(segs)-1 {
			for _, n := range protectedNames {
				if strings.EqualFold(s, n) {
					return true
				}
			}
		}
	}
	return false
}

// globInside reports whether a glob stays inside the roots after cleaning:
// no "..", no "~", no separator inside a {a,b} group, and an absolute glob's
// literal prefix (the parts before the first wildcard) lies inside a root. A
// relative glob is taken relative to a root, which the shell sets as the
// working directory.
func globInside(roots []string, g string, fold bool) bool {
	if g == "" || strings.Contains(g, "..") || strings.HasPrefix(g, "~") {
		return false
	}
	depth := 0
	for _, c := range g {
		switch {
		case c == '{':
			depth++
		case c == '}' && depth > 0:
			depth--
		case isSep(c) && depth > 0:
			return false
		}
	}
	parts := splitPath(g, fold)
	if !parts.ok {
		return false
	}
	if !parts.abs {
		return true
	}
	var lit []string
	for _, s := range parts.segs {
		if strings.ContainsAny(s, "*?[{") {
			break
		}
		lit = append(lit, s)
	}
	prefix := strings.Repeat(`\`, parts.kind) + strings.Join(lit, `\`)
	return insideAny(roots, prefix, fold)
}
