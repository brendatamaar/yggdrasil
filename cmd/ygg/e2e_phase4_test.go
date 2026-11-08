package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/audit"
	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/guard"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/hosts/hoststest"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
	"github.com/brendatamaar/yggdrasil/internal/search/searchtest"
)

// Phase 4 exit criterion: a multi-repo task on the demo project runs end to
// end with automatic verification and a change set shipped as linked merge
// requests against a test host. The whole app runs (HTTP API, as the UI uses
// it), with the mock adapter, local bare remotes that act as the git host
// (hoststest.PushHost) and the GitLab API stub. The mock adapter replays a
// recorded stream and edits no file, so the test edits the worktrees the way
// the agent would while the turn runs.

const p4Token = "glpat-e2eTESTtoken0123456789"

type p4Repo struct {
	name          string
	id            core.RepoID
	dir           string // main checkout
	host          *hoststest.PushHost
	wt            core.WorktreeID
	wtDir, branch string
}

type p4 struct {
	t       *testing.T
	a       *app
	base    string
	env     []string
	stub    *hoststest.GitLabStub
	project core.ProjectID
	repos   map[string]*p4Repo
	task    core.TaskID
	cfg     config
}

func (e *p4) do(method, path string, body, out any) (int, []byte) {
	e.t.Helper()
	code, data := apiDo(e.t, method, e.base+path, body, appClient)
	if out != nil && len(data) > 0 && code < 300 {
		if err := json.Unmarshal(data, out); err != nil {
			e.t.Fatalf("%s %s: decode %s: %v", method, path, data, err)
		}
	}
	return code, data
}

func (e *p4) must(method, path string, body, out any) {
	e.t.Helper()
	if code, data := e.do(method, path, body, out); code >= 300 {
		e.t.Fatalf("%s %s = %d %s", method, path, code, data)
	}
}

func (e *p4) wait(what string, timeout time.Duration, ok func() (bool, string)) {
	e.t.Helper()
	deadline := time.Now().Add(timeout)
	last := ""
	for {
		done, state := ok()
		if done {
			return
		}
		last = state
		if time.Now().After(deadline) {
			e.t.Fatalf("timed out waiting for %s: %s", what, last)
		}
		time.Sleep(40 * time.Millisecond)
	}
}

func (e *p4) mcpCall(tool string, args map[string]any) (string, bool) {
	e.t.Helper()
	token, err := e.a.mcp.Issue(e.task, 1)
	if err != nil {
		e.t.Fatal(err)
	}
	rpc := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}}
	body, _ := json.Marshal(rpc)
	req, _ := http.NewRequest(http.MethodPost, e.base+"/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	raw := buf.String()
	return raw, resp.StatusCode != 200 || strings.Contains(raw, `"isError":true`)
}

func (e *p4) git(dir string, args ...string) string {
	e.t.Helper()
	return searchtest.GitOutput(e.t, e.env, dir, "", args...)
}

// generateProject runs the demo generator's multirepo scenario (its own
// module, started through platform.Start like any child process).
func generateP4Project(t *testing.T) string {
	t.Helper()
	env, err := safety.NewEnvBuilder().Build(safety.EnvOptions{})
	if err != nil {
		t.Fatal(err)
	}
	goExe, err := platform.Resolve("go", "", env)
	if err != nil {
		t.Skipf("go is not installed: %v", err)
	}
	gen, err := filepath.Abs(filepath.Join("..", "..", "fixtures", "demo-gen"))
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "with space", "demo-project")
	var buf bytes.Buffer
	p, err := platform.Start(context.Background(), platform.Spec{
		Argv: []string{goExe, "run", ".", "-out", out, "-scenario", "multirepo"},
		Dir:  gen, Env: env, Stdout: &buf, Stderr: &buf,
	})
	if err != nil {
		t.Fatal(err)
	}
	if code, err := p.Wait(); err != nil || code != 0 {
		t.Fatalf("demo-gen exit %d, %v:\n%s", code, err, buf.String())
	}
	return filepath.Join(out, "acme-shop-platform")
}

func TestPhase4EndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the demo generator and a full multi-repo task")
	}
	e := &p4{t: t, repos: map[string]*p4Repo{}}
	e.env = searchtest.GitEnv(t)

	// ---- Step 1: the demo project, test hosts, the app ----
	projectDir := generateP4Project(t)
	e.stub = hoststest.NewGitLabStub(t, "http://127.0.0.1:1") // web_url base is irrelevant here
	stubURL := e.stub.Server.URL
	stubHost := strings.TrimPrefix(stubURL, "http://")

	order := []string{"shop-core", "shop-checkout", "shop-storefront"}
	for _, name := range order {
		r := &p4Repo{name: name, dir: filepath.Join(projectDir, name)}
		webBase := stubURL + "/acme/" + name
		r.host = hoststest.NewPushHost(t, webBase)
		// The repo's origin looks like a GitLab project on the stub's host; the
		// push goes to the local bare remote that plays the host.
		remote := "http://" + stubHost + "/acme/" + name + ".git"
		e.git(r.dir, "remote", "set-url", "origin", remote)
		e.git(r.dir, "config", "remote.origin.pushurl", r.host.URL())
		e.git(r.dir, "config", "user.name", "Ada Test")
		e.git(r.dir, "config", "user.email", "ada@example.test")
		e.git(r.dir, "config", "commit.gpgsign", "false")
		// The app reads the machine's git config too; keep line endings exactly as
		// committed so the CRLF checks mean what they say.
		e.git(r.dir, "config", "core.autocrlf", "false")
		r.host.MustGit(r.dir, "push", "-q", r.host.URL(), "main")
		e.repos[name] = r
	}

	dataDir := t.TempDir()
	envFile := fmt.Sprintf("YGG_HOST_1=gitlab %s\nYGG_HOST_1_TOKEN=%s\n", stubURL, p4Token)
	if err := os.WriteFile(filepath.Join(dataDir, ".env"), []byte(envFile), 0o600); err != nil {
		t.Fatal(err)
	}
	e.cfg = config{addr: "127.0.0.1:0", dataDir: dataDir, writeModes: true, mockDelay: 300 * time.Millisecond, mockEnforcement: agents.EnforcementHook}
	e.a, e.base = startFullApp(t, e.cfg)

	// Track the project through the API, as the start page does.
	var preview api.PreviewResponse
	e.must(http.MethodPost, "/api/projects/preview", api.PreviewRequest{Path: projectDir}, &preview)
	var picks []api.PickDTO
	for _, c := range preview.Candidates {
		picks = append(picks, api.PickDTO{RelPath: c.RelPath, Name: c.Name})
	}
	if len(picks) != 3 {
		t.Fatalf("preview of the demo project = %+v, want its 3 repos", preview)
	}
	var tracked api.TrackResponse
	e.must(http.MethodPost, "/api/projects", api.TrackProjectRequest{Path: projectDir, Name: "acme-shop-platform", Picks: picks}, &tracked)
	e.project = tracked.ProjectID
	for _, id := range tracked.Added {
		r, err := e.a.store.GetRepo(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if rp, ok := e.repos[r.Name]; ok {
			rp.id = id
		}
	}
	for _, name := range order {
		if e.repos[name].id == "" {
			t.Fatalf("repo %s was not tracked: %+v", name, tracked)
		}
	}

	// The hosts service links each repo to the GitLab instance of the stub (the
	// merge request tool is only offered for a linked GitLab remote).
	e.do(http.MethodPost, "/api/repos/refresh", map[string]string{}, nil)
	for _, name := range order {
		id := e.repos[name].id
		e.wait("host link of "+name, 90*time.Second, func() (bool, string) {
			var h api.RepoHostResponse
			e.do(http.MethodGet, "/api/repos/"+string(id)+"/host", nil, &h)
			if !h.Linked {
				e.do(http.MethodPost, "/api/hosts/refresh", map[string]string{}, nil)
			}
			return h.Linked, fmt.Sprintf("%+v", h)
		})
	}
	// Trust (SF-09). These repos ship no agent config: the trust view is none,
	// which the admission guard and verification both accept.
	for _, name := range order {
		var tr api.TrustDTO
		e.must(http.MethodGet, "/api/repos/"+string(e.repos[name].id)+"/trust", nil, &tr)
		if tr.State != "none" {
			t.Fatalf("trust of %s = %+v, want state none (no agent config in the demo repo)", name, tr)
		}
	}

	// Per-repo verify overrides with portable commands. There is no HTTP
	// endpoint for build settings (reported), so they go through the store
	// with an audit entry, the way the settings service does. shop-checkout
	// fails first: git diff --check rejects trailing whitespace.
	put := func(name string, cmd builds.Command) {
		t.Helper()
		raw, err := builds.FormatBuildSettings(builds.RepoBuildSettings{VerifyOverride: []builds.Command{cmd}, Bootstrap: false})
		if err != nil {
			t.Fatal(err)
		}
		id := e.repos[name].id
		err = e.a.store.Commit(context.Background(), func(tx *db.Tx) error {
			now := time.Now().UTC()
			if err := tx.PutBuildSettings(context.Background(), db.BuildSettingsRecord{Repo: id, Verify: raw, Bootstrap: false, UpdatedAt: now}); err != nil {
				return err
			}
			ent, perr := audit.Prepare(core.AuditEntry{ID: core.AuditID(db.NewID("aud")), At: now, Actor: "user", Action: "build_settings.update", Subject: "repo:" + string(id), Detail: []byte(`{}`)}, safety.NewRedactor(), core.AuditID(db.NewID("aud")), now)
			if perr != nil {
				return perr
			}
			return tx.AppendAudit(context.Background(), ent)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	put("shop-core", builds.Command{Name: "git version", Argv: []string{"git", "--version"}})
	put("shop-storefront", builds.Command{Name: "git version", Argv: []string{"git", "--version"}})
	put("shop-checkout", builds.Command{Name: "whitespace check", Argv: []string{"git", "diff", "--check", "HEAD"}})

	// ---- Step 2: a Code-mode task over all three repos ----
	var created struct{ Task core.Task }
	e.must(http.MethodPost, "/api/tasks", api.CreateTaskRequest{
		ProjectID: e.project, Scope: "project", OptIn: true, Title: "Add currency to checkout", Prompt: "Add the currency code to the money types, the checkout total and the storefront notes.",
		Mode: core.ModeProject, Agent: "mock",
	}, &created)
	e.task = created.Task.ID

	// The worktrees appear before the agent starts; the mock replays 15
	// events, 300 ms apart, so the edits below land while the turn runs.
	var wts api.WorktreesResponse
	e.wait("three worktrees", 60*time.Second, func() (bool, string) {
		e.do(http.MethodGet, "/api/tasks/"+string(e.task)+"/worktrees", nil, &wts)
		return len(wts.Worktrees) == 3, fmt.Sprintf("%+v", wts)
	})
	stored, err := e.a.store.ListWorktrees(context.Background(), e.task)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range stored {
		for _, r := range e.repos {
			if r.id == w.RepoID {
				r.wt, r.wtDir, r.branch = w.ID, w.Path, w.Branch
			}
		}
	}
	for _, r := range e.repos {
		if r.wtDir == "" {
			t.Fatalf("no worktree for %s: %+v", r.name, wts)
		}
	}
	write := func(r *p4Repo, rel, content string) {
		t.Helper()
		p := filepath.Join(r.wtDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	core1, co, sf := e.repos["shop-core"], e.repos["shop-checkout"], e.repos["shop-storefront"]
	moneyV1 := "package com.example.acme.core;\n\npublic record Money(long cents, String currency) {\n    public String label() {\n        return currency + \" \" + cents;\n    }\n}\n"
	write(core1, p4MoneyPath, moneyV1)
	// Trailing whitespace on purpose: the first verification of shop-checkout fails.
	checkoutBad := "package com.example.acme.checkout;\n\nimport com.example.acme.core.Money;\n\npublic class Checkout {\n    public Money total() {\n        return new Money(0, \"EUR\");   \n    }\n}\n"
	write(co, p4CheckoutPath, checkoutBad)
	write(sf, p4NotesPath, p4NotesV1)

	waitTask := func(want core.TaskStatus) {
		t.Helper()
		e.wait("task "+string(want), 90*time.Second, func() (bool, string) {
			var tk struct{ Task core.Task }
			e.do(http.MethodGet, "/api/tasks/"+string(e.task), nil, &tk)
			return tk.Task.Status == want, string(tk.Task.Status)
		})
	}
	waitTask(core.TaskReview)

	// ---- Step 3: automatic verification (VF-01..03) ----
	var vs api.VerificationsResponse
	settle := func(what string) {
		t.Helper()
		e.wait(what, 120*time.Second, func() (bool, string) {
			e.do(http.MethodGet, "/api/tasks/"+string(e.task)+"/verifications", nil, &vs)
			if len(vs.Verifications) < 3 {
				return false, fmt.Sprintf("%d verifications", len(vs.Verifications))
			}
			for _, v := range vs.Verifications {
				switch v.State {
				case api.VerifyPassed, api.VerifyFailed, api.VerifyUnverifiable, api.VerifySkipped, api.VerifyInterrupted, api.VerifyCancelled:
				default:
					return false, fmt.Sprintf("%s is %s", v.RepoName, v.State)
				}
			}
			return true, ""
		})
	}
	byRepo := func(name string) api.VerificationDTO {
		t.Helper()
		for _, v := range vs.Verifications {
			if v.RepoID == e.repos[name].id {
				return v
			}
		}
		t.Fatalf("no verification for %s in %+v", name, vs)
		return api.VerificationDTO{}
	}
	settle("first verifications")
	if v := byRepo("shop-core"); v.State != api.VerifyPassed {
		t.Errorf("shop-core = %+v, want passed", v)
	}
	if v := byRepo("shop-storefront"); v.State != api.VerifyPassed {
		t.Errorf("shop-storefront = %+v, want passed", v)
	}
	failed := byRepo("shop-checkout")
	if failed.State != api.VerifyFailed || !failed.CanSendFailure || !strings.Contains(failed.Message, "trailing whitespace") {
		t.Fatalf("shop-checkout = %+v, want failed with a log tail naming trailing whitespace and a send-failure option", failed)
	}
	if vs.Summary.State != api.VerifyFailed {
		t.Errorf("summary = %+v, want failed", vs.Summary)
	}

	// Send the failure: exactly one agent message, with the (redacted) tail.
	eventsBefore := e.taskMessages()
	// Without the opt-in the project-mode task refuses, nothing is sent and the
	// failure stays sendable; the click then carries the opt-in.
	if code, body := e.do(http.MethodPost, "/api/verifications/"+failed.ID+"/send-failure", map[string]string{}, nil); code != http.StatusConflict || !strings.Contains(string(body), "needs_opt_in") {
		t.Errorf("send-failure without the opt-in = %d %s, want 409 needs_opt_in", code, body)
	}
	if n := len(e.taskMessages()); n != len(eventsBefore) {
		t.Errorf("a refused send-failure added %d message(s)", n-len(eventsBefore))
	}
	e.must(http.MethodPost, "/api/verifications/"+failed.ID+"/send-failure", api.SendFailureRequest{OptIn: true}, nil)
	// A second click must not send the failure twice: once per attempt.
	if code, body := e.do(http.MethodPost, "/api/verifications/"+failed.ID+"/send-failure", api.SendFailureRequest{OptIn: true}, nil); code != http.StatusConflict {
		t.Errorf("a second send-failure = %d %s, want 409 conflict", code, body)
	}
	e.wait("the failure message in the transcript", 30*time.Second, func() (bool, string) {
		return len(e.taskMessages()) > len(eventsBefore), ""
	})
	msgs := e.taskMessages()[len(eventsBefore):]
	if len(msgs) != 1 || !strings.Contains(msgs[0], "trailing whitespace") {
		t.Errorf("failure messages = %q, want exactly one with the log tail", msgs)
	}
	if strings.Contains(strings.Join(msgs, "\n"), p4Token) {
		t.Error("the host token is in the failure message")
	}

	// Second turn: the agent fixes the whitespace.
	e.wait("the second turn to run", 60*time.Second, func() (bool, string) {
		var tk struct{ Task core.Task }
		e.do(http.MethodGet, "/api/tasks/"+string(e.task), nil, &tk)
		return tk.Task.Status == core.TaskRunning, string(tk.Task.Status)
	})
	write(co, p4CheckoutPath, strings.ReplaceAll(checkoutBad, "   \n", " // currency from the order\n"))
	waitTask(core.TaskReview)
	allPassed := func() (bool, string) {
		e.do(http.MethodGet, "/api/tasks/"+string(e.task)+"/verifications", nil, &vs)
		n := 0
		for _, v := range vs.Verifications {
			if v.State == api.VerifyPassed {
				n++
			}
		}
		dbg := ""
		for _, name := range order {
			tr, h, _ := e.a.store.GetRepoTrust(context.Background(), e.repos[name].id)
			dbg += fmt.Sprintf(" trust[%s]=%v/%d", name, tr, len(h))
		}
		turns := e.taskMessages()
		for _, v := range vs.Verifications {
			dbg += fmt.Sprintf("\n  %s %s turn=%s reason=%q", v.RepoName, v.State, v.Turn, v.Reason)
		}
		return n == 3 && len(vs.Verifications) == 3, fmt.Sprintf("turns=%d%s", len(turns), dbg)
	}
	// The end of the second turn verifies again on its own; "Verify again" is
	// the manual way and must land on the same result.
	e.wait("all three passed after the fix", 40*time.Second, allPassed)
	e.must(http.MethodPost, "/api/tasks/"+string(e.task)+"/verify/retry", map[string]string{}, nil)
	e.wait("all three passed after Verify again", 120*time.Second, allPassed)

	e.reviewStep(core1, sf, write)
	e.shipStep(order)
	e.landStep(core1)
}

// taskMessages lists the prompt of every turn of the task, oldest first: the
// messages the agent was sent (the transcript does not echo the user's text).
func (e *p4) taskMessages() []string {
	e.t.Helper()
	turns, err := e.a.store.ListTaskTurns(context.Background(), e.task)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, tr := range turns {
		p, err := e.a.store.GetTurnPrompt(context.Background(), tr.ID)
		if err != nil {
			e.t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

// Regression (found by TestPhase4EndToEnd): a repo that ships no agent config
// has the trust view none, which admission accepts. Verification used to read
// the stored flag, which such a repo never has, and ended "Unverifiable: not
// trusted" with nothing the user could do. It now reads the live view the
// admission guard reads: none, and an agent config the user has not trusted
// is untrusted.
func TestRegressionVerifyTrustIsTheAdmissionView(t *testing.T) {
	cfg := config{addr: "127.0.0.1:0", dataDir: t.TempDir(), writeModes: true}
	a, _ := startFullApp(t, cfg)
	env := searchtest.GitEnv(t)
	repoDir := searchtest.InitRepo(t, env, map[string]string{"README.md": "# shop-api\n"})
	var out bytes.Buffer
	if err := seed(context.Background(), &out, cfg, repoDir, ""); err != nil {
		t.Fatal(err)
	}
	ids := regexp.MustCompile(`(prj|rep)_[0-9a-f]+`).FindAllString(out.String(), -1)
	repo := core.RepoID(ids[1])
	src := trustSource{svc: a.tasks}
	ctx := context.Background()
	if v, err := src.TrustView(ctx, repo); err != nil || v != guard.TrustNone {
		t.Fatalf("view of a repo with no agent config = %q, %v; want none", v, err)
	}
	if err := os.MkdirAll(filepath.Join(repoDir, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, ".claude", "settings.json"), []byte(`{"hooks":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if v, err := src.TrustView(ctx, repo); err != nil || v != guard.TrustUntrusted {
		t.Fatalf("view of a repo with untrusted agent config = %q, %v; want untrusted", v, err)
	}
	if _, err := src.TrustView(ctx, core.RepoID("rep_missing")); err == nil {
		t.Fatal("an unknown repo must be an error (fail closed), not a view")
	}
}
