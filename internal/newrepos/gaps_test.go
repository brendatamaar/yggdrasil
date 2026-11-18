package newrepos

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corenewrepos "github.com/brendatamaar/yggdrasil/internal/core/newrepos"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/git/gittest"
	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// Tests added from the mutation pass over this package (NR-01 to NR-04, safety
// invariants 1, 2, 3, 4 and 8): every test here either closes a mutant the
// older tests let survive or pins an invariant a mutant could break silently.

// stageOf names the stage that a port call belongs to.
func stageOf(op string, nth int) string {
	switch op {
	case "CreateChildDir":
		return "create_directory"
	case "InitRepo":
		return "init_git"
	case "CreateFile":
		if nth == 1 {
			return "write_readme"
		}
		return "write_ignore"
	case "CommitFirst":
		return "first_commit"
	case "CreateRepo":
		return "create_remote"
	case "AddOrigin":
		return "add_origin"
	case "PublishBranch":
		return "push_head"
	}
	return ""
}

// Invariant 2 and 8: the intent of a stage is durable, and the shared main
// lock is held exactly while a local stage mutates, before any effect runs. A
// mutant that runs the effect first (or forgets the lock) is caught at the call.
func TestIntentDurableAndLockHeldAtEveryEffect(t *testing.T) {
	w := newWorld(t)
	rec := w.reserveAndPropose("shop-new")
	op := rec.Operation.ID
	var mu sync.Mutex
	seen := map[string]bool{}
	w.f.observe = func(call string) {
		stage := stageOf(call, w.f.count(call))
		if stage == "" {
			return
		}
		effects, err := w.store.ListEffectsByOperation(w.ctx, op)
		if err != nil {
			t.Errorf("%s: %v", call, err)
			return
		}
		intent := false
		for _, e := range effects {
			if e.Stage == stage && e.Attempt == 1 && e.State == db.EffectIntent {
				intent = true
			}
		}
		if !intent {
			t.Errorf("%s ran before the intent of stage %s was recorded: %+v", call, stage, effects)
		}
		w.locks.mu.Lock()
		held := w.locks.held
		w.locks.mu.Unlock()
		local := call != "CreateRepo" && call != "PublishBranch"
		switch {
		case local && held != 1:
			t.Errorf("%s ran with the main lock held %d times; a local mutation needs it", call, held)
		case !local && held != 0:
			t.Errorf("%s waited on the network while holding the main lock (%d)", call, held)
		}
		mu.Lock()
		seen[call] = true
		mu.Unlock()
	}
	if err := w.svc.ConfirmInit(w.ctx, op, rec.SpecHash, 1); err != nil {
		t.Fatal(err)
	}
	w.waitFor(op, "ready", isState[corenewrepos.Ready])
	if _, err := w.publish(rec, "shop-new"); err != nil {
		t.Fatal(err)
	}
	w.waitFor(op, "published", isState[corenewrepos.Published])
	mu.Lock()
	defer mu.Unlock()
	for _, c := range []string{"CreateChildDir", "InitRepo", "CreateFile", "CommitFirst", "CreateRepo", "AddOrigin", "PublishBranch"} {
		if !seen[c] {
			t.Errorf("the check never saw %s", c)
		}
	}
	if w.locks.held != 0 {
		t.Errorf("the main lock is still held: %d", w.locks.held)
	}
}

// Invariant 2: publishing needs the hash that was previewed.
func TestConfirmPublishWithWrongHashIsRefused(t *testing.T) {
	w := newWorld(t)
	rec := w.reserveAndPropose("shop-new")
	_ = w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1)
	ready := w.waitFor(rec.Operation.ID, "ready", isState[corenewrepos.Ready]).(corenewrepos.Ready)
	w.fake.SetLocalHead(w.dir("shop-new"), "main", ready.InitialHead)
	req := PublishRequest{HostInstance: "github.com", Owner: "acme", OwnerKind: corenewrepos.OwnerOrg, Name: "shop-new"}
	pv, err := w.svc.PreparePublish(w.ctx, rec.Operation.ID, req, w.proposal(rec))
	if err != nil || len(pv.Problems) != 0 {
		t.Fatalf("%v %v", err, pv.Problems)
	}
	req.Visibility = corenewrepos.VisibilityPublic // another spec: another hash
	other, err := w.svc.PreparePublish(w.ctx, rec.Operation.ID, req, "")
	if err != nil || other.Hash == pv.Hash {
		t.Fatalf("a public preview must have another hash: %v", err)
	}
	for name, h := range map[string]string{"zeros": strings.Repeat("0", 64), "empty": "", "the public preview": other.Hash} {
		if err := w.svc.ConfirmPublish(w.ctx, rec.Operation.ID, h, 1); !errors.Is(err, ErrHashMismatch) {
			t.Errorf("%s: err = %v, want ErrHashMismatch", name, err)
		}
	}
	if w.f.count("CreateRepo") != 0 || w.host.creates != 0 {
		t.Fatalf("a remote was created for a hash nobody previewed: %v", w.mutations())
	}
	if _, ok := w.get(rec.Operation.ID).(corenewrepos.Ready); !ok {
		t.Fatalf("state = %T", w.get(rec.Operation.ID))
	}
	// The previewed hash still works afterwards.
	if err := w.svc.ConfirmPublish(w.ctx, rec.Operation.ID, pv.Hash, 1); err != nil {
		t.Fatal(err)
	}
	w.waitFor(rec.Operation.ID, "published", isState[corenewrepos.Published])
}

// Invariant 2: what changed between the preview and the confirm is not
// published: a moved HEAD, or an origin that appeared.
func TestConfirmPublishRefusedWhenThePreviewIsStale(t *testing.T) {
	prepare := func(t *testing.T) (*world, db.ReservationRecord, PublishPreview) {
		w := newWorld(t)
		rec := w.reserveAndPropose("shop-new")
		_ = w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1)
		ready := w.waitFor(rec.Operation.ID, "ready", isState[corenewrepos.Ready]).(corenewrepos.Ready)
		w.fake.SetLocalHead(w.dir("shop-new"), "main", ready.InitialHead)
		pv, err := w.svc.PreparePublish(w.ctx, rec.Operation.ID,
			PublishRequest{HostInstance: "github.com", Owner: "acme", OwnerKind: corenewrepos.OwnerOrg, Name: "shop-new"}, w.proposal(rec))
		if err != nil || len(pv.Problems) != 0 {
			t.Fatalf("%v %v", err, pv.Problems)
		}
		return w, rec, pv
	}
	t.Run("HEAD moved", func(t *testing.T) {
		w, rec, pv := prepare(t)
		gittest.CommitInRepo(t, w.dir("shop-new"), map[string]string{"notes.txt": "later\n"}, "A second commit")
		err := w.svc.ConfirmPublish(w.ctx, rec.Operation.ID, pv.Hash, 1)
		var ref *RefusedError
		if !errors.As(err, &ref) || ref.Reason != corenewrepos.StopPreconditions {
			t.Fatalf("err = %v, want a refusal for changed preconditions", err)
		}
		if w.f.count("CreateRepo") != 0 {
			t.Fatal("a remote was created for a repo whose HEAD moved after the preview")
		}
	})
	t.Run("origin appeared", func(t *testing.T) {
		w, rec, pv := prepare(t)
		if err := w.initW.Origins().AddOrigin(w.ctx, w.dir("shop-new"), "https://git.acme-shop.test/acme/other.git"); err != nil {
			t.Fatal(err)
		}
		err := w.svc.ConfirmPublish(w.ctx, rec.Operation.ID, pv.Hash, 1)
		var ref *RefusedError
		if !errors.As(err, &ref) || ref.Reason != corenewrepos.StopOriginExists {
			t.Fatalf("err = %v, want StopOriginExists", err)
		}
		if w.f.count("CreateRepo") != 0 {
			t.Fatal("a remote was created for a repo that already has an origin")
		}
		if u, _, _ := w.origin.Origin(w.ctx, w.dir("shop-new")); u != "https://git.acme-shop.test/acme/other.git" {
			t.Fatalf("origin was replaced: %q", u)
		}
	})
}

// An unknown first commit is adopted only when tree, message and author are
// exactly what was confirmed; any other commit is a conflict a human looks at.
func TestUnknownFirstCommitIsAdoptedOnlyWhenExact(t *testing.T) {
	foreign := func(t *testing.T, w *world, mutate func()) {
		t.Helper()
		rec := w.reserveAndPropose("shop-new")
		if err := w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1); err != nil {
			t.Fatal(err)
		}
		w.assertOutcome("foreign commit", w.waitSettled(rec.Operation.ID), outcome{stopped: corenewrepos.StopConflict})
		if w.f.count("CommitFirst") != 1 {
			t.Fatalf("the commit was repeated: %v", w.mutations())
		}
		if _, err := w.store.GetRepo(w.ctx, rec.Operation.Repo); !errors.Is(err, db.ErrNotFound) {
			t.Fatalf("a repo with a foreign first commit became ready: %v", err)
		}
		w.restart()
		_ = w.svc.Recover(w.ctx)
		w.assertOutcome("after restart", w.waitSettled(rec.Operation.ID), outcome{stopped: corenewrepos.StopConflict})
	}
	t.Run("another message", func(t *testing.T) {
		w := newWorld(t)
		w.f.onWith("CommitFirst", 1, faultBefore, errInjected, func() {
			files := []string{"README.md", ".gitignore"}
			if _, err := w.initW.Initializer().CommitFirst(w.ctx, w.dir("shop-new"), files, "Somebody else's commit"); err != nil {
				t.Error(err)
			}
		})
		foreign(t, w, nil)
	})
	t.Run("another tree", func(t *testing.T) {
		w := newWorld(t)
		w.f.onWith("CommitFirst", 1, faultLost, errInjected, func() {
			if err := os.WriteFile(filepath.Join(w.dir("shop-new"), "README.md"), []byte("# not what was confirmed\n"), 0o600); err != nil {
				t.Error(err)
			}
		})
		foreign(t, w, nil)
	})
}

// An unknown git init or file write is adopted only when the disk shows exactly
// what was confirmed (the branch of HEAD, the exact content).
func TestUnknownStageWithForeignResultIsAConflict(t *testing.T) {
	t.Run("init_git with another initial branch", func(t *testing.T) {
		w := newWorld(t)
		w.f.onWith("InitRepo", 1, faultBefore, errInjected, func() {
			if err := w.initW.Initializer().InitRepo(w.ctx, w.dir("shop-new"), "trunk"); err != nil {
				t.Error(err)
			}
		})
		rec := w.reserveAndPropose("shop-new")
		_ = w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1)
		w.assertOutcome("foreign git dir", w.waitSettled(rec.Operation.ID), outcome{stopped: corenewrepos.StopConflict})
		if w.f.count("CreateFile") != 0 {
			t.Fatal("a stage ran in a git folder that was not made by this operation")
		}
	})
	for _, c := range []struct {
		nth  int
		file string
	}{{1, "README.md"}, {2, ".gitignore"}} {
		t.Run("write "+c.file+" with other content", func(t *testing.T) {
			w := newWorld(t)
			w.f.onWith("CreateFile", c.nth, faultBefore, errInjected, func() {
				if err := os.WriteFile(filepath.Join(w.dir("shop-new"), c.file), []byte("theirs\n"), 0o600); err != nil {
					t.Error(err)
				}
			})
			rec := w.reserveAndPropose("shop-new")
			_ = w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1)
			w.assertOutcome("foreign file", w.waitSettled(rec.Operation.ID), outcome{stopped: corenewrepos.StopConflict})
			if got, _ := w.readFile("shop-new", c.file); got != "theirs\n" {
				t.Fatalf("%s was replaced: %q", c.file, got)
			}
			if w.f.count("CommitFirst") != 0 {
				t.Fatal("a commit was made on top of a file this operation did not write")
			}
		})
	}
}

// An unknown push is done only when the remote shows exactly the confirmed
// head; another head is a conflict and nothing is pushed over it (no force).
func TestLostPushReplyWithAnotherRemoteHeadIsAConflict(t *testing.T) {
	w := newWorld(t)
	rec := w.reserveAndPropose("shop-new")
	_ = w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1)
	w.waitFor(rec.Operation.ID, "ready", isState[corenewrepos.Ready])
	w.f.onWith("PublishBranch", 1, faultBefore, git.ErrPublishUnknown, func() {
		w.fake.SetRemoteHead("https://github.com/acme/shop-new.git", "main", strings.Repeat("e", 40))
	})
	if _, err := w.publish(rec, "shop-new"); err != nil {
		t.Fatal(err)
	}
	w.assertOutcome("remote moved", w.waitSettled(rec.Operation.ID), outcome{stopped: corenewrepos.StopConflict})
	if w.f.count("PublishBranch") != 1 || w.fake.Pushes() != 0 {
		t.Fatalf("something was pushed over a head that is not ours: %v pushes=%d", w.mutations(), w.fake.Pushes())
	}
	if h := w.fake.RemoteHead("https://github.com/acme/shop-new.git", "main"); h != strings.Repeat("e", 40) {
		t.Fatalf("the remote head was changed: %s", h)
	}
}

// A stopped stage whose effect actually happened is never retried: the retry
// is refused, nothing runs, and the file is not touched.
func TestRetryRefusedWhenTheEffectAlreadyHappened(t *testing.T) {
	w := newWorld(t)
	exact := "# shop-new\r\nline two\r\n"
	w.f.onWith("CreateFile", 1, faultBefore, platform.ErrChanged, func() {
		if err := os.WriteFile(filepath.Join(w.dir("shop-new"), "README.md"), []byte(exact), 0o600); err != nil {
			t.Error(err)
		}
	})
	rec := w.reserveAndPropose("shop-new")
	_ = w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1)
	if s, ok := w.waitSettled(rec.Operation.ID).(corenewrepos.Stopped); !ok || s.Reason != corenewrepos.StopPreconditions {
		t.Fatalf("state = %+v", w.get(rec.Operation.ID))
	}
	err := w.svc.Retry(w.ctx, rec.Operation.ID, rec.SpecHash)
	var ref *RefusedError
	if !errors.As(err, &ref) || ref.Reason != corenewrepos.StopPreconditions {
		t.Fatalf("err = %v, want a refusal", err)
	}
	if w.f.count("CreateFile") != 1 {
		t.Fatalf("a stage that already happened was run again: %v", w.mutations())
	}
}

// ---- nothing is ever deleted or overwritten (safety invariant 1) ----

// diskMonitor remembers what it has seen under root and fails when anything it
// saw is gone or (outside .git) has other content. It reads only.
type diskMonitor struct {
	t    *testing.T
	root string
	mu   sync.Mutex
	last map[string]string
}

func newDiskMonitor(t *testing.T, root string) *diskMonitor {
	return &diskMonitor{t: t, root: root, last: map[string]string{}}
}

func (m *diskMonitor) snap() map[string]string {
	out := map[string]string{}
	_ = filepath.WalkDir(m.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // a file that vanished while walking is reported by the comparison
		}
		rel, _ := filepath.Rel(m.root, p)
		inGit := rel == ".git" || strings.Contains(rel, string(filepath.Separator)+".git")
		switch {
		case d.IsDir():
			out[rel] = "dir"
			if inGit && rel != ".git" && !strings.HasSuffix(rel, ".git") {
				return filepath.SkipDir // internals of .git change legitimately; its existence is what counts
			}
		case inGit:
			if filepath.Base(p) == "HEAD" || filepath.Base(p) == "config" {
				out[rel] = "file"
			}
		default:
			b, rerr := os.ReadFile(p) //nolint:gosec // a temp folder of this test
			if rerr == nil {
				out[rel] = fmt.Sprintf("%x", sha256.Sum256(b))
			}
		}
		return nil
	})
	return out
}

func (m *diskMonitor) check(where string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.snap()
	for p, was := range m.last {
		got, ok := now[p]
		switch {
		case !ok:
			m.t.Errorf("%s: %q was deleted or moved away", where, p)
		case got != was:
			m.t.Errorf("%s: %q was overwritten", where, p)
		}
	}
	for p, v := range now {
		m.last[p] = v
	}
}

// Whatever fails, is lost, is cancelled, is rejected or untracked, and however
// often the service restarts, nothing that existed is removed or rewritten. A
// source scan cannot see a rename, a truncate or a call through a helper; this
// can.
func TestNothingIsEverDeletedOrOverwritten(t *testing.T) {
	type fault struct {
		op   string
		nth  int
		mode faultMode
	}
	var faultsList []fault
	for _, c := range []struct {
		op  string
		nth int
	}{{"CreateChildDir", 1}, {"InitRepo", 1}, {"CreateFile", 1}, {"CreateFile", 2}, {"CommitFirst", 1}} {
		faultsList = append(faultsList, fault{c.op, c.nth, faultBefore}, fault{c.op, c.nth, faultLost})
	}
	for _, f := range faultsList {
		name := fmt.Sprintf("init %s#%d mode %d", f.op, f.nth, f.mode)
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			mon := newDiskMonitor(t, w.parent)
			w.f.observe = func(string) { mon.check("before a port call") }
			w.f.after = func(string) { mon.check("after a port call") }
			// A file somebody else owns sits beside the new folder; it must stay.
			if err := os.WriteFile(filepath.Join(w.parent, "notes.txt"), []byte("mine\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			w.f.on(f.op, f.nth, f.mode, errInjected)
			rec := w.reserveAndPropose("shop-new")
			_ = w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1)
			w.waitSettled(rec.Operation.ID)
			mon.check("settled")
			w.restart()
			_ = w.svc.Recover(w.ctx)
			w.waitSettled(rec.Operation.ID)
			mon.check("after recovery")
			if _, stopped := w.get(rec.Operation.ID).(corenewrepos.Stopped); stopped {
				// A retry after the user's confirmation must not delete either.
				_ = w.svc.Retry(w.ctx, rec.Operation.ID, rec.SpecHash)
				w.waitSettled(rec.Operation.ID)
				mon.check("after retry")
			}
		})
	}
	for _, c := range []struct {
		op  string
		err error
	}{{"CreateRepo", errInjected}, {"AddOrigin", errInjected}, {"PublishBranch", git.ErrPublishUnknown}} {
		for _, mode := range []faultMode{faultBefore, faultLost} {
			t.Run(fmt.Sprintf("publish %s mode %d", c.op, mode), func(t *testing.T) {
				t.Parallel()
				w := newWorld(t)
				mon := newDiskMonitor(t, w.parent)
				rec := w.reserveAndPropose("shop-new")
				_ = w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1)
				w.waitFor(rec.Operation.ID, "ready", isState[corenewrepos.Ready])
				mon.check("ready")
				w.f.observe = func(string) { mon.check("before a port call") }
				w.f.after = func(string) { mon.check("after a port call") }
				w.f.on(c.op, 1, mode, c.err)
				if _, err := w.publish(rec, "shop-new"); err != nil {
					t.Fatal(err)
				}
				w.waitSettled(rec.Operation.ID)
				w.restart()
				_ = w.svc.Recover(w.ctx)
				w.waitSettled(rec.Operation.ID)
				mon.check("after recovery")
			})
		}
	}
	t.Run("untracked while a stage runs", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		mon := newDiskMonitor(t, w.parent)
		w.f.observe = func(string) { mon.check("before a port call") }
		w.f.after = func(string) { mon.check("after a port call") }
		w.f.on("CommitFirst", 1, faultHang, nil)
		rec := w.reserveAndPropose("shop-new")
		_ = w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1)
		<-w.f.entered
		if err := w.svc.Untracked(w.ctx, rec.Operation.ID); err != nil {
			t.Fatal(err)
		}
		w.waitSettled(rec.Operation.ID)
		mon.check("untracked")
		w.restart()
		_ = w.svc.Recover(w.ctx)
		mon.check("after recovery")
	})
	t.Run("closed while a stage runs", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		mon := newDiskMonitor(t, w.parent)
		w.f.observe = func(string) { mon.check("before a port call") }
		w.f.after = func(string) { mon.check("after a port call") }
		w.f.on("CreateFile", 2, faultHang, nil)
		rec := w.reserveAndPropose("shop-new")
		_ = w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1)
		<-w.f.entered
		_ = w.svc.Close()
		mon.check("closed")
		w.restart()
		_ = w.svc.Recover(w.ctx)
		w.waitSettled(rec.Operation.ID)
		mon.check("after recovery")
	})
	t.Run("rejected", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		rec := w.reserveAndPropose("shop-new")
		mon := newDiskMonitor(t, w.parent)
		mon.check("reserved")
		if err := w.svc.Reject(w.ctx, rec.Operation.ID); err != nil {
			t.Fatal(err)
		}
		mon.check("rejected")
		if w.anyMutation() {
			t.Fatalf("a rejection reached a mutating port: %v", w.mutations())
		}
	})
}

// ---- a reserved repo is invisible to every ordinary read (NR-01) ----

// TestPropReservedRepoInvisible: from the reservation until the commit that
// makes it ready, at every effect of the flow and after every stop, no ordinary
// read of the store returns the repo; once ready every one does. The same
// reads are checked in internal/db against the SQL itself.
func TestPropReservedRepoInvisible(t *testing.T) {
	visible := func(w *world, repo core.RepoID) (hits []string) {
		ctx := w.ctx
		if _, err := w.store.GetRepo(ctx, repo); err == nil {
			hits = append(hits, "GetRepo")
		}
		if _, err := w.store.GetRepoIncludingUntracked(ctx, repo); err == nil {
			hits = append(hits, "GetRepoIncludingUntracked")
		}
		if rs, err := w.store.ListRepos(ctx, w.project.ID); err == nil {
			for _, r := range rs {
				if r.ID == repo {
					hits = append(hits, "ListRepos")
				}
			}
		}
		if ws, err := w.store.GetWorkspace(ctx); err == nil {
			for _, p := range ws.Projects {
				for _, r := range p.Repos {
					if r.ID == repo {
						hits = append(hits, "GetWorkspace")
					}
				}
			}
		}
		if _, _, err := w.store.GetRepoTrust(ctx, repo); err == nil {
			hits = append(hits, "GetRepoTrust")
		}
		_ = w.store.Commit(ctx, func(tx *db.Tx) error {
			if _, err := tx.GetTrackedReadyRepo(ctx, w.project.ID, repo); err == nil {
				hits = append(hits, "GetTrackedReadyRepo")
			}
			return nil
		})
		return hits
	}
	t.Run("through the whole flow", func(t *testing.T) {
		w := newWorld(t)
		rec := w.reserveAndPropose("shop-new")
		repo := rec.Operation.Repo
		if h := visible(w, repo); len(h) != 0 {
			t.Fatalf("reserved and proposed, yet visible to %v", h)
		}
		w.f.observe = func(call string) {
			if h := visible(w, repo); len(h) != 0 {
				t.Errorf("visible to %v while %s was running (stage not finished)", h, call)
			}
		}
		_ = w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1)
		w.waitFor(rec.Operation.ID, "ready", isState[corenewrepos.Ready])
		w.f.observe = nil
		if h := visible(w, repo); len(h) < 5 {
			t.Fatalf("a ready repo is visible to only %v", h)
		}
	})
	t.Run("after a stop and after untracking", func(t *testing.T) {
		w := newWorld(t)
		w.f.on("InitRepo", 1, faultBefore, platform.ErrChanged)
		rec := w.reserveAndPropose("shop-new")
		_ = w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1)
		if _, ok := w.waitSettled(rec.Operation.ID).(corenewrepos.Stopped); !ok {
			t.Fatalf("state = %T", w.get(rec.Operation.ID))
		}
		if h := visible(w, rec.Operation.Repo); len(h) != 0 {
			t.Fatalf("a stopped reservation is visible to %v", h)
		}
		w.restart()
		_ = w.svc.Recover(w.ctx)
		if h := visible(w, rec.Operation.Repo); len(h) != 0 {
			t.Fatalf("visible after a restart: %v", h)
		}
	})
	t.Run("rejected reservation", func(t *testing.T) {
		w := newWorld(t)
		rec := w.reserveAndPropose("shop-new")
		if err := w.svc.Reject(w.ctx, rec.Operation.ID); err != nil {
			t.Fatal(err)
		}
		if h := visible(w, rec.Operation.Repo); len(h) != 0 {
			t.Fatalf("a rejected reservation is visible to %v", h)
		}
	})
}
