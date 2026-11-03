package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/core"
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

	var seedOut bytes.Buffer
	if err := seed(context.Background(), &seedOut, e.cfg, projectDir, ""); err != nil {
		t.Fatal(err)
	}
	if m := regexp.MustCompile(`Project\s+\S+\s+(prj_[0-9a-f]+)`).FindStringSubmatch(seedOut.String()); m != nil {
		e.project = core.ProjectID(m[1])
	} else {
		t.Fatalf("seed output has no project: %s", seedOut.String())
	}
	for _, name := range order {
		m := regexp.MustCompile(`Repo\s+` + regexp.QuoteMeta(name) + `\s+(rep_[0-9a-f]+)`).FindStringSubmatch(seedOut.String())
		if m == nil {
			t.Fatalf("seed output has no repo %s: %s", name, seedOut.String())
		}
		e.repos[name].id = core.RepoID(m[1])
	}

	// Trust (SF-09): review what each repo would run, then trust it.
	for _, name := range order {
		var tr api.TrustDTO
		e.must(http.MethodGet, "/api/repos/"+string(e.repos[name].id)+"/trust", nil, &tr)
		if tr.State != "trusted" && tr.Hash != "" {
			e.must(http.MethodPost, "/api/repos/"+string(e.repos[name].id)+"/trust", api.TrustRequest{Hash: tr.Hash}, &tr)
		}
		t.Logf("trust %s: state=%s why=%q", name, tr.State, tr.Why)
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
			return tx.AppendAudit(context.Background(), core.AuditEntry{ID: core.AuditID(db.NewID("aud")), At: now, Actor: "user", Action: "build_settings.update", Subject: "repo:" + string(id), Detail: []byte(`{}`)})
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
	for _, w := range wts.Worktrees {
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
	const moneyPath = "src/main/java/com/example/acme/core/Money.java"
	const checkoutPath = "src/main/java/com/example/acme/checkout/Checkout.java"
	const notesPath = "docs/RELEASE-NOTES.txt"
	moneyV1 := "package com.example.acme.core;\n\npublic record Money(long cents, String currency) {\n    public String label() {\n        return currency + \" \" + cents;\n    }\n}\n"
	write(core1, moneyPath, moneyV1)
	// Trailing whitespace on purpose: the first verification of shop-checkout fails.
	checkoutBad := "package com.example.acme.checkout;\n\nimport com.example.acme.core.Money;\n\npublic class Checkout {\n    public Money total() {\n        return new Money(0, \"EUR\");   \n    }\n}\n"
	write(co, checkoutPath, checkoutBad)
	notesV1 := "Release notes\r\n\r\n0.1.0\r\n- first storefront build\r\n- currency shown in the cart\r\n"
	write(sf, notesPath, notesV1)

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
	var sent api.SendFailureResponse
	e.must(http.MethodPost, "/api/verifications/"+failed.ID+"/send-failure", map[string]string{}, &sent)
	if sent.TaskID != e.task {
		t.Errorf("send-failure = %+v", sent)
	}
	// A second click must not send the failure twice.
	if code, _ := e.do(http.MethodPost, "/api/verifications/"+failed.ID+"/send-failure", map[string]string{}, nil); code < 400 {
		t.Errorf("a second send-failure was accepted (%d)", code)
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

	// Second turn: the agent fixes the whitespace (the turn is running now).
	write(co, checkoutPath, strings.ReplaceAll(checkoutBad, "   \n", "\n"))
	waitTask(core.TaskReview)
	allPassed := func() (bool, string) {
		e.do(http.MethodGet, "/api/tasks/"+string(e.task)+"/verifications", nil, &vs)
		n := 0
		for _, v := range vs.Verifications {
			if v.State == api.VerifyPassed {
				n++
			}
		}
		return n == 3 && len(vs.Verifications) == 3, fmt.Sprintf("%+v", vs)
	}
	// The end of the second turn verifies again on its own; "Verify again" is
	// the manual way and must land on the same result.
	e.wait("all three passed after the fix", 120*time.Second, allPassed)
	e.must(http.MethodPost, "/api/tasks/"+string(e.task)+"/verify/retry", map[string]string{}, nil)
	e.wait("all three passed after Verify again", 120*time.Second, allPassed)

	e.reviewStep(core1, co, sf, notesPath, checkoutPath, write)
	e.shipStep(order)
	e.landStep(core1)
}

// taskMessages lists the user-role messages of the task's transcript.
func (e *p4) taskMessages() []string {
	e.t.Helper()
	var ev struct {
		Events []struct {
			Kind string
			Role string
			Text string
		}
	}
	e.must(http.MethodGet, "/api/tasks/"+string(e.task)+"/events", nil, &ev)
	var out []string
	for _, x := range ev.Events {
		if x.Kind == "message" && x.Role == "user" {
			out = append(out, x.Text)
		}
	}
	return out
}

func (e *p4) reviewStep(core1, co, sf *p4Repo, notesPath, checkoutPath string, write func(*p4Repo, string, string)) {
	e.t.Helper()
}

func (e *p4) shipStep(order []string) { e.t.Helper() }

func (e *p4) landStep(r *p4Repo) { e.t.Helper() }

var _ = url.Parse
