package worktreeenv_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	env "github.com/brendatamaar/yggdrasil/internal/core/worktreeenv"
	"github.com/brendatamaar/yggdrasil/internal/worktreeenv"
	"github.com/brendatamaar/yggdrasil/internal/worktreeenv/worktreeenvtest"
)

// The end-of-operation entry has the ID <worktree>:<attempt>:<index>, whether it
// is written by the transition or later from the queue, and it is written once.
func TestComposeEndAuditHasADeterministicIDAndIsWrittenOnce(t *testing.T) {
	ctx := context.Background()
	r := newComposeRig(t)
	r.confirmedDown(t)
	var down atomic.Bool
	down.Store(true)
	r.store.AuditErr = func(e core.AuditEntry) error {
		if down.Load() && e.Action == env.ActionComposeStarted {
			return errors.New("disk full")
		}
		return nil
	}
	r.up(t)
	r.waitFor(t, func(s env.Service) bool { _, ok := s.(env.Up); return ok })
	down.Store(false)
	for range 3 {
		if _, err := r.svc.Status(ctx, r.wt.ID); err != nil {
			t.Fatal(err)
		}
	}
	var ids []string
	for _, a := range r.store.Audits() {
		if a.Action == env.ActionComposeStarted {
			ids = append(ids, string(a.ID))
		}
	}
	// <worktree>:<boot>:<attempt>:<index>
	if len(ids) != 1 || !strings.HasPrefix(ids[0], string(r.wt.ID)+":") || !strings.HasSuffix(ids[0], ":1:0") {
		t.Fatalf("started entries = %v", ids)
	}
}

// The queue never writes an entry twice, even when it was queued twice.
func TestComposeQueueDedupesByID(t *testing.T) {
	r := newComposeRig(t)
	e := core.AuditEntry{ID: "Wt_x:1:0", At: time.Unix(1, 0), Actor: "system", Action: "worktreeenv.compose_started", Subject: "worktree:Wt_x", Detail: []byte("{}")}
	r.svc.QueueAudit(e, true)
	r.svc.QueueAudit(e, true)
	if _, err := r.svc.Status(context.Background(), r.wt.ID); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		n := 0
		for _, a := range r.store.Audits() {
			if a.ID == e.ID {
				n++
			}
		}
		return n
	}
	if n := count(); n != 1 {
		t.Fatalf("entry written %d times", n)
	}
	r.svc.QueueAudit(e, true) // already written: not queued again
	_, _ = r.svc.Status(context.Background(), r.wt.ID)
	if n := count(); n != 1 {
		t.Fatalf("written again after it was done: %d", n)
	}
}

// blockingSink fails or blocks (until its context ends) while mode is set.
type blockingSink struct {
	*worktreeenvtest.Store
	mode atomic.Int32 // 0 ok, 1 fail Started, 2 block everything
}

func (b *blockingSink) Append(ctx context.Context, e core.AuditEntry) error {
	switch b.mode.Load() {
	case 1:
		if e.Action == env.ActionComposeStarted {
			return errors.New("disk full")
		}
	case 2:
		<-ctx.Done()
		return ctx.Err()
	}
	return b.Store.Append(ctx, e)
}

// A read does not wait long for audit IO: the retry of the queue has a short
// deadline of its own.
func TestComposeStatusDoesNotWaitForAQueuedAudit(t *testing.T) {
	sink := &blockingSink{}
	r := newComposeRigWith(t, func(r *composeRig, cfg *worktreeenv.ComposeConfig) {
		sink.Store = r.store
		cfg.Audit = sink
	})
	r.confirmedDown(t)
	sink.mode.Store(1)
	r.up(t)
	r.waitFor(t, func(s env.Service) bool { _, ok := s.(env.Up); return ok })
	sink.mode.Store(2)
	start := time.Now()
	if _, err := r.svc.Status(context.Background(), r.wt.ID); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("Status waited %v for the audit sink", d)
	}
	sink.mode.Store(0)
	_, _ = r.svc.Status(context.Background(), r.wt.ID)
	found := false
	for _, a := range r.store.Audits() {
		found = found || strings.HasSuffix(a.Action, "started")
	}
	if !found {
		t.Fatal("the queued entry was lost")
	}
}
