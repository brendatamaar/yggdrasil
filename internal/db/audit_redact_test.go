package db

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// The store's redactor is applied to every audit field, and detail stays JSON
// even when masking breaks the text.
func TestAppendAuditAppliesTheStoreRedactor(t *testing.T) {
	d := openTest(t)
	s := NewStore(d, func(in string) string { return strings.ReplaceAll(in, "SECRET", "[x]") })
	ctx := context.Background()
	e := core.AuditEntry{ID: "aud_1", At: time.Now(), Actor: "a-SECRET", Action: "b-SECRET", Subject: "c-SECRET", Detail: json.RawMessage(`{"k":"SECRET"}`)}
	if err := s.Commit(ctx, func(tx *Tx) error { return tx.AppendAudit(ctx, e) }); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListAudit(ctx, "c-[x]", 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %+v, %v; the subject must be stored redacted", rows, err)
	}
	got := rows[0]
	if got.Actor != "a-[x]" || got.Action != "b-[x]" || string(got.Detail) != `{"k":"[x]"}` {
		t.Errorf("entry = %+v", got)
	}

	// A redactor whose output is not JSON: the detail is stored as a JSON string.
	s2 := NewStore(d, func(in string) string { return strings.ReplaceAll(in, "SECRET", `"`) })
	e2 := core.AuditEntry{ID: "aud_2", At: time.Now(), Actor: "a", Action: "b", Subject: "c", Detail: json.RawMessage(`{"k":"SECRET"}`)}
	if err := s2.Commit(ctx, func(tx *Tx) error { return tx.AppendAudit(ctx, e2) }); err != nil {
		t.Fatal(err)
	}
	rows, err = s2.ListAudit(ctx, "c", 10)
	if err != nil || len(rows) != 1 || !json.Valid(rows[0].Detail) || strings.Contains(string(rows[0].Detail), "SECRET") {
		t.Fatalf("rows = %+v, %v; detail must stay valid JSON", rows, err)
	}
}
