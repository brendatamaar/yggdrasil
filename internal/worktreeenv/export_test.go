package worktreeenv

import (
	"context"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// QueueAudit puts an entry in the retry queue (for tests).
func (s *ComposeService) QueueAudit(e core.AuditEntry, det bool) { s.queueAudit(e, det) }

// AppendOnce writes an entry once (for tests).
func (s *ComposeService) AppendOnce(ctx context.Context, e core.AuditEntry, det bool) error {
	return s.appendOnce(ctx, e, det)
}

// QueueLen and Dropped report the retry queue (for tests).
func (s *ComposeService) QueueLen() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.queued) }
func (s *ComposeService) Dropped() int  { s.mu.Lock(); defer s.mu.Unlock(); return s.dropped }
func (s *ComposeService) Remembered() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.written)
}

// FlushAudits runs one flush (for tests).
func (s *ComposeService) FlushAudits() { s.flushAudits(1e9) }

// Boot is the boot token (for tests).
func (s *ComposeService) Boot() string { return s.boot }
