package audit

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

func TestWriterRedactsAndStamps(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	store := db.NewStore(d)
	w := NewSQLiteWriter(store, safety.NewRedactor("s3cretvalue"))

	before := time.Now().Add(-time.Second)
	detail := json.RawMessage(`{"url":"https://u:pw@host/x","auth":"Bearer abc123","token":"ghp_abcdefghijklmnopqrst","note":"s3cretvalue \"quoted\" ok"}`)
	err = w.Write(ctx, core.AuditEntry{ID: "forced", At: time.Unix(0, 0), Actor: "user", Action: "push", Subject: "task:1", Detail: detail})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Write(ctx, core.AuditEntry{Actor: "system", Action: "start", Subject: "task:1"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Write(ctx, core.AuditEntry{Actor: "system", Action: "bad", Subject: "x", Detail: json.RawMessage(`{`)}); err == nil {
		t.Fatal("invalid JSON detail accepted")
	}
	if err := w.Write(ctx, core.AuditEntry{Action: "x", Subject: "x"}); err == nil {
		t.Fatal("entry without actor accepted")
	}

	got, err := store.ListAudit(ctx, "task:1", 10)
	if err != nil || len(got) != 2 {
		t.Fatalf("entries = %+v, %v", got, err)
	}
	for _, e := range got {
		if e.ID == "forced" || e.At.Before(before) {
			t.Errorf("ID/At not assigned: %+v", e)
		}
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
