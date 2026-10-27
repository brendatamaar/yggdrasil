package proposals

import (
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
)

func TestInstallWaitIsBounded(t *testing.T) {
	w := newWorld(t)
	w.builds.release = make(chan struct{}) // the build never ends
	w.cfg.InstallTimeout = 50 * time.Millisecond
	w.start()
	v := w.create(coreprop.Install{Repo: w.repo.ID, Step: "install"})
	w.confirm(v)
	got := w.waitFor(v.ID, core.ProposalFailed)
	if got.FailKind != string(coreprop.FailInterrupted) || !got.Retryable {
		t.Fatalf("failure = %+v", got)
	}
}

func TestInstallRetryWaitsForTheBuildItAlreadyStarted(t *testing.T) {
	w := newWorld(t)
	w.builds.live = map[string]core.BuildID{}
	v := w.create(coreprop.Install{Repo: w.repo.ID, Step: "install"})
	w.builds.live["proposal:"+string(v.ID)] = "bld_live"
	w.confirm(v)
	got := w.waitFor(v.ID, core.ProposalExecuted)
	if len(w.builds.owners) != 0 {
		t.Fatalf("enqueued again: %v", w.builds.owners)
	}
	if got.Result == nil || got.Result.BuildID != "bld_live" {
		t.Fatalf("result = %+v", got.Result)
	}
}

func TestCreateRefusesAnInstallStepTheRepoDoesNotHave(t *testing.T) {
	w := newWorld(t)
	w.builds.known = []string{"install"}
	_, err := w.svc.Create(w.ctx, CreateRequest{Task: w.task.ID, Action: coreprop.Install{Repo: w.repo.ID, Step: "bootstrap"}})
	refusedBadFacts(t, err, "unknown install step")
	w.create(coreprop.Install{Repo: w.repo.ID, Step: "install"})
}
