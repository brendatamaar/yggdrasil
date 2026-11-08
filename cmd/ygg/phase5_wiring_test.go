package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/core"
	ct "github.com/brendatamaar/yggdrasil/internal/core/terminal"
	wenv "github.com/brendatamaar/yggdrasil/internal/core/worktreeenv"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
	"github.com/brendatamaar/yggdrasil/internal/devservers"
	"github.com/brendatamaar/yggdrasil/internal/platform/platformtest"
	"github.com/brendatamaar/yggdrasil/internal/search/searchtest"
	"github.com/brendatamaar/yggdrasil/internal/terminal/terminaltest"
)

// A tracked repo with a package.json (so a dev server is offered) and one
// active worktree in its own folder.
type phase5Rig struct {
	a       *app
	base    string
	cfg     config
	side    *sideDB
	repo    core.RepoID
	repoDir string
	wt      core.WorktreeID
	wtDir   string
	fake    *platformtest.Fake
	pty     *terminaltest.Starter
}

func newPhase5Rig(t *testing.T) *phase5Rig {
	t.Helper()
	fake, pty := &platformtest.Fake{}, &terminaltest.Starter{}
	cfg := config{addr: "127.0.0.1:0", dataDir: t.TempDir(), writeModes: true, phase5: &phase5Hooks{
		start: fake.Start, ports: func(int) ([]int, error) { return nil, nil },
		resolve: func(name, _ string, _ []string) (string, error) { return name, nil },
		pty:     pty.Start,
	}}
	a, base := startFullApp(t, cfg)
	env := searchtest.GitEnv(t)
	repoDir := searchtest.InitRepo(t, env, map[string]string{
		"package.json": `{"name":"shop-web","scripts":{"dev":"vite"}}`, "README.md": "# shop-web\n",
	})
	if err := os.WriteFile(filepath.Join(repoDir, ".env"), []byte("API_URL=http://localhost\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := seed(context.Background(), &out, cfg, repoDir, ""); err != nil {
		t.Fatal(err)
	}
	ids := regexp.MustCompile(`(prj|rep)_[0-9a-f]+`).FindAllString(out.String(), -1)
	project, repoID := core.ProjectID(ids[0]), core.RepoID(ids[1])
	task := dbtest.Task(t, a.store, core.Task{ProjectID: project, Scope: core.ScopeRepo, RepoIDs: []core.RepoID{repoID}, Mode: core.ModeCode, Status: core.TaskReview})
	wtDir := filepath.Join(t.TempDir(), "work tree")
	if err := os.MkdirAll(wtDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtDir, "package.json"), []byte(`{"name":"shop-web","scripts":{"dev":"vite"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	const wt = core.WorktreeID("wt_p5")
	if err := a.store.Commit(context.Background(), func(tx *db.Tx) error {
		_, err := tx.RecordWorktree(context.Background(), core.Worktree{
			ID: wt, RepoID: repoID, TaskID: task.ID, Name: "cart", Path: wtDir, Branch: "ygg/cart", CreatedAt: time.Now().UTC(),
		}, "0000000000000000000000000000000000000000", core.AuditEntry{ID: core.AuditID(db.NewID("aud")), At: time.Now().UTC(), Actor: "test", Action: "worktree.create", Subject: "worktree:wt_p5", Detail: []byte(`{}`)})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return &phase5Rig{a: a, base: base, cfg: cfg, side: openSideDB(t, cfg.dataDir), repo: repoID, repoDir: repoDir, wt: wt, wtDir: wtDir, fake: fake, pty: pty}
}

func (r *phase5Rig) auditCount(action string) int {
	r.side.t.Helper()
	var n int
	if err := r.side.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = ?`, action).Scan(&n); err != nil {
		r.side.t.Fatal(err)
	}
	return n
}

func (r *phase5Rig) waitAudit(t *testing.T, action string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for r.auditCount(action) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("no %q audit row", action)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The Phase 5 services are bound and reachable through the HTTP handler: the
// terminal issues a token only for tracked folders, the dev server list
// answers, and the repo environment settings round-trip through the store.
func TestPhase5ServicesAreWired(t *testing.T) {
	r := newPhase5Rig(t)
	base := r.base

	var list api.DevServersResponse
	if code := getJSON(t, base+"/api/devservers", &list); code != 200 || len(list.Servers) != 0 {
		t.Fatalf("GET /api/devservers = %d %+v", code, list)
	}

	var tok api.TerminalTokenResponse
	code, data := apiDo(t, http.MethodPost, base+"/api/terminal", api.TerminalTokenRequest{RepoID: r.repo}, appClient)
	if code != 200 || json.Unmarshal(data, &tok) != nil || len(tok.Token) != 43 {
		t.Fatalf("terminal for a tracked repo = %d %s", code, data)
	}
	if code, data = apiDo(t, http.MethodPost, base+"/api/terminal", api.TerminalTokenRequest{WorktreeID: r.wt}, appClient); code != 200 {
		t.Fatalf("terminal for a tracked worktree = %d %s", code, data)
	}
	for _, bad := range []api.TerminalTokenRequest{{RepoID: "rep_0000000000000000"}, {WorktreeID: "wt_0000000000000000"}} {
		if code, data = apiDo(t, http.MethodPost, base+"/api/terminal", bad, appClient); code != 404 {
			t.Fatalf("terminal for %+v = %d %s, want 404", bad, code, data)
		}
	}
	if bytes.Contains(data, []byte(r.repoDir)) {
		t.Fatal("the refusal shows a path")
	}

	// Repo environment settings round-trip through the real store.
	var e api.RepoEnvDTO
	if code := getJSON(t, base+"/api/repos/"+string(r.repo)+"/env", &e); code != 200 || e.AgentCopy || len(e.ProfilePaths) != 0 {
		t.Fatalf("default env = %d %+v", code, e)
	}
	put := api.RepoEnvDTO{RepoID: r.repo, ProfilePaths: []string{".env", "config/local.json"}, AgentCopy: true}
	code, data = apiDo(t, http.MethodPut, base+"/api/repos/"+string(r.repo)+"/env", put, appClient)
	if code != 200 {
		t.Fatalf("PUT env = %d %s", code, data)
	}
	if code := getJSON(t, base+"/api/repos/"+string(r.repo)+"/env", &e); code != 200 || !e.AgentCopy || len(e.ProfilePaths) != 2 || e.ProfilePaths[0] != ".env" {
		t.Fatalf("env after PUT = %d %+v", code, e)
	}
	for _, bad := range []string{"../secret", "/etc/passwd", ".git/config"} {
		put.ProfilePaths = []string{bad}
		if code, _ = apiDo(t, http.MethodPut, base+"/api/repos/"+string(r.repo)+"/env", put, appClient); code != 400 {
			t.Fatalf("PUT env with %q = %d, want 400", bad, code)
		}
	}
	var we api.WorktreeEnvDTO
	if code := getJSON(t, base+"/api/worktrees/"+string(r.wt)+"/env", &we); code != 200 || we.Compose != api.ComposeOff {
		t.Fatalf("worktree env = %d %+v", code, we)
	}
	if code := getJSON(t, base+"/api/worktrees/wt_0000000000000000/env", nil); code != 404 {
		t.Fatalf("env of an unknown worktree = %d, want 404", code)
	}
}

// A dev server starts in a tracked worktree with a port block, shows up in the
// list, and stops; start and stop are audited (invariant 8). An untracked
// target is refused.
func TestPhase5DevServerStartStopIsAudited(t *testing.T) {
	r := newPhase5Rig(t)
	r.fake.Push(platformtest.Script{Block: true})
	key := devservers.Target{Repo: r.repo, Worktree: r.wt}.Key("dev")
	id := devservers.IDOf(key)
	body := api.DevServerActionRequest{RepoID: r.repo, WorktreeID: r.wt, Command: "dev"}

	code, data := apiDo(t, http.MethodPost, r.base+"/api/devservers/"+id+"/start", body, appClient)
	if code != 200 {
		t.Fatalf("start = %d %s", code, data)
	}
	var dto api.DevServerDTO
	if err := json.Unmarshal(data, &dto); err != nil {
		t.Fatal(err)
	}
	if dto.BlockSize != core.PortBlockSize || dto.BlockBase < 20000 || dto.RepoName == "" {
		t.Fatalf("server = %+v, want a port block in the default range and the repo name", dto)
	}
	// The process starts right after the state is saved.
	for deadline := time.Now().Add(5 * time.Second); len(r.fake.Started()) == 0 && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	specs := r.fake.Started()
	if len(specs) != 1 || specs[0].Dir != r.wtDir {
		t.Fatalf("started = %+v, want one process in the worktree", specs)
	}
	var list api.DevServersResponse
	if code := getJSON(t, r.base+"/api/devservers", &list); code != 200 || len(list.Servers) != 1 || list.Servers[0].ID != core.DevServerID(id) {
		t.Fatalf("list = %d %+v", code, list)
	}
	if code, data = apiDo(t, http.MethodPost, r.base+"/api/devservers/"+id+"/stop", body, appClient); code != 200 {
		t.Fatalf("stop = %d %s", code, data)
	}
	if r.auditCount("devserver.start") == 0 || r.auditCount("devserver.stop") == 0 {
		t.Fatal("dev server start and stop must write audit rows")
	}

	// A worktree that is not tracked (here: of another ID) starts nothing.
	ghost := api.DevServerActionRequest{RepoID: r.repo, WorktreeID: "wt_0000000000000000", Command: "dev"}
	gid := devservers.IDOf(devservers.Target{Repo: r.repo, Worktree: ghost.WorktreeID}.Key("dev"))
	if code, data = apiDo(t, http.MethodPost, r.base+"/api/devservers/"+gid+"/start", ghost, appClient); code != 404 {
		t.Fatalf("start in an untracked worktree = %d %s, want 404", code, data)
	}
	// A command the repo does not offer is refused.
	bad := api.DevServerActionRequest{RepoID: r.repo, Command: "rm-rf"}
	bid := devservers.IDOf(devservers.Target{Repo: r.repo}.Key("rm-rf"))
	if code, _ = apiDo(t, http.MethodPost, r.base+"/api/devservers/"+bid+"/start", bad, appClient); code != 400 {
		t.Fatalf("start of an unknown command = %d, want 400", code)
	}
	if got := len(r.fake.Started()); got != 1 {
		t.Fatalf("%d processes started, want 1", got)
	}
}

// Opening a terminal is audited (open and close, IDs and cause only), and the
// shell starts in the tracked folder with the clean environment.
func TestPhase5TerminalOpenAndCloseAreAudited(t *testing.T) {
	r := newPhase5Rig(t)
	ctx := context.Background()
	token, _, err := r.a.terminal.Issue(ctx, ct.Target{Worktree: r.wt})
	if err != nil {
		t.Fatal(err)
	}
	sock := terminaltest.NewMemSocket()
	done := make(chan error, 1)
	go func() { done <- r.a.terminal.Open(ctx, token, sock) }()
	r.waitAudit(t, "terminal.open")
	specs := r.pty.Specs()
	if len(specs) != 1 || specs[0].Dir != r.wtDir {
		t.Fatalf("shell specs = %+v, want one in the worktree", specs)
	}
	sock.Disconnect()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the session did not end")
	}
	r.waitAudit(t, "terminal.close")
	var detail string
	if err := r.side.db.QueryRow(`SELECT detail FROM audit_log WHERE action = 'terminal.open'`).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains([]byte(detail), []byte(r.wtDir)) || bytes.Contains([]byte(detail), []byte(token)) {
		t.Fatalf("terminal audit detail holds a path or the token: %s", detail)
	}
}

// Profile copy (WE-01): an agent task copies nothing by default; the toggle
// turns it on; a user-confirmed copy through the API copies and never
// overwrites.
func TestPhase5ProfileCopyAgentDefaultsToNothing(t *testing.T) {
	r := newPhase5Rig(t)
	ctx := context.Background()
	put := api.RepoEnvDTO{RepoID: r.repo, ProfilePaths: []string{".env"}}
	if code, data := apiDo(t, http.MethodPut, r.base+"/api/repos/"+string(r.repo)+"/env", put, appClient); code != 200 {
		t.Fatalf("PUT env = %d %s", code, data)
	}
	w, err := r.a.store.GetWorktree(ctx, r.wt)
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(r.wtDir, ".env")

	if err := r.a.envSvc.Prepare(ctx, w, wenv.TriggerAgentTask); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(dest); err == nil {
		t.Fatal("an agent task worktree got the profile files without the toggle")
	}
	blocks, err := r.a.store.ListLiveWorktreePorts(ctx)
	if err != nil || len(blocks) != 1 || blocks[0].Worktree != r.wt {
		t.Fatalf("Prepare must still allocate the port block: %+v %v", blocks, err)
	}

	// The user confirms the copy: the file arrives, once.
	var we api.WorktreeEnvDTO
	code, data := apiDo(t, http.MethodPost, r.base+"/api/worktrees/"+string(r.wt)+"/profile/copy", map[string]string{}, appClient)
	if code != 200 || json.Unmarshal(data, &we) != nil || len(we.Profile) != 1 || we.Profile[0].Result != "copied" {
		t.Fatalf("copy = %d %s", code, data)
	}
	if b, err := os.ReadFile(dest); err != nil || string(b) != "API_URL=http://localhost\n" {
		t.Fatalf("copied file = %q, %v", b, err)
	}
	if err := os.WriteFile(dest, []byte("EDITED=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, data = apiDo(t, http.MethodPost, r.base+"/api/worktrees/"+string(r.wt)+"/profile/copy", map[string]string{}, appClient)
	if code != 200 || json.Unmarshal(data, &we) != nil || we.Profile[0].Result != "skipped" {
		t.Fatalf("second copy = %d %s", code, data)
	}
	if b, _ := os.ReadFile(dest); string(b) != "EDITED=1\n" {
		t.Fatal("a second copy overwrote the file")
	}
	if we.BlockSize != core.PortBlockSize {
		t.Fatalf("worktree env has no port block: %+v", we)
	}
}

// A removed worktree gives its port block back (WE-02).
func TestPhase5RemovedWorktreeReleasesItsPortBlock(t *testing.T) {
	r := newPhase5Rig(t)
	ctx := context.Background()
	if _, err := r.a.envSvc.Allocate(ctx, r.wt); err != nil {
		t.Fatal(err)
	}
	releaseRemoved(ctx, r.a.store, r.a.envSvc, r.a.log)
	if live, _ := r.a.store.ListLiveWorktreePorts(ctx); len(live) != 1 {
		t.Fatalf("an active worktree lost its block: %+v", live)
	}
	if _, err := r.side.db.Exec(`UPDATE worktrees SET status = 'removed' WHERE id = ?`, string(r.wt)); err != nil {
		t.Fatal(err)
	}
	releaseRemoved(ctx, r.a.store, r.a.envSvc, r.a.log)
	if live, _ := r.a.store.ListLiveWorktreePorts(ctx); len(live) != 0 {
		t.Fatalf("a removed worktree kept its block: %+v", live)
	}
}

func TestCommandKey(t *testing.T) {
	for in, want := range map[string]string{"Run": "run", "dev server": "dev-server", "  ": "dev-server", "Start API!": "start-api"} {
		if got := commandKey(in); got != want {
			t.Errorf("commandKey(%q) = %q, want %q", in, got, want)
		}
	}
}
