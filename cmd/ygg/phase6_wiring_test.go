package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"testing/fstest"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
	"github.com/brendatamaar/yggdrasil/internal/notify"
	"github.com/brendatamaar/yggdrasil/internal/search/searchtest"
)

// p6Rig is a running app with git repos in known states, tracked through the
// same seed path as the other wiring tests.
type p6Rig struct {
	a    *app
	base string
	cfg  config
	env  []string
}

func newP6Rig(t *testing.T) *p6Rig {
	t.Helper()
	cfg := config{addr: "127.0.0.1:0", dataDir: t.TempDir(), writeModes: true}
	a, base := startFullApp(t, cfg)
	return &p6Rig{a: a, base: base, cfg: cfg, env: searchtest.GitEnv(t)}
}

type p6Repo struct {
	project core.ProjectID
	id      core.RepoID
	dir     string
	bare    string
}

// newRepo makes a git repo with an origin (a local bare folder, no network) and
// tracks it.
func (r *p6Rig) newRepo(t *testing.T) *p6Repo {
	t.Helper()
	dir := searchtest.InitRepo(t, r.env, map[string]string{"README.md": "# shop-api\n"})
	setRepoAuthor(t, r.env, dir)
	bare := t.TempDir()
	searchtest.RunGit(t, r.env, bare, "init", "--bare")
	searchtest.RunGit(t, r.env, dir, "remote", "add", "origin", bare)
	searchtest.RunGit(t, r.env, dir, "push", "-u", "origin", "HEAD")
	var out bytes.Buffer
	if err := seed(context.Background(), &out, r.cfg, dir, ""); err != nil {
		t.Fatal(err)
	}
	ids := regexp.MustCompile(`(prj|rep)_[0-9a-f]+`).FindAllString(out.String(), -1)
	return &p6Repo{project: core.ProjectID(ids[0]), id: core.RepoID(ids[1]), dir: dir, bare: bare}
}

// remoteCommit adds one commit to origin from a second clone, then fetches it,
// so the tracked repo is behind its upstream.
func (r *p6Rig) remoteCommit(t *testing.T, p *p6Repo) {
	t.Helper()
	clone := filepath.Join(t.TempDir(), "clone")
	searchtest.RunGit(t, r.env, filepath.Dir(clone), "clone", p.bare, clone)
	setRepoAuthor(t, r.env, clone)
	if err := os.WriteFile(filepath.Join(clone, "remote.txt"), []byte("from the remote\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	searchtest.RunGit(t, r.env, clone, "add", "remote.txt")
	searchtest.RunGit(t, r.env, clone, "commit", "-m", "remote change")
	searchtest.RunGit(t, r.env, clone, "push", "origin", "HEAD")
	searchtest.RunGit(t, r.env, p.dir, "fetch", "origin")
}

func (r *p6Rig) localCommit(t *testing.T, p *p6Repo, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(p.dir, name), []byte(name+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	searchtest.RunGit(t, r.env, p.dir, "add", name)
	searchtest.RunGit(t, r.env, p.dir, "commit", "-m", "local "+name)
}

func (r *p6Rig) head(t *testing.T, p *p6Repo) string {
	t.Helper()
	return searchtest.GitOutput(t, r.env, p.dir, "", "rev-parse", "HEAD")
}

func (r *p6Rig) tree(t *testing.T, p *p6Repo) string {
	t.Helper()
	return r.head(t, p) + "\n" + searchtest.GitOutput(t, r.env, p.dir, "", "status", "--porcelain=v1")
}

// refresh re-reads every repo and waits until each tracked repo has a status.
func (r *p6Rig) refresh(t *testing.T, repos ...*p6Repo) {
	t.Helper()
	ctx := context.Background()
	if err := r.a.status.ReloadRepos(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.a.status.Refresh(ctx, nil, corerepos.ReasonUser); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		snap, err := r.a.status.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ready := len(snap.InFlight) == 0
		for _, p := range repos {
			e, ok := snap.Entries[p.id]
			if _, unknown := e.View.(corerepos.Unknown); !ok || unknown {
				ready = false
			}
		}
		if ready {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the repo status was not read in time")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (r *p6Rig) get(t *testing.T, path string, v any) int {
	t.Helper()
	return getJSON(t, r.base+path, v)
}

func (r *p6Rig) post(t *testing.T, path string, body, v any) (int, []byte) {
	t.Helper()
	code, data := apiDo(t, http.MethodPost, r.base+path, body, appClient)
	if v != nil && code == 200 {
		if err := json.Unmarshal(data, v); err != nil {
			t.Fatalf("decode %s: %v\n%s", path, err, data)
		}
	}
	return code, data
}

func (r *p6Rig) catalogue(t *testing.T, p *p6Repo) api.ActionCatalogueResponse {
	t.Helper()
	var c api.ActionCatalogueResponse
	if code := r.get(t, "/api/repos/"+string(p.id)+"/actions", &c); code != 200 {
		t.Fatalf("catalogue = %d", code)
	}
	return c
}

func (r *p6Rig) offering(t *testing.T, p *p6Repo, kind api.ActionKind) api.ActionOfferingDTO {
	t.Helper()
	for _, o := range r.catalogue(t, p).Offerings {
		if o.Kind == kind {
			return o
		}
	}
	t.Fatalf("no %s offering in %+v", kind, r.catalogue(t, p))
	return api.ActionOfferingDTO{}
}

// execute previews and executes one action the way the browser does.
func (r *p6Rig) execute(t *testing.T, req api.ActionRequestDTO) (api.ActionPreviewDTO, api.ActionResultDTO) {
	t.Helper()
	var pv api.ActionPreviewDTO
	if code, data := r.post(t, "/api/actions/preview", req, &pv); code != 200 {
		t.Fatalf("preview = %d %s", code, data)
	}
	var res api.ActionResultDTO
	exec := api.ActionExecuteRequest{ActionRequestDTO: req, Digest: pv.Digest, ExpectedHead: pv.ExpectedHead}
	if code, data := r.post(t, "/api/actions/execute", exec, &res); code != 200 {
		t.Fatalf("execute = %d %s", code, data)
	}
	return pv, res
}

// Every Phase 6 endpoint bound by W3a answers, and none answers 501. New repos
// stay unbound until W3b: they answer 501.
func TestPhase6ServicesAreWired(t *testing.T) {
	r := newP6Rig(t)
	p := r.newRepo(t)
	r.refresh(t, p)
	id := string(p.id)
	for _, path := range []string{
		"/api/repos/" + id + "/actions",
		"/api/repos/" + id + "/ci/runs/1/failed-jobs",
		"/api/repos/" + id + "/issues",
		"/api/repos/" + id + "/issues/1",
		"/api/repos/" + id + "/mrs/1/context",
		"/api/repos/" + id + "/noise",
		"/api/notifications",
		"/api/projects/" + string(p.project) + "/standup?from=" + url.QueryEscape(time.Now().Add(-24*time.Hour).UTC().Format(time.RFC3339)) + "&to=" + url.QueryEscape(time.Now().Add(time.Hour).UTC().Format(time.RFC3339)),
	} {
		if code := r.get(t, path, nil); code != 200 {
			t.Fatalf("GET %s = %d, want 200", path, code)
		}
	}
	// A tracked repo without a linked host is a blocked answer, not an error.
	var issues api.IssuesResponse
	r.get(t, "/api/repos/"+id+"/issues", &issues)
	if issues.Status.Phase != api.HostContextBlocked || issues.Status.Reason != api.HostContextUntracked {
		t.Fatalf("issues of an unlinked repo = %+v", issues.Status)
	}
	// An unknown repo is 404 for every read, and a draft that does not exist too.
	for _, path := range []string{"/api/repos/rep_0000000000000000/issues", "/api/repos/rep_0000000000000000/actions", "/api/repos/rep_0000000000000000/noise"} {
		if code := r.get(t, path, nil); code != 404 {
			t.Fatalf("GET %s = %d, want 404", path, code)
		}
	}
	if code, _ := r.post(t, "/api/tasks/tsk_0000000000000000/start", api.StartDraftRequest{Mode: core.ModeAsk}, nil); code != 404 {
		t.Fatalf("start of an unknown draft = %d, want 404", code)
	}
	if code := r.get(t, "/api/projects/"+string(p.project)+"/new-repos", nil); code != http.StatusNotImplemented {
		t.Fatalf("new repos = %d, want 501 until W3b binds them", code)
	}
}

// The standup refresh reads the commits, the cached host data and the stored
// builds. Its MR source takes the narrow cache interface (no fetch, no token),
// so a refresh makes no network call; here the repo's only remote is a local
// folder, and the summary still carries the commit.
func TestPhase6StandupRefreshReadsLocalData(t *testing.T) {
	r := newP6Rig(t)
	p := r.newRepo(t)
	r.refresh(t, p)
	from, to := time.Now().Add(-24*time.Hour).UTC(), time.Now().Add(time.Hour).UTC()
	var s api.StandupResponse
	q := "?from=" + url.QueryEscape(from.Format(time.RFC3339)) + "&to=" + url.QueryEscape(to.Format(time.RFC3339))
	if code := r.get(t, "/api/projects/"+string(p.project)+"/standup"+q, &s); code != 200 {
		t.Fatalf("standup = %d", code)
	}
	if len(s.Commits) == 0 || s.Phase == api.StandupFailed {
		t.Fatalf("standup = %+v, want the initial commit", s)
	}
	// The explicit polish starts one Ask task through the action path.
	var res api.ActionResultDTO
	body := api.StandupPolishRequest{RequestID: "req_polish_1", From: from, To: to, Agent: "mock"}
	if code, data := r.post(t, "/api/projects/"+string(p.project)+"/standup/polish", body, &res); code != 200 || res.TaskID == "" {
		t.Fatalf("polish = %d %s", code, data)
	}
	task, err := r.a.store.GetTask(context.Background(), res.TaskID)
	if err != nil || task.Mode != core.ModeAsk {
		t.Fatalf("the polish task = %+v, %v; want Ask mode", task, err)
	}
}

// A pull offered for a repo that is behind makes a pending proposal and pulls
// nothing; a head that moved after the preview refuses the execution.
func TestPhase6PullActionCreatesAPendingProposalOnly(t *testing.T) {
	r := newP6Rig(t)
	p := r.newRepo(t)
	r.remoteCommit(t, p)
	r.refresh(t, p)
	before := r.tree(t, p)

	o := r.offering(t, p, api.ActionPullAndCheck)
	req := api.ActionRequestDTO{RequestID: "req_pull_1", RepoID: p.id, Kind: o.Kind}
	_, res := r.execute(t, req)
	if res.State != api.ActionStateCompleted || len(res.ProposalIDs) != 1 {
		t.Fatalf("result = %+v, want one proposal", res)
	}
	var list api.ProposalsResponse
	if code := r.get(t, "/api/proposals?status=pending", &list); code != 200 || len(list.Proposals) != 1 || list.Proposals[0].ID != res.ProposalIDs[0] {
		t.Fatalf("pending = %d %+v", code, list)
	}
	if got := r.tree(t, p); got != before {
		t.Fatalf("the pull ran before any confirmation: tree was\n%s\nnow\n%s", before, got)
	}

	// Stale: the head moves after the preview. The execution is refused and no
	// second proposal exists.
	var pv api.ActionPreviewDTO
	req2 := api.ActionRequestDTO{RequestID: "req_pull_2", RepoID: p.id, Kind: o.Kind}
	if code, data := r.post(t, "/api/actions/preview", req2, &pv); code != 200 {
		t.Fatalf("preview = %d %s", code, data)
	}
	r.localCommit(t, p, "moved.txt")
	var res2 api.ActionResultDTO
	exec := api.ActionExecuteRequest{ActionRequestDTO: req2, Digest: pv.Digest, ExpectedHead: pv.ExpectedHead}
	r.post(t, "/api/actions/execute", exec, &res2)
	if res2.State != api.ActionStateRefused || len(res2.ProposalIDs) != 0 {
		t.Fatalf("a stale head gave %+v, want a refusal and no proposal", res2)
	}
	r.get(t, "/api/proposals?status=pending", &list)
	if len(list.Proposals) != 1 {
		t.Fatalf("pending proposals = %d, want still 1", len(list.Proposals))
	}
}

// E6: an unreadable folder keeps a waiting draft; no process or worktree.
func TestPhase6UnreadableRepoActionEndsInAWaitingDraft(t *testing.T) {
	r := newP6Rig(t)
	p := r.newRepo(t)
	r.refresh(t, p)
	if err := os.RemoveAll(p.dir); err != nil {
		t.Fatal(err)
	}
	r.refresh(t, p)
	o := r.offering(t, p, api.ActionDiagnoseRepo)
	if !o.WaitingForFolder || o.Reason != api.ReasonUnreadable {
		t.Fatalf("offering = %+v, want waiting for the folder", o)
	}
	_, res := r.execute(t, api.ActionRequestDTO{RequestID: "req_diag_1", RepoID: p.id, Kind: o.Kind, Agent: "mock"})
	if res.State != api.ActionStateCompleted || res.TaskID == "" {
		t.Fatalf("result = %+v", res)
	}
	task, err := r.a.store.GetTask(context.Background(), res.TaskID)
	if err != nil || task.Status != core.TaskDraft || task.Mode != core.ModeAsk {
		t.Fatalf("task = %+v, %v; want an Ask draft", task, err)
	}
}

// ErrClosed of the action service and the notification producer is a 503
// "shutting down", never a 500.
func TestPhase6ClosedServicesAnswer503(t *testing.T) {
	r := newP6Rig(t)
	p := r.newRepo(t)
	r.refresh(t, p)
	if err := r.a.phase6.actions.Close(); err != nil {
		t.Fatal(err)
	}
	if code := r.get(t, "/api/repos/"+string(p.id)+"/actions", nil); code != http.StatusServiceUnavailable {
		t.Fatalf("catalogue after Close = %d, want 503", code)
	}
	// The preview of an action answers 503 as well.
	if code, _ := r.post(t, "/api/actions/preview", api.ActionRequestDTO{RequestID: "req_closed_1", RepoID: p.id, Kind: api.ActionDiagnoseRepo, Agent: "mock"}, nil); code != http.StatusServiceUnavailable {
		t.Fatalf("preview after Close = %d, want 503", code)
	}
}

// Shutdown order: a notice handed over just before the stop is committed before
// the store closes (closeNotices runs after tasks and before the database).
func TestPhase6ShutdownCommitsQueuedNotices(t *testing.T) {
	cfg := config{addr: "127.0.0.1:0", dataDir: t.TempDir(), writeModes: true}
	assets := fstest.MapFS{"index.html": {Data: []byte("<html>ygg</html>")}}
	a, err := newApp(context.Background(), cfg, assets, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.run(ctx, ln) }()
	t.Cleanup(cancel)

	env := searchtest.GitEnv(t)
	dir := searchtest.InitRepo(t, env, map[string]string{"README.md": "# x\n"})
	var out bytes.Buffer
	if err := seed(context.Background(), &out, cfg, dir, ""); err != nil {
		t.Fatal(err)
	}
	ids := regexp.MustCompile(`(prj|rep)_[0-9a-f]+`).FindAllString(out.String(), -1)
	task := dbtest.Task(t, a.store, core.Task{ProjectID: core.ProjectID(ids[0]), Scope: core.ScopeRepo, RepoIDs: []core.RepoID{core.RepoID(ids[1])}, Mode: core.ModeAsk, Status: core.TaskReview})
	// A slow job first keeps the notice queued while the app stops, so only the
	// shutdown order decides whether it reaches the store.
	a.phase6.feeder.enqueue(func(context.Context, *notify.Producer) error {
		time.Sleep(500 * time.Millisecond)
		return nil
	})
	a.phase6.feeder.enqueue(func(ctx context.Context, p *notify.Producer) error {
		return p.TaskStatus(ctx, task.ID, task.Title, core.TaskReview, 7)
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
	side := openSideDB(t, cfg.dataDir)
	var n int
	if err := side.db.QueryRow(`SELECT COUNT(*) FROM notices WHERE task_id = ?`, string(task.ID)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("%d notices after shutdown, want 1: the queued notice was lost", n)
	}
}
