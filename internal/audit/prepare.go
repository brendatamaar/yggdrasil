package audit

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// Prepare is the one way an audit entry is made ready for the log (SF-05,
// invariant 8). It is pure: the caller supplies the ID and the time, so the
// same arguments always give the same entry. It checks that the entry names an
// actor, an action and a subject and that Detail is JSON, then redacts every
// free-text field with r, so tokens never reach the audit log (invariant 4).
//
// Commit the result in the same transaction as the state it describes:
// db.Tx.AppendAudit.
func Prepare(e core.AuditEntry, r safety.Redactor, id core.AuditID, now time.Time) (core.AuditEntry, error) {
	if e.Actor == "" || e.Action == "" || e.Subject == "" {
		return core.AuditEntry{}, errors.New("audit entry needs actor, action and subject")
	}
	if err := id.Validate(); err != nil {
		return core.AuditEntry{}, err
	}
	detail, err := redactDetail(r, e.Detail)
	if err != nil {
		return core.AuditEntry{}, err
	}
	e.ID = id
	e.At = now.UTC()
	e.Detail = detail
	e.Actor, e.Action, e.Subject = r.Redact(e.Actor), r.Redact(e.Action), r.Redact(e.Subject)
	return e, nil
}

// redactDetail redacts the JSON text. If that breaks the JSON (a secret with
// characters JSON escapes), the redacted text is stored as a JSON string.
func redactDetail(r safety.Redactor, raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.RawMessage("{}"), nil
	}
	if !json.Valid(raw) {
		return nil, errors.New("audit detail is not valid JSON")
	}
	red := json.RawMessage(r.Redact(string(raw)))
	if json.Valid(red) {
		return red, nil
	}
	b, err := json.Marshal(r.Redact(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("encode audit detail: %w", err)
	}
	return b, nil
}
