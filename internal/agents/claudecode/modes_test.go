package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/platform"
)

func TestBuildArgsGoldenForTheWritingModes(t *testing.T) {
	base := []string{
		"-p", "--output-format", "stream-json", "--verbose", "--forward-subagent-text",
		"--settings", "SETTINGS",
		"--setting-sources", "project",
		"--permission-mode", "dontAsk",
		"--allowedTools", "Read,Grep,Glob,Edit,Write,MultiEdit,Bash,PowerShell,Agent",
		"--disallowedTools", "NotebookEdit,WebFetch,WebSearch",
	}
	for _, mode := range []core.TaskMode{core.ModeCode, core.ModeCurrentBranch} {
		if got := buildArgs("SETTINGS", agents.StartOptions{Mode: mode}); !slices.Equal(got, base) {
			t.Errorf("%s:\n%q\nwant\n%q", mode, got, base)
		}
	}
	// Project mode adds the other worktrees; resume and the MCP file add theirs.
	o := agents.StartOptions{
		Mode: core.ModeProject, ExtraDirs: []string{"DIR2", "DIR3"}, ResumeSessionID: "sess-1",
		MCPConfigPath: "MCP", Model: "haiku", BudgetUSD: 1.5,
	}
	want := append(slices.Clone(base),
		"--add-dir", "DIR2", "--add-dir", "DIR3",
		"--mcp-config", "MCP", "--strict-mcp-config",
		"--model", "haiku", "--max-budget-usd", "1.5", "--resume", "sess-1")
	if got := buildArgs("SETTINGS", o); !slices.Equal(got, want) {
		t.Errorf("project:\n%q\nwant\n%q", got, want)
	}
	// A mode that is not a writing mode never gets the writing tools.
	for _, mode := range []core.TaskMode{core.ModeAsk, ""} {
		if got := buildArgs("SETTINGS", agents.StartOptions{Mode: mode}); !slices.Contains(got, "Read,Grep,Glob") || slices.Contains(got, base[12]) {
			t.Errorf("mode %q: args %q", mode, got)
		}
	}
	// No prompt, no separator, no characters a .cmd shim misreads.
	for _, a := range want {
		if a == "--" || strings.ContainsAny(a, "\"%&|<>^!\r\n") {
			t.Errorf("argument %q", a)
		}
	}
}

// A real run in each writing mode through the fake CLI: the folders, the policy
// the hook gets, and nothing secret on the command line.
func TestStartInTheWritingModes(t *testing.T) {
	const token = "ygg-mcp-token-S3CRET-0123456789"
	a := newAdapter(t, "capture")
	for _, mode := range []core.TaskMode{core.ModeCode, core.ModeCurrentBranch, core.ModeProject} {
		out := filepath.Join(t.TempDir(), "capture")
		o := startOpts(t, "capture", map[string]string{fakeOut: out})
		o.Mode = mode
		dirs := []string{o.Dir}
		if mode == core.ModeProject {
			second := t.TempDir()
			o.ExtraDirs, dirs = []string{second}, append(dirs, second)
		}
		o.Policy = policyFor(t, mode, dirs...)
		mcp, err := a.WriteMCPConfig(t.TempDir(), "http://127.0.0.1:7777/mcp", token)
		if err != nil {
			t.Fatal(err)
		}
		o.MCPConfigPath = mcp
		res, err := agents.Execute(context.Background(), a, o, &collectSink{})
		if err != nil || res.Status != agents.RunCompleted {
			t.Fatalf("%s: %+v, %v", mode, res, err)
		}
		argv := read(t, out+".args")
		if strings.Contains(argv, token) || strings.Contains(read(t, out+".stdin"), token) || strings.Contains(read(t, out+".settings"), token) {
			t.Errorf("%s: the MCP token reached the command line, stdin or settings", mode)
		}
		args := strings.Split(argv, "\x00")
		if i := slices.Index(args, "--mcp-config"); i < 0 || args[i+1] != mcp || !slices.Contains(args, "--strict-mcp-config") {
			t.Errorf("%s: MCP flags missing in %q", mode, args)
		}
		if i := slices.Index(args, "--allowedTools"); i < 0 || !strings.Contains(args[i+1], "Edit") || strings.Contains(args[i+1], "WebFetch") {
			t.Errorf("%s: allowed tools = %q", mode, args)
		}
		// The only paths on the command line: settings, MCP file, extra folders.
		for i, arg := range args {
			if !filepath.IsAbs(arg) {
				continue
			}
			prev := args[i-1]
			if prev != "--settings" && prev != "--mcp-config" && prev != "--add-dir" {
				t.Errorf("%s: path %q on the command line after %q", mode, arg, prev)
			}
		}
		long, _ := platform.LongPath(dirs[0])
		if cwd := read(t, out+".cwd"); !strings.EqualFold(filepath.Clean(cwd), filepath.Clean(long)) {
			t.Errorf("%s: working directory %q, want %q", mode, cwd, long)
		}
		if mode == core.ModeProject {
			second, _ := platform.LongPath(dirs[1])
			if i := slices.Index(args, "--add-dir"); i < 0 || !strings.EqualFold(args[i+1], second) {
				t.Errorf("project: --add-dir missing in %q", args)
			}
		}
		// The hook got this mode's policy.
		if !strings.Contains(read(t, out+".policy"), `"mode":"`+string(mode)+`"`) {
			t.Errorf("%s: policy = %s", mode, read(t, out+".policy"))
		}
	}
}

func TestStartRefusesAPolicyThatDoesNotCoverTheFolders(t *testing.T) {
	a := newAdapter(t, "stream")
	o := startOpts(t, "stream", nil)
	o.Mode = core.ModeProject
	second := t.TempDir()
	o.ExtraDirs = []string{second}
	o.Policy = policyFor(t, core.ModeProject, o.Dir) // lacks the second folder
	if r, err := a.Start(context.Background(), o); err == nil {
		t.Error("a policy that does not cover every folder was accepted")
		for range r.Events() { //nolint:revive // drain so the run ends
		}
	} else {
		var ue *UserError
		if !errors.As(err, &ue) || strings.Contains(ue.Message, second) {
			t.Errorf("error = %v", err)
		}
	}
	// A writing mode whose write roots don't hold the folder.
	o = startOpts(t, "stream", nil)
	o.Mode = core.ModeCode
	p := policyFor(t, core.ModeCode, o.Dir)
	p.WriteRoots = []string{p.WriteRoots[0] + "-other"}
	o.Policy = p
	if r, err := a.Start(context.Background(), o); err == nil {
		t.Error("a policy whose write roots miss the folder was accepted")
		for range r.Events() { //nolint:revive // drain so the run ends
		}
	}
}

func TestWriteMCPConfig(t *testing.T) {
	a := New(Options{})
	dir := t.TempDir()
	const token = "tok-S3CRET-abcdef"
	path, err := a.WriteMCPConfig(dir, "http://127.0.0.1:8123/mcp", token)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != dir {
		t.Errorf("path = %q", path)
	}
	var got struct {
		Servers map[string]struct {
			Type    string            `json:"type"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(read(t, path)), &got); err != nil {
		t.Fatal(err)
	}
	s, ok := got.Servers["ygg"]
	if len(got.Servers) != 1 || !ok || s.Type != "http" || s.URL != "http://127.0.0.1:8123/mcp" || s.Headers["Authorization"] != "Bearer "+token {
		t.Errorf("config = %+v", got)
	}
	// Owner-only: no more permissive than a file created with 0600 here.
	ref := filepath.Join(t.TempDir(), "ref")
	if err := os.WriteFile(ref, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	rst, _ := os.Stat(ref)
	if st.Mode().Perm() != rst.Mode().Perm() {
		t.Errorf("permissions = %v, want %v", st.Mode().Perm(), rst.Mode().Perm())
	}
	// Writing again over a file with wide permissions narrows them.
	if _, err := a.WriteMCPConfig(dir, "http://127.0.0.1:8123/mcp", token); err != nil {
		t.Errorf("rewrite: %v", err)
	}

	// Bad input is refused, and no message repeats the token.
	for name, c := range map[string][3]string{
		"empty url":            {dir, "", token},
		"not http":             {dir, "file:///x", token},
		"url with a user":      {dir, "http://user:pw@127.0.0.1/mcp", token},
		"no host":              {dir, "http:///mcp", token},
		"empty token":          {dir, "http://127.0.0.1/mcp", ""},
		"token with a newline": {dir, "http://127.0.0.1/mcp", "a\nb" + token},
		"token with a quote":   {dir, "http://127.0.0.1/mcp", "a\"b" + token},
		"relative dir":         {"rel", "http://127.0.0.1/mcp", token},
		"missing dir":          {filepath.Join(dir, "no", "such"), "http://127.0.0.1/mcp", token},
	} {
		if _, err := a.WriteMCPConfig(c[0], c[1], c[2]); err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error(), token) {
			t.Errorf("%s: the error repeats the token: %v", name, err)
		}
	}
	var _ agents.MCPConfigurer = a
}
