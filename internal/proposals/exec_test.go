package proposals

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/mainlock"
)

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(dir, rel string) bool {
	_, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(rel)))
	return err == nil
}

func TestDeleteFilesMovesAndNeverDeletes(t *testing.T) {
	w := newWorld(t)
	writeFile(t, w.wtDir, "x.txt", "keep my bytes x")
	writeFile(t, w.wtDir, "sub/y.txt", "keep my bytes y")
	writeFile(t, w.wtDir, "_to_delete/x.txt", "an earlier move")
	v := w.create(coreprop.DeleteFiles{Where: coreprop.InWorktree{ID: wtID}, Paths: []coreprop.RelPath{"x.txt", "sub/y.txt"}})
	w.confirm(v)
	got := w.waitFor(v.ID, core.ProposalExecuted)

	if exists(w.wtDir, "x.txt") || exists(w.wtDir, "sub/y.txt") {
		t.Fatal("the originals are still in place")
	}
	if readFile(t, w.wtDir, "_to_delete/x.txt") != "an earlier move" {
		t.Fatal("an earlier file in _to_delete was overwritten")
	}
	if readFile(t, w.wtDir, "_to_delete/x.txt.1") != "keep my bytes x" || readFile(t, w.wtDir, "_to_delete/sub/y.txt") != "keep my bytes y" {
		t.Fatal("the moved files do not hold the original bytes")
	}
	moved := got.Result.MovedPaths
	if len(moved) != 2 || moved[0] != "_to_delete/x.txt.1" || moved[1] != "_to_delete/sub/y.txt" {
		t.Fatalf("moved = %v", moved)
	}
}

func TestDeleteFilesReportsWhatCouldNotBeMovedAndRetryFinishesTheRest(t *testing.T) {
	w := newWorld(t)
	writeFile(t, w.wtDir, "a.txt", "a")
	v := w.create(coreprop.DeleteFiles{Where: coreprop.InWorktree{ID: wtID}, Paths: []coreprop.RelPath{"a.txt", "missing.txt"}})
	w.confirm(v)
	got := w.waitFor(v.ID, core.ProposalFailed)
	if !got.Retryable || !strings.Contains(got.Message, "missing.txt") || strings.Contains(got.Message, "a.txt (") {
		t.Fatalf("failure = %+v", got)
	}
	if exists(w.wtDir, "a.txt") || !exists(w.wtDir, "_to_delete/a.txt") {
		t.Fatal("the movable file was not moved before the failure")
	}
	writeFile(t, w.wtDir, "missing.txt", "now here")
	if _, err := w.svc.Retry(w.ctx, v.ID, "user"); err != nil {
		t.Fatal(err)
	}
	done := w.waitFor(v.ID, core.ProposalExecuted)
	if len(done.Result.MovedPaths) != 2 || !exists(w.wtDir, "_to_delete/missing.txt") || !exists(w.wtDir, "_to_delete/a.txt") {
		t.Fatalf("after retry: %+v", done.Result)
	}
}

func TestAgentFileWritesInTheWorktreeAndLeavesNoTempFile(t *testing.T) {
	w := newWorld(t)
	writeFile(t, w.wtDir, "CLAUDE.md", "old rules\n")
	v := w.create(coreprop.AgentFile{Where: coreprop.InWorktree{ID: wtID}, Path: "CLAUDE.md", Content: "new rules\n", ExpectHash: FileHash([]byte("old rules\n"))})
	w.confirm(v)
	w.waitFor(v.ID, core.ProposalExecuted)
	if readFile(t, w.wtDir, "CLAUDE.md") != "new rules\n" {
		t.Fatal("content not replaced")
	}
	entries, _ := os.ReadDir(w.wtDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".ygg-write-") {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}
}

func TestAgentFileCreatesAFileInANewFolder(t *testing.T) {
	w := newWorld(t)
	v := w.create(coreprop.AgentFile{Where: coreprop.InWorktree{ID: wtID}, Path: ".claude/settings.json", Content: "{}\n"})
	w.confirm(v)
	w.waitFor(v.ID, core.ProposalExecuted)
	if readFile(t, w.wtDir, ".claude/settings.json") != "{}\n" {
		t.Fatal("file not created")
	}
}

func TestAgentFileWithAStaleHashChangesNothing(t *testing.T) {
	w := newWorld(t)
	writeFile(t, w.wtDir, "AGENTS.md", "as the agent saw it\n")
	v := w.create(coreprop.AgentFile{Where: coreprop.InWorktree{ID: wtID}, Path: "AGENTS.md", Content: "proposed\n", ExpectHash: FileHash([]byte("as the agent saw it\n"))})
	writeFile(t, w.wtDir, "AGENTS.md", "the user edited it meanwhile\n")
	w.confirm(v)
	got := w.waitFor(v.ID, core.ProposalFailed)
	if got.FailKind != string(coreprop.FailStale) || got.Retryable {
		t.Fatalf("failure = %+v", got)
	}
	if readFile(t, w.wtDir, "AGENTS.md") != "the user edited it meanwhile\n" {
		t.Fatal("a stale agent-file proposal overwrote the user's edit")
	}
}

func TestAgentFileInTheMainCheckoutHoldsTheSharedLock(t *testing.T) {
	w := newWorld(t)
	v := w.create(coreprop.AgentFile{Where: coreprop.MainCheckout{Repo: w.repo.ID}, Path: "AGENTS.md", Content: "rules\n"})
	w.confirm(v)
	w.waitFor(v.ID, core.ProposalExecuted)
	if readFile(t, w.mainDir, "AGENTS.md") != "rules\n" {
		t.Fatal("file not written in the main checkout")
	}
	// The lock is free again afterwards.
	rel, _, ok := w.lock.TryAcquire([]core.RepoID{w.repo.ID}, mainlockBuild("b2"))
	if !ok {
		t.Fatal("the lock was not released")
	}
	rel()
}

func TestInstallGoesThroughBuildsWithoutHoldingTheLock(t *testing.T) {
	w := newWorld(t)
	w.builds.inWait, w.builds.release = make(chan struct{}), make(chan struct{})
	v := w.create(coreprop.Install{Repo: w.repo.ID, Step: "install"})
	w.confirm(v)
	<-w.builds.inWait
	// While the build runs, the builds service must be able to take the lock.
	rel, holder, ok := w.lock.TryAcquire([]core.RepoID{w.repo.ID}, mainlock.Owner{Kind: mainlock.KindBuild, ID: "bld_one"})
	if !ok {
		t.Fatalf("the proposal holds the main-checkout lock during the build (%+v): the build would wait for it forever", holder)
	}
	rel()
	close(w.builds.release)
	got := w.waitFor(v.ID, core.ProposalExecuted)
	if got.Result.BuildID != "bld_one" || !slices.Equal(w.builds.owners, []string{"proposal:" + string(v.ID)}) || w.builds.steps[0] != "install" {
		t.Fatalf("result %+v owners %v steps %v", got.Result, w.builds.owners, w.builds.steps)
	}
}

func TestInstallFailureNamesTheBuild(t *testing.T) {
	w := newWorld(t)
	w.builds.outcome = BuildOutcome{Reason: "npm exited with code 1."}
	v := w.create(coreprop.Install{Repo: w.repo.ID, Step: "install"})
	w.confirm(v)
	got := w.waitFor(v.ID, core.ProposalFailed)
	if got.FailKind != string(coreprop.FailExec) || !got.Retryable || !strings.Contains(got.Message, "bld_one") {
		t.Fatalf("failure = %+v", got)
	}
}

// confirmWithoutRunning is a crash between the Confirm commit and the executor.
func (w *world) confirmWithoutRunning(v View) {
	w.t.Helper()
	_, _, err := w.svc.persistDecision(w.ctx, v.ID, func(rec db.ProposalRecord, tracked bool, now time.Time) coreprop.Input {
		return coreprop.Confirm{Hash: v.ActionHash, By: "user", RepoTracked: tracked, Now: now}
	}, "user")
	if err != nil {
		w.t.Fatal(err)
	}
}

func TestCrashBetweenConfirmAndExecuteIsReconciledAsInterruptedNotExecuted(t *testing.T) {
	w := newWorld(t)
	v := w.create(w.commitAction("a.txt", "feat: a"))
	w.confirmWithoutRunning(v)
	w.svc.Close() // the process dies; nothing ever executed
	w.start()     // restart: Recovered -> Reconcile
	got := w.waitFor(v.ID, core.ProposalFailed)
	if got.FailKind != string(coreprop.FailInterrupted) || !got.Retryable {
		t.Fatalf("failure = %+v", got)
	}
	if w.git.commitCount() != 0 {
		t.Fatal("a restart executed a confirmed proposal on its own")
	}
	// The user decides: Retry runs it.
	if _, err := w.svc.Retry(w.ctx, v.ID, "user"); err != nil {
		t.Fatal(err)
	}
	w.waitFor(v.ID, core.ProposalExecuted)
	if w.git.commitCount() != 1 {
		t.Fatalf("%d commits after one retry", w.git.commitCount())
	}
}

func TestCrashAfterTheCommitLandedReconcilesToExecuted(t *testing.T) {
	w := newWorld(t)
	a := w.commitAction("a.txt", "feat: a")
	v := w.create(a)
	w.confirmWithoutRunning(v)
	// The executor got as far as the commit, then the process died.
	_ = w.git.Fake.Add(w.ctx, w.wtDir, []string{"a.txt"})
	id, err := w.git.Fake.Commit(w.ctx, w.wtDir, []string{"a.txt"}, "feat: a", "")
	if err != nil {
		t.Fatal(err)
	}
	w.svc.Close()
	w.start()
	got := w.waitFor(v.ID, core.ProposalExecuted)
	if got.Result.CommitID != id {
		t.Fatalf("result = %+v, want commit %s", got.Result, id)
	}
	if w.git.commitCount() != 0 {
		t.Fatal("reconcile committed again")
	}
}

func TestReconcileOutcomesPerKind(t *testing.T) {
	w := newWorld(t)
	head := w.head(w.wtDir)
	wt := coreprop.InWorktree{ID: wtID}
	push := w.create(coreprop.Push{Where: wt, Branch: branch, Remote: "origin", ExpectHead: head})
	mr := w.create(coreprop.MergeRequest{Where: wt, Branch: branch, Remote: "origin", TargetBranch: "main", Title: "t", ExpectHead: head})
	writeFile(t, w.wtDir, "AGENTS.md", "")
	af := w.create(coreprop.AgentFile{Where: wt, Path: "CLAUDE.md", Content: "rules\n"})
	writeFile(t, w.wtDir, "gone.txt", "g")
	del := w.create(coreprop.DeleteFiles{Where: wt, Paths: []coreprop.RelPath{"gone.txt"}})
	inst := w.create(coreprop.Install{Repo: w.repo.ID, Step: "install"})
	for _, v := range []View{push, mr, af, del, inst} {
		w.confirmWithoutRunning(v)
	}
	// What had happened before the crash: the branch reached the remote; the
	// agent file was written; the file was moved aside; the install is unknowable.
	if _, err := w.git.Fake.Push(w.ctx, w.wtDir, gitPush(head)); err != nil {
		t.Fatal(err)
	}
	writeFile(t, w.wtDir, "CLAUDE.md", "rules\n")
	dest, err := moveAside(w.wtDir, "gone.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := recordMove(w.wtDir, string(del.ID), "gone.txt", dest); err != nil {
		t.Fatal(err)
	}
	w.svc.Close()
	w.start()
	w.waitFor(push.ID, core.ProposalExecuted)
	w.waitFor(mr.ID, core.ProposalExecuted)
	w.waitFor(af.ID, core.ProposalExecuted)
	w.waitFor(del.ID, core.ProposalExecuted)
	if got := w.waitFor(inst.ID, core.ProposalFailed); got.FailKind != string(coreprop.FailInterrupted) {
		t.Fatalf("install = %+v", got)
	}
}

func TestMergeRequestReconcileIsUnknownWhenTheBranchIsNotThere(t *testing.T) {
	w := newWorld(t)
	v := w.create(coreprop.MergeRequest{Where: coreprop.InWorktree{ID: wtID}, Branch: branch, Remote: "origin", TargetBranch: "main", Title: "t", ExpectHead: w.head(w.wtDir)})
	w.confirmWithoutRunning(v)
	w.svc.Close()
	w.start()
	if got := w.waitFor(v.ID, core.ProposalFailed); got.FailKind != string(coreprop.FailInterrupted) || !got.Retryable {
		t.Fatalf("failure = %+v", got)
	}
}

// P10 on the shell side: whatever the executors ask git to do, the argument
// lists git would run hold no force, delete, reset, clean or remove.
func TestExecutorsAskForNoDestructiveGitForm(t *testing.T) {
	w := newWorld(t)
	wt := coreprop.InWorktree{ID: wtID}
	steps := []func() coreprop.Action{
		func() coreprop.Action { return w.commitAction("a.txt", "feat: a") },
		func() coreprop.Action {
			return coreprop.Push{Where: wt, Branch: branch, Remote: "origin", ExpectHead: w.head(w.wtDir)}
		},
		func() coreprop.Action {
			w.switchBranch("ygg/other")
			return coreprop.MergeRequest{Where: wt, Branch: "ygg/other", Remote: "origin", TargetBranch: "main", Title: "t", Description: "d", ExpectHead: w.head(w.wtDir)}
		},
	}
	for _, mk := range steps {
		v := w.create(mk())
		w.confirm(v)
		w.waitFor(v.ID, core.ProposalExecuted)
	}
	forbidden := []string{"--force", "-f", "--force-with-lease", "--delete", "-d", "-D", "--mirror", "reset", "clean", "rm", "branch", "-A", "--all"}
	check := func(argv []string) {
		t.Helper()
		for _, tok := range argv {
			if slices.Contains(forbidden, tok) || strings.HasPrefix(tok, "+") || strings.HasPrefix(tok, ":") {
				t.Errorf("argv %v holds %q", argv, tok)
			}
		}
	}
	for _, p := range w.git.pushes {
		check(git.PushArgv(p, "H"))
	}
	for _, paths := range w.git.adds {
		check(git.AddArgv(paths))
		check(git.CommitArgv(paths, "H"))
	}
	if len(w.git.pushes) == 0 || len(w.git.adds) == 0 {
		t.Fatal("no git call was observed; the test checks nothing")
	}
}
