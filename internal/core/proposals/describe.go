package proposals

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// canon builds the canonical encoding: every field is length-prefixed, so no
// two different actions can produce the same bytes.
type canon struct{ b strings.Builder }

func (c *canon) s(v string) {
	c.b.WriteString(strconv.Itoa(len(v)))
	c.b.WriteByte(':')
	c.b.WriteString(v)
	c.b.WriteByte(';')
}

func (c *canon) paths(ps []RelPath) {
	c.s(strconv.Itoa(len(ps)))
	for _, p := range ps {
		c.s(string(p))
	}
}

func (c *canon) target(t Target) {
	switch v := t.(type) {
	case InWorktree:
		c.s("worktree")
		c.s(string(v.ID))
	case MainCheckout:
		c.s("main")
		c.s(string(v.Repo))
	default:
		c.s("none")
		c.s("")
	}
}

func canonical(a Action) string {
	var c canon
	c.s("ygg-proposal-v1")
	c.s(string(a.Kind()))
	switch v := a.(type) {
	case Commit:
		c.target(v.Where)
		c.paths(v.Paths)
		c.s(v.Message)
		c.s(v.ExpectHead)
		c.s(v.ExpectDiffHash)
	case Push:
		c.target(v.Where)
		c.s(v.Branch)
		c.s(v.Remote)
		c.s(v.ExpectHead)
	case MergeRequest:
		c.target(v.Where)
		c.s(v.Branch)
		c.s(v.Remote)
		c.s(v.TargetBranch)
		c.s(v.Title)
		c.s(v.Description)
		c.s(v.ExpectHead)
	case AgentFile:
		c.target(v.Where)
		c.s(string(v.Path))
		c.s(v.Content)
		c.s(v.ExpectHash)
	case DeleteFiles:
		c.target(v.Where)
		c.paths(v.Paths)
	case Install:
		c.s(string(v.Repo))
		c.s(v.Step)
	}
	return c.b.String()
}

// Hash is what the user confirms: sha256 hex of the canonical action, a
// function of the action alone (P8). A nil action has no hash ("").
func Hash(a Action) string {
	if a == nil {
		return ""
	}
	sum := sha256.Sum256([]byte(canonical(a)))
	return hex.EncodeToString(sum[:])
}

func describeTarget(t Target) string {
	switch v := t.(type) {
	case InWorktree:
		return "worktree " + string(v.ID)
	case MainCheckout:
		return "the main checkout of repo " + string(v.Repo)
	}
	return "an unknown location"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func pathLines(ps []RelPath) []Line {
	out := make([]Line, 0, len(ps))
	for _, p := range ps {
		out = append(out, Line{Text: "  " + string(p)})
	}
	return out
}

// Describe is the exact words the confirmation dialog shows: every path, the
// branch, the remote, the message, the target and the expected state of the
// action (P9). The UI shows this next to, never instead of, the agent's
// summary.
func Describe(a Action) []Line {
	var out []Line
	add := func(f string, args ...any) { out = append(out, Line{Text: fmt.Sprintf(f, args...)}) }
	switch v := a.(type) {
	case Commit:
		add("Commit %s in %s.", plural(len(v.Paths), "file", "files"), describeTarget(v.Where))
		out = append(out, pathLines(v.Paths)...)
		add("Commit message: %s", v.Message)
		add("Only the files listed above are committed. This changes the repository history; it does not push.")
		add("Expected branch tip: %s. Diff checksum: %s.", orNone(v.ExpectHead), orNone(v.ExpectDiffHash))
	case Push:
		add("Push branch %s to remote %s from %s.", v.Branch, v.Remote, describeTarget(v.Where))
		add("This is a normal push. It is never forced, and it cannot delete a branch.")
		add("Expected branch tip: %s.", orNone(v.ExpectHead))
	case MergeRequest:
		add("Push branch %s to remote %s from %s and open a merge request into %s.", v.Branch, v.Remote, describeTarget(v.Where), v.TargetBranch)
		add("The push is part of this merge request. It is a normal push, never forced.")
		add("Title: %s", v.Title)
		add("Description: %s", orNone(v.Description))
		add("The description is one line: the merge request is opened through git push options, which cannot carry more.")
		add("Expected branch tip: %s.", orNone(v.ExpectHead))
	case AgentFile:
		add("Write the agent configuration file %s in %s.", v.Path, describeTarget(v.Where))
		if v.ExpectHash == "" {
			add("The file does not exist yet; it will be created.")
		} else {
			add("The file exists and will be replaced (current checksum %s).", v.ExpectHash)
		}
		add("New content (%s):", plural(len(v.Content), "byte", "bytes"))
		out = append(out, Line{Text: v.Content})
	case DeleteFiles:
		add("Move %s out of the way in %s.", plural(len(v.Paths), "file", "files"), describeTarget(v.Where))
		out = append(out, pathLines(v.Paths)...)
		add("Files are moved into the _to_delete folder, never deleted. You delete them yourself.")
	case Install:
		add("Run install step %s in the main checkout of repo %s.", v.Step, v.Repo)
		add("This can download and run code from the network (package scripts) inside the repo.")
	default:
		return nil
	}
	return out
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}
