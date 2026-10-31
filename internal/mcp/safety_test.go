package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	cmcp "github.com/brendatamaar/yggdrasil/internal/core/mcp"
	"github.com/brendatamaar/yggdrasil/internal/mcp"
	"github.com/brendatamaar/yggdrasil/internal/mcp/mcptest"
)

// SF-02, invariant 6: an untracked repo, an unknown repo and an out-of-scope
// repo get the identical answer on every content and action tool (M3), and
// nothing runs.
func TestVisibilityNoRepoOracle(t *testing.T) {
	e := newEnv(t)
	calls := map[string]func(repo string) map[string]any{
		"recent_commits":       func(r string) map[string]any { return map[string]any{"repo": r} },
		"changed_files":        func(r string) map[string]any { return map[string]any{"repo": r} },
		"get_diff":             func(r string) map[string]any { return map[string]any{"repo": r} },
		"fetch":                func(r string) map[string]any { return map[string]any{"repo": r} },
		"pull":                 func(r string) map[string]any { return map[string]any{"repo": r} },
		"start_build":          func(r string) map[string]any { return map[string]any{"repos": []string{r}} },
		"propose_install":      func(r string) map[string]any { return map[string]any{"repo": r} },
		"propose_agent_file":   func(r string) map[string]any { return map[string]any{"repo": r, "path": "AGENTS.md", "content": "x"} },
		"propose_delete_files": func(r string) map[string]any { return map[string]any{"repo": r, "paths": []string{"a.txt"}} },
		"propose_commit": func(r string) map[string]any {
			return map[string]any{"repo": r, "paths": []string{"a"}, "message": "m"}
		},
		"propose_push": func(r string) map[string]any { return map[string]any{"repo": r, "branch": "b", "remote": "origin"} },
	}
	// "vault" is a repo the user untracked: it is not in the snapshot at all.
	// shop-admin is visible but out of scope. "r-admin" is the same by ID.
	for tool, mk := range calls {
		var answers []string
		for _, repo := range []string{"vault", "r-vault", "shop-admin", "r-admin", "SHOP-ADMIN", "nope"} {
			r := e.callTool(t, tool, mk(repo))
			if !r.Result.IsError {
				t.Errorf("%s on %q was served: %s", tool, repo, r.Raw)
			}
			answers = append(answers, r.text())
		}
		for _, a := range answers[1:] {
			if a != answers[0] {
				t.Errorf("%s: answers differ (%q vs %q): the agent could probe", tool, answers[0], a)
			}
		}
	}
	if n := len(e.git.All()) + len(e.bl.All()) + len(e.rd.All()) + len(e.prop.Created) + e.audit.Count(); n != 0 {
		t.Fatalf("%d effects ran for hidden repos", n)
	}
}

// Metadata tools may name a visible repo outside the scope, but never an
// untracked one.
func TestMetadataToolsSeeVisibleReposOnly(t *testing.T) {
	e := newEnv(t)
	if r := e.callTool(t, "repo_status", map[string]any{"repo": "shop-admin"}); r.Result.IsError {
		t.Fatalf("visible repo refused: %s", r.Raw)
	}
	if r := e.callTool(t, "repo_status", map[string]any{"repo": "vault"}); !r.Result.IsError {
		t.Fatalf("untracked repo served: %s", r.Raw)
	}
	if r := e.callTool(t, "list_repos", nil); strings.Contains(r.text(), "vault") {
		t.Fatalf("list_repos names an untracked repo: %s", r.text())
	}
}

// A repo untracked mid-run disappears at the next call.
func TestRepoUntrackedMidRunDisappears(t *testing.T) {
	e := newEnv(t)
	if r := e.callTool(t, "changed_files", map[string]any{"repo": "shop-web"}); r.Result.IsError {
		t.Fatal(r.Raw)
	}
	e.ws.Untrack(webRepo)
	if r := e.callTool(t, "changed_files", map[string]any{"repo": "shop-web"}); !r.Result.IsError || r.text() != "No such repo. Use list_repos to see the repos you can use." {
		t.Fatalf("after untrack: %s", r.Raw)
	}
	if r := e.callTool(t, "list_repos", nil); strings.Contains(r.text(), "shop-web") {
		t.Fatal("untracked repo still listed")
	}
}

// Invariant 4: the token is not in any result, log line, audit record, or the
// listing, even when a tool's own output contains it.
func TestTokenNeverAppearsAnywhere(t *testing.T) {
	e := newEnv(t)
	e.rd.Extra = "leak:" + e.token // a tool output that happens to hold the token
	var texts []string
	for tool, args := range map[string]map[string]any{
		"get_diff":        {"repo": "shop-api"},
		"recent_commits":  {"repo": "shop-api"},
		"fetch":           {"repo": "shop-web"},
		"start_build":     {"repos": []string{"shop-web"}},
		"propose_install": {"repo": "shop-web"},
		"repo_status":     {"repo": "nope"},
	} {
		r := e.callTool(t, tool, args)
		texts = append(texts, r.Raw)
	}
	e.bl.StartErr = fmt.Errorf("boom %s", e.token)
	texts = append(texts, e.callTool(t, "start_build", map[string]any{"repos": []string{"shop-web"}}).Raw)
	texts = append(texts, e.post(t, e.token, "tools/list", map[string]any{}).Raw)
	for i, s := range texts {
		if strings.Contains(s, e.token) {
			t.Errorf("response %d contains the token: %s", i, s)
		}
	}
	if strings.Contains(e.logs.String(), e.token) {
		t.Errorf("the log contains the token:\n%s", e.logs.String())
	}
	b, _ := json.Marshal(e.audit.Records)
	if strings.Contains(string(b), e.token) {
		t.Errorf("an audit record contains the token: %s", b)
	}
	// The rest of the output survives; only the secret is masked.
	if d := e.callTool(t, "get_diff", map[string]any{"repo": "shop-api"}); !strings.Contains(d.text(), "leak:") || !strings.Contains(d.text(), "+hello") {
		t.Errorf("redaction removed more than the secret: %s", d.text())
	}
}

// M7: results hold no absolute path of the machine.
func TestResultsHaveNoAbsolutePaths(t *testing.T) {
	e := newEnv(t)
	e.rd.Extra = "\nat C:\\Users\\ada\\work\\shop-api\\src\\a.go:3 and /home/ada/work/shop-api/b.go and \\\\srv\\share\\x and C:/Users/ada/x"
	r := e.callTool(t, "get_diff", map[string]any{"repo": "shop-api"})
	for _, bad := range []string{"C:\\Users", "/home/ada", "\\\\srv", "C:/Users", "ada"} {
		if strings.Contains(r.text(), bad) {
			t.Errorf("result keeps %q: %s", bad, r.text())
		}
	}
	if !strings.Contains(r.text(), "+hello") {
		t.Errorf("the diff itself was lost: %s", r.text())
	}
}

// M9: results are capped and flagged.
func TestResultCap(t *testing.T) {
	e := newEnv(t)
	e.rd.Diffs[apiRepo] = strings.Repeat("x", cmcp.MaxDiffBytes)
	e.rd.Extra = strings.Repeat("y", 10)
	r := e.callTool(t, "get_diff", map[string]any{"repo": "shop-api", "max_bytes": 100})
	if len(r.text()) > 200 || r.Result.Meta["truncated"] != true {
		t.Fatalf("len %d meta %v", len(r.text()), r.Result.Meta)
	}
}

// M8: when the audit entry cannot be written, a state-changing tool does not
// run.
func TestMutatingToolsAreAuditedAndFailClosed(t *testing.T) {
	mutating := map[string]map[string]any{
		"start_build":           {"repos": []string{"shop-web"}},
		"run_build_preset":      {"preset": "p"},
		"fetch":                 {"repo": "shop-web"},
		"pull":                  {"repo": "shop-web"},
		"propose_commit":        {"repo": "shop-api", "paths": []string{"a"}, "message": "m"},
		"propose_push":          {"repo": "shop-api", "branch": "b", "remote": "origin"},
		"propose_merge_request": {"repo": "shop-api", "branch": "b", "remote": "origin", "target": "main", "title": "t"},
		"propose_install":       {"repo": "shop-web"},
		"propose_agent_file":    {"repo": "shop-web", "path": "AGENTS.md", "content": "x"},
		"propose_delete_files":  {"repo": "shop-web", "paths": []string{"a"}},
	}
	for tool, args := range mutating {
		t.Run(tool, func(t *testing.T) {
			e := newEnv(t)
			e.bl.PresetList = []mcp.BuildPreset{{ID: "p", Name: "p", Repos: []core.RepoID{webRepo}}}
			if r := e.callTool(t, tool, args); r.Result.IsError {
				t.Fatalf("%s", r.Raw)
			}
			if e.audit.Count() != 1 {
				t.Fatalf("audit entries: %d, want 1", e.audit.Count())
			}
			// Now with a broken audit store: nothing may run.
			e2 := newEnv(t)
			e2.bl.PresetList = e.bl.PresetList
			e2.audit.Fail = true
			r := e2.callTool(t, tool, args)
			if !r.Result.IsError {
				t.Fatalf("the call ran without an audit entry: %s", r.Raw)
			}
			if n := len(e2.git.All()) + len(e2.bl.All()) + len(e2.prop.Created); n != 0 {
				t.Fatalf("%d effects ran without an audit entry", n)
			}
		})
	}
}

func TestLimiterAnswersSlowDown(t *testing.T) {
	e := newEnv(t)
	limited := 0
	for range cmcp.Burst + 5 {
		if r := e.callTool(t, "list_repos", nil); r.Result.IsError && strings.Contains(r.text(), "Too many calls") {
			limited++
		}
	}
	if limited != 5 {
		t.Fatalf("limited %d calls, want 5", limited)
	}
	e.advance(time.Second)
	if r := e.callTool(t, "list_repos", nil); r.Result.IsError {
		t.Fatalf("still limited after a second: %s", r.Raw)
	}
}

// blockingReader holds a read until released, to revoke during a call.
type blockingReader struct {
	*mcptest.FakeReader
	in, out chan struct{}
}

func (b *blockingReader) Status(ctx context.Context, at mcp.Loc) (mcp.RepoStatus, error) {
	close(b.in)
	<-b.out
	return b.FakeReader.Status(ctx, at)
}

// Run ends while a call is in flight: the call finishes against its own
// context and its result is dropped.
func TestRevokeDuringACallDropsTheResult(t *testing.T) {
	e := newEnv(t)
	br := &blockingReader{FakeReader: e.rd, in: make(chan struct{}), out: make(chan struct{})}
	e.withReader(t, br)
	var wg sync.WaitGroup
	var r rpcResp
	wg.Add(1)
	go func() {
		defer wg.Done()
		r = e.callTool(t, "repo_status", map[string]any{"repo": "shop-api"})
	}()
	<-br.in
	e.srv.Revoke(e.task)
	close(br.out)
	wg.Wait()
	if !r.Result.IsError || strings.Contains(r.text(), "branch main") {
		t.Fatalf("the result of a revoked session was delivered: %s", r.Raw)
	}
}

// Concurrent calls from one session all complete, with at most MaxInFlight
// running at once (checked by the race detector and the counting reader).
func TestConcurrentCalls(t *testing.T) {
	e := newEnv(t)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r := e.callTool(t, "repo_status", map[string]any{"repo": "shop-web"}); r.Status != 200 {
				t.Errorf("status %d", r.Status)
			}
		}()
	}
	wg.Wait()
}

// A panic in a tool does not leak internals and the session stays valid.
type panicReader struct{ *mcptest.FakeReader }

func (panicReader) Status(context.Context, mcp.Loc) (mcp.RepoStatus, error) {
	panic("secret internal detail")
}

func TestToolPanicIsContained(t *testing.T) {
	e := newEnv(t)
	e.withReader(t, panicReader{e.rd})
	r := e.callTool(t, "repo_status", map[string]any{"repo": "shop-web"})
	if !r.Result.IsError || strings.Contains(r.Raw, "secret internal detail") || !strings.Contains(r.text(), "That tool failed") {
		t.Fatalf("answer: %s", r.Raw)
	}
	if r := e.callTool(t, "list_repos", nil); r.Result.IsError {
		t.Fatal("the session died with the panic")
	}
}
