package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/guard"
	"github.com/brendatamaar/yggdrasil/internal/platform"
)

func inputOf(t *testing.T, tool, cwd string, input any) hookInput {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return hookInput{ToolName: tool, ToolInput: raw, Cwd: cwd}
}

func TestToCallMapsEveryTool(t *testing.T) {
	h := newHookEnv(t, 0, 0)
	b := newCallBuilder(h.getenv)
	file := filepath.Join(h.repo, "a.go")
	cases := []struct {
		name  string
		tool  string
		input any
		want  guard.Tool
		paths int
		globs int
	}{
		{"read", "Read", map[string]any{"file_path": file}, guard.ToolRead, 1, 0},
		{"grep without a folder searches the cwd", "Grep", map[string]any{"pattern": "x"}, guard.ToolSearch, 1, 0},
		{"grep with a glob filter", "Grep", map[string]any{"pattern": "x", "path": h.repo, "glob": "*.go"}, guard.ToolSearch, 1, 1},
		{"glob", "Glob", map[string]any{"pattern": "**/*.go"}, guard.ToolSearch, 1, 1},
		{"edit", "Edit", map[string]any{"file_path": file}, guard.ToolWrite, 1, 0},
		{"write", "Write", map[string]any{"file_path": file}, guard.ToolWrite, 1, 0},
		{"multiedit", "MultiEdit", map[string]any{"file_path": file}, guard.ToolWrite, 1, 0},
		{"notebook edit", "NotebookEdit", map[string]any{"notebook_path": file}, guard.ToolWrite, 1, 0},
		{"bash", "Bash", map[string]any{"command": "go test ./..."}, guard.ToolExec, 0, 0},
		{"powershell", "PowerShell", map[string]any{"command": "go vet ./..."}, guard.ToolExec, 0, 0},
		{"agent", "Agent", map[string]any{"description": "d", "prompt": "p"}, guard.ToolSubagent, 0, 0},
		{"task", "Task", map[string]any{"description": "d"}, guard.ToolSubagent, 0, 0},
		// Everything else is Other, which the core denies.
		{"web fetch", "WebFetch", map[string]any{"url": "http://example.test"}, guard.ToolOther, 0, 0},
		{"web search", "WebSearch", map[string]any{"query": "x"}, guard.ToolOther, 0, 0},
		{"our own mcp tool, not wired yet", "mcp__ygg__propose_commit", map[string]any{}, guard.ToolOther, 0, 0},
		{"another server's mcp tool", "mcp__other__run", map[string]any{}, guard.ToolOther, 0, 0},
		{"unknown tool", "SomethingNew", map[string]any{}, guard.ToolOther, 0, 0},
		{"lower case read is not Read", "read", map[string]any{"file_path": file}, guard.ToolOther, 0, 0},
		{"read without a path", "Read", map[string]any{}, guard.ToolOther, 0, 0},
		{"edit without a path", "Edit", map[string]any{"old_string": "x"}, guard.ToolOther, 0, 0},
		{"notebook edit with file_path only", "NotebookEdit", map[string]any{"file_path": file}, guard.ToolOther, 0, 0},
		{"glob without a pattern", "Glob", map[string]any{}, guard.ToolOther, 0, 0},
		{"bash without a command", "Bash", map[string]any{"command": "  "}, guard.ToolOther, 0, 0},
		{"path of the wrong type", "Read", map[string]any{"file_path": 12}, guard.ToolOther, 0, 0},
		{"empty tool name", "", map[string]any{}, guard.ToolOther, 0, 0},
	}
	for _, c := range cases {
		got := b.toCall(inputOf(t, c.tool, h.repo, c.input))
		if got.Tool != c.want || len(got.Paths) != c.paths || len(got.Globs) != c.globs {
			t.Errorf("%s: tool %q, %d paths, %d globs; want %q, %d, %d", c.name, got.Tool, len(got.Paths), len(got.Globs), c.want, c.paths, c.globs)
		}
		for _, p := range got.Paths {
			if !p.OK {
				t.Errorf("%s: path %q was not resolved", c.name, p.Raw)
			}
		}
	}
	// Malformed JSON, and the wrong JSON type, in the tool input.
	for _, raw := range []string{`{`, `"text"`, `[1]`, `12`} {
		got := b.toCall(hookInput{ToolName: "Read", ToolInput: json.RawMessage(raw), Cwd: h.repo})
		if got.Tool != guard.ToolOther {
			t.Errorf("tool input %s: tool %q, want Other", raw, got.Tool)
		}
	}
	// A call with no input at all.
	if got := b.toCall(hookInput{ToolName: "Read"}); got.Tool != guard.ToolOther {
		t.Errorf("no input: tool %q, want Other", got.Tool)
	}
	// A sub-agent with no input is a plain one; its isolation must be read.
	for iso, want := range map[string]string{`"worktree"`: "worktree", `"remote"`: "remote", `""`: "", `null`: "", `true`: "unreadable", `{"a":1}`: "unreadable"} {
		in := hookInput{ToolName: "Agent", ToolInput: json.RawMessage(`{"isolation":` + iso + `}`)}
		if got := b.toCall(in); got.Tool != guard.ToolSubagent || got.Isolation != want {
			t.Errorf("isolation %s: tool %q isolation %q, want %q", iso, got.Tool, got.Isolation, want)
		}
	}
}

func TestToCallResolvesPathsTheWayTheGuardNeeds(t *testing.T) {
	h := newHookEnv(t, 0, 0)
	b := newCallBuilder(h.getenv)
	canonRepo, err := platform.ResolveRoot(h.repo)
	if err != nil {
		t.Fatal(err)
	}
	ref := func(cwd, p string) guard.PathRef {
		c := b.toCall(inputOf(t, "Read", cwd, map[string]any{"file_path": p}))
		if len(c.Paths) != 1 {
			t.Fatalf("%q: %d paths", p, len(c.Paths))
		}
		return c.Paths[0]
	}

	// A relative path is taken from the CLI's working directory; with none, it
	// can't be judged.
	if got := ref(h.repo, "src"+string(filepath.Separator)+"a.go"); !got.OK || got.Canon != filepath.Join(canonRepo, "src", "a.go") {
		t.Errorf("relative path = %+v", got)
	}
	if got := ref("", "a.go"); got.OK {
		t.Errorf("a relative path with no working directory was resolved: %+v", got)
	}
	if got := ref("relative", "a.go"); got.OK {
		t.Errorf("a relative path from a relative working directory was resolved: %+v", got)
	}
	// "..": collapsed, so it leaves the root and the core denies it (D3/D4).
	if got := ref(h.repo, filepath.Join("..", "outside.txt")); got.OK {
		if v := guard.Decide(policyFor(t, core.ModeAsk, h.repo), b.toCall(inputOf(t, "Read", h.repo, map[string]any{"file_path": filepath.Join("..", "outside.txt")}))); v.Allow {
			t.Error("a read through .. was allowed")
		}
	}
	// Streams, a bare drive-relative form and empty input are refused or unresolved.
	for _, p := range []string{h.repo + ":stream", "C:relative.txt", "/", ""} {
		if p == "" {
			continue // an empty path is a call without a path: Other
		}
		if got := ref(h.repo, p); got.OK && !strings.HasPrefix(got.Canon, canonRepo) {
			// resolved to something outside: the core denies it; just record it
			if v := guard.Decide(policyFor(t, core.ModeAsk, h.repo), guard.Call{Tool: guard.ToolRead, Paths: []guard.PathRef{got}}); v.Allow {
				t.Errorf("%q resolved to %q and was allowed", p, got.Canon)
			}
		}
	}
	// A device name is refused where the platform refuses it.
	dev := filepath.Join(h.repo, "CON")
	if _, err := platform.CleanRootPath(dev); err == nil {
		t.Log("device names are not special on this platform")
	} else if got := ref(h.repo, dev); got.OK {
		t.Errorf("device name resolved: %+v", got)
	}
}

func TestToCallResolvesShortNamesAndLinks(t *testing.T) {
	h := newHookEnv(t, 0, 0)
	b := newCallBuilder(h.getenv)
	ask := policyFor(t, core.ModeAsk, h.repo)

	// 8.3: the short spelling of a folder in the worktree is the long one.
	long := filepath.Join(h.repo, "A Long Directory Name")
	if err := os.MkdirAll(long, 0o700); err != nil {
		t.Fatal(err)
	}
	if short := shortName(t, long); short != "" {
		c := b.toCall(inputOf(t, "Read", h.repo, map[string]any{"file_path": filepath.Join(short, "f.txt")}))
		canonLong, _ := platform.ResolveRoot(long)
		if !c.Paths[0].OK || c.Paths[0].Canon != filepath.Join(canonLong, "f.txt") {
			t.Errorf("8.3 path = %+v, want the long form", c.Paths[0])
		}
		if v := guard.Decide(ask, c); !v.Allow {
			t.Errorf("a read through the 8.3 spelling of a folder inside the worktree was denied: %v", v.Reason)
		}
	}

	// A link inside the worktree that leads out of it.
	outside := t.TempDir()
	link := filepath.Join(h.repo, "link")
	makeDirLink(t, link, outside)
	c := b.toCall(inputOf(t, "Read", h.repo, map[string]any{"file_path": filepath.Join(link, "secret.txt")}))
	if v := guard.Decide(ask, c); v.Allow {
		t.Errorf("a read through a link out of the worktree was allowed: %+v", c.Paths)
	}
	w := b.toCall(inputOf(t, "Write", h.repo, map[string]any{"file_path": filepath.Join(link, "x.txt")}))
	if v := guard.Decide(policyFor(t, core.ModeCode, h.repo), w); v.Allow {
		t.Error("a write through a link out of the worktree was allowed")
	}
}

func TestToCallResolvesTheProgramWithoutSearchingTheWorktree(t *testing.T) {
	h := newHookEnv(t, 0, 0)
	code := policyFor(t, core.ModeCode, h.repo)
	exec := func(getenv func(string) string, cmd string) (guard.Call, guard.Verdict) {
		c := newCallBuilder(getenv).toCall(inputOf(t, "Bash", h.repo, map[string]any{"command": cmd}))
		return c, guard.Decide(code, c)
	}

	// A program found on the PATH, outside the worktree.
	c, v := exec(h.getenv, "go test ./...")
	if c.Tool != guard.ToolExec || c.Program == nil || !c.Program.OK || !v.Allow {
		t.Errorf("go test ./...: call %+v, verdict %+v", c, v)
	}
	// The same name, but the PATH only holds the worktree: the agent could have
	// written that file, so it is not run (D11).
	for _, name := range []string{"go", "go.exe"} {
		if err := os.WriteFile(filepath.Join(h.repo, name), []byte("x"), 0o755); err != nil { //nolint:gosec // a fake program in a temp folder
			t.Fatal(err)
		}
	}
	inRepo := func(k string) string {
		if k == "PATH" {
			return h.repo
		}
		return h.getenv(k)
	}
	if _, v := exec(inRepo, "go test ./..."); v.Allow || v.Reason != guard.ReasonNotAllowlisted {
		t.Errorf("a program inside the worktree: verdict %+v", v)
	}
	// Not found at all, a relative PATH entry, and a name with a separator.
	none := func(k string) string {
		if k == "PATH" {
			return "."
		}
		return ""
	}
	if c, v := exec(none, "go test ./..."); v.Allow || c.Program == nil || c.Program.OK {
		t.Errorf("a program found only through the current directory: call %+v, verdict %+v", c, v)
	}
	for _, cmd := range []string{"./go test ./...", "bin/go test ./...", "go.exe/../go test ./..."} {
		if _, v := exec(h.getenv, cmd); v.Allow {
			t.Errorf("%q was allowed", cmd)
		}
	}
	// Allowlist: exact leading arguments only.
	for cmd, want := range map[string]bool{
		"go test ./...":                    true,
		"go vet ./...":                     true,
		"go test -exec=evil ./...":         false,
		"go test ./... extra":              false,
		"go run main.go":                   false,
		"rm -rf x":                         false,
		"curl http://example.test":         false,
		"go test ./... && curl x":          false,
		"go test ./... | tee out":          false,
		"go test ./... > out":              false,
		"go test $(curl x)":                false,
		"go test `curl x`":                 false,
		"go test ./...\ncurl x":            false,
		"go test %PATH%":                   false,
		"":                                 false,
		"  ":                               false,
		`"go" test ./...`:                  true,
		"go  test   ./...":                 true,
		"go test ./... ; curl x":           false,
		"cmd /c go test":                   false,
		"powershell -c go":                 false,
		"go test ./... & curl x":           false,
		"GOFLAGS=-exec=evil go test ./...": false,
	} {
		if cmd == "" || cmd == "  " {
			// Blank commands become Other before the allowlist.
			if got := newCallBuilder(h.getenv).toCall(inputOf(t, "Bash", h.repo, map[string]any{"command": cmd})); got.Tool != guard.ToolOther {
				t.Errorf("blank command: tool %q", got.Tool)
			}
			continue
		}
		if _, v := exec(h.getenv, cmd); v.Allow != want {
			t.Errorf("%q: allowed = %v, want %v (%v)", cmd, v.Allow, want, v.Reason)
		}
	}
}
