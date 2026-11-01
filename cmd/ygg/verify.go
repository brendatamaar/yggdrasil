package main

import (
	"context"
	"fmt"
	"sync/atomic"

	"golang.org/x/sync/errgroup"

	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/builds/toolchains"
	"github.com/brendatamaar/yggdrasil/internal/core"
	cv "github.com/brendatamaar/yggdrasil/internal/core/verify"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/safety"
	"github.com/brendatamaar/yggdrasil/internal/tasks"
	"github.com/brendatamaar/yggdrasil/internal/verify"
)

// Wiring of automatic verification (docs/design/verify.md). This file builds
// the service and its adapters and starts its loop; app.go (the lead) calls
// newVerifyService after the builds, tasks and git services exist and adds
// startVerify to the app errgroup.

// taskMessenger sends a failure to the task's agent as a new message in the
// mode of the task. Worktrees exist only for Code mode, so the message never
// asks for the opt-in that current-branch or project modes need.
type taskMessenger struct {
	svc   *tasks.Service
	store *db.Store
}

func (m taskMessenger) SendMessage(ctx context.Context, task core.TaskID, text string) error {
	st, err := m.store.GetTaskState(ctx, task)
	if err != nil {
		return fmt.Errorf("read task %s: %w", task, err)
	}
	_, err = m.svc.Message(ctx, tasks.MessageRequest{TaskID: task, Prompt: text, Mode: string(st.Task.Mode)})
	return err
}

// lateVerifyHub forwards verification changes to the API server, which is
// built after the verify service. Set pub once, before the loop starts.
type lateVerifyHub struct {
	pub atomic.Pointer[verify.Publisher]
}

// Set installs the publisher (the api server once AP1 gives it
// PublishVerification).
func (h *lateVerifyHub) Set(p verify.Publisher) { h.pub.Store(&p) }

func (h *lateVerifyHub) PublishVerification(id cv.VerificationID, task core.TaskID, repo core.RepoID, state string) {
	if p := h.pub.Load(); p != nil {
		(*p).PublishVerification(id, task, repo, state)
	}
}

// wakeOnBuild wraps the builds publisher: every build change also wakes the
// verify loop, so a finished build is seen at once and not at the next tick.
type wakeOnBuild struct {
	next builds.Publisher
	svc  atomic.Pointer[verify.Service]
}

func (w *wakeOnBuild) PublishBuildChanged(id core.BuildID, status core.BuildStatus) {
	if w.next != nil {
		w.next.PublishBuildChanged(id, status)
	}
	if s := w.svc.Load(); s != nil {
		s.Wake()
	}
}

func (w *wakeOnBuild) PublishBuildLog(id core.BuildID, step, attempt int, line string) {
	if w.next != nil {
		w.next.PublishBuildLog(id, step, attempt, line)
	}
}

// newVerifyService builds the verify service over the stores and services of
// the app. The caller passes the same redactor as the rest of the app; hub may
// be a *lateVerifyHub.
func newVerifyService(store *db.Store, bld *builds.Service, tsk *tasks.Service, g *git.Git, red safety.Redactor, hub verify.Publisher) (*verify.Service, error) {
	svc, err := verify.New(verify.Deps{
		Store: verify.NewSQLiteStore(store), Builds: bld, Logs: bld, Messenger: taskMessenger{svc: tsk, store: store},
		Probe: g, Toolchains: toolchains.All(), Hub: hub, Redactor: red,
	})
	if err != nil {
		return nil, fmt.Errorf("create verify service: %w", err)
	}
	return svc, nil
}

// startVerify runs the verify loop in the app errgroup. It returns when ctx is
// done; the loop's goroutine is owned by ctx inside Run.
func startVerify(g *errgroup.Group, ctx context.Context, svc *verify.Service) {
	g.Go(func() error { return svc.Run(ctx) })
}
