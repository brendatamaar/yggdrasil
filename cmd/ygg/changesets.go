package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"sync/atomic"

	"golang.org/x/sync/errgroup"

	"github.com/brendatamaar/yggdrasil/internal/builds/toolchains"
	"github.com/brendatamaar/yggdrasil/internal/changesets"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/review"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/proposals"
	"github.com/brendatamaar/yggdrasil/internal/safety"
	"github.com/brendatamaar/yggdrasil/internal/tasks"
)

// Wiring of change sets and review notes (docs/design/changesets.md). This
// file builds the service and its adapters and starts its loops; app.go (the
// lead) calls newChangeSetsService after the proposals, tasks and git
// services exist, forwards the "proposals" hub topic to
// Service.OnProposalChanged (a new proposal reopens a settled set), and adds
// startChangeSets to the app errgroup.

// confirmer is the part of *proposals.Service the adapter calls.
type confirmer interface {
	Confirm(ctx context.Context, id core.ProposalID, hash, by string) (proposals.View, error)
}

// changeSetProposals adapts the proposals service: a "no" (stale hash, not
// pending, untracked repo, unknown proposal) becomes *changesets.Refused, any
// other error stays a transient error.
type changeSetProposals struct{ svc confirmer }

func (p changeSetProposals) Confirm(ctx context.Context, id core.ProposalID, hash, by string) error {
	_, err := p.svc.Confirm(ctx, id, hash, by)
	var ref *proposals.Refusal
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ref):
		return &changesets.Refused{Message: ref.UserMessage()}
	case errors.Is(err, proposals.ErrAlreadyDecided):
		return &changesets.Refused{Message: "That proposal was already decided.", AlreadyDecided: true}
	case errors.Is(err, db.ErrNotFound):
		return &changesets.Refused{Message: "That proposal no longer exists."}
	}
	return err
}

// noteMessenger sends review notes to the task's agent as a new message in the
// mode of the task, like verify's failure message, and returns the turn.
type noteMessenger struct {
	svc   *tasks.Service
	store *db.Store
}

func (m noteMessenger) SendMessage(ctx context.Context, task core.TaskID, text string) (string, error) {
	st, err := m.store.GetTaskState(ctx, task)
	if err != nil {
		return "", fmt.Errorf("read task %s: %w", task, err)
	}
	res, err := m.svc.Message(ctx, tasks.MessageRequest{TaskID: task, Prompt: text, Mode: string(st.Task.Mode)})
	if err != nil {
		return "", err
	}
	return string(res.Turn), nil
}

// changeSetDiffs gives the per-file diff hashes of a task's active worktree in
// a repo (the same hashes the Review tab shows), so notes can go stale.
type changeSetDiffs struct {
	store *db.Store
	git   *git.Git
}

func (d changeSetDiffs) FileHashes(ctx context.Context, task core.TaskID, repo core.RepoID) (map[review.RelPath]string, error) {
	wts, err := d.store.ListWorktrees(ctx, task)
	if err != nil {
		return nil, err
	}
	for _, w := range wts {
		if w.RepoID != repo || w.Status != core.WorktreeActive {
			continue
		}
		base, err := d.store.GetWorktreeBaseCommit(ctx, w.ID)
		if err != nil {
			return nil, err
		}
		diff, err := d.git.DiffAgainstBase(ctx, w.Path, base, git.BaseDiffOptions{})
		if err != nil {
			return nil, err
		}
		if diff.FilesTruncated {
			return nil, errors.New("too many files changed to judge notes")
		}
		out := make(map[review.RelPath]string, len(diff.Files))
		for _, f := range diff.Files {
			out[review.RelPath(f.Path)] = f.Hash
		}
		return out, nil
	}
	return nil, errors.New("no active worktree for this repo")
}

// lateChangeSetHub forwards changes to the API server, which is built after
// the service. Set the publisher once, before the loops start.
type lateChangeSetHub struct {
	pub atomic.Pointer[changesets.Publisher]
}

// Set installs the publisher (the api server once AP1 gives it the topic).
func (h *lateChangeSetHub) Set(p changesets.Publisher) { h.pub.Store(&p) }

func (h *lateChangeSetHub) PublishChangeSet(id core.ChangeSetID, task core.TaskID, st core.ChangeSetStatus) {
	if p := h.pub.Load(); p != nil {
		(*p).PublishChangeSet(id, task, st)
	}
}

func (h *lateChangeSetHub) PublishNotes(task core.TaskID) {
	if p := h.pub.Load(); p != nil {
		(*p).PublishNotes(task)
	}
}

// newChangeSetsService builds the service over the stores and services of the
// app. The caller passes the same redactor as the rest of the app; hub may be
// a *lateChangeSetHub.
func newChangeSetsService(store *db.Store, prop *proposals.Service, tsk *tasks.Service, g *git.Git, red safety.Redactor, hub changesets.Publisher, log *slog.Logger) (*changesets.Service, error) {
	deps := changesets.ToolchainDeps{
		Toolchains: toolchains.All(), Log: log,
		OpenRepo: func(ctx context.Context, id core.RepoID) (fs.FS, error) {
			r, err := store.GetRepo(ctx, id)
			if err != nil {
				return nil, err
			}
			return os.DirFS(r.Path), nil
		},
	}
	svc, err := changesets.New(changesets.Deps{
		Store: changesets.NewSQLiteStore(store, red), Proposals: changeSetProposals{svc: prop},
		Messenger: noteMessenger{svc: tsk, store: store}, Deps: deps, Diffs: changeSetDiffs{store: store, git: g},
		Hub: hub, Redactor: red, Log: log,
	})
	if err != nil {
		return nil, fmt.Errorf("create change-set service: %w", err)
	}
	return svc, nil
}

// startChangeSets runs the loops in the app errgroup. It returns when ctx is
// done; the goroutines are owned by ctx inside Run.
func startChangeSets(g *errgroup.Group, ctx context.Context, svc *changesets.Service) {
	g.Go(func() error { return svc.Run(ctx) })
}
