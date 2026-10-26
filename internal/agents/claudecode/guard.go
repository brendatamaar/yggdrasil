package claudecode

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/core/guard"
	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// This file is the boundary between Claude Code's hook input and the pure
// decision in core/guard (docs/design/guard.md, "Boundary"). toCall parses a
// PreToolUse input into a guard.Call and resolves every path with the
// platform; guard.Decide judges it. Anything toCall cannot map is ToolOther,
// which Decide denies (D15), so a renamed field or a new tool fails closed.

// callBuilder holds what toCall needs from the machine.
type callBuilder struct {
	paths platform.PathResolver
	// program resolves argv[0] to an executable path without searching the
	// working directory; an error means "not found".
	program func(name string) (string, error)
}

// newCallBuilder returns the real builder. env is the environment the hook
// runs in; only the variables that decide where programs are found are used.
func newCallBuilder(getenv func(string) string) callBuilder {
	var env []string
	for _, k := range []string{"PATH", "PATHEXT", "SystemRoot"} {
		if v := getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	if env == nil {
		env = []string{} // Resolve refuses a nil environment
	}
	return callBuilder{
		paths: platform.RealPaths{},
		program: func(name string) (string, error) {
			return platform.Resolve(name, "", env)
		},
	}
}

type toolFields struct {
	FilePath     string          `json:"file_path"`
	NotebookPath string          `json:"notebook_path"`
	Path         string          `json:"path"`
	Pattern      string          `json:"pattern"`
	Glob         string          `json:"glob"`
	Command      string          `json:"command"`
	Isolation    json.RawMessage `json:"isolation"`
}

// toCall maps a hook input to a guard.Call. It never fails: what it cannot
// understand becomes ToolOther.
func (b callBuilder) toCall(in hookInput) guard.Call {
	other := guard.Call{Tool: guard.ToolOther, ToolName: safeName(in.ToolName)}
	var f toolFields
	if len(in.ToolInput) > 0 && string(in.ToolInput) != "null" {
		if err := json.Unmarshal(in.ToolInput, &f); err != nil {
			return other
		}
	}
	c := guard.Call{ToolName: safeName(in.ToolName)}
	switch in.ToolName {
	case "Read":
		if f.FilePath == "" {
			return other
		}
		c.Tool, c.Paths = guard.ToolRead, []guard.PathRef{b.ref(in.Cwd, f.FilePath)}
	case "Grep", "Glob":
		if in.ToolName == "Glob" && f.Pattern == "" {
			return other
		}
		c.Tool = guard.ToolSearch
		// A search without a folder searches the working directory.
		where := f.Path
		if where == "" {
			where = in.Cwd
		}
		c.Paths = []guard.PathRef{b.ref(in.Cwd, where)}
		if in.ToolName == "Glob" {
			c.Globs = []string{f.Pattern}
		} else if f.Glob != "" { // Grep's pattern is a regular expression, not a path
			c.Globs = []string{f.Glob}
		}
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		p := f.FilePath
		if in.ToolName == "NotebookEdit" {
			p = f.NotebookPath
		}
		if p == "" {
			return other
		}
		c.Tool, c.Paths = guard.ToolWrite, []guard.PathRef{b.ref(in.Cwd, p)}
	case "Bash", "PowerShell":
		if strings.TrimSpace(f.Command) == "" {
			return other
		}
		c.Tool = guard.ToolExec
		argv, err := guard.ParseCommandLine(f.Command)
		if err != nil {
			c.ParseErr = true
			return c
		}
		c.Argv = argv
		c.Program = b.programRef(argv[0])
	case "Agent", "Task":
		c.Tool = guard.ToolSubagent
		c.Isolation = isolationOf(f.Isolation)
	default:
		// WebFetch, WebSearch, MCP tools (until Yggdrasil's own MCP server is
		// wired, M1) and anything new.
		return other
	}
	return c
}

// isolationOf reads the isolation field: absent, null or "" is none; anything
// else, including a value of an unexpected type, counts as a request for it.
func isolationOf(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "unreadable"
	}
	return s
}

// ref resolves one path the agent wrote. A relative path is taken from the
// working directory the CLI reports; with none, it can't be judged.
func (b callBuilder) ref(cwd, raw string) guard.PathRef {
	ref := guard.PathRef{Raw: raw}
	p := raw
	if !filepath.IsAbs(p) && !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, `\`) &&
		!(len(p) >= 2 && p[1] == ':') && filepath.IsAbs(cwd) {
		p = filepath.Join(cwd, p)
	}
	canon, err := b.paths.ResolveRoot(p)
	if err != nil {
		return ref
	}
	ref.Canon, ref.OK = canon, true
	return ref
}

// programRef resolves argv[0]. A name with a path separator is never resolved
// (a relative one would be searched from the working directory); the allowlist
// matches bare names only, so it is denied either way.
func (b callBuilder) programRef(name string) *guard.PathRef {
	ref := &guard.PathRef{Raw: name}
	if strings.ContainsAny(name, `/\`) {
		return ref
	}
	exe, err := b.program(name)
	if err != nil {
		return ref
	}
	canon, err := b.paths.ResolveRoot(exe)
	if err != nil {
		return ref
	}
	ref.Canon, ref.OK = canon, true
	return ref
}

// ownAgentConfig names the first file or folder of the repo that configures
// Claude Code itself: settings (which can hold hooks), MCP servers and
// sub-agent definitions (whose headers can hold hooks). Such a repo could run
// its own commands in any mode, so Start refuses it unless the user trusted
// the repo (SF-09, invariant 10).
func ownAgentConfig(dir string, exists func(path string) bool) string {
	for _, name := range []string{
		filepath.Join(".claude", "settings.json"),
		filepath.Join(".claude", "settings.local.json"),
		".mcp.json",
		filepath.Join(".claude", "agents"),
	} {
		if exists(filepath.Join(dir, name)) {
			return filepath.ToSlash(name)
		}
	}
	return ""
}
