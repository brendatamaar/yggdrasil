package proposals

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/landing"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/platform"
)

func (w *landWorld) stagedDirFor(t *testing.T, leaf string) string {
	t.Helper()
	root, err := platform.ResolveRoot(w.root)
	if err != nil {
		t.Fatal(err)
	}
	d := filepath.Join(root, landingStagingDir, leaf)
	if err := os.MkdirAll(d, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "keep.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return d
}

func (w *landWorld) setRow(id core.WorktreeID, f func(*WorktreeInfo)) {
	w.res.mu.Lock()
	defer w.res.mu.Unlock()
	r := w.res.wts[id]
	f(&r)
	w.res.wts[id] = r
}

func (w *landWorld) failMarks(tag string) {
	w.marks.mu.Lock()
	w.marks.failTag = tag
	w.marks.mu.Unlock()
}

// Worktree "a" must not claim the leftover of "a-b" by name prefix.
func TestRemoveDoesNotClaimAnotherWorktreesStagedFolder(t *testing.T) {
	t.Parallel()
	w := newLandWorld(t)
	idA, dirA, _ := w.worktree("a", true)
	idAB, _, _ := w.worktree("a-b", true)
	left := w.stagedDirFor(t, "wt-a-b-old1") // a-b's, from an earlier run
	w.setRow(idAB, func(r *WorktreeInfo) { r.Status, r.StagedLeaf = core.WorktreeRemoving, "wt-a-b-old1" })
	w.run(coreprop.RemoveWorktree{Worktree: idA}, core.ProposalExecuted)
	if _, err := os.Stat(left); err != nil {
		t.Fatalf("a-b's staged folder was touched: %v", err)
	}
	if _, err := os.Stat(dirA); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a was not removed")
	}
	w.res.mu.Lock()
	defer w.res.mu.Unlock()
	if w.res.wts[idAB].Status != core.WorktreeRemoving || w.res.wts[idA].Status != core.WorktreeRemoved {
		t.Fatalf("rows: a=%s a-b=%s", w.res.wts[idA].Status, w.res.wts[idAB].Status)
	}
}

// The live folder and the remembered staged folder both present: nothing is
// deleted and nothing is marked.
func TestRemoveRefusesWhenStagedAndLiveFolderBothExist(t *testing.T) {
	t.Parallel()
	w := newLandWorld(t)
	id, dir, _ := w.worktree("a", true)
	left := w.stagedDirFor(t, "wt-a-old1")
	w.setRow(id, func(r *WorktreeInfo) { r.Status, r.StagedLeaf = core.WorktreeRemoving, "wt-a-old1" })
	w.run(coreprop.RemoveWorktree{Worktree: id}, core.ProposalFailed)
	for _, d := range []string{dir, left} {
		if _, err := os.Stat(d); err != nil {
			t.Fatalf("%s was touched: %v", d, err)
		}
	}
	if m := w.marks.entries(); len(m) != 0 {
		t.Fatalf("marks = %v", m)
	}
}

// A failure to record the end of a removal is a retryable failure, and the
// retry writes the marks without deleting anything again.
func TestRemoveRecordFailureIsRetryable(t *testing.T) {
	t.Parallel()
	w := newLandWorld(t)
	id, dir, _ := w.worktree("a", true)
	w.failMarks("removed")
	v := w.run(coreprop.RemoveWorktree{Worktree: id}, core.ProposalFailed)
	if !v.Retryable || !strings.Contains(v.Message, "could not record") {
		t.Fatalf("failure = %+v", v)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the folder should be gone")
	}
	w.failMarks("")
	if _, err := w.svc.Retry(w.ctx, v.ID, "user"); err != nil {
		t.Fatal(err)
	}
	w.settle(v.ID, core.ProposalExecuted)
	if m := w.marks.entries(); m[len(m)-1] != "removed" {
		t.Fatalf("marks = %v", m)
	}
}

func TestMergeRecordFailureIsRetryable(t *testing.T) {
	t.Parallel()
	w := newLandWorld(t)
	id, _, tip := w.worktree("m", true)
	w.failMarks("merged")
	v := w.run(w.merge(id, "m", tip), core.ProposalFailed)
	if !v.Retryable || !strings.Contains(v.Message, "could not record") {
		t.Fatalf("failure = %+v", v)
	}
	w.failMarks("")
	if _, err := w.svc.Retry(w.ctx, v.ID, "user"); err != nil {
		t.Fatal(err)
	}
	w.settle(v.ID, core.ProposalExecuted)
	w.res.mu.Lock()
	defer w.res.mu.Unlock()
	if w.res.wts[id].Status != core.WorktreeMerged {
		t.Fatalf("status = %s", w.res.wts[id].Status)
	}
}

// Only "does not exist" means nothing is staged; any other error is surfaced.
func TestFindStagedOnlyNotExistMeansAbsent(t *testing.T) {
	t.Parallel()
	r := &removeRun{stageID: "id1"}
	r.req.Root, r.req.Name = filepath.Join(t.TempDir(), "root"), "a"
	if _, ok, err := r.findStaged(); ok || err != nil {
		t.Fatalf("absent: ok=%v err=%v", ok, err)
	}
	r.lstat = func(string) (fs.FileInfo, error) { return nil, fs.ErrPermission }
	if _, ok, err := r.findStaged(); ok || err == nil {
		t.Fatalf("unreadable: ok=%v err=%v", ok, err)
	}
}

// An agent cannot create a merge or a removal proposal (defence in depth).
func TestAgentCannotCreateLandingProposals(t *testing.T) {
	t.Parallel()
	w := newLandWorld(t)
	id, _, tip := w.worktree("a", true)
	for _, a := range []coreprop.Action{w.merge(id, "a", tip), coreprop.RemoveWorktree{Worktree: id}} {
		_, err := w.svc.Create(w.ctx, CreateRequest{Task: w.task, Action: a, Summary: "x", Source: coreprop.AgentSource{Task: w.task}})
		var rf *Refusal
		if !errors.As(err, &rf) {
			t.Fatalf("%T from an agent: %v", a, err)
		}
	}
}

// WorktreeMarks contract: monotone, idempotent, a wrong status is a no-op, a
// missing worktree is db.ErrNotFound. Run against the fake and the real one.
func runMarksContract(t *testing.T, m WorktreeMarks, commit func(func(tx *db.Tx) error) error, status func() core.WorktreeStatus, leaf func() string, missing, id core.WorktreeID) {
	t.Helper()
	ctx := context.Background()
	if err := commit(func(tx *db.Tx) error { return m.MarkRemoved(ctx, tx, id) }); err != nil || status() != core.WorktreeActive {
		t.Fatalf("removed from active must be a no-op: %v %s", err, status())
	}
	for range 2 { // idempotent
		if err := commit(func(tx *db.Tx) error { return m.MarkMerged(ctx, tx, id) }); err != nil || status() != core.WorktreeMerged {
			t.Fatalf("merged: %v %s", err, status())
		}
	}
	for range 2 {
		if err := commit(func(tx *db.Tx) error { return m.MarkRemoving(ctx, tx, id, "stage_1") }); err != nil || status() != core.WorktreeRemoving {
			t.Fatalf("removing: %v %s", err, status())
		}
	}
	if got := leaf(); got != "stage_1" {
		t.Fatalf("MarkRemoving must remember the leaf, got %q", got)
	}
	if err := commit(func(tx *db.Tx) error { return m.MarkRemoving(ctx, tx, id, "stage_other") }); err != nil || status() != core.WorktreeRemoving || leaf() != "stage_1" {
		t.Fatalf("a repeated MarkRemoving must keep the first leaf: %v %s %q", err, status(), leaf())
	}
	if err := commit(func(tx *db.Tx) error { return m.MarkMerged(ctx, tx, id) }); err != nil || status() != core.WorktreeRemoving {
		t.Fatalf("merged must not move backwards: %v %s", err, status())
	}
	for range 2 {
		if err := commit(func(tx *db.Tx) error { return m.MarkRemoved(ctx, tx, id) }); err != nil || status() != core.WorktreeRemoved {
			t.Fatalf("removed: %v %s", err, status())
		}
	}
	if err := commit(func(tx *db.Tx) error { return m.MarkRemoving(ctx, tx, id, "stage_2") }); err != nil || status() != core.WorktreeRemoved {
		t.Fatalf("removing after removed must be a no-op: %v %s", err, status())
	}
	for name, f := range map[string]func(tx *db.Tx) error{
		"merged":   func(tx *db.Tx) error { return m.MarkMerged(ctx, tx, missing) },
		"removing": func(tx *db.Tx) error { return m.MarkRemoving(ctx, tx, missing, "s") },
		"removed":  func(tx *db.Tx) error { return m.MarkRemoved(ctx, tx, missing) },
	} {
		if err := commit(f); !errors.Is(err, db.ErrNotFound) {
			t.Fatalf("missing worktree (%s): %v", name, err)
		}
	}
}

func TestWorktreeMarksContractFake(t *testing.T) {
	t.Parallel()
	w := newLandWorld(t)
	id, _, _ := w.worktree("c", false)
	runMarksContract(t, w.marks, func(f func(tx *db.Tx) error) error { return f(nil) },
		func() core.WorktreeStatus { w.res.mu.Lock(); defer w.res.mu.Unlock(); return w.res.wts[id].Status },
		func() string { w.res.mu.Lock(); defer w.res.mu.Unlock(); return w.res.wts[id].StagedLeaf }, "wt_missing", id)
}

func TestWorktreeMarksContractDB(t *testing.T) {
	t.Parallel()
	w := newLandWorld(t)
	ctx := context.Background()
	now := w.svc.now()
	wt := core.Worktree{ID: "wt_db", RepoID: w.repo, TaskID: w.task, Name: "calm-pine", Path: filepath.Join(t.TempDir(), "calm-pine"), Branch: "ygg/calm-pine", CreatedAt: now}
	aud := core.AuditEntry{ID: "aud_wt", At: now, Actor: "system", Action: "worktree.create", Subject: "task:" + string(w.task)}
	if err := w.store.Commit(ctx, func(tx *db.Tx) error { _, err := tx.RecordWorktree(ctx, wt, "abc", aud); return err }); err != nil {
		t.Fatal(err)
	}
	runMarksContract(t, DBMarks{}, func(f func(tx *db.Tx) error) error { return w.store.Commit(ctx, f) },
		func() core.WorktreeStatus {
			g, err := w.store.GetWorktree(ctx, wt.ID)
			if err != nil {
				t.Fatal(err)
			}
			return g.Status
		},
		func() string {
			n, err := w.store.GetWorktreeStagedName(ctx, wt.ID)
			if err != nil {
				t.Fatal(err)
			}
			return n
		}, "wt_missing", wt.ID)
}

func (w *landWorld) plannedLeaf(t *testing.T, name string, pid core.ProposalID) string {
	t.Helper()
	root, err := platform.ResolveRoot(w.root)
	if err != nil {
		t.Fatal(err)
	}
	sp, err := landing.StagingPath(root, name, StageIDFor(pid))
	if err != nil {
		t.Fatal(err)
	}
	return sp.Leaf
}

// A crash between the rename and MarkRemoving leaves the folder staged under
// the leaf this proposal plans; the confirmed run resumes and finishes.
func TestRemoveResumesAfterCrashBetweenRenameAndMark(t *testing.T) {
	t.Parallel()
	w := newLandWorld(t)
	id, dir, _ := w.worktree("a", true)
	other := w.stagedDirFor(t, "wt-a-b-old1") // someone else's leftover
	v := w.create(coreprop.RemoveWorktree{Worktree: id})
	staged := w.stagedDirFor(t, w.plannedLeaf(t, "wt-a", v.ID))
	if err := os.RemoveAll(dir); err != nil { // the rename already happened
		t.Fatal(err)
	}
	if _, err := w.svc.Confirm(w.ctx, v.ID, v.ActionHash, "user"); err != nil {
		t.Fatal(err)
	}
	w.settle(v.ID, core.ProposalExecuted)
	if _, err := os.Stat(staged); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the staged folder should be deleted")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("another folder was touched: %v", err)
	}
	w.res.mu.Lock()
	defer w.res.mu.Unlock()
	if w.res.wts[id].Status != core.WorktreeRemoved {
		t.Fatalf("status = %s", w.res.wts[id].Status)
	}
}

// MarkRemoving failing on both tries leaves the folder staged and the row
// untouched; the retry plans the same leaf and resumes.
func TestRemoveResumesWhenMarkRemovingFailedTwice(t *testing.T) {
	t.Parallel()
	w := newLandWorld(t)
	id, dir, _ := w.worktree("a", true)
	w.marks.mu.Lock()
	w.marks.failN = 2 // exactly the two persist attempts
	w.marks.mu.Unlock()
	v := w.run(coreprop.RemoveWorktree{Worktree: id}, core.ProposalFailed)
	if !v.Retryable {
		t.Fatalf("failure = %+v", v)
	}
	staged := filepath.Join(w.rootResolved(t), landingStagingDir, w.plannedLeaf(t, "wt-a", v.ID))
	if _, err := os.Stat(staged); err != nil {
		t.Fatalf("the folder should be staged: %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the live folder should be gone")
	}
	if _, err := w.svc.Retry(w.ctx, v.ID, "user"); err != nil {
		t.Fatal(err)
	}
	w.settle(v.ID, core.ProposalExecuted)
	if _, err := os.Stat(staged); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the staged folder should be deleted")
	}
	w.res.mu.Lock()
	defer w.res.mu.Unlock()
	if w.res.wts[id].Status != core.WorktreeRemoved {
		t.Fatalf("status = %s", w.res.wts[id].Status)
	}
}

func (w *landWorld) rootResolved(t *testing.T) string {
	t.Helper()
	root, err := platform.ResolveRoot(w.root)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestStageIDForIsDeterministicDistinctAndValid(t *testing.T) {
	t.Parallel()
	ids := []core.ProposalID{"prop_1", "prop_2", "a/b", "a_b", "a b", "", "  ", "é", "x", core.ProposalID(strings.Repeat("z", 100)), core.ProposalID(strings.Repeat("z", 101))}
	seen := map[string]core.ProposalID{}
	for _, id := range ids {
		s := StageIDFor(id)
		if s != StageIDFor(id) {
			t.Fatalf("%q: not deterministic", id)
		}
		if _, err := landing.StagingPath("root", "n", s); err != nil {
			t.Fatalf("%q -> %q invalid: %v", id, s, err)
		}
		if prev, dup := seen[s]; dup {
			t.Fatalf("%q and %q share %q", prev, id, s)
		}
		seen[s] = id
	}
}

// Two proposals get different leaves, so names a and a-b stay apart.
func TestTwoRemovalsGetDifferentLeaves(t *testing.T) {
	t.Parallel()
	w := newLandWorld(t)
	if a, b := w.plannedLeaf(t, "wt-a", "prop_1"), w.plannedLeaf(t, "wt-a", "prop_2"); a == b {
		t.Fatalf("same leaf %q", a)
	}
	if a, b := w.plannedLeaf(t, "wt-a", "prop_1"), w.plannedLeaf(t, "wt-a-b", "prop_1"); a == b {
		t.Fatalf("same leaf %q", a)
	}
}

// A symlink or file at the staged leaf is unreadable, not absent.
func TestFindStagedNonDirectoryIsUnreadable(t *testing.T) {
	t.Parallel()
	r := &removeRun{stageID: "id1"}
	r.req.Root, r.req.Name = filepath.Join(t.TempDir(), "root"), "a"
	r.lstat = func(string) (fs.FileInfo, error) { return fakeInfo{dir: false}, nil }
	if _, ok, err := r.findStaged(); ok || err == nil {
		t.Fatalf("non-dir: ok=%v err=%v", ok, err)
	}
	r.lstat = func(string) (fs.FileInfo, error) { return fakeInfo{dir: true}, nil }
	if _, ok, err := r.findStaged(); !ok || err != nil {
		t.Fatalf("dir: ok=%v err=%v", ok, err)
	}
}

// A remembered leaf that is not valid is refused before the disk is touched.
func TestFindStagedRejectsInvalidRememberedLeaf(t *testing.T) {
	t.Parallel()
	r := &removeRun{lstat: func(string) (fs.FileInfo, error) { t.Fatal("touched the disk"); return nil, nil }}
	r.req.Root, r.req.Name, r.req.StagedLeaf = t.TempDir(), "a", "../evil"
	if _, ok, err := r.findStaged(); ok || err == nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

type fakeInfo struct {
	fs.FileInfo
	dir bool
}

func (f fakeInfo) IsDir() bool { return f.dir }

func TestUnrecordedMergeMessage(t *testing.T) {
	t.Parallel()
	with := unrecordedMergeMsg([]landing.MergeCommand{landing.WriteAudit{}, landing.MarkWorktreeMerged{}})
	without := unrecordedMergeMsg([]landing.MergeCommand{landing.WriteAudit{}})
	if !strings.Contains(with, "done on disk") || strings.Contains(without, "done on disk") || strings.Contains(without, "merged twice") {
		t.Fatalf("with=%q without=%q", with, without)
	}
}
