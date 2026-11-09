package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	wenv "github.com/brendatamaar/yggdrasil/internal/core/worktreeenv"
	"github.com/brendatamaar/yggdrasil/internal/worktreeenv"
	"github.com/brendatamaar/yggdrasil/internal/worktreeenv/worktreeenvtest"
)

// Invariant 8 at shutdown: closePhase5 makes a last attempt at the compose
// audit entries that could not be written while the sink was down.
func TestClosePhase5FlushesAQueuedComposeAudit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := worktreeenvtest.NewStore("rep_1")
	files := &worktreeenvtest.ComposeFiles{}
	wt := core.Worktree{ID: "wt_1", RepoID: "rep_1", Path: "/wt/one"}
	files.Put(wt.Path, "compose.yml", "services: {db: {ports: ['${YGG_PORT_0}:5432']}}")
	var down atomic.Bool
	store.AuditErr = func(e core.AuditEntry) error {
		if down.Load() && e.Action == wenv.ActionComposeStarted {
			return errors.New("disk full")
		}
		return nil
	}
	var n atomic.Int32
	runner := &worktreeenvtest.ComposeRunner{}
	svc, err := worktreeenv.NewComposeService(ctx, worktreeenv.ComposeConfig{
		Store: store, Audit: store, Runner: runner, Files: files,
		Worktree: func(context.Context, core.WorktreeID) (core.Worktree, error) { return wt, nil },
		Block: func(context.Context, core.WorktreeID) (core.PortBlock, bool, error) {
			return core.PortBlock{Base: 20040, Size: 20}, true, nil
		},
		Now:   func() time.Time { return time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC) },
		NewID: func(p string) string { return p + "_" + strconv.Itoa(int(n.Add(1))) },
	})
	if err != nil {
		t.Fatal(err)
	}
	waitState := func(pred func(wenv.Service) bool) {
		t.Helper()
		for range 400 {
			st, err := svc.Status(ctx, wt.ID)
			if err != nil {
				t.Fatal(err)
			}
			if pred(st.State) {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("state never reached")
	}
	if err := svc.Configure(ctx, "rep_1", true, "compose.yml"); err != nil {
		t.Fatal(err)
	}
	st, err := svc.Status(ctx, wt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Confirm(ctx, wt.ID, st.Hash); err != nil {
		t.Fatal(err)
	}
	waitState(func(s wenv.Service) bool { _, ok := s.(wenv.Down); return ok })
	down.Store(true)
	if _, err := svc.Up(ctx, wt.ID); err != nil {
		t.Fatal(err)
	}
	// Wait for the transition without a Status call: a read would flush the queue.
	for range 400 {
		if len(runner.Calls()) >= 2 { // the startup ps and the up
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // the end of the operation fails its audit write and queues it
	started := func() int {
		c := 0
		for _, a := range store.Audits() {
			if a.Action == wenv.ActionComposeStarted {
				c++
			}
		}
		return c
	}
	if started() != 0 {
		t.Fatal("the entry was written while the sink was down")
	}
	down.Store(false)
	a := &app{compose: svc, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	a.closePhase5()
	if started() != 1 {
		t.Fatalf("closePhase5 wrote %d started entries, want 1", started())
	}
}
