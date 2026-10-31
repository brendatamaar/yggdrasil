package mcp_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	cmcp "github.com/brendatamaar/yggdrasil/internal/core/mcp"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/mcp"
)

func (e *env) listTools(t *testing.T) []string {
	t.Helper()
	r := e.post(t, e.token, "tools/list", map[string]any{})
	var out []string
	for _, x := range r.Result.Tools {
		out = append(out, x.Name)
	}
	return out
}

// Every tool, one row: arguments, what the answer must contain, and the
// effect on the fakes.
func TestToolTable(t *testing.T) {
	type row struct {
		tool  string
		args  map[string]any
		want  string // substring of the text
		check func(t *testing.T, e *env)
	}
	rows := []row{
		{"list_repos", nil, "shop-admin (id r-admin)", nil},
		{"repo_status", map[string]any{"repo": "shop-admin"}, "branch main", nil},
		{"repo_status", map[string]any{"repo": "shop-api"}, "ahead 1", func(t *testing.T, e *env) {
			if !slices.Contains(e.rd.All(), "status r-api wt=w-api") {
				t.Errorf("the task worktree was not read: %v", e.rd.All())
			}
		}},
		{"remote_status", map[string]any{"repo": "shop-api"}, "MR !4 open", nil},
		{"recent_commits", map[string]any{"repo": "shop-api", "limit": 5}, "Add cart", func(t *testing.T, e *env) {
			if !slices.Contains(e.rd.All(), "commits r-api wt=w-api limit=5") {
				t.Errorf("calls: %v", e.rd.All())
			}
		}},
		{"changed_files", map[string]any{"repo": "shop-web"}, "src/cart.go", nil},
		{"get_diff", map[string]any{"repo": "shop-api", "path": "src/x.go"}, "+hello", nil},
		{"build_info", map[string]any{"repo": "shop-admin"}, "node", nil},
		{"build_order", nil, "shop-web\nshop-api\nshop-admin", nil},
		{"list_build_presets", nil, "web+api", nil},
		{"list_builds", nil, "bld_ok", nil},
		{"build_log", map[string]any{"build": "bld_ok"}, "compiled", nil},
		{"start_build", map[string]any{"repos": []string{"shop-api", "shop-web"}}, "build bld_1 queued (task worktree)", func(t *testing.T, e *env) {
			want := []string{
				"start task=t1 repo=r-api wt=w-api steps=[verify]",
				"start task=t1 repo=r-web wt= steps=[verify]",
			}
			if got := e.bl.All(); !slices.Equal(got, want) {
				t.Errorf("builds started: %v, want %v", got, want)
			}
			if e.audit.Count() != 1 {
				t.Errorf("audit entries: %d, want 1", e.audit.Count())
			}
		}},
		{"run_build_preset", map[string]any{"preset": "web+api"}, "queued", func(t *testing.T, e *env) {
			if e.audit.Count() != 1 {
				t.Errorf("audit entries: %d", e.audit.Count())
			}
		}},
		{"list_dev_servers", nil, "No dev servers are running.", nil},
		{"dev_server_log", map[string]any{"name": "vite"}, "No such dev server.", nil},
		{"fetch", map[string]any{"repo": "shop-web"}, "shop-web: fetched", func(t *testing.T, e *env) {
			if e.git.All()[0] != "fetch task=t1 repo=r-web" || e.audit.Count() != 1 {
				t.Errorf("git %v audit %d", e.git.All(), e.audit.Count())
			}
		}},
		{"pull", map[string]any{"repo": "shop-web"}, "shop-web: pulled", func(t *testing.T, e *env) {
			if e.git.All()[0] != "pull task=t1 repo=r-web" || e.audit.Count() != 1 {
				t.Errorf("git %v audit %d", e.git.All(), e.audit.Count())
			}
		}},
		{"propose_commit", map[string]any{"repo": "shop-api", "paths": []string{"src/cart.go"}, "message": "Add cart"}, "Nothing has been done yet", func(t *testing.T, e *env) {
			p := e.prop.Created[0]
			c, ok := p.Action.(coreprop.Commit)
			if !ok || c.Where != (coreprop.InWorktree{ID: "w-api"}) || p.Task != "t1" {
				t.Errorf("proposal %+v", p)
			}
		}},
		{"propose_push", map[string]any{"repo": "shop-api", "branch": "ygg/cart", "remote": "origin"}, "status pending", nil},
		{"propose_merge_request", map[string]any{"repo": "shop-api", "branch": "ygg/cart", "remote": "origin", "target": "main", "title": "Cart"}, "status pending", nil},
		{"propose_install", map[string]any{"repo": "shop-web"}, "status pending", func(t *testing.T, e *env) {
			if in, ok := e.prop.Created[0].Action.(coreprop.Install); !ok || in.Step != "install" || in.Repo != webRepo {
				t.Errorf("install action %+v", e.prop.Created[0].Action)
			}
		}},
		{"propose_agent_file", map[string]any{"repo": "shop-web", "path": "CLAUDE.md", "content": "# rules"}, "status pending", func(t *testing.T, e *env) {
			if a, ok := e.prop.Created[0].Action.(coreprop.AgentFile); !ok || a.Where != (coreprop.MainCheckout{Repo: webRepo}) {
				t.Errorf("agent file action %+v", e.prop.Created[0].Action)
			}
		}},
		{"propose_delete_files", map[string]any{"repo": "shop-api", "paths": []string{"old.txt"}}, "status pending", nil},
	}
	for _, r := range rows {
		t.Run(r.tool, func(t *testing.T) {
			e := newEnv(t)
			e.bl.PresetList = []mcp.BuildPreset{{ID: "pre1", Name: "web+api", Repos: []core.RepoID{webRepo, apiRepo}, Steps: []string{"verify"}}}
			e.bl.BuildList = []mcp.BuildSummary{{ID: "bld_ok", Status: "succeeded", Repos: []core.RepoID{webRepo}}}
			e.bl.Logs["bld_ok"] = "compiled"
			res := e.callTool(t, r.tool, r.args)
			wantErr := r.tool == "dev_server_log" // none exist in this phase
			if res.Status != 200 || res.Err != nil || res.Result.IsError != wantErr {
				t.Fatalf("%s failed: %s", r.tool, res.Raw)
			}
			if !strings.Contains(res.text(), r.want) {
				t.Fatalf("%s answered %q, want it to contain %q", r.tool, res.text(), r.want)
			}
			if r.check != nil {
				r.check(t, e)
			}
		})
	}
	// The table covers every tool the server registers.
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r.tool] = true
	}
	for _, tl := range cmcp.AllTools() {
		if !seen[string(tl)] {
			t.Errorf("tool %s has no row in the table", tl)
		}
	}
}

// M4: the tool set is closed and has no way to confirm, reject, execute or
// publish. propose_publish and propose_subtasks are not registered.
func TestToolsHaveNoConfirm(t *testing.T) {
	e := newEnv(t)
	for _, name := range e.listTools(t) {
		for _, bad := range []string{"confirm", "reject", "execute", "run_proposal", "publish", "subtasks", "retry", "delete_branch", "force", "reset"} {
			if slices.Contains(strings.Split(name, "_"), bad) || strings.Contains(name, bad) && bad != "reset" {
				t.Errorf("tool %q looks like it can %s", name, bad)
			}
		}
		if !strings.HasPrefix(name, "propose_") && slices.Contains([]string{"commit", "push", "install", "merge_request"}, name) {
			t.Errorf("tool %q changes a repo without being a proposal", name)
		}
		if cmcp.Tool(name).Validate() != nil {
			t.Errorf("tool %q is outside the closed set", name)
		}
	}
	for _, name := range []string{"propose_publish", "propose_subtasks", "confirm_proposal"} {
		r := e.callTool(t, name, map[string]any{})
		if r.Err == nil {
			t.Errorf("calling %s did not fail: %s", name, r.Raw)
		}
	}
}

func TestDescriptorsCoverAllTools(t *testing.T) {
	e := newEnv(t)
	got := e.listTools(t)
	if len(got) != len(cmcp.AllTools()) {
		t.Fatalf("offered %d tools, core knows %d: %v", len(got), len(cmcp.AllTools()), got)
	}
}

// propose_* creates a pending proposal and nothing runs: the git fake, the
// builds fake and the reader see no call.
func TestProposeOnlyCreatesPendingProposals(t *testing.T) {
	e := newEnv(t)
	for tool, args := range map[string]map[string]any{
		"propose_commit":       {"repo": "shop-api", "paths": []string{"a.go"}, "message": "m"},
		"propose_push":         {"repo": "shop-api", "branch": "ygg/cart", "remote": "origin"},
		"propose_install":      {"repo": "shop-web"},
		"propose_agent_file":   {"repo": "shop-web", "path": "AGENTS.md", "content": "x"},
		"propose_delete_files": {"repo": "shop-web", "paths": []string{"old.txt"}},
	} {
		if r := e.callTool(t, tool, args); r.Result.IsError || !strings.Contains(r.text(), "pending") {
			t.Fatalf("%s: %s", tool, r.Raw)
		}
	}
	if len(e.prop.Created) != 5 {
		t.Fatalf("proposals created: %d", len(e.prop.Created))
	}
	if len(e.git.All())+len(e.bl.All())+len(e.rd.All()) != 0 {
		t.Fatalf("a propose tool ran something: git %v builds %v reads %v", e.git.All(), e.bl.All(), e.rd.All())
	}
}

// start_build cannot install: installing is a proposal (invariant 2).
func TestStartBuildRefusesInstall(t *testing.T) {
	e := newEnv(t)
	r := e.callTool(t, "start_build", map[string]any{"repos": []string{"shop-web"}, "steps": []string{"install"}})
	if !r.Result.IsError || !strings.Contains(r.text(), "propose_install") {
		t.Fatalf("answer: %s", r.Raw)
	}
	if len(e.bl.All()) != 0 {
		t.Fatal("a build started")
	}
}

// Commit, push and merge request need a task worktree; the others do not.
func TestWorktreeRules(t *testing.T) {
	e := newEnv(t)
	r := e.callTool(t, "propose_commit", map[string]any{"repo": "shop-web", "paths": []string{"a.go"}, "message": "m"})
	if !r.Result.IsError || !strings.Contains(r.text(), "no worktree") {
		t.Fatalf("commit in a repo without a worktree: %s", r.Raw)
	}
	// In current_branch mode the main checkout is the working tree.
	e.ws.Set(e.task, snapshot(core.ModeCurrentBranch))
	r = e.callTool(t, "propose_commit", map[string]any{"repo": "shop-web", "paths": []string{"a.go"}, "message": "m"})
	if r.Result.IsError {
		t.Fatalf("current_branch commit refused: %s", r.Raw)
	}
	if c := e.prop.Created[0].Action.(coreprop.Commit); c.Where != (coreprop.MainCheckout{Repo: webRepo}) {
		t.Fatalf("target %+v", c.Where)
	}
}

// Invariant 9: Ask mode never changes a working tree, so pull is neither
// offered nor callable there; fetch (remote refs only) stays.
func TestAskModeHasNoPull(t *testing.T) {
	e := newEnv(t)
	if !slices.Contains(e.listTools(t), "pull") {
		t.Fatal("pull not offered in Code mode")
	}
	e.ws.Set(e.task, snapshot(core.ModeAsk))
	tools := e.listTools(t)
	if slices.Contains(tools, "pull") || !slices.Contains(tools, "fetch") {
		t.Fatalf("Ask mode tools: %v", tools)
	}
	before := e.audit.Count()
	r := e.callTool(t, "pull", map[string]any{"repo": "shop-api"})
	// A tool that is not offered is refused either as unknown or as unavailable.
	if r.Err == nil && (!r.Result.IsError || !strings.Contains(r.text(), "Ask mode")) {
		t.Fatalf("pull in Ask mode: %s", r.Raw)
	}
	if e.audit.Count() != before || len(e.git.All()) != 0 {
		t.Fatalf("pull in Ask mode had an effect: audit %d, calls %v", e.audit.Count(), e.git.All())
	}
}

func TestMergeRequestToolNeedsAGitLabRemote(t *testing.T) {
	e := newEnv(t)
	if !slices.Contains(e.listTools(t), "propose_merge_request") {
		t.Fatal("not offered with a GitLab remote")
	}
	e.hosts.Remotes[apiRepo] = mcp.RemoteInfo{GitLab: false}
	if slices.Contains(e.listTools(t), "propose_merge_request") {
		t.Fatal("offered without a GitLab remote")
	}
}

func TestPresetOutsideScopeLooksLikeNoPreset(t *testing.T) {
	e := newEnv(t)
	e.bl.PresetList = []mcp.BuildPreset{{ID: "pre2", Name: "all", Repos: []core.RepoID{webRepo, adminRepo}}}
	a := e.callTool(t, "run_build_preset", map[string]any{"preset": "all"})
	b := e.callTool(t, "run_build_preset", map[string]any{"preset": "does-not-exist"})
	if !a.Result.IsError || a.text() != b.text() {
		t.Fatalf("out-of-scope preset %q, unknown preset %q", a.text(), b.text())
	}
	if len(e.bl.All()) != 0 {
		t.Fatal("a build started")
	}
}

func TestBuildLogOutsideScopeLooksLikeNoBuild(t *testing.T) {
	e := newEnv(t)
	e.bl.BuildList = []mcp.BuildSummary{{ID: "bld_admin", Status: "succeeded", Repos: []core.RepoID{adminRepo}}}
	e.bl.Logs["bld_admin"] = "admin secrets"
	a := e.callTool(t, "build_log", map[string]any{"build": "bld_admin"})
	b := e.callTool(t, "build_log", map[string]any{"build": "bld_nope"})
	if !a.Result.IsError || a.text() != b.text() || strings.Contains(a.Raw, "admin secrets") {
		t.Fatalf("a=%q b=%q", a.text(), b.text())
	}
	// list_builds shows visible builds, also out-of-scope ones, by name.
	if l := e.callTool(t, "list_builds", nil); !strings.Contains(l.text(), "bld_admin") {
		t.Fatalf("metadata list: %s", l.Raw)
	}
}

// Invalid arguments are MCP invalid params with a plain message.
func TestInvalidArgumentsAreInvalidParams(t *testing.T) {
	e := newEnv(t)
	for name, c := range map[string]struct {
		tool string
		args map[string]any
		want string
	}{
		"path escape":   {"get_diff", map[string]any{"repo": "shop-api", "path": "../x"}, ""},
		"repo is path":  {"repo_status", map[string]any{"repo": "C:/Users/me/repo"}, "name"},
		"unknown field": {"fetch", map[string]any{"repo": "shop-web", "force": true}, ""},
		"missing repo":  {"fetch", map[string]any{}, "required"},
		"too many":      {"recent_commits", map[string]any{"repo": "shop-api", "limit": 100000}, ""},
	} {
		r := e.callTool(t, c.tool, c.args)
		if r.Err == nil || r.Err.Code != -32602 || !strings.Contains(r.Err.Message, c.want) {
			t.Errorf("%s: %s", name, r.Raw)
		}
	}
	if len(e.git.All()) != 0 {
		t.Fatal("a call with bad arguments ran")
	}
}
