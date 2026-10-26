package tasks

import (
	"context"
	"errors"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/guard"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
)

func (h *harness) projectMsg(prompt string) MessageRequest {
	return MessageRequest{ProjectID: h.project.ID, Prompt: prompt, Mode: "project", Scope: "project", Agent: "mock", OptIn: true}
}

// ---- S5: whole-project tasks (mode project) ----

func TestProjectTurnUsesAWorktreePerRepoAndEveryRepoAllowlist(t *testing.T) {
	h := newHarness(t, []string{"shop-api", "shop-web"})
	h.adapter.caps.Enforcement = agents.EnforcementHook
	h.adapter.fixture = "edits"
	h.tune = func(c *Config) {
		c.Allowlist = func(_ context.Context, r core.Repo) ([]guard.CommandRule, error) {
			return []guard.CommandRule{{Program: "tool-" + r.Name, Tail: guard.TailNone}}, nil
		}
	}
	h.start()
	res := h.send(h.projectMsg("change both"))
	h.waitStatus(res.Task.ID, core.TaskReview)

	wts, _ := h.store.ListWorktrees(context.Background(), res.Task.ID)
	if len(wts) != 2 {
		t.Fatalf("worktrees = %+v, want one per repo", wts)
	}
	o := h.adapter.Starts()[0]
	if len(o.ExtraDirs) != 1 || len(o.Policy.WriteRoots) != 2 || len(o.Policy.ReadRoots) != 2 {
		t.Errorf("dirs %q + %q, policy = %+v", o.Dir, o.ExtraDirs, o.Policy)
	}
	for _, d := range append([]string{o.Dir}, o.ExtraDirs...) {
		if d == h.repos[0].Path || d == h.repos[1].Path {
			t.Errorf("project mode ran in the main checkout %q", d)
		}
	}
	got := map[string]bool{}
	for _, r := range o.Policy.Allow {
		got[r.Program] = true
	}
	if !got["tool-shop-api"] || !got["tool-shop-web"] {
		t.Errorf("allowlist = %+v, want the rules of both repos", o.Policy.Allow)
	}
	if !o.Admitted || o.Mode != core.ModeProject {
		t.Errorf("admitted=%v mode=%s", o.Admitted, o.Mode)
	}
}

func TestProjectModeNeedsEveryRepoTrusted(t *testing.T) {
	h := newHarness(t, []string{"shop-api", "shop-web"})
	h.adapter.caps.Enforcement = agents.EnforcementHook
	h.adapter.fixture = "edits"
	h.trust(0, claudeConfig)
	h.scanner.set(h.repos[1].Path, claudeConfig) // has agent config, never trusted
	h.start()
	_, err := h.svc.Message(context.Background(), h.projectMsg("change both"))
	var rj *RejectedError
	if !errors.As(err, &rj) || rj.Reason != string(guard.ReasonRepoUntrusted) {
		t.Fatalf("got %v, want repo_untrusted", err)
	}
	if n := len(h.adapter.Starts()); n != 0 {
		t.Fatalf("%d runs started", n)
	}
	h.trust(1, claudeConfig)
	res := h.send(h.projectMsg("change both"))
	h.waitStatus(res.Task.ID, core.TaskReview)
}

func TestFollowUpToAProjectTaskIsAccepted(t *testing.T) {
	h := newHarness(t, []string{"shop-api", "shop-web"})
	h.adapter.caps.Enforcement = agents.EnforcementHook
	h.adapter.fixture = "edits"
	h.start()
	first := h.send(h.projectMsg("change both"))
	h.waitStatus(first.Task.ID, core.TaskReview)

	req := h.projectMsg("and tests")
	req.TaskID = first.Task.ID
	second := h.send(req)
	if second.Task.ID != first.Task.ID {
		t.Fatalf("second = %+v", second)
	}
	waitFor(t, 10e9, func() bool {
		st := h.state(first.Task.ID)
		return st.Task.Turns == 2 && st.Row.Status == core.TaskReview && st.Task.State.(coretasks.Idle).Turn == second.Turn
	}, "the follow-up turn")
	if o := h.adapter.Starts()[1]; len(o.ExtraDirs) != 1 || o.Prompt != "and tests" {
		t.Errorf("follow-up start = %+v", o)
	}
}
