package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/changesets"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
	"github.com/brendatamaar/yggdrasil/internal/proposals"
	"github.com/brendatamaar/yggdrasil/internal/search/searchtest"
	"github.com/brendatamaar/yggdrasil/internal/tasks"
)

// A task with a real worktree on a branch with one commit, in a tracked repo.
type landingRig struct {
	a       *app
	base    string
	env     []string
	repoDir string
	wtDir   string
	task    core.Task
	repo    core.RepoID
	mainTip string
}

func newLandingRig(t *testing.T) *landingRig {
	t.Helper()
	cfg := config{addr: "127.0.0.1:0", dataDir: t.TempDir(), writeModes: true}
	a, base := startFullApp(t, cfg)
	env := searchtest.GitEnv(t)
	repoDir := searchtest.InitRepo(t, env, map[string]string{"README.md": "# shop-api\n"})
	var out bytes.Buffer
	if err := seed(context.Background(), &out, cfg, repoDir, ""); err != nil {
		t.Fatal(err)
	}
	ids := regexp.MustCompile(`(prj|rep)_[0-9a-f]+`).FindAllString(out.String(), -1)
	project, repoID := core.ProjectID(ids[0]), core.RepoID(ids[1])
	mainTip := searchtest.GitOutput(t, env, repoDir, "", "rev-parse", "HEAD")

	task := dbtest.Task(t, a.store, core.Task{ProjectID: project, Scope: core.ScopeRepo, RepoIDs: []core.RepoID{repoID}, Mode: core.ModeCode, Status: core.TaskReview})
	wtDir := filepath.Join(cfg.dataDir, "wt", "cart")
	searchtest.RunGit(t, env, repoDir, "worktree", "add", "-q", "-b", "ygg/cart", wtDir)
	if err := os.WriteFile(filepath.Join(wtDir, "cart.txt"), []byte("one item\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	searchtest.RunGit(t, env, wtDir, "add", "cart.txt")
	searchtest.RunGit(t, env, wtDir, "commit", "-q", "-m", "feat: cart")
	if err := a.store.Commit(context.Background(), func(tx *db.Tx) error {
		_, err := tx.RecordWorktree(context.Background(), core.Worktree{
			ID: "wt_p4", RepoID: repoID, TaskID: task.ID, Name: "cart", Path: wtDir, Branch: "ygg/cart", CreatedAt: time.Now().UTC(),
		}, mainTip, core.AuditEntry{ID: core.AuditID(db.NewID("aud")), At: time.Now().UTC(), Actor: "test", Action: "worktree.create", Subject: "worktree:wt_p4", Detail: []byte(`{}`)})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return &landingRig{a: a, base: base, env: env, repoDir: repoDir, wtDir: wtDir, task: task, repo: repoID, mainTip: mainTip}
}

// The app boots with every Phase 4 service bound, and the endpoints answer
// from real data: the diff of a worktree, the verifications of a task, its
// notes, and a merge-local that only proposes (RV-05) until the user confirms.
func TestPhase4ServicesAreWiredAndMergeLocalWaitsForConfirm(t *testing.T) {
	r := newLandingRig(t)
	task, base := string(r.task.ID), r.base

	var diff api.WorktreeDiffDTO
	if code := getJSON(t, base+"/api/tasks/"+task+"/repos/"+string(r.repo)+"/diff", &diff); code != 200 {
		t.Fatalf("diff = %d", code)
	}
	if len(diff.Files) != 1 || diff.Files[0].Path != "cart.txt" || diff.Text == "" {
		t.Fatalf("diff = %+v", diff)
	}
	if code := getJSON(t, base+"/api/tasks/tsk_0000000000000000/repos/"+string(r.repo)+"/diff", &diff); code != 404 {
		t.Fatalf("diff of a task with no worktree = %d, want 404", code)
	}

	var vs api.VerificationsResponse
	if code := getJSON(t, base+"/api/tasks/"+task+"/verifications", &vs); code != 200 {
		t.Fatalf("verifications = %d", code)
	}
	var notes api.ReviewNotesResponse
	if code := getJSON(t, base+"/api/tasks/"+task+"/notes", &notes); code != 200 || len(notes.Notes) != 0 {
		t.Fatalf("notes = %d %+v", code, notes)
	}

	if code, _ := apiDo(t, http.MethodPost, base+"/api/worktrees/wt_missing000000000/merge-local", map[string]string{}, appClient); code != 404 {
		t.Fatalf("merge-local of a missing worktree = %d, want 404", code)
	}

	// Proposing changes nothing.
	var p api.ProposalDTO
	code, data := apiDo(t, http.MethodPost, base+"/api/worktrees/wt_p4/merge-local", map[string]string{}, appClient)
	if code != 201 {
		t.Fatalf("merge-local = %d %s", code, data)
	}
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	if p.Status != core.ProposalPending || p.Kind != core.ProposalMergeLocal {
		t.Fatalf("proposal = %+v", p)
	}
	if got := searchtest.GitOutput(t, r.env, r.repoDir, "", "rev-parse", "HEAD"); got != r.mainTip {
		t.Fatal("the main checkout moved before any confirmation")
	}
	if _, err := os.Stat(filepath.Join(r.repoDir, "cart.txt")); err == nil {
		t.Fatal("the change reached the main checkout before any confirmation")
	}

	// The proposal made the task's change set, with the proposal as member.
	var cs api.ChangeSetDTO
	deadline := time.Now().Add(10 * time.Second)
	for {
		if code := getJSON(t, base+"/api/tasks/"+task+"/changeset", &cs); code == 200 && len(cs.Members) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("change set = %+v", cs)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if cs.Members[0].ProposalID != p.ID || cs.State != api.ChangeSetStateOpen {
		t.Fatalf("change set = %+v", cs)
	}

	// Only the user's confirmation merges (live probe: the task is in review).
	if code, data := apiDo(t, http.MethodPost, base+"/api/proposals/"+string(p.ID)+"/confirm", api.ConfirmProposalRequest{Hash: p.ActionHash}, appClient); code >= 300 {
		t.Fatalf("confirm = %d %s", code, data)
	}
	deadline = time.Now().Add(120 * time.Second)
	for {
		getJSON(t, base+"/api/proposals/"+string(p.ID), &p)
		if p.Status == core.ProposalExecuted {
			break
		}
		if p.Status == core.ProposalFailed || time.Now().After(deadline) {
			t.Fatalf("merge ended as %q: %+v", p.Status, p)
		}
		time.Sleep(25 * time.Millisecond)
	}
	if got := searchtest.GitOutput(t, r.env, r.repoDir, "", "rev-parse", "HEAD"); got == r.mainTip {
		t.Fatal("the confirmed merge did not move the main checkout")
	}
}

// A task that is still running blocks the merge: the live probe answers.
func TestLiveProbeAnswersFromTheStoresAndFailsWithoutVerify(t *testing.T) {
	r := newLandingRig(t)
	live := &liveProbe{store: r.a.store, builds: r.a.builds}
	live.verify.Store(r.a.verify)
	if got, err := live.TaskLive(context.Background(), r.task.ID); err != nil || got {
		t.Fatalf("task in review: live = %v, %v", got, err)
	}
	got, err := live.BuildLive(context.Background(), "wt_p4")
	if err != nil || got {
		t.Fatalf("idle worktree: build live = %v, %v", got, err)
	}
	if _, err := (&liveProbe{store: r.a.store, builds: r.a.builds}).BuildLive(context.Background(), "wt_p4"); err == nil {
		t.Fatal("a probe with no verify service answered instead of failing")
	}
}

func TestRefusalsOfTheTaskServiceAreClassified(t *testing.T) {
	busy := &tasks.RejectedError{Reason: "task_busy", Message: "busy"}
	// Verify: a refusal is a Conflict (a certain "not taken"); anything else is unclassified.
	if c, ok := apperr.ClassOf(verifyRefusal(busy)); !ok || c != apperr.Conflict {
		t.Fatalf("class = %v %v", c, ok)
	}
	if _, ok := apperr.ClassOf(verifyRefusal(errors.New("timeout"))); ok {
		t.Fatal("an unknown error was classified as a refusal")
	}
	if verifyRefusal(nil) != nil {
		t.Fatal("nil became an error")
	}
	// Change sets: busy and queue-full are named, other errors pass through.
	if err := noteRefusal(busy); !errors.Is(err, changesets.ErrAgentBusy) {
		t.Fatalf("busy = %v", err)
	}
	if err := noteRefusal(&tasks.RejectedError{Reason: "queue_full", Message: "full"}); !errors.Is(err, changesets.ErrQueueFull) {
		t.Fatalf("full = %v", err)
	}
	other := errors.New("boom")
	if err := noteRefusal(other); !errors.Is(err, other) || errors.Is(err, changesets.ErrAgentBusy) {
		t.Fatalf("other = %v", err)
	}
}

type recPublisher struct {
	mu  sync.Mutex
	got []core.ProposalID
}

func (p *recPublisher) Publish(e proposals.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.got = append(p.got, e.ID)
}

// Publish forwards to the hub at once and never blocks, even with no change-set
// service bound or run loop running.
func TestProposalFanoutForwardsAndNeverBlocks(t *testing.T) {
	next := &recPublisher{}
	f := newProposalFanout(next, nil)
	done := make(chan struct{})
	go func() {
		for range 1000 {
			f.Publish(proposals.Event{ID: "prp_0000000000000001"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked")
	}
	if len(next.got) != 1000 {
		t.Fatalf("forwarded %d", len(next.got))
	}
	if len(f.pending) != 1 {
		t.Fatalf("the same proposal was queued %d times", len(f.pending))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.run(ctx); err != nil {
		t.Fatal(err)
	}
}

// Shutdown stops verify, change sets and the fan-out with the errgroup, before
// the store closes: run returns cleanly and the store is closed afterwards.
func TestShutdownStopsPhase4LoopsBeforeTheStoreCloses(t *testing.T) {
	cfg := config{addr: "127.0.0.1:0", dataDir: t.TempDir()}
	assets := fstest.MapFS{"index.html": {Data: []byte("<html>ygg</html>")}}
	var logs bytes.Buffer
	a, err := newApp(context.Background(), cfg, assets, &logs)
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
	time.Sleep(200 * time.Millisecond) // let the loops start and run a pass
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("run did not return")
	}
	if _, err := a.store.GetTask(context.Background(), "tsk_0000000000000000"); err == nil || errors.Is(err, db.ErrNotFound) {
		t.Fatalf("the store should be closed after run, got %v", err)
	}
}

// Pins which task statuses count as a live agent, and that a full build list
// fails closed.
func TestLiveProbeStatusesAndFullListFailClosed(t *testing.T) {
	want := map[core.TaskStatus]bool{core.TaskQueued: true, core.TaskRunning: true, core.TaskNeedsYou: true}
	for _, st := range []core.TaskStatus{core.TaskDraft, core.TaskQueued, core.TaskRunning, core.TaskNeedsYou, core.TaskReview, core.TaskDone, core.TaskFailed, core.TaskCancelled} {
		if got := taskStatusLive(st); got != want[st] {
			t.Errorf("taskStatusLive(%s) = %v", st, got)
		}
	}
	if listFull(buildListLimit-1) || !listFull(buildListLimit) {
		t.Error("a list at the limit must count as possibly cut off")
	}
}
