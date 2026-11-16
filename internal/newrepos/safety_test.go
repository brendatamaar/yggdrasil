package newrepos

import (
	"database/sql"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	corenewrepos "github.com/brendatamaar/yggdrasil/internal/core/newrepos"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/git"
)

// Safety invariant 2 (every mutation is confirmed): TestPropInitOnlyAfterConfirm
// at shell level. Random sequences of every public call that is not a matching
// confirm never reach a mutating port.
func TestPropInitOnlyAfterConfirm(t *testing.T) {
	for seed := int64(1); seed <= 25; seed++ {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			w := newWorld(t)
			rec := w.reserveAndPropose("shop-new")
			op := rec.Operation.ID
			for i := 0; i < 40; i++ {
				switch rng.Intn(9) {
				case 0:
					_ = w.svc.ConfirmInit(w.ctx, op, strings.Repeat("a", 64), 1)
				case 1:
					_ = w.svc.ConfirmInit(w.ctx, op, "", 1)
				case 2:
					_ = w.svc.ConfirmInit(w.ctx, op, rec.SpecHash, 0) // right hash, no attempt
				case 3:
					_ = w.svc.Recover(w.ctx)
				case 4:
					_ = w.svc.ReconcileOperation(w.ctx, op)
				case 5:
					_ = w.svc.AttachProposal(w.ctx, op, "prp_other", strings.Repeat("b", 64))
				case 6:
					_, _ = w.svc.PreviewReserve(w.ctx, w.project.ID, w.reserveReq("shop-new"))
				case 7:
					_, _ = w.svc.Reserve(w.ctx, w.project.ID, w.reserveReq("shop-new"), "")
				case 8:
					_ = w.svc.ConfirmPublish(w.ctx, op, rec.SpecHash, 1)
				}
				if w.anyMutation() {
					t.Fatalf("step %d: a mutating port was called before a matching confirm: %v", i, w.mutations())
				}
			}
			if err := w.svc.ConfirmInit(w.ctx, op, rec.SpecHash, 1); err != nil {
				t.Fatal(err)
			}
			w.waitFor(op, "ready", isState[corenewrepos.Ready])
			if w.f.count("CreateChildDir") != 1 {
				t.Fatalf("CreateChildDir ran %d times", w.f.count("CreateChildDir"))
			}
		})
	}
}

// Safety invariant 1 (nothing is overwritten or deleted): TestPropInitNeverOverwrites.
func TestPropInitNeverOverwrites(t *testing.T) {
	t.Run("an existing empty folder is refused at reserve", func(t *testing.T) {
		w := newWorld(t)
		if err := os.Mkdir(w.dir("shop-new"), 0o750); err != nil {
			t.Fatal(err)
		}
		_, err := w.svc.Reserve(w.ctx, w.project.ID, w.reserveReq("shop-new"), "")
		var ref *RefusedError
		if !errors.As(err, &ref) || ref.Reason != corenewrepos.StopPathExists {
			t.Fatalf("err = %v", err)
		}
		entries, _ := os.ReadDir(w.dir("shop-new"))
		if len(entries) != 0 || w.anyMutation() {
			t.Fatalf("the folder was touched: %v %v", entries, w.mutations())
		}
	})
	t.Run("a folder that appears before the confirm is not adopted", func(t *testing.T) {
		w := newWorld(t)
		rec := w.reserveAndPropose("shop-new")
		if err := os.Mkdir(w.dir("shop-new"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1); err != nil {
			t.Fatal(err)
		}
		w.assertOutcome("existing folder", w.waitSettled(rec.Operation.ID), outcome{stopped: corenewrepos.StopPathExists})
		entries, _ := os.ReadDir(w.dir("shop-new"))
		if len(entries) != 0 {
			t.Fatalf("the existing folder was written to: %v", entries)
		}
		if w.f.count("InitRepo") != 0 || w.f.count("CreateFile") != 0 {
			t.Fatalf("stages ran in a folder that was not ours: %v", w.mutations())
		}
	})
	t.Run("a file another process wrote is never replaced", func(t *testing.T) {
		w := newWorld(t)
		w.f.hook("CreateFile", 1, func() {
			if err := os.WriteFile(filepath.Join(w.dir("shop-new"), "README.md"), []byte("theirs\n"), 0o600); err != nil {
				t.Error(err)
			}
		})
		rec := w.reserveAndPropose("shop-new")
		if err := w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1); err != nil {
			t.Fatal(err)
		}
		w.assertOutcome("existing file", w.waitSettled(rec.Operation.ID), outcome{stopped: corenewrepos.StopPathExists})
		if got, _ := w.readFile("shop-new", "README.md"); got != "theirs\n" {
			t.Fatalf("README was overwritten: %q", got)
		}
		if w.f.count("CommitFirst") != 0 {
			t.Fatal("a commit was made on top of files this operation did not write")
		}
	})
	t.Run("a project folder swapped for a link is refused before any write", func(t *testing.T) {
		w := newWorld(t)
		rec := w.reserveAndPropose("shop-new")
		moved := w.parent + "-moved"
		if err := os.Rename(w.parent, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(moved, w.parent); err != nil {
			t.Skipf("cannot create a link here: %v", err)
		}
		if err := w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1); err != nil {
			t.Fatal(err)
		}
		w.assertOutcome("swapped parent", w.waitSettled(rec.Operation.ID), outcome{stopped: corenewrepos.StopPreconditions})
		if entries, _ := os.ReadDir(moved); len(entries) != 0 {
			t.Fatalf("something was created in the swapped-in folder: %v", entries)
		}
		if w.anyMutation() {
			t.Fatalf("a mutating port ran: %v", w.mutations())
		}
	})
}

// A remote with another marker is never adopted.
func TestPropNoUnrelatedRemoteAdoption(t *testing.T) {
	ready := func(t *testing.T) (*world, db.ReservationRecord) {
		w := newWorld(t)
		rec := w.reserveAndPropose("shop-new")
		if err := w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1); err != nil {
			t.Fatal(err)
		}
		w.waitFor(rec.Operation.ID, "ready", isState[corenewrepos.Ready])
		return w, rec
	}
	t.Run("name taken by someone else is a collision", func(t *testing.T) {
		w, rec := ready(t)
		w.host.seed("acme", "shop-new", "Created by Yggdrasil [ygg:ffffffffffffffffffffffffffffffff]", true)
		if _, err := w.publish(rec, "shop-new"); err != nil {
			t.Fatal(err)
		}
		w.assertOutcome("collision", w.waitSettled(rec.Operation.ID), outcome{stopped: corenewrepos.StopNameCollision})
		w.assertNoPublishEffects()
	})
	t.Run("lost reply and a foreign repo with another marker is a conflict", func(t *testing.T) {
		w, rec := ready(t)
		w.host.seed("acme", "shop-new", "Created by Yggdrasil [ygg:ffffffffffffffffffffffffffffffff]", true)
		w.f.on("CreateRepo", 1, faultBefore, errInjected) // unknown: the shell must look, not adopt
		if _, err := w.publish(rec, "shop-new"); err != nil {
			t.Fatal(err)
		}
		w.assertOutcome("foreign", w.waitSettled(rec.Operation.ID), outcome{stopped: corenewrepos.StopConflict})
		w.assertNoPublishEffects()
		if w.f.count("LookupRepo") == 0 {
			t.Fatal("the shell never looked the remote up")
		}
	})
	t.Run("lost reply and an own repo with a different visibility is a conflict", func(t *testing.T) {
		w, rec := ready(t)
		// Same marker (the shell derives it), but public although private was confirmed.
		pv, err := w.svc.PreparePublish(w.ctx, rec.Operation.ID, PublishRequest{HostInstance: "github.com", Owner: "acme", OwnerKind: corenewrepos.OwnerOrg, Name: "shop-new"}, "")
		if err != nil {
			t.Fatal(err)
		}
		w.host.seed("acme", "shop-new", pv.Spec.Description(), false)
		w.f.on("CreateRepo", 1, faultBefore, errInjected)
		if _, err := w.publish(rec, "shop-new"); err != nil {
			t.Fatal(err)
		}
		w.assertOutcome("visibility", w.waitSettled(rec.Operation.ID), outcome{stopped: corenewrepos.StopConflict})
		w.assertNoPublishEffects()
	})
}

func (w *world) assertNoPublishEffects() {
	w.t.Helper()
	if w.f.count("AddOrigin") != 0 || w.f.count("PublishBranch") != 0 || w.fake.Pushes() != 0 {
		w.t.Fatalf("origin or push ran for a remote this operation did not create: %v", w.mutations())
	}
	if u, ok, _ := w.origin.Origin(w.ctx, w.dir("shop-new")); ok {
		w.t.Fatalf("origin was added: %s", u)
	}
}

// An unknown outcome is reconciled read-only and never repeated blindly.
func TestPropUnknownNeverBlindRetry(t *testing.T) {
	for _, c := range []struct {
		op  string
		err error
	}{{"CreateChildDir", errInjected}, {"InitRepo", errInjected}, {"CreateFile", errInjected}, {"CommitFirst", errInjected}} {
		t.Run(c.op, func(t *testing.T) {
			w := newWorld(t)
			w.f.on(c.op, 1, faultBefore, c.err)
			rec := w.reserveAndPropose("shop-new")
			if err := w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1); err != nil {
				t.Fatal(err)
			}
			w.waitSettled(rec.Operation.ID)
			for i := 0; i < 3; i++ {
				w.restart()
				_ = w.svc.Recover(w.ctx)
				_ = w.svc.ReconcileOperation(w.ctx, rec.Operation.ID)
				w.waitSettled(rec.Operation.ID)
			}
			if n := w.f.count(c.op); n != 1 {
				t.Fatalf("%s ran %d times after an unknown outcome and three recoveries", c.op, n)
			}
			if _, ok := w.get(rec.Operation.ID).(corenewrepos.Stopped); !ok {
				t.Fatalf("state = %T", w.get(rec.Operation.ID))
			}
		})
	}
	t.Run("publish", func(t *testing.T) {
		w := newWorld(t)
		rec := w.reserveAndPropose("shop-new")
		_ = w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1)
		w.waitFor(rec.Operation.ID, "ready", isState[corenewrepos.Ready])
		w.f.on("PublishBranch", 1, faultBefore, git.ErrPublishUnknown)
		if _, err := w.publish(rec, "shop-new"); err != nil {
			t.Fatal(err)
		}
		w.waitSettled(rec.Operation.ID)
		w.restart()
		_ = w.svc.Recover(w.ctx)
		w.waitSettled(rec.Operation.ID)
		if w.f.count("CreateRepo") != 1 || w.f.count("PublishBranch") != 1 {
			t.Fatalf("an effect was repeated: %v", w.mutations())
		}
		if _, ok := w.get(rec.Operation.ID).(corenewrepos.Stopped); !ok {
			t.Fatalf("an absent push must stop for a human retry, got %T", w.get(rec.Operation.ID))
		}
	})
}

// Safety invariant 3: tokens never reach child processes. This package starts
// no process at all, publishing uses the in-process transport, and the one git
// call (origin) gets a credential-free URL.
func TestInvariant_3_PublishNoTokenChild(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", sentinelToken)
	t.Setenv("GH_TOKEN", sentinelToken)
	fset := token.NewFileSet()
	for _, name := range mustGlob(t, "*.go") {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			switch imp.Path.Value {
			case `"os/exec"`, `"syscall"`, `"golang.org/x/sys/windows"`:
				t.Errorf("%s imports %s: every process starts through internal/platform", name, imp.Path.Value)
			}
		}
	}
	for _, f := range mustGlob(t, "*.go") {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, _ := os.ReadFile(f)
		for _, bad := range []string{"os.Environ", "exec.Command", "os.RemoveAll", "os.Remove(", "os.Setenv"} {
			if strings.Contains(string(b), bad) {
				t.Errorf("%s uses %s", f, bad)
			}
		}
	}
	w := newWorld(t)
	rec := w.reserveAndPropose("shop-new")
	_ = w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1)
	w.waitFor(rec.Operation.ID, "ready", isState[corenewrepos.Ready])
	if _, err := w.publish(rec, "shop-new"); err != nil {
		t.Fatal(err)
	}
	w.waitFor(rec.Operation.ID, "published", isState[corenewrepos.Published])
	w.tr.mu.Lock()
	defer w.tr.mu.Unlock()
	if len(w.tr.urls) == 0 {
		t.Fatal("the transport was never used")
	}
	for _, u := range w.tr.urls {
		if strings.Contains(u, "@") || strings.Contains(u, sentinelToken) || !strings.HasPrefix(u, "https://") {
			t.Errorf("transport got a URL with credentials or a token: %q", u)
		}
	}
	cfg, _ := os.ReadFile(filepath.Join(w.dir("shop-new"), ".git", "config"))
	if strings.Contains(string(cfg), sentinelToken) {
		t.Fatal("a token is in the git config")
	}
}

func mustGlob(t *testing.T, pattern string) []string {
	t.Helper()
	m, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

var userinfoURL = regexp.MustCompile(`://[^/\s"]*@`)

// Safety invariant 4: no token in a URL, the git config, a log or the audit
// log, even when the host's own error text carries one.
func TestInvariant_4_PublishCredentialFree(t *testing.T) {
	w := newWorld(t)
	rec := w.reserveAndPropose("shop-new")
	_ = w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1)
	w.waitFor(rec.Operation.ID, "ready", isState[corenewrepos.Ready])
	// The host refuses, and its error text has the token and a token URL in it.
	w.f.on("CreateRepo", 1, faultBefore, fmt.Errorf("401 Authorization: Bearer %s at https://x-access-token:%s@github.com/acme/shop-new.git", sentinelToken, sentinelToken))
	if _, err := w.publish(rec, "shop-new"); err != nil {
		t.Fatal(err)
	}
	st := w.waitSettled(rec.Operation.ID)
	// The fake host cannot tell a refusal from an unknown outcome here, so settle
	// on whichever the shell chose; what matters is where the text went.
	if _, ok := st.(corenewrepos.Stopped); !ok {
		if _, ok := st.(corenewrepos.Uncertain); !ok {
			t.Fatalf("state = %T", st)
		}
	}
	if strings.Contains(w.logs.String(), sentinelToken) {
		t.Fatalf("a token reached the log:\n%s", w.logs.String())
	}
	if w.logs.String() == "" {
		t.Fatal("expected the failure to be logged (redacted)")
	}
	sqlDB, err := sql.Open("sqlite", "file:"+w.dbPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sqlDB.Close() }()
	tables, err := sqlDB.Query(`SELECT name FROM sqlite_master WHERE type='table'`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for tables.Next() {
		var n string
		_ = tables.Scan(&n)
		names = append(names, n)
	}
	_ = tables.Close()
	rows := 0
	for _, n := range names {
		r, err := sqlDB.Query(`SELECT * FROM "` + n + `"`) //nolint:gosec // table names come from sqlite_master
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := r.Columns()
		for r.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			_ = r.Scan(ptrs...)
			text := fmt.Sprint(vals...)
			rows++
			if strings.Contains(text, sentinelToken) || userinfoURL.MatchString(text) {
				t.Errorf("table %s holds a credential: %s", n, text)
			}
		}
		_ = r.Close()
	}
	if rows == 0 {
		t.Fatal("the scan saw no rows")
	}
	w.tr.mu.Lock()
	defer w.tr.mu.Unlock()
	for _, u := range w.tr.urls {
		if userinfoURL.MatchString(u) {
			t.Errorf("url with credentials: %s", u)
		}
	}
}
