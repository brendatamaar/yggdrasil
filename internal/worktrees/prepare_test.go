package worktrees_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	env "github.com/brendatamaar/yggdrasil/internal/core/worktreeenv"
	"github.com/brendatamaar/yggdrasil/internal/worktrees"
	"github.com/brendatamaar/yggdrasil/internal/worktrees/worktreestest"
)

type recordingPreparer struct {
	got  []core.Worktree
	trig []env.Trigger
	err  error
}

func (p *recordingPreparer) Prepare(_ context.Context, w core.Worktree, t env.Trigger) error {
	p.got = append(p.got, w)
	p.trig = append(p.trig, t)
	return p.err
}

func newPrepared(t *testing.T, p worktrees.Preparer, onErr func(core.Worktree, error)) (*worktrees.Ensurer, []worktrees.RepoRef) {
	t.Helper()
	e, err := worktrees.New(worktrees.Config{
		Git: worktreestest.NewGit(), Store: worktreestest.NewStore(), Rand: &worktreestest.Rand{}, Now: time.Now, NewID: newID,
		DataDir: filepath.Join(t.TempDir(), "data"), Prepare: p, OnPrepareError: onErr,
	})
	if err != nil {
		t.Fatal(err)
	}
	return e, []worktrees.RepoRef{{ID: "rep_1", Path: filepath.Join(t.TempDir(), "shop-api"), DefaultBranch: "main"}}
}

func TestEnsureCallsPrepareOnceForNewWorktreesOnly(t *testing.T) {
	p := &recordingPreparer{}
	e, repos := newPrepared(t, p, nil)
	ctx := context.Background()
	first, err := e.Ensure(ctx, "tsk_1", repos)
	if err != nil || len(p.got) != 1 || p.got[0].ID != first[0].ID {
		t.Fatalf("first: %v %v", p.got, err)
	}
	if p.trig[0] != env.TriggerAgentTask {
		t.Fatalf("Ensure must default to the agent-task trigger, got %v", p.trig[0])
	}
	if _, err := e.EnsureFor(ctx, "tsk_1", repos, env.TriggerUserStarted); err != nil || len(p.got) != 1 {
		t.Fatalf("existing worktree prepared again: %d %v", len(p.got), err)
	}
	if _, err := e.EnsureFor(ctx, "tsk_2", repos, env.TriggerUserStarted); err != nil || len(p.got) != 2 || p.trig[1] != env.TriggerUserStarted {
		t.Fatalf("user trigger lost: %v %v", p.trig, err)
	}
}

func TestPrepareFailureDoesNotFailEnsure(t *testing.T) {
	boom := errors.New("boom")
	var reported error
	e, repos := newPrepared(t, &recordingPreparer{err: boom}, func(_ core.Worktree, err error) { reported = err })
	got, err := e.Ensure(context.Background(), "tsk_1", repos)
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}
	if !errors.Is(reported, boom) {
		t.Fatalf("reported %v", reported)
	}
}
