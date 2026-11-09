package worktreeenv

import "github.com/brendatamaar/yggdrasil/internal/core"

// QueueAudit puts an entry in the retry queue (for tests).
func (s *ComposeService) QueueAudit(e core.AuditEntry) { s.queueAudit(e) }
