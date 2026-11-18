package main

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corenotify "github.com/brendatamaar/yggdrasil/internal/core/notify"
	cv "github.com/brendatamaar/yggdrasil/internal/core/verify"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/notify"
	"github.com/brendatamaar/yggdrasil/internal/tasks"
	"github.com/brendatamaar/yggdrasil/internal/verify"
)

// Notifications (UI-06, docs/design/phase-6-notifications.md). Task statuses
// and verification results are already committed when the task and verify
// services announce them; the producer then commits the notice and only after
// that publishes it (commit before publish). The announcements arrive on the
// services' own goroutines, so they are queued here and handed to the producer
// by one worker: an announcement never waits for a database write, and none is
// dropped while the worker runs. The worker is started by run and stopped by
// Close AFTER the task service closed (so the last statuses are handed over)
// and BEFORE the producer and the store close.

// noticeJob is one announcement waiting for the worker.
type noticeJob func(ctx context.Context, p *notify.Producer) error

// noticeFeeder queues announcements for the notification producer.
type noticeFeeder struct {
	log  *slog.Logger
	prod atomicProducer

	mu      sync.Mutex
	queue   []noticeJob
	closed  bool
	started bool
	wake    chan struct{}
	done    chan struct{}
}

func newNoticeFeeder(log *slog.Logger) *noticeFeeder {
	return &noticeFeeder{log: log, wake: make(chan struct{}, 1), done: make(chan struct{})}
}

// bind sets the producer the worker hands jobs to.
func (f *noticeFeeder) bind(p *notify.Producer) { f.prod.Store(p) }

func (f *noticeFeeder) enqueue(j noticeJob) {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.queue = append(f.queue, j)
	f.mu.Unlock()
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

// start runs the worker. It runs until close.
func (f *noticeFeeder) start() {
	f.mu.Lock()
	if f.started || f.closed {
		f.mu.Unlock()
		return
	}
	f.started = true
	f.mu.Unlock()
	go f.loop() // owned: close waits for done
}

func (f *noticeFeeder) next() (noticeJob, bool, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.queue) > 0 {
		j := f.queue[0]
		f.queue = f.queue[1:]
		return j, true, f.closed
	}
	return nil, false, f.closed
}

func (f *noticeFeeder) loop() {
	defer close(f.done)
	for {
		j, ok, closed := f.next()
		if ok {
			f.run(j)
			continue
		}
		if closed {
			return
		}
		<-f.wake
	}
}

func (f *noticeFeeder) run(j noticeJob) {
	p := f.prod.Load()
	if p == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := j(ctx, p); err != nil && !errors.Is(err, notify.ErrClosed) && !errors.Is(err, notify.ErrInvalid) {
		f.log.Warn("could not record a notification", "err", err)
	}
}

// close stops accepting jobs, lets the worker hand over what is queued and waits
// for it. It is safe to call without start and more than once.
func (f *noticeFeeder) close() {
	f.mu.Lock()
	was := f.closed
	f.closed = true
	started := f.started
	f.mu.Unlock()
	if was {
		return
	}
	select {
	case f.wake <- struct{}{}:
	default:
	}
	if started {
		<-f.done
	}
}

// taskNoticeHub wraps the tasks hub: every status is announced to the browser as
// before and then queued for a notice. A status without a positive revision has
// no ordering and is not noticed.
type taskNoticeHub struct {
	tasks.Hub
	feeder *noticeFeeder
}

func (h taskNoticeHub) Status(m tasks.StatusMessage) {
	h.Hub.Status(m)
	if m.Revision <= 0 {
		return
	}
	h.feeder.enqueue(func(ctx context.Context, p *notify.Producer) error {
		return p.TaskStatus(ctx, m.TaskID, m.Title, m.Status, m.Revision)
	})
}

// verifyNoticeHub wraps the verification publisher: the browser is told as
// before; a passed or failed verification is read back from the store (state and
// version as committed) and queued for a notice.
type verifyNoticeHub struct {
	next   verify.Publisher
	feeder *noticeFeeder
	store  *db.Store
	rows   verify.Store
}

var _ verify.Publisher = (*verifyNoticeHub)(nil)

func (h *verifyNoticeHub) PublishVerification(id cv.VerificationID, task core.TaskID, repo core.RepoID, state string) {
	h.next.PublishVerification(id, task, repo, state)
	if state != "passed" && state != "failed" {
		return
	}
	h.feeder.enqueue(func(ctx context.Context, p *notify.Producer) error {
		row, err := h.rows.Get(ctx, string(id))
		if err != nil {
			return err
		}
		res := corenotify.VerifyPassed
		if row.State == "failed" {
			res = corenotify.VerifyFailed
		} else if row.State != "passed" {
			return nil // it moved on; the next announcement carries the result
		}
		t, err := h.store.GetTask(ctx, task)
		if err != nil {
			return err
		}
		return p.VerificationResult(ctx, task, t.Title, row.ID, res, row.Version)
	})
}

// atomicProducer holds the producer the worker hands jobs to.
type atomicProducer struct {
	v atomic.Pointer[notify.Producer]
}

func (a *atomicProducer) Store(p *notify.Producer) { a.v.Store(p) }
func (a *atomicProducer) Load() *notify.Producer   { return a.v.Load() }
