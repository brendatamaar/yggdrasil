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

// asciiEqualFold compares a and b byte by byte, treating only A-Z and a-z as
// the same letter. Unicode folding (strings.EqualFold) would also match the
// Kelvin sign U+212A with "k" and the long s U+017F with "s", names Windows
// does not treat as equal to the ASCII ones. Non-ASCII bytes match exactly.
func asciiEqualFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		if asciiLower(a[i]) != asciiLower(b[i]) {
			return false
		}
	}
	return true
}

func asciiLower(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

func segEq(a, b string, fold bool) bool {
	if fold {
		return asciiEqualFold(a, b)
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
// are proposals (invariant 2). internal/core/proposals keeps the same lists
// (it may not import this package); a test there fails if they differ.
var protectedNames = []string{
	".mcp.json", "CLAUDE.md", "CLAUDE.local.md", "AGENTS.md", "opencode.json", "GEMINI.md", ".gitmodules",
}
var protectedDirs = []string{
	".git", ".claude", ".opencode", ".codex", ".gemini", ".cursor", ".husky", ".githooks",
}

// ProtectedDirNames returns the folders whose whole content is protected.
func ProtectedDirNames() []string { return append([]string(nil), protectedDirs...) }

// ProtectedFileNames returns the protected file names.
func ProtectedFileNames() []string { return append([]string(nil), protectedNames...) }

// isProtected reports whether a path has a .git or agent-config part. It
// compares without regard to ASCII case on every platform: refusing a
// harmless look-alike is safe, missing a real one is not.
func isProtected(canon string) bool {
	segs := strings.FieldsFunc(canon, isSep)
	for i, s := range segs {
		for _, d := range protectedDirs {
			if asciiEqualFold(s, d) {
				return true
			}
		}
		if i == len(segs)-1 {
			for _, n := range protectedNames {
				if asciiEqualFold(s, n) {
					return true
				}
			}
		}
	}
	return false
}

// readsGitInternals reports whether the parts name .git/config or anything
// under .git/hooks: files that can start programs, which a read must not
// expose to an agent that could then ask for them to be changed.
func readsGitInternals(segs []string) bool {
	for i := 0; i+1 < len(segs); i++ {
		if asciiEqualFold(segs[i], ".git") &&
			(asciiEqualFold(segs[i+1], "config") || asciiEqualFold(segs[i+1], "hooks")) {
			return true
		}
	}
	return false
}

func isGitInternal(canon string) bool { return readsGitInternals(strings.FieldsFunc(canon, isSep)) }

// globInside reports whether a glob stays inside the roots after cleaning:
// no "..", no "~", no separator inside a {a,b} group, no [..] class that can
// match "..", and an absolute glob's literal prefix (the parts before the
// first wildcard) lies inside a root. A relative glob is taken relative to a
// root, which the shell sets as the working directory.
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
	for _, s := range parts.segs {
		if strings.Contains(s, "[") && segmentMayMatchParent(s) {
			return false
		}
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

// globReadsGitInternals reports whether a glob names .git/config or
// .git/hooks. A wildcard in the .git or config part is not looked through:
// those globs stay allowed, and the shell's own file filter must skip them.
func globReadsGitInternals(g string) bool {
	return readsGitInternals(strings.FieldsFunc(g, isSep))
}

// glob atoms for segmentMayMatchParent.
type globAtom struct {
	kind byte // 'l' literal, '?' any one, '*' any run, '[' class
	lit  byte
	set  string // class body without brackets
	neg  bool
}

func parseGlobSegment(s string) []globAtom {
	var out []globAtom
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '?':
			out = append(out, globAtom{kind: '?'})
		case '*':
			out = append(out, globAtom{kind: '*'})
		case '[':
			j := i + 1
			neg := false
			if j < len(s) && (s[j] == '!' || s[j] == '^') {
				neg = true
				j++
			}
			start := j
			if j < len(s) && s[j] == ']' {
				j++
			}
			for j < len(s) && s[j] != ']' {
				j++
			}
			if j >= len(s) { // unterminated: a literal "["
				out = append(out, globAtom{kind: 'l', lit: c})
				continue
			}
			out = append(out, globAtom{kind: '[', set: s[start:j], neg: neg})
			i = j
		default:
			out = append(out, globAtom{kind: 'l', lit: c})
		}
	}
	return out
}

// classHasDot reports whether a class body contains "." literally or in a
// range.
func classHasDot(set string) bool {
	for i := 0; i < len(set); i++ {
		lo, hi := set[i], set[i]
		if i+2 < len(set) && set[i+1] == '-' {
			hi = set[i+2]
			i += 2
		}
		if lo <= '.' && '.' <= hi {
			return true
		}
	}
	return false
}

func (a globAtom) matchesDot() bool {
	switch a.kind {
	case 'l':
		return a.lit == '.'
	case '?', '*':
		return true
	case '[':
		return classHasDot(a.set) != a.neg
	}
	return false
}

// segmentMayMatchParent reports whether a glob part, read as a pattern, can
// match the two-character name "..". Wildcards are taken at their widest.
func segmentMayMatchParent(seg string) bool {
	atoms := parseGlobSegment(seg)
	// Try to consume exactly two dots, with "*" matching any run.
	var match func(i, n int) bool
	match = func(i, n int) bool {
		if i == len(atoms) {
			return n == 2
		}
		a := atoms[i]
		if a.kind == '*' {
			for k := 0; n+k <= 2; k++ {
				if match(i+1, n+k) {
					return true
				}
			}
			return false
		}
		return n < 2 && a.matchesDot() && match(i+1, n+1)
	}
	return match(0, 0)
}
