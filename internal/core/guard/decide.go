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
		// D4b: .git/config and .git/hooks hold what a repo would run.
		for _, ref := range c.Paths {
			if isGitInternal(ref.Canon) {
				return Deny(ReasonProtectedPath)
			}
		}
		for _, g := range c.Globs {
			if globReadsGitInternals(g) {
				return Deny(ReasonProtectedPath)
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
		// D11: the executable the shell resolved must be known and must not
		// be a file the agent could have written.
		if c.Program == nil {
			return Deny(ReasonNotAllowlisted)
		}
		if !c.Program.OK {
			return Deny(ReasonPathUnresolved)
		}
		if !programOutsideWriteRoots(p, c.Program.Canon) {
			return Deny(ReasonNotAllowlisted)
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

// programOutsideWriteRoots reports whether the resolved program is an
// absolute, ordinary path that is in no write root. A path we cannot reason
// about (relative, "..", a part Windows would trim) counts as inside.
func programOutsideWriteRoots(p Policy, canon string) bool {
	parts := splitPath(canon, true)
	if !parts.ok || !parts.abs || parts.trims {
		return false
	}
	return !insideAny(p.WriteRoots, canon, p.FoldCase)
}

// programMatches reports whether argv[0] is the rule's program: the bare name
// or the name plus ".exe", nothing else. ".bat", ".cmd" and ".com" are not
// dropped: they are other files, which the shell resolves from the bare name
// (platform resolution) if the user's own tools provide them.
func programMatches(arg0, program string, fold bool) bool {
	return segEq(arg0, program, fold) || segEq(arg0, program+".exe", fold)
}

// ruleMatches reports whether argv is the rule's program with its fixed
// arguments, plus (for TailPaths) plain words or paths inside the read roots.
// A program given with a folder part never matches: only a bare name is
// resolved through the task's own search path.
func ruleMatches(r CommandRule, argv, readRoots []string, fold bool) bool {
	if len(argv) == 0 || r.Program == "" || strings.ContainsAny(argv[0], `/\`) {
		return false
	}
	if !programMatches(argv[0], r.Program, fold) {
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

// tailArgOK accepts a plain word, or a path inside the read roots. The word
// is judged after the character allowlist of ParseCommandLine: a word with a
// backslash, expansion, glob or control character is refused whatever it
// looks like, because the shell may not run the word we are looking at.
// Anything that starts with "-" (a flag), "@" (a response file) or "~" is
// refused.
func tailArgOK(a string, readRoots []string, fold bool) bool {
	if a == "" || !wordAllowed(a) || strings.ContainsAny(a[:1], "-@~") {
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
