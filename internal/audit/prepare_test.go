package audit

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

func openStore(t *testing.T) *db.Store {
	t.Helper()
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return db.NewStore(d, safety.NewRedactor().Redact)
}

func TestPrepareRedactsAndStamps(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	r := safety.NewRedactor("s3cretvalue")
	at := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

	detail := json.RawMessage(`{"url":"https://u:pw@host/x","auth":"Bearer abc123","token":"ghp_abcdefghijklmnopqrst","note":"s3cretvalue \"quoted\" ok"}`)
	first, err := Prepare(core.AuditEntry{ID: "forced", At: time.Unix(0, 0), Actor: "user", Action: "push", Subject: "task:1", Detail: detail}, r, "aud_1", at)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Prepare(core.AuditEntry{Actor: "system", Action: "start", Subject: "task:1"}, r, "aud_2", at.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != "aud_1" || !first.At.Equal(at) {
		t.Errorf("ID and time must come from the caller: %+v", first)
	}
	if string(second.Detail) != "{}" {
		t.Errorf("empty detail = %s", second.Detail)
	}
	err = store.Commit(ctx, func(tx *db.Tx) error {
		if err := tx.AppendAudit(ctx, first); err != nil {
			return err
		}
		return tx.AppendAudit(ctx, second)
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := store.ListAudit(ctx, "task:1", 10)
	if err != nil || len(got) != 2 {
		t.Fatalf("entries = %+v, %v", got, err)
	}
	for _, e := range got {
		if !json.Valid(e.Detail) {
			t.Errorf("detail is not JSON: %s", e.Detail)
		}
		for _, leak := range []string{"pw@", "abc123", "ghp_", "s3cretvalue"} {
			if strings.Contains(string(e.Detail), leak) {
				t.Errorf("audit detail leaks %q: %s", leak, e.Detail)
			}
		}
	}
}

func TestPrepareIsPure(t *testing.T) {
	r := safety.NewRedactor()
	at := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	e := core.AuditEntry{Actor: "user", Action: "x", Subject: "task:1", Detail: json.RawMessage(`{"a":"Bearer abc123"}`)}
	a, err1 := Prepare(e, r, "aud_1", at)
	b, err2 := Prepare(e, r, "aud_1", at)
	if err1 != nil || err2 != nil || a.ID != b.ID || !a.At.Equal(b.At) || string(a.Detail) != string(b.Detail) {
		t.Errorf("same arguments gave %+v and %+v (%v, %v)", a, b, err1, err2)
	}
}

func TestPrepareRejects(t *testing.T) {
	r := safety.NewRedactor()
	at := time.Now()
	ok := core.AuditEntry{Actor: "system", Action: "x", Subject: "task:1"}
	for name, c := range map[string]struct {
		mutate func(*core.AuditEntry)
		id     core.AuditID
	}{
		"no actor":      {func(e *core.AuditEntry) { e.Actor = "" }, "aud_1"},
		"no action":     {func(e *core.AuditEntry) { e.Action = "" }, "aud_1"},
		"no subject":    {func(e *core.AuditEntry) { e.Subject = "" }, "aud_1"},
		"invalid JSON":  {func(e *core.AuditEntry) { e.Detail = json.RawMessage(`{`) }, "aud_1"},
		"empty ID":      {func(*core.AuditEntry) {}, ""},
		"ID with space": {func(*core.AuditEntry) {}, "a b"},
	} {
		e := ok
		c.mutate(&e)
		if _, err := Prepare(e, r, c.id, at); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The state and its audit entry are one unit: a failing write after
// AppendAudit rolls both back (invariant 8).
func TestCommitRollsBackStateAndAuditTogether(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	entry, err := Prepare(core.AuditEntry{Actor: "user", Action: "project.track", Subject: "project:p"}, safety.NewRedactor(), "aud_1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("later write failed")
	err = store.Commit(ctx, func(tx *db.Tx) error {
		if err := tx.Workspace().InsertProject(ctx, corerepos.InsertProject{Project: corerepos.Project{ID: "p", Name: "acme-shop", Path: "/work/acme-shop", Life: corerepos.Active{Since: time.Now()}}}); err != nil {
			return err
		}
		if err := tx.AppendAudit(ctx, entry); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Commit = %v, want the failing write's error", err)
	}
	if ps, err := store.ListProjects(ctx); err != nil || len(ps) != 0 {
		t.Errorf("project survived the rollback: %+v, %v", ps, err)
	}
	if rows, err := store.ListAudit(ctx, "", 10); err != nil || len(rows) != 0 {
		t.Errorf("audit entry survived the rollback: %+v, %v", rows, err)
	}
}
