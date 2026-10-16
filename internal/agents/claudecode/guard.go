package claudecode

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// checkToolCall is Yggdrasil's own check of a tool call, run by the hook on
// PreToolUse. The CLI flags say the same things, but a flag is a request:
// enforcement happens here (SF-06, docs/AGENTS.md 6 invariants 5 and 6). It
// returns the reason a call is refused, or "" to let it through. The reason is
// shown to the agent, so it is plain language and holds no path.
func checkToolCall(cfg HookConfig, in hookInput) string {
	if len(cfg.AllowedTools) > 0 && !slices.Contains(cfg.AllowedTools, in.ToolName) {
		return fmt.Sprintf("%s isn't available in Ask mode: this task can only read files.", safeName(in.ToolName))
	}
	if cfg.AllowedDir == "" {
		return ""
	}
	return checkPaths(cfg.AllowedDir, in)
}

// pathFields are the tool input fields that name a file or folder.
var pathFields = []string{"file_path", "path", "notebook_path"}

// globFields are the fields that hold a glob; they must stay inside the repo
// too, so no ".." and no absolute pattern.
var globFields = []string{"pattern", "glob"}

func checkPaths(dir string, in hookInput) string {
	var input map[string]any
	if len(in.ToolInput) > 0 {
		if err := json.Unmarshal(in.ToolInput, &input); err != nil {
			return "The call could not be checked against the repo folder, so it is blocked."
		}
	}
	str := func(field string) string {
		s, _ := input[field].(string)
		return s
	}
	// A file read must name its file: without a path the call can't be checked.
	if in.ToolName == "Read" && str("file_path") == "" {
		return "A read must name a file inside the repo."
	}
	for _, f := range pathFields {
		if p := str(f); p != "" && !within(dir, p) {
			return "That path is outside the repo this task may read."
		}
	}
	// Grep's pattern is a regular expression, not a path.
	fields := globFields
	if in.ToolName == "Grep" {
		fields = []string{"glob"}
	}
	for _, f := range fields {
		if g := str(f); g != "" && escapesRepo(g) {
			return "That pattern reaches outside the repo this task may read."
		}
	}
	return ""
}

// escapesRepo reports whether a glob could name something outside the repo:
// a ".." segment, an absolute path, a drive or stream marker.
func escapesRepo(glob string) bool {
	if strings.Contains(glob, ":") || filepath.IsAbs(glob) || strings.HasPrefix(glob, "/") || strings.HasPrefix(glob, `\`) {
		return true
	}
	for seg := range strings.FieldsFuncSeq(glob, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return true
		}
	}
	return false
}

// within reports whether path p (absolute, or relative to dir) lies inside
// dir. Both are put in long form and have their symlinks and junctions
// resolved first, so neither a short name nor a link can lead out.
func within(dir, p string) bool {
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	rel, err := filepath.Rel(canonical(dir), canonical(p))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

// canonical returns p in long form with links resolved. A path that doesn't
// exist yet (a read of a missing file) is handled by resolving its deepest
// existing folder and appending the rest, so a short-name folder and its long
// form compare equal either way. ".." is collapsed lexically first, which is
// how Windows reads a path before it touches the disk.
func canonical(p string) string {
	p = filepath.Clean(p)
	var tail []string
	for cur := p; ; {
		if l, err := platform.LongPath(cur); err == nil {
			if r, err := filepath.EvalSymlinks(l); err == nil {
				slices.Reverse(tail)
				return filepath.Join(append([]string{r}, tail...)...)
			}
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
}

// ownAgentConfig names the first file or folder of the repo that configures
// Claude Code itself: settings (which can hold hooks), MCP servers and
// sub-agent definitions (whose headers can hold hooks). Such a repo could run
// its own commands even in Ask mode, so Yggdrasil refuses to run an agent in
// it until the user can trust a repo once (SF-09, invariant 10).
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
