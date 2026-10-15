package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

type sqliteWriter struct {
	store *db.Store
	r     safety.Redactor
}

// NewSQLiteWriter returns a Writer that appends to the audit_log table. It
// sets ID and At, and redacts Detail with r before the row is written.
func NewSQLiteWriter(store *db.Store, r safety.Redactor) Writer {
	return sqliteWriter{store: store, r: r}
}

// Write implements Writer.
func (w sqliteWriter) Write(ctx context.Context, e core.AuditEntry) error {
	if e.Actor == "" || e.Action == "" || e.Subject == "" {
		return errors.New("audit entry needs actor, action and subject")
	}
	e.ID = core.AuditID(db.NewID("aud"))
	e.At = time.Now().UTC()
	detail, err := w.redactDetail(e.Detail)
	if err != nil {
		return err
	}
	e.Detail = detail
	e.Actor, e.Action, e.Subject = w.r.Redact(e.Actor), w.r.Redact(e.Action), w.r.Redact(e.Subject)
	return w.store.InsertAudit(ctx, e)
}

// redactDetail redacts the JSON text. If that breaks the JSON (a secret with
// characters JSON escapes), the redacted text is stored as a JSON string.
func (w sqliteWriter) redactDetail(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.RawMessage("{}"), nil
	}
	if !json.Valid(raw) {
		return nil, errors.New("audit detail is not valid JSON")
	}
	red := json.RawMessage(w.r.Redact(string(raw)))
	if json.Valid(red) {
		return red, nil
	}
	b, err := json.Marshal(w.r.Redact(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("encode audit detail: %w", err)
	}
	return b, nil
}
