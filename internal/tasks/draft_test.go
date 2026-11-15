package tasks

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
)

func issueSpec(h *harness) coretasks.DraftSpec {
	return coretasks.DraftSpec{RequestID: "req_issue", Project: h.project.ID, Repo: h.repos[0].ID,
		Issue: coretasks.IssueRef{Instance: "git.acme.dev", Project: "acme/shop-api", Number: 12},
		Title: "Fix login", Brief: "Issue context: users cannot log in.", Agent: "mock"}
}

func TestIssueImportHasZeroExecutionEffectsAndSurvivesRestart(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.start()
	ctx := context.Background()
	spec := issueSpec(h)
	result, err := h.svc.ImportIssueDraft(ctx, spec)
	if err != nil || !result.Created {
		t.Fatalf("import: %+v %v", result, err)
	}
	h.restart()
	again, err := h.svc.ImportIssueDraft(ctx, spec)
	if err != nil || again.Created || again.Task.ID != result.Task.ID {
		t.Fatalf("replay: %+v %v", again, err)
	}
	spec.RequestID = "req_other"
	again, err = h.svc.ImportIssueDraft(ctx, spec)
	if err != nil || again.Created || again.Task.ID != result.Task.ID {
		t.Fatalf("issue dedup: %+v %v", again, err)
	}
	turns, err := h.store.ListTaskTurns(ctx, result.Task.ID)
	if err != nil || len(turns) != 0 {
		t.Fatalf("turns: %v %v", turns, err)
	}
	wts, err := h.store.ListWorktrees(ctx, result.Task.ID)
	if err != nil || len(wts) != 0 || len(h.adapter.Starts()) != 0 {
		t.Fatalf("execution effects: %v %v", wts, err)
	}
	if !h.hasAudit(result.Task.ID, "task.import") || result.Task.Mode != core.ModeAsk || result.Task.Status != core.TaskDraft || result.Task.Source == nil {
		t.Fatalf("draft: %+v", result)
	}
	spec.Brief = "Changed"
	if _, err = h.svc.ImportIssueDraft(ctx, spec); !errors.Is(err, db.ErrDigestMismatch) {
		t.Fatalf("changed payload: %v", err)
	}
	start, err := h.svc.StartDraft(ctx, coretasks.DraftStart{Task: result.Task.ID, Mode: core.ModeAsk}, "mock")
	if err != nil {
		t.Fatal(err)
	}
	h.waitStatus(start.Task.ID, core.TaskDone)
	if _, err = h.svc.StartDraft(ctx, coretasks.DraftStart{Task: result.Task.ID, Mode: core.ModeAsk}, "mock"); err == nil {
		t.Fatal("started a non-draft")
	}
}

func TestConcurrentIssueImportsHaveOneWinner(t *testing.T) {
	for _, sameRequest := range []bool{false, true} {
		t.Run(map[bool]string{true: "same request", false: "same issue"}[sameRequest], func(t *testing.T) {
			h := newHarness(t, []string{"shop-api"})
			h.start()
			var wg sync.WaitGroup
			results := make([]DraftResult, 8)
			errs := make([]error, 8)
			for i := range results {
				wg.Add(1)
				go func() {
					defer wg.Done()
					spec := issueSpec(h)
					if !sameRequest {
						spec.RequestID = core.RequestID(db.NewID("req"))
					}
					results[i], errs[i] = h.svc.ImportIssueDraft(context.Background(), spec)
				}()
			}
			wg.Wait()
			created := 0
			for i, res := range results {
				if errs[i] != nil {
					t.Fatal(errs[i])
				}
				if res.Task.ID != results[0].Task.ID {
					t.Fatal("duplicate task")
				}
				if res.Created {
					created++
				}
			}
			if created != 1 {
				t.Fatalf("creations %d", created)
			}
			tasks, err := h.store.ListTasks(context.Background(), h.project.ID)
			if err != nil || len(tasks) != 1 {
				t.Fatalf("losing writes: %v %v", tasks, err)
			}
		})
	}
}

func TestIssueScopeAndSetupRefusals(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.start()
	ctx := context.Background()
	spec := issueSpec(h)
	other := dbtest.Project(t, h.store, core.Project{Name: "other", Path: t.TempDir()})
	spec.Project = other.ID
	if _, err := h.svc.ImportIssueDraft(ctx, spec); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("foreign scope: %v", err)
	}
	task := core.Task{ID: "tsk_setup", ProjectID: h.project.ID, RepoIDs: []core.RepoID{h.repos[0].ID}, Title: "Setup", Prompt: "setup", Scope: core.ScopeRepo, Mode: core.ModeAsk, Agent: "mock", Status: core.TaskDraft, CreatedAt: h.cfg.Now(), UpdatedAt: h.cfg.Now(), Source: &core.TaskSource{Kind: core.SourceRepoSetup}}
	if err := h.store.Commit(ctx, func(tx *db.Tx) error { _, err := tx.CreateTask(ctx, task); return err }); err != nil {
		t.Fatal(err)
	}
	_, err := h.svc.StartDraft(ctx, coretasks.DraftStart{Task: task.ID, Mode: core.ModeAsk}, "mock")
	var user *coretasks.UserError
	if !errors.As(err, &user) {
		t.Fatalf("setup started: %v", err)
	}
	req := h.msg("ask", "bypass", 0)
	req.TaskID = task.ID
	if _, err = h.svc.Message(ctx, req); !errors.As(err, &user) {
		t.Fatalf("setup bypass: %v", err)
	}
}

func TestRequestSubmissionConcurrentReplayAndImmutablePayload(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.start()
	ctx := context.Background()
	req := h.msg("ask", "explain", 0)
	var wg sync.WaitGroup
	results := make([]MessageResult, 8)
	errs := make([]error, 8)
	for i := range results {
		wg.Add(1)
		go func() { defer wg.Done(); results[i], errs[i] = h.svc.SubmitWithRequest(ctx, "req_message", req) }()
	}
	wg.Wait()
	for i, res := range results {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if res.Task.ID != results[0].Task.ID || res.Turn != results[0].Turn {
			t.Fatal("duplicate admission")
		}
	}
	h.waitStatus(results[0].Task.ID, core.TaskDone)
	h.restart()
	again, err := h.svc.SubmitWithRequest(ctx, "req_message", req)
	if err != nil || again.Task.ID != results[0].Task.ID || again.Turn != results[0].Turn {
		t.Fatalf("restart: %+v %v", again, err)
	}
	req.Model = "other"
	if _, err = h.svc.SubmitWithRequest(ctx, "req_message", req); !errors.Is(err, db.ErrDigestMismatch) {
		t.Fatalf("changed: %v", err)
	}
	tasks, err := h.store.ListTasks(ctx, h.project.ID)
	if err != nil || len(tasks) != 1 || len(h.adapter.Starts()) != 1 {
		t.Fatalf("effects: %v %v", tasks, err)
	}
	if id, err := h.svc.LookupRequest(ctx, "req_message"); err != nil || id != again.Task.ID {
		t.Fatalf("lookup: %s %v", id, err)
	}
}

func TestRequestCommitFailureRollsBackLinkTaskTurnAndAudit(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.start()
	h.svc.beforeCommit = func(core.TaskID, coretasks.Input) error { return errors.New("injected failure") }
	ctx := context.Background()
	if _, err := h.svc.SubmitWithRequest(ctx, "req_fail", h.msg("ask", "hello", 0)); err == nil {
		t.Fatal("missing error")
	}
	if _, err := h.svc.LookupRequest(ctx, "req_fail"); !errors.Is(err, db.ErrNotFound) {
		t.Fatal(err)
	}
	tasks, err := h.store.ListTasks(ctx, h.project.ID)
	if err != nil || len(tasks) != 0 {
		t.Fatalf("partial commit: %v %v", tasks, err)
	}
}

func TestSubmissionAuditFailureRollsBackAllWrites(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.start()
	ctx := context.Background()
	imported, err := h.svc.ImportIssueDraft(ctx, issueSpec(h))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := h.store.ListAudit(ctx, "task:"+string(imported.Task.ID), 100)
	if err != nil || len(entries) == 0 {
		t.Fatalf("audit: %v %v", entries, err)
	}
	originalID := h.svc.c.NewID
	h.svc.c.NewID = func(prefix string) string {
		if prefix == "aud" {
			return string(entries[0].ID)
		}
		return originalID(prefix)
	}
	_, err = h.svc.SubmitWithRequest(ctx, "req_audit_fail", h.msg("ask", "hello", 0))
	if err == nil {
		t.Fatal("audit constraint was ignored")
	}
	if _, err = h.svc.LookupRequest(ctx, "req_audit_fail"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("partial link: %v", err)
	}
	tasks, err := h.store.ListTasks(ctx, h.project.ID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("partial task/turn: %v %v", tasks, err)
	}
}
