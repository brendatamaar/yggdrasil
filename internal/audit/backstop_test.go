package audit

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// The store redacts again: an entry that skipped Prepare still cannot put a
// token in the log (invariant 4, backstop).
func TestAppendAuditRedactsEntriesThatSkippedPrepare(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	const tok = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	raw := core.AuditEntry{
		ID: "aud_raw", At: time.Now(), Actor: "agent:" + tok, Action: "x " + tok, Subject: "task:1",
		Detail: json.RawMessage(`{"cmd":"git push https://u:pw@host/x","t":"` + tok + `"}`),
	}
	if err := store.Commit(ctx, func(tx *db.Tx) error { return tx.AppendAudit(ctx, raw) }); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ListAudit(ctx, "", 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %+v, %v", rows, err)
	}
	e := rows[0]
	all := e.Actor + e.Action + e.Subject + string(e.Detail)
	for _, leak := range []string{"ghp_", "pw@"} {
		if strings.Contains(all, leak) {
			t.Errorf("%q survived in %+v", leak, e)
		}
	}
	if !json.Valid(e.Detail) {
		t.Errorf("detail is not JSON: %s", e.Detail)
	}
}
