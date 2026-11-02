package main

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

type recChangeSetHub struct {
	mu  sync.Mutex
	got []string
}

func (h *recChangeSetHub) PublishChangeSet(id core.ChangeSetID, _ core.TaskID, st core.ChangeSetStatus) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.got = append(h.got, string(id)+":"+string(st))
}

func (h *recChangeSetHub) PublishNotes(task core.TaskID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.got = append(h.got, "notes:"+string(task))
}

func TestLateChangeSetHubForwardsOnceSet(t *testing.T) {
	var h lateChangeSetHub
	h.PublishChangeSet("cs_1", "tsk_1", core.ChangeSetOpen) // nothing set: dropped, no panic
	h.PublishNotes("tsk_1")
	rec := &recChangeSetHub{}
	h.Set(rec)
	h.PublishChangeSet("cs_1", "tsk_1", core.ChangeSetShipped)
	h.PublishNotes("tsk_1")
	if len(rec.got) != 2 || rec.got[0] != "cs_1:shipped" || rec.got[1] != "notes:tsk_1" {
		t.Fatalf("got %v", rec.got)
	}
}

// The service starts and its loops end with the context. Store, proposals,
// tasks and git are nil: New only checks that the dependencies are present,
// and the loops are cancelled before they read.
func TestChangeSetServiceStartsAndStops(t *testing.T) {
	svc, err := newChangeSetsService(nil, nil, nil, nil, redactorForTest(), &lateChangeSetHub{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var g errgroup.Group
	startChangeSets(&g, ctx, svc)
	done := make(chan error, 1)
	go func() { done <- g.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the change-set loops did not stop")
	}
}

func TestChangeSetAdaptersFailPlainlyWithoutATaskOrWorktree(t *testing.T) {
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "ygg.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	store := db.NewStore(d, nil)
	if _, err := (noteMessenger{store: store}).SendMessage(context.Background(), "tsk_missing", "hi"); err == nil {
		t.Fatal("a message to an unknown task was sent")
	}
	if _, err := (changeSetDiffs{store: store}).FileHashes(context.Background(), "tsk_missing", "rep_1"); err == nil {
		t.Fatal("a diff of a task with no worktree was read")
	}
}
