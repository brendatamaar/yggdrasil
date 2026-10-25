package guard

import (
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Decide is the path and command decision for one tool call (rows D1 to D15
// of docs/design/guard.md). It is stateless and pure. Every doubt is a Deny.
func Decide(p Policy, c Call) Verdict {
	// D1
	if c.Tool.Validate() != nil || p.Mode.Validate() != nil {
		return Deny(ReasonUnknownTool)
	}
	// D2
	for _, ref := range c.Paths {
		if !ref.OK {
			return Deny(ReasonPathUnresolved)
		}
	}
	switch c.Tool {
	case ToolRead, ToolSearch:
		// D3, D4
		for _, ref := range c.Paths {
			if !insideAny(p.ReadRoots, ref.Canon, p.FoldCase) {
				return Deny(ReasonOutsideRepo)
			}
		}
		for _, g := range c.Globs {
			if !globInside(p.ReadRoots, g, p.FoldCase) {
				return Deny(ReasonOutsideRepo)
			}
		}
		return Allowed()
	case ToolWrite:
		// D5
		if p.Mode == core.ModeAsk {
			return Deny(ReasonAskIsReadOnly)
		}
		// D6
		for _, ref := range c.Paths {
			if isProtected(ref.Canon) {
				return Deny(ReasonProtectedPath)
			}
		}
		// D7, D8. A write that names no path cannot be checked: refused.
		if len(c.Paths) == 0 {
			return Deny(ReasonOutsideWorktree)
		}
		for _, ref := range c.Paths {
			if !insideAny(p.WriteRoots, ref.Canon, p.FoldCase) {
				return Deny(ReasonOutsideWorktree)
			}
		}
		return Allowed()
	case ToolExec:
		// D9
		if p.Mode == core.ModeAsk {
			return Deny(ReasonAskIsReadOnly)
		}
		// D10
		if c.ParseErr {
			return Deny(ReasonShellSyntax)
		}
		// D11, D12
		for _, r := range p.Allow {
			if ruleMatches(r, c.Argv, p.ReadRoots, p.FoldCase) {
				return Allowed()
			}
		}
		return Deny(ReasonNotAllowlisted)
	case ToolSubagent:
		// D13, D14
		if c.Isolation != "" {
			return Deny(ReasonAgentWorktreeBlocked)
		}
		return Allowed()
	}
	// D15
	return Deny(ReasonUnknownTool)
}

// programExts are the Windows wrapper extensions dropped before comparing a
// program name with a rule.
var programExts = []string{".exe", ".cmd", ".bat", ".com"}

func programBase(s string) string {
	l := strings.ToLower(s)
	for _, e := range programExts {
		if strings.HasSuffix(l, e) && len(s) > len(e) {
			return s[:len(s)-len(e)]
		}
	}
	return s
}

// ruleMatches reports whether argv is the rule's program with its fixed
// arguments, plus (for TailPaths) plain words or paths inside the read roots.
// A program given with a folder part never matches: only a bare name is
// resolved through the task's own search path.
func ruleMatches(r CommandRule, argv, readRoots []string, fold bool) bool {
	if len(argv) == 0 || r.Program == "" || strings.ContainsAny(argv[0], `/\`) {
		return false
	}
	if !segEq(programBase(argv[0]), r.Program, fold) {
		return false
	}
	rest := argv[1:]
	if len(rest) < len(r.Args) {
		return false
	}
	for i, a := range r.Args {
		if rest[i] != a {
			return false
		}
	}
	tail := rest[len(r.Args):]
	switch r.Tail {
	case TailNone:
		return len(tail) == 0
	case TailPaths:
		for _, a := range tail {
			if !tailArgOK(a, readRoots, fold) {
				return false
			}
		}
		return true
	}
	return false
}

// tailArgOK accepts a plain word, or a path inside the read roots. Anything
// that starts with "-" (a flag), "@" (a response file) or "~" is refused.
func tailArgOK(a string, readRoots []string, fold bool) bool {
	if a == "" || strings.HasPrefix(a, "-") || strings.HasPrefix(a, "@") || strings.HasPrefix(a, "~") {
		return false
	}
	parts := splitPath(a, fold)
	if !parts.ok {
		return false
	}
	if parts.abs {
		return insideAny(readRoots, a, fold)
	}
	return true
}
