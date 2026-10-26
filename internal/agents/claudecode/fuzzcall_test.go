package claudecode

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/guard"
	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// lexInside is an independent containment check: split on both separators,
// refuse ".." and compare segments case-insensitively. It does not use
// platform.ResolveRoot or the core's matcher.
func lexInside(root, p string) bool {
	split := func(s string) []string {
		return strings.FieldsFunc(s, func(r rune) bool { return r == '/' || r == 92 })
	}
	rs, ps := split(root), split(p)
	for _, s := range ps {
		if s == ".." {
			return false
		}
	}
	if len(ps) < len(rs) {
		return false
	}
	for i := range rs {
		if !strings.EqualFold(rs[i], ps[i]) {
			return false
		}
	}
	return true
}

var knownTools = map[string]bool{
	"Read": true, "Grep": true, "Glob": true, "Edit": true, "Write": true, "MultiEdit": true,
	"NotebookEdit": true, "Bash": true, "PowerShell": true, "Agent": true, "Task": true,
}

func FuzzToCall(f *testing.F) {
	f.Add("Read", `{"file_path":"a.go"}`, "")
	f.Add("Write", `{"file_path":"..\\..\\x"}`, "")
	f.Add("Bash", `{"command":"go test ./..."}`, "")
	f.Add("Agent", `{"isolation":"worktree"}`, "")
	f.Add("Glob", `{"pattern":"../**"}`, "")
	f.Add("Read", `{"file_path":"C:foo"}`, "")
	f.Add("Weird\x00Tool", `{`, "relative")
	// One fixture for every iteration: a worktree, an empty PATH folder and a
	// Code-mode policy over the worktree.
	h := struct{ repo string }{f.TempDir()}
	bin := f.TempDir()
	b := newCallBuilder(func(k string) string {
		if k == "PATH" {
			return bin
		}
		return ""
	})
	canon, err := platform.ResolveRoot(h.repo)
	if err != nil {
		f.Fatal(err)
	}
	pol := guard.Policy{
		Mode: core.ModeCode, FoldCase: platform.FoldCase(), Enforcement: guard.EnforcementHook,
		ReadRoots: []string{canon}, WriteRoots: []string{canon},
	}
	f.Fuzz(func(t *testing.T, tool, input, cwdSuffix string) {
		cwd := h.repo
		if cwdSuffix != "" {
			cwd = filepath.Join(h.repo, cwdSuffix)
		}
		in := hookInput{ToolName: tool, ToolInput: json.RawMessage(input), Cwd: cwd}
		call := b.toCall(in) // must not panic
		v := guard.Decide(pol, call)
		if !knownTools[tool] {
			if call.Tool != guard.ToolOther || v.Allow {
				t.Fatalf("unknown tool %q: tool %q allow %v", tool, call.Tool, v.Allow)
			}
			return
		}
		if !json.Valid([]byte(input)) && input != "" {
			if call.Tool != guard.ToolOther || v.Allow {
				t.Fatalf("garbled input %q: tool %q allow %v", input, call.Tool, v.Allow)
			}
		}
		if !v.Allow {
			return
		}
		switch call.Tool {
		case guard.ToolRead, guard.ToolSearch, guard.ToolWrite:
			roots := pol.ReadRoots
			if call.Tool == guard.ToolWrite {
				roots = pol.WriteRoots
			}
			for _, ref := range call.Paths {
				ok := false
				for _, r := range roots {
					ok = ok || lexInside(r, ref.Canon)
				}
				if !ok {
					t.Fatalf("allowed %s of %q outside %q", call.Tool, ref.Canon, roots)
				}
				// The raw path, joined to the working directory and cleaned,
				// must also be inside the worktree (given as a temp path or as
				// its resolved form).
				raw := ref.Raw
				if !filepath.IsAbs(raw) {
					raw = filepath.Join(cwd, raw)
				}
				if !lexInside(h.repo, raw) && !lexInside(roots[0], raw) {
					t.Fatalf("allowed %s of raw %q, cwd %q, outside the worktree", call.Tool, ref.Raw, cwd)
				}
			}
		}
	})
}
