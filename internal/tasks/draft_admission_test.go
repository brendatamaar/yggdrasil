package tasks

import (
	"context"
	"errors"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/core"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
)

func TestNoRemoteDraftAdmissionMatrix(t *testing.T) {
	// Harness repos deliberately have no origin. Its absence never selects a mode.
	for _, tc := range []struct {
		name, mode                     string
		optIn, writes, enforce, config bool
		reason                         string
	}{
		{"explicit direct", "current_branch", true, true, true, false, ""},
		{"no opt in", "current_branch", false, true, true, false, "needs_opt_in"},
		{"writes off", "current_branch", true, false, true, false, "write_modes_off"},
		{"limited direct", "current_branch", true, true, false, false, "limited_enforcement"},
		{"untrusted direct", "current_branch", true, true, true, true, "repo_untrusted"},
		{"ordinary code", "code", false, true, false, false, ""},
		{"ask untrusted", "ask", false, false, false, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, []string{"shop-api"})
			if tc.enforce {
				h.adapter.caps.Enforcement = agents.EnforcementHook
			}
			if tc.config {
				h.adapter.configs = []string{".claude/settings.json"}
				h.scanner.set(h.repos[0].Path, claudeConfig)
			}
			h.tune = func(c *Config) { c.WriteModes = tc.writes }
			h.start()
			ctx := context.Background()
			draft, err := h.svc.ImportIssueDraft(ctx, issueSpec(h))
			if err != nil {
				t.Fatal(err)
			}
			res, err := h.svc.StartDraft(ctx, coretasks.DraftStart{Task: draft.Task.ID, Mode: core.TaskMode(tc.mode), OptIn: tc.optIn}, "mock")
			if tc.reason != "" {
				var r *RejectedError
				if !errors.As(err, &r) || r.Reason != tc.reason {
					t.Fatalf("want %s got %v", tc.reason, err)
				}
				turns, _ := h.store.ListTaskTurns(ctx, draft.Task.ID)
				if len(turns) != 0 || len(h.adapter.Starts()) != 0 {
					t.Fatal("refused draft ran")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			status := core.TaskReview
			if tc.mode == "ask" {
				status = core.TaskDone
			}
			h.waitStatus(res.Task.ID, status)
			starts := h.adapter.Starts()
			if len(starts) != 1 {
				t.Fatalf("starts: %v", starts)
			}
			if tc.mode == "current_branch" && (res.Warning != "edits_main_checkout" || !samePath(starts[0].Dir, h.repos[0].Path)) {
				t.Fatal("direct mode missing warning or wrong directory")
			}
			if tc.mode == "code" && samePath(starts[0].Dir, h.repos[0].Path) {
				t.Fatal("Code silently edited main")
			}
		})
	}
}

func TestRequestCrashAfterCommitReconcilesWithoutReadmission(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.afterCommit = func(_ core.TaskID, cmds []coretasks.Command) bool {
		for _, cmd := range cmds {
			if _, ok := cmd.(coretasks.Enqueue); ok {
				return true
			}
		}
		return false
	}
	h.start()
	ctx := context.Background()
	req := h.msg("ask", "explain", 0)
	first, err := h.svc.SubmitWithRequest(ctx, "req_crash", req)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.adapter.Starts()) != 0 {
		t.Fatal("crash hook did not stop dispatch")
	}
	h.restart()
	again, err := h.svc.SubmitWithRequest(ctx, "req_crash", req)
	if err != nil || again.Task.ID != first.Task.ID || again.Turn != first.Turn {
		t.Fatalf("reconcile: %+v %v", again, err)
	}
	h.waitStatus(first.Task.ID, core.TaskDone)
	turns, err := h.store.ListTaskTurns(ctx, first.Task.ID)
	if err != nil || len(turns) != 1 || len(h.adapter.Starts()) != 1 {
		t.Fatalf("readmitted: %v %v", turns, err)
	}
}

func TestDraftStartChecksFreshTrust(t *testing.T) {
	h := trustHarness(t, claudeConfig)
	h.adapter.caps.Enforcement = agents.EnforcementHook
	h.trust(0, claudeConfig)
	ctx := context.Background()
	draft, err := h.svc.ImportIssueDraft(ctx, issueSpec(h))
	if err != nil {
		t.Fatal(err)
	}
	h.scanner.set(h.repos[0].Path, map[string]string{".claude/settings.json": `{"hooks":{"changed":true}}`})
	_, err = h.svc.StartDraft(ctx, coretasks.DraftStart{Task: draft.Task.ID, Mode: core.ModeCurrentBranch, OptIn: true}, "mock")
	var rejected *RejectedError
	if !errors.As(err, &rejected) || rejected.Reason != "repo_untrusted" {
		t.Fatalf("stale trust: %v", err)
	}
	if trusted, _, err := h.store.GetRepoTrust(ctx, h.repos[0].ID); err != nil || trusted {
		t.Fatalf("trust not cleared: %v", err)
	}
}

func TestStartDraftAgentModelImmutable(t *testing.T) {
	for _, tc := range []struct {
		name, agent, model string
		refuse             bool
	}{
		{"omitted", "", "", false}, {"matching", "mock", "model_one", false},
		{"agent change", "other", "model_one", true}, {"model change", "mock", "model_two", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, []string{"shop-api"})
			h.start()
			ctx := context.Background()
			spec := issueSpec(h)
			spec.Model = "model_one"
			draft, err := h.svc.ImportIssueDraft(ctx, spec)
			if err != nil {
				t.Fatal(err)
			}
			res, err := h.svc.StartDraft(ctx, coretasks.DraftStart{Task: draft.Task.ID, Mode: core.ModeAsk, Model: tc.model}, tc.agent)
			if tc.refuse {
				var user *coretasks.UserError
				if !errors.As(err, &user) {
					t.Fatalf("identity changed: %v", err)
				}
				turns, _ := h.store.ListTaskTurns(ctx, draft.Task.ID)
				if len(turns) != 0 || len(h.adapter.Starts()) != 0 {
					t.Fatal("refusal ran")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			h.waitStatus(res.Task.ID, core.TaskDone)
			starts := h.adapter.Starts()
			if len(starts) != 1 || starts[0].Model != "model_one" {
				t.Fatal("stored model not used")
			}
		})
	}
}

func TestStartedDraftKeepsItsFirstTurnWritingModeAcrossAskAndRestart(t *testing.T) {
	for _, mode := range []core.TaskMode{core.ModeCode, core.ModeCurrentBranch} {
		t.Run(string(mode), func(t *testing.T) {
			h := newHarness(t, []string{"shop-api"})
			h.adapter.caps.Enforcement = agents.EnforcementHook
			h.start()
			ctx := context.Background()
			draft, err := h.svc.ImportIssueDraft(ctx, issueSpec(h))
			if err != nil {
				t.Fatal(err)
			}
			first, err := h.svc.StartDraft(ctx, coretasks.DraftStart{Task: draft.Task.ID, Mode: mode, OptIn: true}, "")
			if err != nil {
				t.Fatal(err)
			}
			h.waitStatus(first.Task.ID, core.TaskReview)
			_, err = h.svc.Message(ctx, MessageRequest{TaskID: draft.Task.ID, Mode: "ask", Prompt: "explain"})
			if err != nil {
				t.Fatal(err)
			}
			h.waitStatus(draft.Task.ID, core.TaskDone)
			h.restart()
			_, err = h.svc.Message(ctx, MessageRequest{TaskID: draft.Task.ID, Mode: string(mode), Prompt: "continue", OptIn: true})
			if err != nil {
				t.Fatalf("first-turn mode was lost: %v", err)
			}
			h.waitStatus(draft.Task.ID, core.TaskReview)
			other := core.ModeCurrentBranch
			if mode == other {
				other = core.ModeCode
			}
			_, err = h.svc.Message(ctx, MessageRequest{TaskID: draft.Task.ID, Mode: string(other), Prompt: "switch", OptIn: true})
			var rejected *RejectedError
			if !errors.As(err, &rejected) || rejected.Reason != "mode_switch_not_allowed" {
				t.Fatalf("widened writing mode: %v", err)
			}
		})
	}
}
