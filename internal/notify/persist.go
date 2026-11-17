package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/brendatamaar/yggdrasil/internal/audit"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corenotify "github.com/brendatamaar/yggdrasil/internal/core/notify"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// noticeAuditDetail is the audit detail of a notice: IDs, status and revision,
// never the title or any content.
type noticeAuditDetail struct {
	NoticeID string `json:"noticeId"`
	Status   string `json:"status"`
	Source   string `json:"source"`
	Revision int64  `json:"revision"`
}

// persistNotice commits the notice, its high-water mark, the retention trim
// (all in AppendNotice) and the audit entry in one transaction. A revision not
// above the stored mark is db.ErrConflict and writes nothing.
func (p *Producer) persistNotice(ctx context.Context, c corenotify.PersistNoticeAndAudit) (corenotify.Notice, error) {
	n := c.Notice
	detail, err := json.Marshal(noticeAuditDetail{NoticeID: string(n.ID), Status: string(n.Status), Source: string(n.Source.Kind), Revision: n.SourceRevision})
	if err != nil {
		return n, fmt.Errorf("audit notice %s: %w", n.ID, err)
	}
	entry, err := audit.Prepare(core.AuditEntry{
		Actor: "system", Action: "notice.create", Subject: "task:" + string(n.Task), Detail: detail,
	}, p.deps.Redactor, core.AuditID(p.deps.IDs("aud")), n.CreatedAt)
	if err != nil {
		return n, fmt.Errorf("audit notice %s: %w", n.ID, err)
	}
	var stored corenotify.Notice
	err = p.deps.Store.Commit(ctx, func(tx *db.Tx) error {
		var aerr error
		if stored, aerr = tx.AppendNotice(ctx, n); aerr != nil {
			return aerr
		}
		return tx.AppendAudit(ctx, entry)
	})
	switch {
	case err == nil:
		return stored, nil
	case errors.Is(err, db.ErrConflict):
		return n, err
	case errors.Is(err, db.ErrNotFound):
		return n, fmt.Errorf("%w: %w", ErrUnknownTask, err)
	}
	return n, fmt.Errorf("store notice %s: %w", n.ID, err)
}

// persistHighWater commits only a new mark (an uninteresting newer status). It
// writes no audit entry: it changes dedup bookkeeping, not anything a person
// sees, and one task would otherwise fill the log with every running or queued turn.
func (p *Producer) persistHighWater(ctx context.Context, c corenotify.PersistHighWater) error {
	now := p.deps.Clock().UTC()
	err := p.deps.Store.Commit(ctx, func(tx *db.Tx) error {
		_, aerr := tx.AdvanceNoticeHighWater(ctx, c.Key, c.Revision, now)
		return aerr
	})
	switch {
	case err == nil:
		return nil
	case errors.Is(err, db.ErrNotFound):
		return fmt.Errorf("%w: %w", ErrUnknownTask, err)
	}
	return fmt.Errorf("store notification high-water mark: %w", err)
}
