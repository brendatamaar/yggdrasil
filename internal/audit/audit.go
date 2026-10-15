package audit

import (
	"context"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Writer records every state change (SF-05, invariant 8). Every handler,
// proposal executor and git mutation takes one. Implementations set ID and
// At, and redact Detail before writing: tokens never reach the audit log
// (invariant 4).
type Writer interface {
	Write(ctx context.Context, entry core.AuditEntry) error
}
