package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/proposals"
	"github.com/brendatamaar/yggdrasil/internal/repos"
)

// pullBatches is the part of the git-action service the confirmed pull uses.
type pullBatches interface {
	Start(ctx context.Context, req repos.BatchRequest, actor corerepos.Actor) (corerepos.BatchID, error)
	Get(id corerepos.BatchID) (repos.BatchView, bool)
}

// pullRunner implements proposals.PullRunner (PullFF, AC-04) over the existing
// git-action service: the same fast-forward-only pull, the same shared main
// lock, no merge, rebase or reset. It runs only for a confirmed proposal; the
// executor has already checked the branch and the expected tip.
type pullRunner struct {
	batches pullBatches
	poll    time.Duration
}

var _ proposals.PullRunner = pullRunner{}

// pullRefusal carries the plain message of a refused pull to the executor.
type pullRefusal struct{ msg string }

func (e *pullRefusal) Error() string       { return "pull refused: " + e.msg }
func (e *pullRefusal) UserMessage() string { return e.msg }

// pullActor is the audit actor of a pull the user confirmed as a proposal.
const pullActor = corerepos.Actor("proposal")

func (p pullRunner) PullFF(ctx context.Context, repo core.RepoID, branch, upstream, expectHead string, verify bool) (proposals.PullFFResult, error) {
	req := repos.BatchRequest{Kind: corerepos.KindPull, Repos: []core.RepoID{repo}}
	id, err := p.batches.Start(ctx, req, pullActor)
	if err != nil {
		return proposals.PullFFResult{}, fmt.Errorf("start the pull of repo %s: %w", repo, err)
	}
	poll := p.poll
	if poll <= 0 {
		poll = 100 * time.Millisecond
	}
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		v, ok := p.batches.Get(id)
		if ok && v.Finished {
			return pullResult(v)
		}
		select {
		case <-ctx.Done():
			return proposals.PullFFResult{}, ctx.Err()
		case <-t.C:
		}
	}
}

func pullResult(v repos.BatchView) (proposals.PullFFResult, error) {
	if len(v.Items) != 1 {
		return proposals.PullFFResult{}, errors.New("the pull batch has an unexpected shape")
	}
	it := v.Items[0]
	switch {
	case it.State == "skipped":
		if it.Reason == string(corerepos.SkipBuildRunning) {
			return proposals.PullFFResult{}, fmt.Errorf("%s: %w", it.Message, proposals.ErrPullBusy)
		}
		return proposals.PullFFResult{}, &pullRefusal{msg: it.Message}
	case it.Outcome == "pulled":
		return proposals.PullFFResult{NewHead: it.To}, nil
	case it.Outcome == "up_to_date":
		return proposals.PullFFResult{}, nil
	case it.Outcome == "pull_refused":
		return proposals.PullFFResult{}, &pullRefusal{msg: it.Message}
	}
	return proposals.PullFFResult{}, fmt.Errorf("the pull of repo %s did not finish: %s", it.Repo, it.Outcome)
}
