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
	"time"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/guard"
)

func drain(r agents.Run) {
	for range r.Events() { //nolint:revive // drain so the run ends
	}
	_ = r.Wait()
}

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in   string
		want cliVersion
		ok   bool
	}{
		{"2.1.299 (Claude Code)", cliVersion{2, 1, 299}, true},
		{"2.1.299 (Claude Code)\r\n", cliVersion{2, 1, 299}, true},
		{"  2.1.295", cliVersion{2, 1, 295}, true},
		{"10.0.0 (Claude Code)", cliVersion{10, 0, 0}, true},
		{"", cliVersion{}, false},
		{"Claude Code 2.1.299", cliVersion{}, false},
		{"2.1", cliVersion{}, false},
		{"2.1.x", cliVersion{}, false},
		{"-2.1.299", cliVersion{}, false},
		{"2.1.99999999999999999999", cliVersion{}, false},
	}
	for _, c := range cases {
		got, ok := parseVersion(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseVersion(%q) = %v, %v; want %v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
	for in, tooOld := range map[string]bool{"2.1.294": true, "2.0.999": true, "1.9.999": true, "2.1.295": false, "2.2.0": false, "3.0.0": false} {
		v, _ := parseVersion(in)
		if v.older(minVersion) != tooOld {
			t.Errorf("%s older than minimum = %v, want %v", in, !tooOld, tooOld)
		}
	}
}

func FuzzParseVersion(f *testing.F) {
	for _, s := range []string{"2.1.299 (Claude Code)", "", "0.0.0", "1.2", "9999999999999999999.1.1"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		v, ok := parseVersion(s)
		if ok && (v.major < 0 || v.minor < 0 || v.patch < 0) {
			t.Fatalf("negative part in %v from %q", v, s)
		}
		if !ok && v != (cliVersion{}) {
			t.Fatalf("not ok but %v", v)
		}
	})
}

func TestOldCLIIsRefused(t *testing.T) {
	var ue *UserError
	a := New(Options{Binary: testExe(t), HookExecutable: testExe(t), TempDir: t.TempDir(),
		CheckEnv: map[string]string{fakeEnv: "version", "YGG_FAKE_VERSION": "2.1.294 (Claude Code)"}})
	if err := a.Check(context.Background()); !errors.As(err, &ue) || !strings.Contains(ue.Message, "update") {
		t.Errorf("Check with an old CLI = %v", err)
	}
	garbled := New(Options{Binary: testExe(t), CheckEnv: map[string]string{fakeEnv: "version", "YGG_FAKE_VERSION": "hello"}})
	if err := garbled.Check(context.Background()); !errors.As(err, &ue) {
		t.Errorf("Check with an unreadable version = %v", err)
	}

	o := startOpts(t, "stream", map[string]string{"YGG_FAKE_VERSION": "2.1.200 (Claude Code)"})
	r, err := newAdapter(t, "stream").Start(context.Background(), o)
	if err == nil {
		drain(r)
		t.Fatal("Start accepted an old CLI")
	}
	if !errors.As(err, &ue) || !strings.Contains(ue.Message, "update") {
		t.Errorf("Start error = %v", err)
	}
}

func TestStartRefusesMissingHookExecutable(t *testing.T) {
	a := New(Options{Binary: testExe(t), HookExecutable: filepath.Join(t.TempDir(), "gone-ygg.exe"), TempDir: t.TempDir()})
	r, err := a.Start(context.Background(), startOpts(t, "stream", nil))
	if err == nil {
		drain(r)
		t.Fatal("Start accepted a hook executable that does not exist")
	}
}

func TestStartAlwaysPinsSettingsAndMCP(t *testing.T) {
	a := newAdapter(t, "capture")
	out := filepath.Join(t.TempDir(), "cap")
	o := startOpts(t, "capture", map[string]string{fakeOut: out})
	res, err := agents.Execute(context.Background(), a, o, &collectSink{})
	if err != nil || res.Status != agents.RunCompleted {
		t.Fatalf("%+v %v", res, err)
	}
	argv := strings.Split(read(t, out+".args"), "\x00")
	i := slices.Index(argv, "--setting-sources")
	if i < 0 || argv[i+1] != "" {
		t.Errorf("--setting-sources not empty in %q", argv)
	}
	if !slices.Contains(argv, "--strict-mcp-config") {
		t.Errorf("no --strict-mcp-config in %q", argv)
	}
	var f struct {
		Servers map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(read(t, out+".mcp")), &f); err != nil || f.Servers == nil || len(f.Servers) != 0 {
		t.Errorf("the default MCP file = %q, %v", read(t, out+".mcp"), err)
	}
	if !strings.Contains(read(t, out+".settings"), `"disableAllHooks": false`) {
		t.Errorf("settings = %s", read(t, out+".settings"))
	}
	if _, err := os.Stat(read(t, out+".mcppath")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the MCP file outlived the run: %v", err)
	}
}

func TestPolicyMustNotBeWiderThanTheFolders(t *testing.T) {
	a := newAdapter(t, "stream")
	otherWT := t.TempDir() // another worktree under the data dir
	parent := func(dir string) string { return filepath.Dir(dir) }
	cases := map[string]func(o *agents.StartOptions){
		"code: write root is another worktree": func(o *agents.StartOptions) {
			o.Mode = core.ModeCode
			o.Policy = policyFor(t, core.ModeCode, o.Dir)
			o.Policy.WriteRoots = append(o.Policy.WriteRoots, policyFor(t, core.ModeCode, otherWT).WriteRoots...)
		},
		"code: write root is the folder above": func(o *agents.StartOptions) {
			o.Mode = core.ModeCode
			o.Policy = policyFor(t, core.ModeCode, o.Dir)
			o.Policy.WriteRoots = policyFor(t, core.ModeCode, parent(o.Dir)).WriteRoots
		},
		"code: read root is wider": func(o *agents.StartOptions) {
			o.Mode = core.ModeCode
			o.Policy = policyFor(t, core.ModeCode, o.Dir)
			o.Policy.ReadRoots = append(o.Policy.ReadRoots, policyFor(t, core.ModeCode, otherWT).ReadRoots...)
		},
		"ask: read root is wider": func(o *agents.StartOptions) {
			o.Policy = policyFor(t, core.ModeAsk, o.Dir, otherWT)
		},
		"project: write root outside the given folders": func(o *agents.StartOptions) {
			o.Mode = core.ModeProject
			o.ExtraDirs = []string{t.TempDir()}
			o.Policy = policyFor(t, core.ModeProject, o.Dir, o.ExtraDirs[0], otherWT)
		},
	}
	for name, mutate := range cases {
		o := startOpts(t, "stream", nil)
		mutate(&o)
		r, err := a.Start(context.Background(), o)
		if err == nil {
			drain(r)
			t.Errorf("%s: Start succeeded", name)
		}
	}
	// The matching policy is fine, in every writing mode.
	o := startOpts(t, "stream", nil)
	o.Mode = core.ModeCode
	o.Policy = policyFor(t, core.ModeCode, o.Dir)
	r, err := a.Start(context.Background(), o)
	if err != nil {
		t.Fatalf("a matching policy was refused: %v", err)
	}
	drain(r)
}

func TestDenyReporterListsHookDenials(t *testing.T) {
	a := newAdapter(t, "denies")
	o := startOpts(t, "denies", nil)
	r, err := a.Start(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	drain(r)
	dr, ok := r.(agents.DenyReporter)
	if !ok {
		t.Fatal("the run does not report denials")
	}
	got := dr.Denies()
	if len(got) != 2 || got[0].Tool != "Bash" || got[1].Tool != "Write" ||
		got[0].Reason != guard.ReasonNotAllowlisted.Message() || got[1].Reason != guard.ReasonOutsideWorktree.Message() ||
		got[0].At.IsZero() {
		t.Errorf("denies = %+v", got)
	}
}

func TestDeniesFromLogIgnoresOtherRecords(t *testing.T) {
	log := filepath.Join(t.TempDir(), "hook.log")
	for _, r := range []hookRecord{
		{Time: "2026-01-02T03:04:05Z", Event: eventPreToolUse, Tool: "Read", Decision: decisionAllow},
		{Time: "2026-01-02T03:04:06Z", Event: eventPreToolUse, Tool: "Bash\x00\n" + strings.Repeat("x", 200), Decision: decisionDeny, Reason: "no"},
		{Time: "garbage", Event: eventSubagentStop, Decision: decisionLog},
	} {
		if err := appendRecord(log, r); err != nil {
			t.Fatal(err)
		}
	}
	got := deniesFromLog(log)
	if len(got) != 1 || len(got[0].Tool) > 64 || strings.ContainsAny(got[0].Tool, "\x00\n") || got[0].At.IsZero() {
		t.Errorf("denies = %+v", got)
	}
	if deniesFromLog(filepath.Join(t.TempDir(), "none")) != nil {
		t.Error("a missing log has denials")
	}
}

func TestMCPConfigIsCopiedIntoTheRunFolder(t *testing.T) {
	a := newAdapter(t, "capture")
	callerDir := t.TempDir()
	path, err := a.WriteMCPConfig(callerDir, "http://127.0.0.1:8123/mcp", "tok-S3CRET-abc")
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "cap")
	o := startOpts(t, "capture", map[string]string{fakeOut: out})
	o.MCPConfigPath = path
	if res, err := agents.Execute(context.Background(), a, o, &collectSink{}); err != nil || res.Status != agents.RunCompleted {
		t.Fatalf("%+v %v", res, err)
	}
	used := read(t, out+".mcppath")
	if used == path || filepath.Dir(used) == callerDir {
		t.Errorf("the CLI got the caller's file %q", used)
	}
	if !strings.Contains(read(t, out+".mcp"), "tok-S3CRET-abc") {
		t.Errorf("the copy lacks the server: %s", read(t, out+".mcp"))
	}
	if _, err := os.Stat(used); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the copy with the token outlived the run: %v", err)
	}

	// A file inside a folder the agent can read or write is refused.
	o = startOpts(t, "stream", nil)
	o.Mode = core.ModeCode
	o.Policy = policyFor(t, core.ModeCode, o.Dir)
	inside, err := a.WriteMCPConfig(o.Dir, "http://127.0.0.1:8123/mcp", "tok-S3CRET-abc")
	if err != nil {
		t.Fatal(err)
	}
	o.MCPConfigPath = inside
	r, err := newAdapter(t, "stream").Start(context.Background(), o)
	if err == nil {
		drain(r)
		t.Fatal("an MCP file inside the worktree was accepted")
	}
	if strings.Contains(err.Error(), "tok-S3CRET") {
		t.Error("the error repeats the token")
	}
}

func TestDriveRelativePathsAreDenied(t *testing.T) {
	h := newHookEnv(t, 0, 0)
	b := newCallBuilder(h.getenv)
	for _, p := range []string{"C:foo.txt", "c:foo", "Z:", `C:..\x`} {
		c := b.toCall(inputOf(t, "Read", h.repo, map[string]any{"file_path": p}))
		if len(c.Paths) != 1 || c.Paths[0].OK {
			t.Errorf("%q: path = %+v", p, c.Paths)
		}
		if guard.Decide(policyFor(t, core.ModeCode, h.repo), c).Allow {
			t.Errorf("%q was allowed", p)
		}
	}
}

func TestIsolationValueIsNotEchoed(t *testing.T) {
	h := newHookEnv(t, 2, 4)
	in := agentCall(map[string]any{"tool_input": map[string]any{"isolation": "SECRET-ISOLATION-VALUE-" + strings.Repeat("z", 500)}})
	code, stderr := h.call("PreToolUse", in)
	if code != 2 || strings.Contains(stderr, "SECRET-ISOLATION") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
	// Also when the guard lets the call reach the hook's own sub-agent check.
	rec := decide(HookConfig{LogPath: h.log}, eventPreToolUse, strings.NewReader(
		`{"tool_name":"Agent","tool_input":{"isolation":"SECRET-ISOLATION-VALUE"}}`), time.Now(), func(string) string { return "" })
	if strings.Contains(rec.Reason, "SECRET-ISOLATION") || len(rec.Isolation) > 32 {
		t.Errorf("record = %+v", rec)
	}
}

func TestInitLineIsNeverLogged(t *testing.T) {
	stream := filepath.Join(t.TempDir(), "s.jsonl")
	init := `{"type":"system","subtype":"init","session_id":"s1","cwd":"C:\\ACCOUNT-PATH-MARKER","mcp_servers":[{"name":"ACCOUNT-CONNECTOR-MARKER","status":"connected"}],"apiKeySource":"ACCOUNT-KEY-MARKER","tools":["Read"]}` + "\n"
	result := `{"type":"result","subtype":"success","is_error":false,"session_id":"s1","total_cost_usd":0,"num_turns":1,"duration_ms":1}` + "\n"
	if err := os.WriteFile(stream, []byte(init+result), 0o600); err != nil {
		t.Fatal(err)
	}
	o := startOpts(t, "stream", map[string]string{fakeStream: stream})
	sink := &collectSink{}
	res, err := agents.Execute(context.Background(), newAdapter(t, "stream"), o, sink)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(sink.all())
	if strings.Contains(string(data), "MARKER") || strings.Contains(res.SessionID, "MARKER") {
		t.Errorf("the init line leaked into the events: %s", data)
	}
}

func TestSeenToolUses(t *testing.T) {
	o := startOpts(t, "stream", map[string]string{fakeStream: mustAbs(t, filepath.Join("testdata", "edits.jsonl"))})
	r, err := newAdapter(t, "stream").Start(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	drain(r)
	tr, ok := r.(agents.ToolUseReporter)
	if !ok {
		t.Fatal("the run does not report tool uses")
	}
	if got := tr.SeenToolUses(); !slices.Equal(got, []string{"toolu_e1", "toolu_e2", "toolu_e3"}) {
		t.Errorf("SeenToolUses = %q", got)
	}
}
