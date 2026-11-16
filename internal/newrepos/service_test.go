package newrepos

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/api"
	corenewrepos "github.com/brendatamaar/yggdrasil/internal/core/newrepos"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// NR-01: a reservation writes rows and an audit entry and nothing on disk; the
// reserved repo is invisible to ordinary reads.
func TestReserveWritesNoDiskEffect(t *testing.T) {
	w := newWorld(t)
	rec := w.reserveAndPropose("shop-new")
	if _, err := os.Lstat(w.dir("shop-new")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a reservation must not create the folder: %v", err)
	}
	if w.anyMutation() {
		t.Fatalf("mutating port called by a reservation: %v", w.mutations())
	}
	if _, err := w.store.GetRepo(w.ctx, rec.Operation.Repo); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("a reserved repo must be invisible to GetRepo, got %v", err)
	}
	if _, err := w.store.GetReservedRepo(w.ctx, rec.Operation.Repo); err != nil {
		t.Fatalf("the setup path must see it: %v", err)
	}
	if _, ok := w.get(rec.Operation.ID).(corenewrepos.AwaitingConfirm); !ok {
		t.Fatalf("state = %T", w.get(rec.Operation.ID))
	}
	audit, err := w.store.ListAudit(w.ctx, "newrepo:"+string(rec.Operation.ID), 10)
	if err != nil || len(audit) < 2 {
		t.Fatalf("reserve and proposal must be audited: %v %v", audit, err)
	}
	// A repeat of the same reservation returns the same one.
	again, err := w.svc.Reserve(w.ctx, w.project.ID, w.reserveReq("shop-new"), "")
	if err != nil || again.Operation.ID != rec.Operation.ID {
		t.Fatalf("a repeated reservation must be idempotent: %v %v", again.Operation.ID, err)
	}
}

// NR-01: the leaf is one child name and never a path.
func TestReserveRejectsPathsAsLeaf(t *testing.T) {
	w := newWorld(t)
	for _, leaf := range []string{"", "..", "a/b", `a\b`, "C:evil", "a:stream", "con", ".git", "x~1", "name.", "/abs"} {
		if _, err := w.svc.Reserve(w.ctx, w.project.ID, ReserveRequest{Leaf: leaf}, ""); err == nil {
			t.Errorf("leaf %q was accepted", leaf)
		}
		if _, err := w.svc.PreviewReserve(w.ctx, w.project.ID, ReserveRequest{Leaf: leaf}); err == nil {
			t.Errorf("preview accepted leaf %q", leaf)
		}
	}
	if w.anyMutation() {
		t.Fatal("a rejected leaf reached a mutating port")
	}
}

// Missing author refuses before any disk effect, at reserve and at confirm.
func TestMissingAuthorRefusesBeforeDiskEffects(t *testing.T) {
	w := newWorld(t)
	w.initW.SetAuthor(false)
	_, err := w.svc.Reserve(w.ctx, w.project.ID, w.reserveReq("shop-new"), "")
	var ref *RefusedError
	if !errors.As(err, &ref) || ref.Reason != corenewrepos.StopMissingAuthor {
		t.Fatalf("want a missing-author refusal, got %v", err)
	}
	pv, err := w.svc.PreviewReserve(w.ctx, w.project.ID, w.reserveReq("shop-new"))
	if err != nil || len(pv.Problems) == 0 {
		t.Fatalf("the preview must list the problem: %+v %v", pv, err)
	}
	if _, err := os.Lstat(w.dir("shop-new")); err == nil {
		t.Fatal("folder created")
	}
	// Author removed after the reservation: the confirm stops before CreateChildDir.
	w.initW.SetAuthor(true)
	rec := w.reserveAndPropose("shop-new")
	w.initW.SetAuthor(false)
	if err := w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1); err != nil {
		t.Fatal(err)
	}
	st := w.waitSettled(rec.Operation.ID)
	stopped, ok := st.(corenewrepos.Stopped)
	if !ok || stopped.Reason != corenewrepos.StopMissingAuthor {
		t.Fatalf("state = %+v", st)
	}
	if n := w.f.count("CreateChildDir"); n != 0 {
		t.Fatalf("the folder was created despite no author (%d calls)", n)
	}
}

// NR-01/02: the whole init flow on the real filesystem and real git.
func TestInitHappyPath(t *testing.T) {
	w := newWorld(t)
	var seen []api.NewRepoState
	w.hub.check = func(m api.NewRepoChangedMessage) {
		// Every event is published after its state is committed, so the
		// operation it names is always stored already.
		if _, err := w.store.GetReservation(w.ctx, m.OperationID); err != nil {
			t.Errorf("event for an operation that is not stored: %v", err)
			return
		}
		seen = append(seen, m.State)
	}
	rec := w.reserveAndPropose("shop-new")
	if err := w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1); err != nil {
		t.Fatal(err)
	}
	st := w.waitFor(rec.Operation.ID, "ready", isState[corenewrepos.Ready])
	ready := st.(corenewrepos.Ready)

	if got, _ := w.readFile("shop-new", "README.md"); got != "# shop-new\r\nline two\r\n" {
		t.Fatalf("README = %q", got)
	}
	if got, _ := w.readFile("shop-new", ".gitignore"); got != "*.log\n" {
		t.Fatalf(".gitignore = %q", got)
	}
	fc, found, err := w.git.ReadFirstCommit(w.ctx, w.dir("shop-new"), "main")
	if err != nil || !found || fc.Commit != ready.InitialHead {
		t.Fatalf("first commit = %+v %v %v, want %s", fc, found, err, ready.InitialHead)
	}
	if fc.Tree != expectedTree(mustSpec(t, w, "shop-new")) {
		t.Fatalf("the computed tree %s differs from git's %s", expectedTree(mustSpec(t, w, "shop-new")), fc.Tree)
	}
	// The repo became an ordinary, ready repo in the commit of the Ready state.
	if r, err := w.store.GetRepo(w.ctx, rec.Operation.Repo); err != nil || r.Name != "shop-new" {
		t.Fatalf("the repo is not ready: %+v %v", r, err)
	}
	// Exactly-once effects, every intent settled, audit written.
	want := map[string]int{"CreateChildDir": 1, "CreateFile": 2, "InitRepo": 1, "CommitFirst": 1}
	for op, n := range want {
		if w.f.count(op) != n {
			t.Errorf("%s ran %d times, want %d", op, w.f.count(op), n)
		}
	}
	effects, err := w.store.ListEffectsByOperation(w.ctx, rec.Operation.ID)
	if err != nil || len(effects) != 6 {
		t.Fatalf("effects = %d %v", len(effects), err)
	}
	for _, e := range effects {
		if e.State != db.EffectSucceeded {
			t.Errorf("effect %s is %s", e.Stage, e.State)
		}
	}
	audit, _ := w.store.ListAudit(w.ctx, "newrepo:"+string(rec.Operation.ID), 100)
	if len(audit) < 10 {
		t.Errorf("only %d audit entries for a full init", len(audit))
	}
	if w.locks.max > 1 || w.locks.held != 0 || w.locks.total != 5 {
		t.Errorf("main lock: max held %d, still held %d, takes %d (want one per local mutating stage, none left)", w.locks.max, w.locks.held, w.locks.total)
	}
	if len(seen) < 7 {
		t.Errorf("hub events = %v", seen)
	}
}

func mustSpec(t *testing.T, w *world, leaf string) corenewrepos.InitSpec {
	t.Helper()
	s, err := w.svc.parseSpec(w.project.ID, w.reserveReq(leaf))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// NR-03/04: publish creates the remote once, adds origin, pushes, no token anywhere.
func TestPublishHappyPath(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", sentinelToken)
	w := newWorld(t)
	rec := w.reserveAndPropose("shop-new")
	if err := w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1); err != nil {
		t.Fatal(err)
	}
	w.waitFor(rec.Operation.ID, "ready", isState[corenewrepos.Ready])
	pv, err := w.publish(rec, "shop-new")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(pv.Lines, " "), "private") {
		t.Fatalf("the default must be private and shown: %v", pv.Lines)
	}
	st := w.waitFor(rec.Operation.ID, "published", isState[corenewrepos.Published]).(corenewrepos.Published)
	if st.URL != "https://github.com/acme/shop-new.git" || strings.ContainsAny(st.URL, "@?#") {
		t.Fatalf("url = %q", st.URL)
	}
	if w.host.creates != 1 || w.fake.Pushes() != 1 {
		t.Fatalf("creates=%d pushes=%d", w.host.creates, w.fake.Pushes())
	}
	if got := w.fake.RemoteHead(st.URL, "main"); got != st.Head {
		t.Fatalf("remote head %q != %q", got, st.Head)
	}
	cfg, err := os.ReadFile(filepath.Join(w.dir("shop-new"), ".git", "config"))
	if err != nil || !strings.Contains(string(cfg), st.URL) || strings.Contains(string(cfg), sentinelToken) {
		t.Fatalf("git config must hold the plain URL and no token: %s %v", cfg, err)
	}
	if h := w.host.repos["acme/shop-new"]; !h.private {
		t.Fatal("the remote must be private by default")
	}
}

// A hash that is not the previewed one never starts anything.
func TestConfirmWithWrongHashIsRefused(t *testing.T) {
	w := newWorld(t)
	rec := w.reserveAndPropose("shop-new")
	if err := w.svc.ConfirmInit(w.ctx, rec.Operation.ID, strings.Repeat("0", 64), 1); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("err = %v", err)
	}
	if err := w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 0); !errors.Is(err, ErrNotApplicable) {
		t.Fatalf("attempt 0: err = %v", err)
	}
	if w.anyMutation() {
		t.Fatalf("a mutating port was called: %v", w.mutations())
	}
}

func TestRejectAndUntrackedStopWithoutTouchingDisk(t *testing.T) {
	w := newWorld(t)
	a := w.reserveAndPropose("shop-a")
	if err := w.svc.Reject(w.ctx, a.Operation.ID); err != nil {
		t.Fatal(err)
	}
	if s, ok := w.get(a.Operation.ID).(corenewrepos.Stopped); !ok || s.Reason != corenewrepos.StopRejected {
		t.Fatalf("state = %+v", w.get(a.Operation.ID))
	}
	if err := w.svc.ConfirmInit(w.ctx, a.Operation.ID, a.SpecHash, 1); !errors.Is(err, ErrNotApplicable) {
		t.Fatalf("a rejected operation must not start: %v", err)
	}
	b := w.reserveAndPropose("shop-b")
	if err := w.svc.Untracked(w.ctx, b.Operation.ID); err != nil {
		t.Fatal(err)
	}
	if s, ok := w.get(b.Operation.ID).(corenewrepos.Stopped); !ok || s.Reason != corenewrepos.StopUntracked {
		t.Fatalf("state = %+v", w.get(b.Operation.ID))
	}
	if w.anyMutation() {
		t.Fatalf("mutating port called: %v", w.mutations())
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	w := newWorld(t)
	if err := w.svc.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.svc.Close(); err != nil {
		t.Fatal(err)
	}
	rec := w.reserveAndPropose("shop-new")
	if err := w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1); err == nil {
		t.Fatal("a closed service must not start a worker")
	}
}
