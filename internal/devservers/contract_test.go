package devservers_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
	"github.com/brendatamaar/yggdrasil/internal/devservers"
	"github.com/brendatamaar/yggdrasil/internal/devservers/devserverstest"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

func TestFakeStoreContract(t *testing.T) {
	devserverstest.DevServerStoreContract(t, func(t *testing.T) devserverstest.StoreHarness {
		s := devserverstest.NewStore("rep_1")
		return devserverstest.StoreHarness{Store: s, Repo: "rep_1", Audit: s.Audit}
	})
}

func newSQLite(t *testing.T) *db.Store {
	t.Helper()
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "ygg.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return db.NewStore(d, safety.NewRedactor().Redact)
}

func TestSQLiteStoreContract(t *testing.T) {
	devserverstest.DevServerStoreContract(t, func(t *testing.T) devserverstest.StoreHarness {
		store := newSQLite(t)
		project := dbtest.Project(t, store, core.Project{Name: "acme-shop", Path: t.TempDir()})
		dir := filepath.Join(project.Path, "shop-web")
		repo := dbtest.Repo(t, store, core.Repo{ProjectID: project.ID, Name: "shop-web", Path: dir})
		return devserverstest.StoreHarness{
			Store: devservers.NewSQLiteStore(store, safety.NewRedactor()), Repo: repo.ID,
			Audit: func(subject string) []core.AuditEntry {
				out, err := store.ListAudit(context.Background(), subject, 100)
				if err != nil {
					t.Fatal(err)
				}
				return out
			},
		}
	})
}

func TestWakesOnFakeClock(t *testing.T) {
	devserverstest.WakesContract(t, func(t *testing.T) devserverstest.WakeHarness {
		c := devserverstest.NewClock()
		w := devservers.NewWakes(c)
		t.Cleanup(w.Close)
		return devserverstest.WakeHarness{Wakes: w, Clock: c, Wait: c.Advance}
	})
}

func TestWakesOnRealClock(t *testing.T) {
	devserverstest.WakesContract(t, func(t *testing.T) devserverstest.WakeHarness {
		c := devservers.RealClock{}
		w := devservers.NewWakes(c)
		t.Cleanup(w.Close)
		return devserverstest.WakeHarness{Wakes: w, Clock: c, Wait: time.Sleep}
	})
}

func TestMemorySinkContract(t *testing.T) {
	devserverstest.LogSinkContract(t, func(t *testing.T, size int, r devservers.Redact, p func(devservers.LogLine)) devservers.LogSink {
		s, err := devservers.NewMemorySink(r, size, p)
		if err != nil {
			t.Fatal(err)
		}
		return s
	})
}

func TestFileSinkContract(t *testing.T) {
	devserverstest.LogSinkContract(t, func(t *testing.T, size int, r devservers.Redact, p func(devservers.LogLine)) devservers.LogSink {
		s, err := devservers.NewFileSink(filepath.Join(t.TempDir(), "my logs"), "dsv_a", r, size, 0, p)
		if err != nil {
			t.Fatal(err)
		}
		return s
	})
}

func TestSQLiteStoreRedactsAuditEntries(t *testing.T) {
	ctx := context.Background()
	store := newSQLite(t)
	s := devservers.NewSQLiteStore(store, safety.NewRedactor())
	const tok = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	err := s.Commit(ctx, func(tx devservers.Tx) error {
		return tx.Audit(ctx, core.AuditEntry{Actor: "system", Action: "devserver.fail", Subject: "devserver:dsv_a",
			Detail: []byte(`{"reason":"` + tok + `"}`)}, "aud_1", time.Now())
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := store.ListAudit(ctx, "devserver:dsv_a", 10)
	if err != nil || len(rows) != 1 || strings.Contains(string(rows[0].Detail), "ghp_") {
		t.Fatalf("rows = %+v, %v", rows, err)
	}
}

func TestLogSeqContract(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		devserverstest.LogSeqContract(t, func(t *testing.T, size int, r devservers.Redact, p func(devservers.LogLine)) devservers.LogSink {
			s, err := devservers.NewMemorySink(r, size, p)
			if err != nil {
				t.Fatal(err)
			}
			return s
		})
	})
	t.Run("file", func(t *testing.T) {
		devserverstest.LogSeqContract(t, func(t *testing.T, size int, r devservers.Redact, p func(devservers.LogLine)) devservers.LogSink {
			s, err := devservers.NewFileSink(t.TempDir(), "dsv_a", r, size, 0, p)
			if err != nil {
				t.Fatal(err)
			}
			return s
		})
	})
}

func TestSinksRequireARedactor(t *testing.T) {
	if _, err := devservers.NewMemorySink(nil, 5, nil); !errors.Is(err, devservers.ErrNoRedactor) {
		t.Errorf("memory sink without a redactor: %v", err)
	}
	if _, err := devservers.NewFileSink(t.TempDir(), "dsv_a", nil, 5, 0, nil); !errors.Is(err, devservers.ErrNoRedactor) {
		t.Errorf("file sink without a redactor: %v", err)
	}
}
