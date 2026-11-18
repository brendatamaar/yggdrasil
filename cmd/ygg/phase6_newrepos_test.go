package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	corenewrepos "github.com/brendatamaar/yggdrasil/internal/core/newrepos"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/hosts"
	"github.com/brendatamaar/yggdrasil/internal/proposals"
	"github.com/brendatamaar/yggdrasil/internal/search/searchtest"
)

// ---- fakes: a git host and a publish transport that count every call ----

type fakeRepoHost struct {
	mu    sync.Mutex
	calls map[string]int
	repos map[string]string // owner/name -> description (the operation marker)
	// loseReply makes CreateRepo create the repository, then block until its
	// context ends and report an unknown outcome (a lost reply).
	loseReply bool
	entered   chan struct{}
}

func newFakeRepoHost() *fakeRepoHost {
	return &fakeRepoHost{calls: map[string]int{}, repos: map[string]string{}, entered: make(chan struct{}, 4)}
}

func (h *fakeRepoHost) count(op string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls[op]
}

// effects is every call that is not the read-only account check.
func (h *fakeRepoHost) effects() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for op, c := range h.calls {
		if op != "ResolveOwner" {
			n += c
		}
	}
	return n
}

func (h *fakeRepoHost) ResolveOwner(context.Context, corehosts.InstanceID, string) (hosts.RepoOwnerInfo, error) {
	h.mu.Lock()
	h.calls["ResolveOwner"]++
	h.mu.Unlock()
	return hosts.RepoOwnerInfo{Kind: "org", CanCreate: true}, nil
}

func (h *fakeRepoHost) CreateRepo(ctx context.Context, spec hosts.CreateRepoSpec) (hosts.CreatedRepo, error) {
	h.mu.Lock()
	h.calls["CreateRepo"]++
	h.repos[spec.Owner+"/"+spec.Name] = spec.Description
	lose := h.loseReply
	h.mu.Unlock()
	if lose {
		select {
		case h.entered <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return hosts.CreatedRepo{}, fmt.Errorf("%w: %w", hosts.ErrOutcomeUnknown, ctx.Err())
	}
	return hosts.CreatedRepo{RemoteID: "1001", URL: "https://" + string(spec.Instance) + "/" + spec.Owner + "/" + spec.Name + ".git"}, nil
}

func (h *fakeRepoHost) LookupRepo(_ context.Context, spec hosts.CreateRepoSpec) (hosts.RepoLookup, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls["LookupRepo"]++
	desc, ok := h.repos[spec.Owner+"/"+spec.Name]
	switch {
	case !ok:
		return hosts.RepoLookup{Outcome: hosts.RepoAbsent}, nil
	case desc != spec.Description:
		return hosts.RepoLookup{Outcome: hosts.RepoConflict}, nil
	}
	return hosts.RepoLookup{Outcome: hosts.RepoExactMatch, RemoteID: "1001",
		URL: "https://" + string(spec.Instance) + "/" + spec.Owner + "/" + spec.Name + ".git"}, nil
}

type fakePublishTransport struct {
	mu     sync.Mutex
	calls  map[string]int
	remote map[string]string // url -> head
}

func newFakePublishTransport() *fakePublishTransport {
	return &fakePublishTransport{calls: map[string]int{}, remote: map[string]string{}}
}

func (f *fakePublishTransport) count(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[op]
}

func (f *fakePublishTransport) RemoteBranch(_ context.Context, req git.PublishRequest) (git.RemoteRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["RemoteBranch"]++
	h, ok := f.remote[req.URL]
	return git.RemoteRef{Exists: ok, Head: h}, nil
}

func (f *fakePublishTransport) PublishBranch(_ context.Context, req git.PublishRequest) (git.PublishResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["PublishBranch"]++
	f.remote[req.URL] = req.ExpectedHead
	return git.PublishResult{Head: req.ExpectedHead}, nil
}

// ---- the rig ----

type nrRig struct {
	t       *testing.T
	cfg     config
	a       *app
	base    string
	stopApp func() error
	host    *fakeRepoHost
	tr      *fakePublishTransport
	project core.ProjectID
	parent  string
}

// startNRApp starts the app on cfg and returns how to stop it (run's own
// shutdown order), so a test can restart over the same data folder.
func startNRApp(t *testing.T, cfg config) (*app, string, func() error) {
	t.Helper()
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
	base := "http://" + ln.Addr().String()
	if tok := a.srv.LaunchToken(); tok != "" {
		openSession(t, base, tok)
	}
	var once sync.Once
	var stopErr error
	stop := func() error {
		once.Do(func() { cancel(); stopErr = <-done })
		return stopErr
	}
	t.Cleanup(func() { _ = stop() })
	return a, base, stop
}

// newNRRig tracks a project folder with one repo in it and starts the app with
// the fake host and transport. Git knows an author through a private HOME, so
// no machine identity is used.
func newNRRig(t *testing.T) *nrRig {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[user]\n\tname = Ada Test\n\temail = ada@example.test\n[commit]\n\tgpgsign = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	env := searchtest.GitEnv(t)
	parent := t.TempDir()
	repo := filepath.Join(parent, "shop-api")
	if err := os.MkdirAll(repo, 0o750); err != nil {
		t.Fatal(err)
	}
	searchtest.RunGit(t, env, repo, "init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# shop-api\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	searchtest.RunGit(t, env, repo, "add", "-A")
	searchtest.RunGit(t, env, repo, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "init")
	r := &nrRig{t: t, host: newFakeRepoHost(), tr: newFakePublishTransport(), parent: parent}
	r.cfg = config{addr: "127.0.0.1:0", dataDir: t.TempDir(), writeModes: true,
		newRepoFakes: &newRepoFakes{Remote: r.host, Transport: r.tr}}
	var out bytes.Buffer
	if err := seed(context.Background(), &out, r.cfg, parent, "acme-shop"); err != nil {
		t.Fatal(err)
	}
	ids := regexp.MustCompile(`(prj|rep)_[0-9a-f]+`).FindAllString(out.String(), -1)
	r.project = core.ProjectID(ids[0])
	r.a, r.base, r.stopApp = startNRApp(t, r.cfg)
	return r
}

// restart stops the app (the normal shutdown) and starts a new one over the
// same data folder and the same fakes.
func (r *nrRig) restart() {
	r.t.Helper()
	if err := r.stopApp(); err != nil {
		r.t.Fatalf("shutdown: %v", err)
	}
	r.a, r.base, r.stopApp = startNRApp(r.t, r.cfg)
}

func (r *nrRig) do(method, path string, body, out any) (int, []byte) {
	r.t.Helper()
	code, data := apiDo(r.t, method, r.base+path, body, appClient)
	if out != nil && code == 200 {
		if err := jsonUnmarshal(data, out); err != nil {
			r.t.Fatalf("%s %s: decode %s: %v", method, path, data, err)
		}
	}
	return code, data
}

func (r *nrRig) must(method, path string, body, out any) {
	r.t.Helper()
	if code, data := r.do(method, path, body, out); code != 200 {
		r.t.Fatalf("%s %s = %d %s", method, path, code, data)
	}
}

func (r *nrRig) wait(what string, ok func() bool) {
	r.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			r.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func (r *nrRig) op(id core.OperationID) api.NewRepoOperationDTO {
	r.t.Helper()
	var d api.NewRepoOperationDTO
	r.must(http.MethodGet, "/api/new-repos/"+string(id), nil, &d)
	return d
}

func (r *nrRig) proposal(id core.ProposalID) api.ProposalDTO {
	r.t.Helper()
	var p api.ProposalDTO
	r.must(http.MethodGet, "/api/proposals/"+string(id), nil, &p)
	return p
}

func (r *nrRig) waitState(id core.OperationID, want api.NewRepoState) api.NewRepoOperationDTO {
	r.t.Helper()
	var last api.NewRepoOperationDTO
	r.wait("the new repo to be "+string(want), func() bool { last = r.op(id); return last.State == want })
	return last
}

// reserve previews and reserves a new repo, the way the browser does.
func (r *nrRig) reserve(leaf string) api.NewRepoOperationDTO {
	r.t.Helper()
	var pv api.NewRepoPreviewResponse
	r.must(http.MethodPost, "/api/projects/"+string(r.project)+"/new-repos/preview", api.NewRepoPreviewRequest{Leaf: leaf}, &pv)
	if len(pv.Problems) != 0 {
		r.t.Fatalf("preview problems: %v", pv.Problems)
	}
	var op api.NewRepoOperationDTO
	r.must(http.MethodPost, "/api/projects/"+string(r.project)+"/new-repos", api.ReserveNewRepoRequest{
		RequestID: core.RequestID("req_" + leaf), NewRepoPreviewRequest: api.NewRepoPreviewRequest{Leaf: leaf}, Hash: pv.Hash,
	}, &op)
	return op
}

func (r *nrRig) confirm(id core.ProposalID) {
	r.t.Helper()
	p := r.proposal(id)
	r.must(http.MethodPost, "/api/proposals/"+string(id)+"/confirm", api.ConfirmProposalRequest{Hash: p.ActionHash}, nil)
}

// initRepo reserves, confirms and waits until the repo is ready.
func (r *nrRig) initRepo(leaf string) api.NewRepoOperationDTO {
	r.t.Helper()
	op := r.reserve(leaf)
	r.confirm(op.ProposalID)
	return r.waitState(op.OperationID, api.NewRepoReady)
}

func (r *nrRig) publishRequest(leaf string) api.PublishPreviewRequest {
	return api.PublishPreviewRequest{HostInstance: "github.com", Owner: "acme", OwnerKind: api.OwnerOrg, Name: leaf}
}

// publishProposal previews and makes the pending publish proposal.
func (r *nrRig) publishProposal(op core.OperationID, leaf string) api.ProposalDTO {
	r.t.Helper()
	var pv api.PublishPreviewResponse
	r.must(http.MethodPost, "/api/new-repos/"+string(op)+"/publish/preview", r.publishRequest(leaf), &pv)
	if len(pv.Problems) != 0 {
		r.t.Fatalf("publish preview problems: %v", pv.Problems)
	}
	var p api.ProposalDTO
	r.must(http.MethodPost, "/api/new-repos/"+string(op)+"/publish", api.PublishNewRepoRequest{
		RequestID: core.RequestID("req_pub_" + leaf), PublishPreviewRequest: r.publishRequest(leaf), Hash: pv.Hash,
	}, &p)
	return p
}

func (r *nrRig) leafDir(leaf string) string { return filepath.Join(r.parent, leaf) }

func (r *nrRig) sideState(op core.OperationID) string {
	r.t.Helper()
	side := openSideDB(r.t, r.cfg.dataDir)
	var state string
	if err := side.db.QueryRow(`SELECT state FROM repo_reservations WHERE operation_id = ?`, string(op)).Scan(&state); err != nil {
		r.t.Fatal(err)
	}
	return state
}

func jsonUnmarshal(data []byte, out any) error { return json.Unmarshal(data, out) }

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// mcpRPC calls one MCP tool with an agent's per-run token.
func mcpRPC(t *testing.T, base, token, tool string, args map[string]any) (string, bool) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
	req, err := http.NewRequest(http.MethodPost, base+"/mcp", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	raw := buf.String()
	return raw, resp.StatusCode != 200 || strings.Contains(raw, `"isError":true`) || strings.Contains(raw, `"error":{`)
}

// ---- tests ----

// Reserve makes a pending proposal and touches nothing; confirming the
// proposal (its action hash, as the browser does) initializes the folder and
// tracks the repo; the lifecycle got the immutable spec hash, not the action
// hash, so the init ran (NR-01, NR-02).
func TestNewRepoReserveConfirmInitializesAndTracks(t *testing.T) {
	r := newNRRig(t)
	op := r.reserve("shop-new")
	if op.State != api.NewRepoAwaitingConfirm || op.ProposalID == "" || op.SpecHash == "" {
		t.Fatalf("reserved = %+v, want a pending proposal and the spec hash", op)
	}
	p := r.proposal(op.ProposalID)
	if p.Status != core.ProposalPending || p.Kind != core.ProposalInitRepo || p.FromAgent {
		t.Fatalf("proposal = %+v", p)
	}
	if p.ActionHash == op.SpecHash {
		t.Fatal("the proposal's action hash equals the spec hash; the test cannot tell them apart")
	}
	if _, err := os.Lstat(r.leafDir("shop-new")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the folder exists before the confirmation: %v", err)
	}
	if r.host.effects() != 0 || r.tr.count("PublishBranch") != 0 {
		t.Fatal("a host call happened before the confirmation")
	}

	r.confirm(op.ProposalID)
	ready := r.waitState(op.OperationID, api.NewRepoReady)
	for _, s := range ready.Stages {
		if s.Status != api.StageDone {
			t.Fatalf("stages after ready = %+v", ready.Stages)
		}
	}
	r.wait("the init proposal to be executed", func() bool { return r.proposal(op.ProposalID).Status == core.ProposalExecuted })
	for _, f := range []string{".git", "README.md", ".gitignore"} {
		if _, err := os.Lstat(filepath.Join(r.leafDir("shop-new"), f)); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	repo, err := r.a.store.GetRepo(context.Background(), ready.RepoID)
	if err != nil || repo.Readiness != core.RepoReady && repo.Readiness != "" {
		t.Fatalf("the new repo is not tracked and ready: %+v, %v", repo, err)
	}
	if r.host.effects() != 0 {
		t.Fatalf("initializing called the host %d times", r.host.effects())
	}
}

// Publish: preview, then a pending proposal that creates nothing; only the
// confirmation creates the remote, adds origin and pushes (NR-03, NR-04).
func TestNewRepoPublishCreatesRemoteOnlyAfterConfirmation(t *testing.T) {
	r := newNRRig(t)
	ready := r.initRepo("shop-new")
	p := r.publishProposal(ready.OperationID, "shop-new")
	if p.Status != core.ProposalPending || p.Kind != core.ProposalPublish {
		t.Fatalf("publish proposal = %+v", p)
	}
	// Only the read-only account check ran for the preview and the proposal.
	if r.host.effects() != 0 || r.tr.count("PublishBranch") != 0 || r.tr.count("RemoteBranch") != 0 {
		t.Fatalf("a remote call happened before the confirmation: host %v transport %v", r.host.calls, r.tr.calls)
	}
	if got := r.op(ready.OperationID).State; got != api.NewRepoReady {
		t.Fatalf("state before the confirmation = %s", got)
	}
	// A repeated request returns the same pending proposal.
	again := r.publishProposal(ready.OperationID, "shop-new")
	if again.ID != p.ID {
		t.Fatalf("a repeated publish made another proposal: %s and %s", p.ID, again.ID)
	}

	r.confirm(p.ID)
	done := r.waitState(ready.OperationID, api.NewRepoPublished)
	if !strings.HasPrefix(done.RemoteURL, "https://github.com/acme/shop-new") {
		t.Fatalf("remote url = %q", done.RemoteURL)
	}
	if r.host.count("CreateRepo") != 1 || r.tr.count("PublishBranch") != 1 {
		t.Fatalf("host %v transport %v, want one create and one push", r.host.calls, r.tr.calls)
	}
	r.wait("the publish proposal to be executed", func() bool { return r.proposal(p.ID).Status == core.ProposalExecuted })
}

// Rejecting the init proposal settles its operation: stopped as rejected, no
// folder, no retry (NR-01).
func TestNewRepoRejectSettlesTheOperation(t *testing.T) {
	r := newNRRig(t)
	op := r.reserve("shop-no")
	r.must(http.MethodPost, "/api/proposals/"+string(op.ProposalID)+"/reject", nil, nil)
	got := r.waitState(op.OperationID, api.NewRepoStopped)
	if got.CanRetry || !strings.Contains(got.Reason, "rejected") {
		t.Fatalf("rejected operation = %+v", got)
	}
	if _, err := os.Lstat(r.leafDir("shop-no")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a rejected new repo left a folder: %v", err)
	}
}

// A rejection that was recorded for the proposal but never reached the
// operation (the process stopped between the two commits) is settled at the
// next start.
func TestNewRepoRejectionLostByACrashIsSettledAtStart(t *testing.T) {
	r := newNRRig(t)
	op := r.reserve("shop-lost")
	// Reject through the proposals service itself: the new-repo service is not told.
	if _, err := r.a.proposals.Reject(context.Background(), op.ProposalID, "user"); err != nil {
		t.Fatal(err)
	}
	if got := r.op(op.OperationID).State; got != api.NewRepoAwaitingConfirm {
		t.Fatalf("state = %s, the test needs the lost rejection", got)
	}
	r.restart()
	r.waitState(op.OperationID, api.NewRepoStopped)
}

// An agent can propose a publish (pending only) and can never create an
// InitRepo: no tool for it, and the proposals service refuses an agent's one
// (MCP-05, invariant 2).
func TestAgentProposesPublishOnlyAndNeverInit(t *testing.T) {
	r := newNRRig(t)
	ready := r.initRepo("shop-agent")
	task := dbtest.Task(t, r.a.store, core.Task{ProjectID: r.project, Scope: core.ScopeRepo, RepoIDs: []core.RepoID{ready.RepoID},
		Mode: core.ModeAsk, Status: core.TaskRunning})
	call := func(tool string, args map[string]any) (string, bool) {
		token, err := r.a.mcp.Issue(task.ID, 1)
		if err != nil {
			t.Fatal(err)
		}
		return mcpRPC(t, r.base, token, tool, args)
	}
	raw, isErr := call("propose_publish", map[string]any{"repo": string(ready.RepoID), "host": "github.com", "owner": "acme",
		"owner_kind": "org", "name": "shop-agent", "visibility": "private", "summary": "Publish it"})
	if isErr {
		t.Fatalf("propose_publish: %s", raw)
	}
	pending, err := r.a.proposals.List(context.Background(), proposals.ListFilter{Task: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, v := range pending {
		if v.Kind == core.ProposalPublish {
			found = true
			if v.Status != core.ProposalPending || !v.FromAgent {
				t.Fatalf("agent publish proposal = %+v, want pending from the agent", v)
			}
		}
	}
	if !found {
		t.Fatalf("no publish proposal was made: %s", raw)
	}
	if r.host.effects() != 0 || r.tr.count("PublishBranch") != 0 || r.host.count("CreateRepo") != 0 {
		t.Fatalf("an agent's proposal made a remote call: host %v transport %v", r.host.calls, r.tr.calls)
	}
	if _, isErr := call("propose_init_repo", map[string]any{"leaf": "x"}); !isErr {
		t.Fatal("an init tool exists for agents")
	}
	_, err = r.a.proposals.Create(context.Background(), proposals.CreateRequest{
		Task: ready.SetupTaskID, Source: coreprop.AgentSource{Task: task.ID},
		Action: coreprop.InitRepo{Operation: ready.OperationID, Project: r.project, Repo: ready.RepoID, Leaf: "x", Branch: "main", README: "x\n", Ignore: ".env\n", CommitMessage: "x"},
	})
	var ref *proposals.Refusal
	if !errors.As(err, &ref) {
		t.Fatalf("an agent's InitRepo was not refused: %v", err)
	}
}

// A crash while the remote was being created: the reply was lost. The shutdown
// records the stage Uncertain before the store closes; the next start
// reconciles it read-only (the fake host has the repo) and finishes without
// creating the remote a second time (NR-02, NR-03).
func TestNewRepoShutdownAndRestartRecoverWithoutRepeating(t *testing.T) {
	r := newNRRig(t)
	ready := r.initRepo("shop-crash")
	p := r.publishProposal(ready.OperationID, "shop-crash")
	r.host.mu.Lock()
	r.host.loseReply = true
	r.host.mu.Unlock()
	r.confirm(p.ID)
	select {
	case <-r.host.entered:
	case <-time.After(20 * time.Second):
		t.Fatal("the remote was never created")
	}
	if err := r.stopApp(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	// The worker's last state was committed before the store closed.
	if got := r.sideState(ready.OperationID); got != "uncertain" {
		t.Fatalf("state after shutdown = %q, want uncertain (the store closed before the worker stopped)", got)
	}
	r.host.mu.Lock()
	r.host.loseReply = false
	r.host.mu.Unlock()
	r.a, r.base, r.stopApp = startNRApp(t, r.cfg)
	done := r.waitState(ready.OperationID, api.NewRepoPublished)
	if r.host.count("CreateRepo") != 1 {
		t.Fatalf("CreateRepo ran %d times, want 1 (a completed stage was repeated)", r.host.count("CreateRepo"))
	}
	if r.tr.count("PublishBranch") != 1 || done.RemoteURL == "" {
		t.Fatalf("push count %d, remote %q", r.tr.count("PublishBranch"), done.RemoteURL)
	}
	r.wait("the publish proposal to follow the outcome", func() bool {
		return r.proposal(p.ID).Status == core.ProposalExecuted
	})
}

// Untracking the project while a stage runs stops the operation: it is not
// published, the unknown effect is left for a person, and nothing is deleted.
func TestNewRepoUntrackWhileRunningStopsTheOperation(t *testing.T) {
	r := newNRRig(t)
	ready := r.initRepo("shop-untrack")
	p := r.publishProposal(ready.OperationID, "shop-untrack")
	r.host.mu.Lock()
	r.host.loseReply = true
	r.host.mu.Unlock()
	r.confirm(p.ID)
	select {
	case <-r.host.entered:
	case <-time.After(20 * time.Second):
		t.Fatal("the remote was never created")
	}
	r.must(http.MethodPost, "/api/projects/"+string(r.project)+"/untrack", nil, nil)
	r.wait("the operation to stop", func() bool {
		st := r.sideState(ready.OperationID)
		return st == "stopped" || st == "uncertain"
	})
	if _, err := os.Lstat(filepath.Join(r.leafDir("shop-untrack"), ".git")); err != nil {
		t.Fatalf("the folder was touched by the untrack: %v", err)
	}
	if r.tr.count("PublishBranch") != 0 {
		t.Fatal("the push ran after the project was untracked")
	}
}

// The wiring maps a lost reply and a refusal of the comment writer for the
// proposals executor (AC-02).
func TestCommentHostClassifiesWriterErrors(t *testing.T) {
	cases := []struct {
		name        string
		err         error
		unknown     bool
		writeRefuse bool
	}{
		{"lost reply", fmt.Errorf("x: %w", hosts.ErrOutcomeUnknown), true, false},
		{"host rejected", hosts.ErrWriteRejected, false, true},
		{"token refused", &hosts.FetchError{Kind: corehosts.FailUnauthorized}, false, true},
		{"unclassified", errors.New("boom"), false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := commentHost{inner: errComments{err: c.err}}.PostReviewComment(context.Background(), "rep_0000000000000001", 1, "h", "b", "m")
			if errors.Is(err, proposals.ErrOutcomeUnknown) != c.unknown || errors.Is(err, proposals.ErrWriteRefused) != c.writeRefuse {
				t.Fatalf("%v: unknown=%v refused=%v", err, errors.Is(err, proposals.ErrOutcomeUnknown), errors.Is(err, proposals.ErrWriteRefused))
			}
		})
	}
}

type errComments struct{ err error }

func (e errComments) MRHead(context.Context, core.RepoID, int) (string, error) { return "", e.err }
func (e errComments) PostReviewComment(context.Context, core.RepoID, int, string, string, string) (string, error) {
	return "", e.err
}
func (e errComments) FindReviewComment(context.Context, core.RepoID, int, string, string, string) (coreprop.CommentLookup, error) {
	return coreprop.CommentLookup{}, e.err
}

// The push credential is bound to the one repository path and the configured
// host (invariants 3 and 4).
func TestPushAuthorizerBindsTheRepositoryPath(t *testing.T) {
	spy := &spyAuthorizer{}
	a := pushAuthorizer{confirmed: spy, instances: fixedInstances{{ID: "github.com", Kind: corehosts.KindGitHub, Base: "https://github.com"}}}
	req := func(u string) *http.Request {
		r, err := http.NewRequest(http.MethodGet, u, nil)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	ok := req("https://github.com/acme/shop.git/info/refs?service=git-receive-pack")
	if err := a.Authorize(context.Background(), ok); err != nil || ok.Header.Get("Authorization") != "Bearer secret" {
		t.Fatalf("authorize: %v header %q", err, ok.Header.Get("Authorization"))
	}
	for _, bad := range []string{
		"https://evil.test/acme/shop.git/info/refs", // another host
		"https://github.com/acme/shop/info/refs",    // no repository path
		"https://github.com/acme/shop.git/../other.git/info/refs",
	} {
		r := req(bad)
		if err := a.Authorize(context.Background(), r); err == nil || r.Header.Get("Authorization") != "" {
			t.Fatalf("%s: authorized (%v, %q)", bad, err, r.Header.Get("Authorization"))
		}
	}
	if spy.calls != 1 {
		t.Fatalf("the credential holder was asked %d times, want 1", spy.calls)
	}
}

type spyAuthorizer struct{ calls int }

func (s *spyAuthorizer) Authorize(_ context.Context, _ corehosts.InstanceID, req *http.Request) error {
	s.calls++
	req.Header.Set("Authorization", "Bearer secret")
	return nil
}

type fixedInstances []corehosts.Instance

func (f fixedInstances) Instances(context.Context) ([]corehosts.Instance, error) { return f, nil }

// The op notifier hands every change to the proposals service, in order and
// without blocking its caller, and drains what is queued on close.
func TestOpNotifierDrainsOnClose(t *testing.T) {
	var mu sync.Mutex
	var got []core.OperationID
	n := &opNotifier{log: discardLogger(), wake: make(chan struct{}, 1), seen: map[core.OperationID]bool{},
		follow: func(_ context.Context, op core.OperationID) error {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, op)
			return nil
		}}
	n.start(context.Background())
	n.notify("opr_1")
	n.notify("opr_2")
	n.close()
	n.notify("opr_3") // after close: dropped, never blocks
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0] != "opr_1" || got[1] != "opr_2" {
		t.Fatalf("followed %v, want opr_1 opr_2", got)
	}
}

var _ = db.ErrNotFound
var _ = corenewrepos.DefaultBranch
