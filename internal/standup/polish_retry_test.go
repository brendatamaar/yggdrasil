package standup_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/repos"
	"github.com/brendatamaar/yggdrasil/internal/standup"
)

// restart builds a new Service over the same sources and the same polisher
// store, as Yggdrasil does after a restart: the in-memory results are gone.
func (e *env) restart(t *testing.T) {
	t.Helper()
	svc, err := standup.New(standup.Deps{Scope: e.scope, Commits: e.commits, MRs: e.mrs, Builds: e.builds, Polish: e.polish, Hub: e.hub, Clock: func() time.Time { return from }})
	if err != nil {
		t.Fatal(err)
	}
	e.svc = svc
}

func isConflict(err error) bool {
	var ae *apperr.Error
	return errors.As(err, &ae) && ae.Class == apperr.Conflict
}

// The first call made its task but the reply was lost. New commits then land in
// the range, so the rebuilt text differs; the retry still returns the first task.
func TestPolishRetryAfterLostReplyReturnsFirstTask(t *testing.T) {
	e := newEnv(t, apiRepo)
	e.commits.Seed(apiRepo.ID, []repos.HistoryCommit{hc(1, "fix cart", from.Add(time.Hour))})
	e.polish.SubmitThenFail = errors.New("connection reset")
	req := api.StandupPolishRequest{RequestID: "req_lost", From: from, To: to, Agent: "claude-code", Model: "opus/4"}
	if _, err := e.svc.Polish(context.Background(), project, req); err == nil {
		t.Fatal("the lost reply was not an error")
	}
	e.commits.Seed(apiRepo.ID, []repos.HistoryCommit{
		hc(1, "fix cart", from.Add(time.Hour)), hc(2, "add coupon", from.Add(2*time.Hour))})
	res, err := e.svc.Polish(context.Background(), project, req)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if res.TaskID != "tsk_1" || res.State != api.ActionStateCompleted || res.RequestID != "req_lost" {
		t.Fatalf("retry result = %+v", res)
	}
	if n := e.polish.CallCount(); n != 1 {
		t.Fatalf("the retry submitted again: %d submissions", n)
	}
}

// The reply arrived, Yggdrasil restarted and new commits landed; a repeat of the
// request (new Service, empty memory) returns the same task.
func TestPolishRetryAfterRestartReturnsFirstTask(t *testing.T) {
	e := newEnv(t, apiRepo)
	e.commits.Seed(apiRepo.ID, []repos.HistoryCommit{hc(1, "fix cart", from.Add(time.Hour))})
	req := api.StandupPolishRequest{RequestID: "req_restart", From: from, To: to, Agent: "claude-code"}
	first, err := e.svc.Polish(context.Background(), project, req)
	if err != nil {
		t.Fatal(err)
	}
	e.restart(t)
	e.commits.Seed(apiRepo.ID, []repos.HistoryCommit{
		hc(1, "fix cart", from.Add(time.Hour)), hc(2, "add coupon", from.Add(2*time.Hour))})
	again, err := e.svc.Polish(context.Background(), project, req)
	if err != nil || again.TaskID != first.TaskID {
		t.Fatalf("after restart = %+v, %v (first %+v)", again, err, first)
	}
	if n := e.polish.CallCount(); n != 1 {
		t.Fatalf("submissions = %d, want 1", n)
	}
}

// A genuinely different request under the same ID is refused, also after a
// restart, and never starts a second task.
func TestPolishSameIDOtherRequestRefused(t *testing.T) {
	for _, restart := range []bool{false, true} {
		e := newEnv(t, apiRepo)
		e.commits.Seed(apiRepo.ID, []repos.HistoryCommit{hc(1, "fix cart", from.Add(time.Hour))})
		req := api.StandupPolishRequest{RequestID: "req_x", From: from, To: to, Agent: "claude-code", Model: "opus/4"}
		if _, err := e.svc.Polish(context.Background(), project, req); err != nil {
			t.Fatal(err)
		}
		if restart {
			e.restart(t)
		}
		for name, edit := range map[string]func(*api.StandupPolishRequest){
			"range end":   func(r *api.StandupPolishRequest) { r.To = r.To.Add(time.Hour) },
			"range start": func(r *api.StandupPolishRequest) { r.From = r.From.Add(time.Hour) },
			"agent":       func(r *api.StandupPolishRequest) { r.Agent = "other" },
			"model":       func(r *api.StandupPolishRequest) { r.Model = "sonnet" },
		} {
			other := req
			edit(&other)
			if _, err := e.svc.Polish(context.Background(), project, other); !isConflict(err) {
				t.Errorf("restart=%v %s: err = %v, want a conflict", restart, name, err)
			}
		}
		if n := e.polish.CallCount(); n != 1 {
			t.Errorf("restart=%v: submissions = %d, want 1", restart, n)
		}
	}
}

// A repeat that is answered from the stored link does not read the sources.
func TestPolishRepeatDoesNotRebuildText(t *testing.T) {
	e := newEnv(t, apiRepo)
	e.commits.Seed(apiRepo.ID, []repos.HistoryCommit{hc(1, "fix cart", from.Add(time.Hour))})
	req := api.StandupPolishRequest{RequestID: "req_nr", From: from, To: to, Agent: "claude-code"}
	if _, err := e.svc.Polish(context.Background(), project, req); err != nil {
		t.Fatal(err)
	}
	e.restart(t)
	e.scope.Set(project) // the sources are gone: a rebuild would fail
	if _, err := e.svc.Polish(context.Background(), project, req); err != nil {
		t.Fatalf("repeat needed the sources: %v", err)
	}
}
