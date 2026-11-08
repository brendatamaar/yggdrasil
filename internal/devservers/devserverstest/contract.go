package devserverstest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	dsc "github.com/brendatamaar/yggdrasil/internal/core/devservers"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/devservers"
)

// StoreHarness is what DevServerStoreContract needs from an implementation.
type StoreHarness struct {
	Store devservers.Store
	// Repo is a repo the store knows.
	Repo core.RepoID
	// Audit returns the entries stored for a subject.
	Audit func(subject string) []core.AuditEntry
}

var storeTime = time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)

// States is one server per state, for contract suites and tests.
func States() []dsc.Server {
	b := core.PortBlock{Base: 20000, Size: 20}
	return []dsc.Server{
		dsc.Stopped{},
		dsc.Stopped{Attempt: 1, Last: dsc.LastExited, Code: 3, At: storeTime},
		dsc.Starting{Attempt: 1, Since: storeTime, Block: b},
		dsc.Running{Attempt: 1, Since: storeTime, Pid: 9, Block: b, Ports: []dsc.Port{3000, 20000}, Announced: []dsc.Port{20000}},
		dsc.Stopping{Attempt: 1, Cause: dsc.CauseRestart, KillAttempt: dsc.KillSecond, Since: storeTime, Pid: 9, Block: b},
		dsc.Failed{Attempt: 1, Kind: dsc.FailCrashed, Code: 139, At: storeTime},
		dsc.Orphaned{Attempt: 1, Pid: 9, SeenAt: storeTime},
	}
}

// DevServerStoreContract is the suite every devservers.Store passes: rows
// round trip per state, inconsistent rows are refused, versions guard
// updates, a failed commit writes nothing, state and audit commit together,
// and ListLive returns exactly the live states.
func DevServerStoreContract(t *testing.T, mk func(t *testing.T) StoreHarness) {
	ctx := context.Background()
	key := func(h StoreHarness, cmd string) dsc.Key { return dsc.Key{Repo: h.Repo, CommandKey: cmd} }
	save := func(h StoreHarness, id string, row dsc.Row, expected int64) (int64, error) {
		var v int64
		err := h.Store.Commit(ctx, func(tx devservers.Tx) error {
			var err error
			v, err = tx.Save(ctx, id, row, expected, storeTime)
			return err
		})
		return v, err
	}

	t.Run("row round trip per state", func(t *testing.T) {
		h := mk(t)
		version := db.NewDevServer
		for _, st := range States() {
			row := dsc.RowOf(key(h, "dev"), st)
			v, err := save(h, "dsv_a", row, version)
			if err != nil {
				t.Fatalf("%#v: %v", st, err)
			}
			version = v
			got, err := h.Store.Get(ctx, "dsv_a")
			if err != nil {
				t.Fatal(err)
			}
			back, err := dsc.ServerFromRow(got.Row)
			if err != nil || !reflect.DeepEqual(back, dsc.Persisted(st)) || got.Version != version {
				t.Errorf("stored %#v, read %#v (version %d/%d, %v)", st, back, got.Version, version, err)
			}
			if got.Row.Key != key(h, "dev") {
				t.Errorf("key = %+v", got.Row.Key)
			}
		}
	})

	t.Run("unknown server is not found", func(t *testing.T) {
		h := mk(t)
		if _, err := h.Store.Get(ctx, "dsv_none"); !errors.Is(err, db.ErrNotFound) {
			t.Errorf("Get = %v, want ErrNotFound", err)
		}
		if _, err := save(h, "dsv_none", dsc.RowOf(key(h, "dev"), dsc.Stopped{}), 3); !errors.Is(err, db.ErrNotFound) {
			t.Errorf("update of a missing server = %v, want ErrNotFound", err)
		}
	})

	t.Run("unknown repo is not found", func(t *testing.T) {
		h := mk(t)
		row := dsc.RowOf(dsc.Key{Repo: "rep_nope", CommandKey: "dev"}, dsc.Stopped{})
		if _, err := save(h, "dsv_x", row, db.NewDevServer); !errors.Is(err, db.ErrNotFound) {
			t.Errorf("insert for an unknown repo = %v, want ErrNotFound", err)
		}
	})

	t.Run("version conflict", func(t *testing.T) {
		h := mk(t)
		row := dsc.RowOf(key(h, "dev"), dsc.Stopped{})
		if _, err := save(h, "dsv_a", row, db.NewDevServer); err != nil {
			t.Fatal(err)
		}
		if _, err := save(h, "dsv_a", row, db.NewDevServer); !errors.Is(err, db.ErrConflict) {
			t.Errorf("second insert = %v, want ErrConflict", err)
		}
		if _, err := save(h, "dsv_a", row, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := save(h, "dsv_a", row, 0); !errors.Is(err, db.ErrConflict) {
			t.Errorf("stale update = %v, want ErrConflict", err)
		}
	})

	t.Run("inconsistent rows are refused", func(t *testing.T) {
		h := mk(t)
		bad := dsc.RowOf(key(h, "dev"), dsc.Running{Attempt: 1, Since: storeTime, Pid: 9})
		bad.Pid = 0
		if _, err := save(h, "dsv_a", bad, db.NewDevServer); err == nil {
			t.Error("a running row without a pid was stored")
		}
		if _, err := h.Store.Get(ctx, "dsv_a"); !errors.Is(err, db.ErrNotFound) {
			t.Errorf("the refused row exists: %v", err)
		}
	})

	t.Run("a failed commit writes nothing", func(t *testing.T) {
		h := mk(t)
		sentinel := errors.New("boom")
		err := h.Store.Commit(ctx, func(tx devservers.Tx) error {
			if _, err := tx.Save(ctx, "dsv_a", dsc.RowOf(key(h, "dev"), dsc.Stopped{}), db.NewDevServer, storeTime); err != nil {
				return err
			}
			if err := tx.Audit(ctx, core.AuditEntry{Actor: "system", Action: "devserver.start", Subject: "devserver:dsv_a"}, "aud_1", storeTime); err != nil {
				return err
			}
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("Commit = %v", err)
		}
		if _, err := h.Store.Get(ctx, "dsv_a"); !errors.Is(err, db.ErrNotFound) {
			t.Errorf("rolled-back row exists: %v", err)
		}
		if n := len(h.Audit("devserver:dsv_a")); n != 0 {
			t.Errorf("rolled-back audit entries: %d", n)
		}
	})

	t.Run("state and audit commit together", func(t *testing.T) {
		h := mk(t)
		err := h.Store.Commit(ctx, func(tx devservers.Tx) error {
			if _, err := tx.Save(ctx, "dsv_a", dsc.RowOf(key(h, "dev"), dsc.Stopped{}), db.NewDevServer, storeTime); err != nil {
				return err
			}
			return tx.Audit(ctx, core.AuditEntry{Actor: "system", Action: "devserver.start", Subject: "devserver:dsv_a"}, "aud_1", storeTime)
		})
		if err != nil {
			t.Fatal(err)
		}
		if n := len(h.Audit("devserver:dsv_a")); n != 1 {
			t.Errorf("audit entries = %d, want 1", n)
		}
	})

	t.Run("list live returns exactly the live states", func(t *testing.T) {
		h := mk(t)
		want := []string{}
		for i, st := range States() {
			id := fmt.Sprintf("dsv_%02d", i)
			if _, err := save(h, id, dsc.RowOf(key(h, fmt.Sprintf("c%d", i)), st), db.NewDevServer); err != nil {
				t.Fatal(err)
			}
			switch st.(type) {
			case dsc.Starting, dsc.Running, dsc.Stopping, dsc.Orphaned:
				want = append(want, id)
			}
		}
		live, err := h.Store.ListLive(ctx)
		if err != nil {
			t.Fatal(err)
		}
		got := []string{}
		for _, r := range live {
			got = append(got, r.ID)
		}
		if !slices.Equal(got, want) {
			t.Errorf("live = %v, want %v", got, want)
		}
		all, err := h.Store.ListByRepo(ctx, h.Repo)
		if err != nil || len(all) != len(States()) {
			t.Errorf("by repo = %d, %v", len(all), err)
		}
		if other, err := h.Store.ListByRepo(ctx, "rep_other"); err != nil || len(other) != 0 {
			t.Errorf("by other repo = %d, %v", len(other), err)
		}
	})
}

// WakeHarness is what WakesContract needs. Wait lets time pass: Advance on a
// fake clock, a short sleep on the real one.
type WakeHarness struct {
	Wakes *devservers.Wakes
	Clock devservers.Clock
	Wait  func(d time.Duration)
}

// WakesContract is the suite the wake scheduler passes on a fake clock and on
// the real one: a call runs once, not before its time, a key replaces its
// pending call, Cancel drops it, and Close drops the rest and stops further
// calls.
func WakesContract(t *testing.T, mk func(t *testing.T) WakeHarness) {
	unit := 20 * time.Millisecond
	t.Run("fires once at its time", func(t *testing.T) {
		h := mk(t)
		var mu sync.Mutex
		n := 0
		h.Wakes.At("k", h.Clock.Now().Add(2*unit), func(time.Time) { mu.Lock(); n++; mu.Unlock() })
		h.Wait(unit / 2)
		mu.Lock()
		early := n
		mu.Unlock()
		if early != 0 {
			t.Fatalf("fired early (%d)", early)
		}
		h.Wait(10 * unit)
		h.Wait(10 * unit)
		mu.Lock()
		defer mu.Unlock()
		if n != 1 {
			t.Errorf("fired %d times, want 1", n)
		}
	})

	t.Run("a key replaces its pending call", func(t *testing.T) {
		h := mk(t)
		var mu sync.Mutex
		var got []string
		add := func(s string) func(time.Time) {
			return func(time.Time) { mu.Lock(); got = append(got, s); mu.Unlock() }
		}
		h.Wakes.At("k", h.Clock.Now().Add(2*unit), add("first"))
		h.Wakes.At("k", h.Clock.Now().Add(3*unit), add("second"))
		h.Wait(10 * unit)
		h.Wait(10 * unit)
		mu.Lock()
		defer mu.Unlock()
		if !slices.Equal(got, []string{"second"}) {
			t.Errorf("fired %v, want [second]", got)
		}
	})

	t.Run("cancel drops the call", func(t *testing.T) {
		h := mk(t)
		var mu sync.Mutex
		n := 0
		h.Wakes.At("k", h.Clock.Now().Add(2*unit), func(time.Time) { mu.Lock(); n++; mu.Unlock() })
		h.Wakes.Cancel("k")
		h.Wait(10 * unit)
		h.Wait(10 * unit)
		mu.Lock()
		defer mu.Unlock()
		if n != 0 {
			t.Errorf("cancelled call ran %d times", n)
		}
	})

	t.Run("close drops pending calls and refuses new ones", func(t *testing.T) {
		h := mk(t)
		var mu sync.Mutex
		n := 0
		fire := func(time.Time) { mu.Lock(); n++; mu.Unlock() }
		h.Wakes.At("a", h.Clock.Now().Add(2*unit), fire)
		h.Wakes.Close()
		h.Wakes.At("b", h.Clock.Now(), fire)
		h.Wait(10 * unit)
		h.Wait(10 * unit)
		h.Wakes.Close() // twice is fine
		mu.Lock()
		defer mu.Unlock()
		if n != 0 {
			t.Errorf("calls after Close: %d", n)
		}
	})

	t.Run("a past time fires", func(t *testing.T) {
		h := mk(t)
		done := make(chan struct{})
		h.Wakes.At("k", h.Clock.Now().Add(-time.Hour), func(time.Time) { close(done) })
		h.Wait(unit)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("a past time did not fire")
		}
	})
}

// LogSinkContract is the suite every devservers.LogSink passes: the ring
// keeps the last lines in order, redaction is applied before anything is kept
// or published, long lines are cut, and nothing is written after Close.
func LogSinkContract(t *testing.T, mk func(t *testing.T, size int, redact devservers.Redact, publish func(string)) devservers.LogSink) {
	redact := func(s string) string { return strings.ReplaceAll(s, "SECRET-VALUE", "[REDACTED]") }
	t.Run("keeps the last lines in order", func(t *testing.T) {
		s := mk(t, 3, redact, nil)
		defer s.Close()
		for i := 1; i <= 5; i++ {
			s.Write(fmt.Sprintf("line %d", i))
		}
		if got := s.Tail(-1); !slices.Equal(got, []string{"line 3", "line 4", "line 5"}) {
			t.Errorf("tail = %v", got)
		}
		if got := s.Tail(2); !slices.Equal(got, []string{"line 4", "line 5"}) {
			t.Errorf("tail(2) = %v", got)
		}
		if got := s.Tail(10); len(got) != 3 {
			t.Errorf("tail(10) = %v", got)
		}
	})
	t.Run("fewer lines than the ring", func(t *testing.T) {
		s := mk(t, 5, redact, nil)
		defer s.Close()
		s.Write("a")
		s.Write("b")
		if got := s.Tail(-1); !slices.Equal(got, []string{"a", "b"}) {
			t.Errorf("tail = %v", got)
		}
	})
	t.Run("redacts before keeping and publishing", func(t *testing.T) {
		var mu sync.Mutex
		var pub []string
		s := mk(t, 5, redact, func(l string) { mu.Lock(); pub = append(pub, l); mu.Unlock() })
		defer s.Close()
		s.Write("token=SECRET-VALUE")
		if got := s.Tail(-1); !slices.Equal(got, []string{"token=[REDACTED]"}) {
			t.Errorf("tail = %v", got)
		}
		mu.Lock()
		defer mu.Unlock()
		if !slices.Equal(pub, []string{"token=[REDACTED]"}) {
			t.Errorf("published = %v", pub)
		}
	})
	t.Run("cuts a very long line", func(t *testing.T) {
		s := mk(t, 5, redact, nil)
		defer s.Close()
		s.Write(strings.Repeat("x", devservers.MaxLineBytes*2))
		if got := s.Tail(1); len(got) != 1 || len(got[0]) > devservers.MaxLineBytes+16 {
			t.Errorf("long line kept at %d bytes", len(got[0]))
		}
	})
	t.Run("no write after close", func(t *testing.T) {
		var mu sync.Mutex
		n := 0
		s := mk(t, 5, redact, func(string) { mu.Lock(); n++; mu.Unlock() })
		s.Write("before")
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		s.Write("after")
		if got := s.Tail(-1); !slices.Equal(got, []string{"before"}) {
			t.Errorf("tail = %v", got)
		}
		mu.Lock()
		defer mu.Unlock()
		if n != 1 {
			t.Errorf("published %d lines, want 1", n)
		}
		if err := s.Close(); err != nil {
			t.Errorf("second Close = %v", err)
		}
	})
}
