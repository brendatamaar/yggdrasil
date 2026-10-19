package buildstest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/audit"
	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// Harness is one store under test with the rows it needs to exist.
type Harness struct {
	Store builds.BuildStore
	// AuditFor returns the committed audit entries of a subject.
	AuditFor func(ctx context.Context, subject string) []core.AuditEntry
	Project  core.ProjectID
	// Repos are two repos of Project; Other is a repo of another project.
	Repos    [2]core.RepoID
	Other    core.RepoID
	Worktree core.WorktreeID
}

var t0 = time.Date(2025, 3, 1, 10, 0, 0, 0, time.UTC)

func at(sec int) time.Time { return t0.Add(time.Duration(sec) * time.Second) }

func spec(h Harness, steps int, wt bool) corebuilds.Spec {
	s := corebuilds.Spec{Target: corebuilds.MainCheckout{}}
	if wt {
		s.Target = corebuilds.InWorktree{WorktreeID: h.Worktree}
	}
	for i := 0; i < steps; i++ {
		s.Steps = append(s.Steps, corebuilds.StepSpec{RepoID: h.Repos[i%2], Name: fmt.Sprintf("step %d", i), Argv: []string{"tool", "a b", fmt.Sprint(i)}})
	}
	return s
}

func step(t *testing.T, s corebuilds.State, in corebuilds.Input) corebuilds.State {
	t.Helper()
	n, _ := corebuilds.Step(s, in)
	if reflect.DeepEqual(n, s) {
		t.Fatalf("input %T did not change %T", in, s)
	}
	return n
}

func enqueue(t *testing.T, h Harness, id string, sp corebuilds.Spec, sec int) corebuilds.State {
	t.Helper()
	return step(t, corebuilds.None{}, corebuilds.Enqueue{ID: core.BuildID(id), Spec: sp, Actor: "user", Now: at(sec)})
}

func save(t *testing.T, h Harness, s corebuilds.State, expected int64) (int64, error) {
	t.Helper()
	var v int64
	err := h.Store.Commit(context.Background(), func(tx builds.BuildTx) error {
		var err error
		v, err = tx.SaveBuild(context.Background(), s, expected)
		return err
	})
	return v, err
}

func mustSave(t *testing.T, h Harness, s corebuilds.State, expected int64) int64 {
	t.Helper()
	v, err := save(t, h, s, expected)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	return v
}

func id(s corebuilds.State) core.BuildID { i, _, _, _ := common(s); return i }

// StoreContract runs the builds store contract against newStore, which must
// return a fresh, empty store each call.
func StoreContract(t *testing.T, newStore func(t *testing.T) Harness) {
	ctx := context.Background()
	run := func(name string, f func(t *testing.T, h Harness)) {
		t.Run(name, func(t *testing.T) { f(t, newStore(t)) })
	}

	run("round trip of every state variant", func(t *testing.T, h Harness) {
		type path struct {
			name string
			ins  []corebuilds.Input
			sp   corebuilds.Spec
		}
		ok := func(i, sec int) corebuilds.Input {
			return corebuilds.StepExited{Index: i, Attempt: 1, Now: at(sec)}
		}
		paths := []path{
			{"succeeded with artifacts", []corebuilds.Input{corebuilds.Admit{Now: at(2)}, ok(0, 3), ok(1, 4),
				corebuilds.ArtifactsCollected{Artifacts: []corebuilds.Artifact{{RepoID: h.Repos[0], Path: "target/a.jar", Size: 12}, {RepoID: h.Repos[1], Path: "z.zip", Size: 0}}, Now: at(5)}}, spec(h, 2, true)},
			{"succeeded without artifacts", []corebuilds.Input{corebuilds.Admit{Now: at(2)}, ok(0, 3), corebuilds.ArtifactsCollected{Now: at(4)}}, spec(h, 1, false)},
			{"failed", []corebuilds.Input{corebuilds.Admit{Now: at(2)}, ok(0, 3), corebuilds.StepExited{Index: 1, Attempt: 1, ExitCode: 2, LogTruncated: true, Now: at(4)}}, spec(h, 3, false)},
			{"start failed", []corebuilds.Input{corebuilds.Admit{Now: at(2)}, corebuilds.StepStartFailed{Index: 0, Attempt: 1, Reason: corebuilds.StartNotFound, Now: at(3)}}, spec(h, 2, false)},
			{"cancelled while queued", []corebuilds.Input{corebuilds.Cancel{Actor: "user", Now: at(2)}}, spec(h, 2, false)},
			{"cancelled while running", []corebuilds.Input{corebuilds.Admit{Now: at(2)}, ok(0, 3), corebuilds.Cancel{Actor: "user", Now: at(4)}, corebuilds.StepExited{Index: 1, Attempt: 1, ExitCode: 130, Now: at(5)}}, spec(h, 3, false)},
			{"interrupted", []corebuilds.Input{corebuilds.Admit{Now: at(2)}, corebuilds.ProcessLost{Now: at(3)}}, spec(h, 2, true)},
		}
		for i, p := range paths {
			t.Run(p.name, func(t *testing.T) {
				bid := fmt.Sprintf("rt%d", i)
				s := enqueue(t, h, bid, p.sp, 1)
				ver := mustSave(t, h, s, db.NewBuild)
				check := func(s corebuilds.State, ver int64) {
					t.Helper()
					got, v, err := h.Store.Get(ctx, core.BuildID(bid))
					if err != nil || v != ver || !reflect.DeepEqual(got, s) {
						t.Fatalf("load of %T: err=%v version=%d (want %d)\n got  %#v\n want %#v", s, err, v, ver, got, s)
					}
				}
				check(s, ver)
				for _, in := range p.ins {
					s = step(t, s, in)
					ver = mustSave(t, h, s, ver)
					check(s, ver)
				}
			})
		}
	})

	run("stale version is a conflict and changes nothing", func(t *testing.T, h Harness) {
		q := enqueue(t, h, "b1", spec(h, 2, false), 1)
		v := mustSave(t, h, q, db.NewBuild)
		r := step(t, q, corebuilds.Admit{Now: at(2)})
		v = mustSave(t, h, r, v)
		c := step(t, r, corebuilds.Cancel{Actor: "user", Now: at(3)})
		if _, err := save(t, h, c, v-1); !errors.Is(err, db.ErrConflict) {
			t.Fatalf("stale save: %v, want ErrConflict", err)
		}
		if got, _, _ := h.Store.Get(ctx, "b1"); !reflect.DeepEqual(got, r) {
			t.Fatalf("a conflicting save changed the build: %#v", got)
		}
		if _, err := save(t, h, c, v+5); !errors.Is(err, db.ErrConflict) {
			t.Fatalf("future version: %v, want ErrConflict", err)
		}
	})

	run("missing build is not found", func(t *testing.T, h Harness) {
		if _, _, err := h.Store.Get(ctx, "nope"); !errors.Is(err, db.ErrNotFound) {
			t.Fatalf("Get: %v", err)
		}
		err := h.Store.Commit(ctx, func(tx builds.BuildTx) error {
			_, _, err := tx.LoadBuild(ctx, "nope")
			return err
		})
		if !errors.Is(err, db.ErrNotFound) {
			t.Fatalf("LoadBuild: %v", err)
		}
		q := enqueue(t, h, "ghost", spec(h, 1, false), 1)
		if _, err := save(t, h, q, 0); !errors.Is(err, db.ErrNotFound) {
			t.Fatalf("update of a missing build: %v, want ErrNotFound", err)
		}
	})

	run("duplicate ID on enqueue is a conflict", func(t *testing.T, h Harness) {
		q := enqueue(t, h, "dup", spec(h, 1, false), 1)
		mustSave(t, h, q, db.NewBuild)
		other := enqueue(t, h, "dup", spec(h, 2, false), 9)
		if _, err := save(t, h, other, db.NewBuild); !errors.Is(err, db.ErrConflict) {
			t.Fatalf("duplicate insert: %v, want ErrConflict", err)
		}
		if got, _, _ := h.Store.Get(ctx, "dup"); !reflect.DeepEqual(got, q) {
			t.Fatal("the duplicate insert changed the stored build")
		}
	})

	run("audit rows commit and roll back with the state", func(t *testing.T, h Harness) {
		entry := func(aid string) core.AuditEntry {
			e, err := audit.Prepare(core.AuditEntry{Actor: "user", Action: "build.queue", Subject: "build:a1", Detail: []byte(`{"steps":"1"}`)}, safety.NewRedactor(), core.AuditID(aid), at(1))
			if err != nil {
				t.Fatal(err)
			}
			return e
		}
		boom := errors.New("boom")
		err := h.Store.Commit(ctx, func(tx builds.BuildTx) error {
			if _, err := tx.SaveBuild(ctx, enqueue(t, h, "a1", spec(h, 1, false), 1), db.NewBuild); err != nil {
				return err
			}
			if err := tx.AppendAudit(ctx, entry("aud1")); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("Commit: %v", err)
		}
		if _, _, err := h.Store.Get(ctx, "a1"); !errors.Is(err, db.ErrNotFound) {
			t.Fatalf("state survived a rollback: %v", err)
		}
		if n := len(h.AuditFor(ctx, "build:a1")); n != 0 {
			t.Fatalf("%d audit rows survived a rollback", n)
		}
		err = h.Store.Commit(ctx, func(tx builds.BuildTx) error {
			if _, err := tx.SaveBuild(ctx, enqueue(t, h, "a1", spec(h, 1, false), 1), db.NewBuild); err != nil {
				return err
			}
			return tx.AppendAudit(ctx, entry("aud2"))
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := h.Store.Get(ctx, "a1"); err != nil {
			t.Fatal(err)
		}
		if got := h.AuditFor(ctx, "build:a1"); len(got) != 1 || got[0].ID != "aud2" {
			t.Fatalf("audit rows after commit: %+v", got)
		}
	})

	run("queue reads and filters", func(t *testing.T, h Harness) {
		mk := func(bid string, sec int, sp corebuilds.Spec) corebuilds.State {
			s := enqueue(t, h, bid, sp, sec)
			mustSave(t, h, s, db.NewBuild)
			return s
		}
		otherSpec := corebuilds.Spec{Target: corebuilds.MainCheckout{}, Steps: []corebuilds.StepSpec{{RepoID: h.Other, Name: "x", Argv: []string{"x"}}}}
		mk("q-b", 5, spec(h, 1, false))
		mk("q-a", 5, spec(h, 1, false))
		mk("q-0", 1, otherSpec)
		// Admit the oldest one.
		s, v, err := h.Store.Get(ctx, "q-0")
		if err != nil {
			t.Fatal(err)
		}
		mustSave(t, h, step(t, s, corebuilds.Admit{Now: at(6)}), v)

		refs, err := h.Store.ListQueued(ctx)
		if err != nil || len(refs) != 2 || refs[0].ID != "q-a" || refs[1].ID != "q-b" || !refs[0].QueuedAt.Equal(at(5)) {
			t.Fatalf("ListQueued = %+v, %v (want q-a then q-b, ordered by time then ID)", refs, err)
		}
		if n, err := h.Store.CountRunning(ctx); err != nil || n != 1 {
			t.Fatalf("CountRunning = %d, %v", n, err)
		}
		// A cancelling build still counts as running.
		s, v, _ = h.Store.Get(ctx, "q-0")
		mustSave(t, h, step(t, s, corebuilds.Cancel{Actor: "user", Now: at(7)}), v)
		if n, _ := h.Store.CountRunning(ctx); n != 1 {
			t.Fatalf("CountRunning while cancelling = %d", n)
		}
		act, err := h.Store.ListActive(ctx)
		if err != nil || len(act) != 1 || id(act[0]) != "q-0" {
			t.Fatalf("ListActive = %+v, %v", act, err)
		}
		if _, ok := act[0].(corebuilds.Cancelling); !ok {
			t.Fatalf("ListActive returned %T", act[0])
		}
		ids := func(f db.BuildFilter) []core.BuildID {
			t.Helper()
			l, err := h.Store.List(ctx, f)
			if err != nil {
				t.Fatalf("List(%+v): %v", f, err)
			}
			out := []core.BuildID{}
			for _, s := range l {
				out = append(out, id(s))
			}
			return out
		}
		eq := func(got []core.BuildID, want ...core.BuildID) {
			t.Helper()
			if !reflect.DeepEqual(got, append([]core.BuildID{}, want...)) {
				t.Fatalf("got %v, want %v", got, want)
			}
		}
		eq(ids(db.BuildFilter{}), "q-b", "q-a", "q-0")
		eq(ids(db.BuildFilter{Project: h.Project}), "q-b", "q-a")
		eq(ids(db.BuildFilter{Repo: h.Other}), "q-0")
		eq(ids(db.BuildFilter{Status: core.BuildQueued}), "q-b", "q-a")
		eq(ids(db.BuildFilter{Status: core.BuildRunning}), "q-0")
		eq(ids(db.BuildFilter{Limit: 1}), "q-b")
		if _, err := h.Store.List(ctx, db.BuildFilter{Status: "stuck"}); err == nil {
			t.Fatal("an unknown status filter was accepted")
		}
	})

	run("presets", func(t *testing.T, h Harness) {
		p := corebuilds.Preset{ID: "pre1", ProjectID: h.Project, Name: "Full", Steps: spec(h, 2, false).Steps}
		put := func(p corebuilds.Preset) error {
			return h.Store.Commit(ctx, func(tx builds.BuildTx) error { return tx.InsertPreset(ctx, p, at(1)) })
		}
		if err := put(p); err != nil {
			t.Fatal(err)
		}
		got, v, err := h.Store.GetPreset(ctx, "pre1")
		if err != nil || v != 0 || !reflect.DeepEqual(got, p) {
			t.Fatalf("GetPreset = %+v v=%d err=%v", got, v, err)
		}
		dup := p
		dup.ID = "pre2"
		if err := put(dup); !errors.Is(err, db.ErrConflict) {
			t.Fatalf("duplicate name: %v, want ErrConflict", err)
		}
		if err := put(p); !errors.Is(err, db.ErrConflict) {
			t.Fatalf("duplicate ID: %v, want ErrConflict", err)
		}
		dup.Name = "Quick"
		dup.Steps = dup.Steps[:1]
		if err := put(dup); err != nil {
			t.Fatal(err)
		}
		upd := func(p corebuilds.Preset, ver int64) (int64, error) {
			var nv int64
			err := h.Store.Commit(ctx, func(tx builds.BuildTx) error {
				var err error
				nv, err = tx.UpdatePreset(ctx, p, ver)
				return err
			})
			return nv, err
		}
		p.Name = "Quick"
		if _, err := upd(p, 0); !errors.Is(err, db.ErrConflict) {
			t.Fatalf("rename onto a taken name: %v, want ErrConflict", err)
		}
		p.Name = "Everything"
		if nv, err := upd(p, 0); err != nil || nv != 1 {
			t.Fatalf("update: v=%d err=%v", nv, err)
		}
		if _, err := upd(p, 0); !errors.Is(err, db.ErrConflict) {
			t.Fatalf("stale update: %v, want ErrConflict", err)
		}
		missing := p
		missing.ID = "ghost"
		if _, err := upd(missing, 0); !errors.Is(err, db.ErrNotFound) {
			t.Fatalf("update of a missing preset: %v, want ErrNotFound", err)
		}
		if _, _, err := h.Store.GetPreset(ctx, "ghost"); !errors.Is(err, db.ErrNotFound) {
			t.Fatalf("GetPreset: %v", err)
		}
		l, err := h.Store.ListPresets(ctx, h.Project)
		if err != nil || len(l) != 2 || l[0].Name != "Everything" || l[1].Name != "Quick" {
			t.Fatalf("ListPresets = %+v, %v (want by name)", l, err)
		}
		if l, _ := h.Store.ListPresets(ctx, "other-project"); len(l) != 0 {
			t.Fatalf("presets leaked across projects: %+v", l)
		}
	})
}
