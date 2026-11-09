package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/audit"
	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
	"github.com/brendatamaar/yggdrasil/internal/search/searchtest"
)

// Helpers of TestPhase5EndToEnd (docs/phases/phase-5.md, "Exit criteria").
// Nothing about processes is faked: the dev servers are the fixture servers of
// fixtures/demo-gen, started through the real services and platform.Start.

// p5Project is the generated demo project, tracked in a data folder, with the
// task worktrees of the repos the test asked for.
type p5Project struct {
	t       *testing.T
	env     []string
	dir     string // the acme-shop folder
	cfg     config
	project core.ProjectID
	repos   map[string]core.RepoID // by repo name
	// worktrees[taskName][repoName] is the worktree of that repo in that task.
	worktrees map[string]map[string]core.Worktree
	taskIDs   map[string]core.TaskID
	tasks     []core.TaskID
	wtRoot    string
}

// p5NeedTool skips the test when a program the fixture needs is not installed.
func p5NeedTool(t *testing.T, names ...string) {
	t.Helper()
	env, err := safety.NewEnvBuilder().Build(safety.EnvOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if _, err := platform.Resolve(n, "", env); err != nil {
			t.Skipf("%s is not installed on this machine: %v", n, err)
		}
	}
}

// p5Generate runs the demo generator (its own module, through platform.Start)
// and returns the acme-shop folder. The output folder has a space in its name.
func p5Generate(t *testing.T) string {
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
		Argv: []string{goExe, "run", ".", "-out", out},
		Dir:  gen, Env: env, Stdout: &buf, Stderr: &buf,
	})
	if err != nil {
		t.Fatal(err)
	}
	if code, err := p.Wait(); err != nil || code != 0 {
		t.Fatalf("demo-gen exit %d, %v:\n%s", code, err, buf.String())
	}
	return filepath.Join(out, "acme-shop")
}

// newP5Project generates the demo project, tracks it in the data folder of cfg
// (through the same seed the CLI uses) and creates, for every task name, one
// real worktree of each wanted repo, recorded as an active worktree. No app
// runs while it does, so a caller can start an in-process app or a real ygg
// binary on the data folder afterwards.
func newP5Project(t *testing.T, tasks, wanted []string, cfg config) *p5Project {
	t.Helper()
	p := &p5Project{t: t, env: searchtest.GitEnv(t), dir: p5Generate(t), cfg: cfg,
		repos: map[string]core.RepoID{}, worktrees: map[string]map[string]core.Worktree{}, taskIDs: map[string]core.TaskID{}}
	var out bytes.Buffer
	if err := seed(context.Background(), &out, cfg, p.dir, ""); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if m := regexp.MustCompile(`Project\s+\S+\s+(prj_[0-9a-f]+)`).FindStringSubmatch(out.String()); m != nil {
		p.project = core.ProjectID(m[1])
	}
	for _, m := range regexp.MustCompile(`Repo\s+(\S+)\s+(rep_[0-9a-f]+)`).FindAllStringSubmatch(out.String(), -1) {
		p.repos[m[1]] = core.RepoID(m[2])
	}
	for _, n := range wanted {
		if p.repos[n] == "" {
			t.Fatalf("repo %s was not tracked: %s", n, out.String())
		}
	}
	for _, task := range tasks {
		p.addTask(task, wanted, nil)
	}
	return p
}

// addTask records a review-state task with one real worktree of each wanted
// repo. ref[repo] names an existing branch to check out (in the worktree) in
// place of a new branch off the current HEAD. No app may be running.
func (p *p5Project) addTask(task string, wanted []string, ref map[string]string) {
	t := p.t
	t.Helper()
	ctx := context.Background()
	d, store, err := openStore(ctx, p.cfg, safety.NewRedactor())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if p.wtRoot == "" {
		p.wtRoot = filepath.Join(t.TempDir(), "work trees")
	}
	ids := make([]core.RepoID, 0, len(wanted))
	for _, n := range wanted {
		ids = append(ids, p.repos[n])
	}
	tk := dbtest.Task(t, store, core.Task{ProjectID: p.project, Scope: core.ScopeProject, RepoIDs: ids, Mode: core.ModeCode, Status: core.TaskReview, Title: task})
	p.tasks = append(p.tasks, tk.ID)
	p.taskIDs[task] = tk.ID
	p.worktrees[task] = map[string]core.Worktree{}
	for _, n := range wanted {
		repoDir := filepath.Join(p.dir, n)
		wtDir := filepath.Join(p.wtRoot, task+"-"+n)
		branch := "ygg/" + task + "-" + n
		if r := ref[n]; r != "" {
			branch = r
			searchtest.RunGit(t, p.env, repoDir, "worktree", "add", "-q", wtDir, r)
		} else {
			searchtest.RunGit(t, p.env, repoDir, "worktree", "add", "-q", "-b", branch, wtDir)
		}
		var w core.Worktree
		err := store.Commit(ctx, func(tx *db.Tx) error {
			var err error
			w, err = tx.CreateWorktree(ctx, core.Worktree{
				ID: core.WorktreeID(db.NewID("wt")), RepoID: p.repos[n], TaskID: tk.ID, Name: task + "-" + n,
				Path: wtDir, Branch: branch, CreatedAt: time.Now().UTC(),
			})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		p.worktrees[task][n] = w
	}
}

// setVerify stores a verify override (one command) for a repo, with bootstrap
// on, and writes the audit entry the settings service writes. There is no HTTP
// endpoint for build settings, so it goes through the store, like the Phase 4
// end-to-end test. No app may be running.
func (p *p5Project) setVerify(repo string, argv ...string) {
	t := p.t
	t.Helper()
	ctx := context.Background()
	raw, err := builds.FormatBuildSettings(builds.RepoBuildSettings{
		VerifyOverride: []builds.Command{{Name: argv[0] + " " + argv[1], Argv: argv}}, Bootstrap: true})
	if err != nil {
		t.Fatal(err)
	}
	d, store, err := openStore(ctx, p.cfg, safety.NewRedactor())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	err = store.Commit(ctx, func(tx *db.Tx) error {
		now := time.Now().UTC()
		if err := tx.PutBuildSettings(ctx, db.BuildSettingsRecord{Repo: p.repos[repo], Verify: raw, Bootstrap: true, UpdatedAt: now}); err != nil {
			return err
		}
		ent, perr := audit.Prepare(core.AuditEntry{ID: core.AuditID(db.NewID("aud")), At: now, Actor: "user", Action: "build_settings.update", Subject: "repo:" + string(p.repos[repo]), Detail: []byte(`{}`)}, safety.NewRedactor(), core.AuditID(db.NewID("aud")), now)
		if perr != nil {
			return perr
		}
		return tx.AppendAudit(ctx, ent)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// p5Client talks to a running ygg through its HTTP API.
type p5Client struct {
	t      *testing.T
	base   string
	client *http.Client
}

func (c *p5Client) do(method, path string, body, out any) (int, []byte) {
	c.t.Helper()
	code, data := apiDo(c.t, method, c.base+path, body, c.client)
	if out != nil && len(data) > 0 && code < 300 {
		if err := json.Unmarshal(data, out); err != nil {
			c.t.Fatalf("%s %s: decode %s: %v", method, path, data, err)
		}
	}
	return code, data
}

func (c *p5Client) servers() []api.DevServerDTO {
	c.t.Helper()
	var list api.DevServersResponse
	if code, data := c.do(http.MethodGet, "/api/devservers", nil, &list); code != 200 {
		c.t.Fatalf("GET /api/devservers = %d %s", code, data)
	}
	return list.Servers
}

// server finds the server of repo in worktree wt.
func (c *p5Client) server(repoName string, wt core.WorktreeID) (api.DevServerDTO, bool) {
	for _, s := range c.servers() {
		if s.RepoName == repoName && s.WorktreeID == wt {
			return s, true
		}
	}
	return api.DevServerDTO{}, false
}

func actionOf(s api.DevServerDTO) api.DevServerActionRequest {
	return api.DevServerActionRequest{RepoID: s.RepoID, WorktreeID: s.WorktreeID, Command: s.Command}
}

// start starts the dev server of repo in worktree wt and waits until it is
// running with a detected port.
func (c *p5Client) start(repoName string, wt core.WorktreeID) api.DevServerDTO {
	c.t.Helper()
	s, ok := c.server(repoName, wt)
	if !ok {
		c.t.Fatalf("no dev server offered for %s in worktree %s: %+v", repoName, wt, c.servers())
	}
	if code, data := c.do(http.MethodPost, "/api/devservers/"+string(s.ID)+"/start", actionOf(s), nil); code != 200 {
		c.t.Fatalf("start %s: %d %s", repoName, code, data)
	}
	return c.waitPort(repoName, wt)
}

func (c *p5Client) waitPort(repoName string, wt core.WorktreeID) api.DevServerDTO {
	c.t.Helper()
	var last api.DevServerDTO
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if s, ok := c.server(repoName, wt); ok {
			last = s
			if s.Status == api.DevServerRunning && len(s.Ports) > 0 {
				return s
			}
			if s.Status == api.DevServerFailed {
				c.t.Fatalf("dev server %s failed: %+v", repoName, s)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	c.t.Fatalf("dev server %s never reported a port: %+v", repoName, last)
	return last
}

func (c *p5Client) stop(s api.DevServerDTO) {
	_, _ = c.do(http.MethodPost, "/api/devservers/"+string(s.ID)+"/stop", actionOf(s), nil)
}

func (c *p5Client) logText(s api.DevServerDTO) string {
	c.t.Helper()
	var r api.DevServerLogResponse
	if code, data := c.do(http.MethodGet, "/api/devservers/"+string(s.ID)+"/log?tail=100", nil, &r); code != 200 {
		c.t.Fatalf("log of %s = %d %s", s.ID, code, data)
	}
	var b bytes.Buffer
	for _, l := range r.Lines {
		fmt.Fprintln(&b, l.Line)
	}
	return b.String()
}

// httpGet fetches a page of a dev server with a short timeout.
func httpGet(url string) (string, error) {
	cl := &http.Client{Timeout: 5 * time.Second}
	resp, err := cl.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("%s: status %d", url, resp.StatusCode)
	}
	return string(b), nil
}

// httpGetEventually retries httpGet for a few seconds (the port is bound a
// moment before the server answers).
func httpGetEventually(t *testing.T, url string) string {
	t.Helper()
	var err error
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var s string
		if s, err = httpGet(url); err == nil {
			return s
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("GET %s: %v", url, err)
	return ""
}
