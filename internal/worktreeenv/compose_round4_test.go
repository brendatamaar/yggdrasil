package worktreeenv_test

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	env "github.com/brendatamaar/yggdrasil/internal/core/worktreeenv"
	"github.com/brendatamaar/yggdrasil/internal/worktreeenv"
)

func startedIDs(r *composeRig) []core.AuditID {
	var out []core.AuditID
	for _, a := range r.store.Audits() {
		if a.Action == env.ActionComposeStarted {
			out = append(out, a.ID)
		}
	}
	return out
}

func entryWith(id string) core.AuditEntry {
	return core.AuditEntry{ID: core.AuditID(id), At: time.Unix(1, 0), Actor: "system", Action: "worktreeenv.compose_started", Subject: "worktree:Wt_x", Detail: []byte("{}")}
}

// A restart: attempt counters start at 1 again, and the audit log's primary key
// already holds the IDs of the first run. The second run's end entry must still
// be written.
func TestComposeEndAuditAfterARestartIsWritten(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newComposeRig(t)
	r.confirmedDown(t)
	r.up(t)
	r.waitFor(t, func(s env.Service) bool { _, ok := s.(env.Up); return ok })
	if len(startedIDs(r)) != 1 {
		t.Fatalf("first run: %v", startedIDs(r))
	}
	if _, err := r.svc.Stop(ctx, r.wt.ID); err != nil {
		t.Fatal(err)
	}
	r.waitFor(t, func(s env.Service) bool { _, ok := s.(env.Down); return ok })

	var n atomic.Int32
	svc2, err := worktreeenv.NewComposeService(ctx, worktreeenv.ComposeConfig{
		Store: r.store, Audit: r.store, Runner: r.runner, Files: r.files,
		Worktree: func(context.Context, core.WorktreeID) (core.Worktree, error) { return r.wt, nil },
		Block: func(context.Context, core.WorktreeID) (core.PortBlock, bool, error) {
			return core.PortBlock{Base: 20040, Size: 20}, true, nil
		},
		Now:   func() time.Time { return time.Date(2026, 5, 2, 9, 0, 0, 0, time.UTC) },
		NewID: func(p string) string { return p + "_second_" + strconv.Itoa(int(n.Add(1))) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc2.Wait()
	for range 400 {
		st, err := svc2.Status(ctx, r.wt.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := st.State.(env.Down); ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := svc2.Up(ctx, r.wt.ID); err != nil {
		t.Fatal(err)
	}
	for range 400 {
		if len(startedIDs(r)) == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	ids := startedIDs(r)
	if len(ids) != 2 || ids[0] == ids[1] {
		t.Fatalf("started entries after a restart = %v", ids)
	}
	if svc2.QueueLen() != 0 {
		t.Fatalf("the second run queued %d entries", svc2.QueueLen())
	}
}

// A duplicate key means the entry is already in the log; it never blocks the
// entries behind it. A permanently failing one is quarantined after a number of
// flushes, also without blocking.
func TestComposePoisonedHeadDoesNotBlockTheQueue(t *testing.T) {
	r := newComposeRig(t)
	if err := r.store.Append(context.Background(), entryWith("taken")); err != nil {
		t.Fatal(err)
	}
	r.svc.QueueAudit(entryWith("taken"), true)
	r.svc.QueueAudit(entryWith("later1"), true)
	r.svc.FlushAudits()
	if r.svc.QueueLen() != 0 {
		t.Fatalf("queue = %d after a duplicate head", r.svc.QueueLen())
	}
	found := false
	for _, a := range r.store.Audits() {
		found = found || a.ID == "later1"
	}
	if !found {
		t.Fatal("the entry behind a duplicate was not written")
	}

	r.store.AuditErr = func(e core.AuditEntry) error {
		if e.ID == "bad" {
			return errors.New("cannot be stored")
		}
		return nil
	}
	r.svc.QueueAudit(entryWith("bad"), true)
	r.svc.QueueAudit(entryWith("later2"), true)
	for range 12 {
		r.svc.FlushAudits()
	}
	if r.svc.QueueLen() != 0 || r.svc.Dropped() != 1 {
		t.Fatalf("queue %d dropped %d: the bad entry was not quarantined", r.svc.QueueLen(), r.svc.Dropped())
	}
	found = false
	for _, a := range r.store.Audits() {
		found = found || a.ID == "later2"
	}
	if !found {
		t.Fatal("the entry behind a quarantined one was not written")
	}
}

// Two writers of the same ID write it once and neither fails.
func TestComposeAppendOnceRace(t *testing.T) {
	r := newComposeRig(t)
	e := entryWith("Wt_x:boot_1:1:0")
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- r.svc.AppendOnce(context.Background(), e, true)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	for _, a := range r.store.Audits() {
		if a.ID == e.ID {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("written %d times", n)
	}
}

// The retry queue and the set of remembered IDs are bounded.
func TestComposeQueueAndRememberedSetAreCapped(t *testing.T) {
	r := newComposeRigWith(t, func(_ *composeRig, cfg *worktreeenv.ComposeConfig) {
		cfg.AuditQueueMax = 2
		cfg.AuditRememberMax = 3
	})
	for i := range 5 {
		r.svc.QueueAudit(entryWith("q"+strconv.Itoa(i)), true)
	}
	if r.svc.QueueLen() != 2 || r.svc.Dropped() != 3 {
		t.Fatalf("queue %d dropped %d", r.svc.QueueLen(), r.svc.Dropped())
	}
	for i := range 6 {
		if err := r.svc.AppendOnce(context.Background(), entryWith("w"+strconv.Itoa(i)), true); err != nil {
			t.Fatal(err)
		}
	}
	if r.svc.Remembered() != 3 {
		t.Fatalf("remembered %d IDs, want 3", r.svc.Remembered())
	}
}

// Wait does not hang on an operation that does not end.
func TestComposeWaitIsBounded(t *testing.T) {
	r := newComposeRigWith(t, func(_ *composeRig, cfg *worktreeenv.ComposeConfig) {
		cfg.WaitTimeout = 100 * time.Millisecond
	})
	r.confirmedDown(t)
	r.runner.Gate = make(chan struct{}) // the up never returns
	r.up(t)
	for range 400 {
		if len(r.ops()) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	done := make(chan struct{})
	go func() { r.svc.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Wait hung on a docker call that does not return")
	}
}
