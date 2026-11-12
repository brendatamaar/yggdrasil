package notify

import (
	"context"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corenotify "github.com/brendatamaar/yggdrasil/internal/core/notify"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// Consumer-local ports of the notification producer (UI-06, D11,
// docs/design/phase-6-notifications.md). Declarations only; the notification
// task (Q) implements the producer. Browser permission and toasts are UI
// effects, not backend commands.

// NoticeStore is the durable notice history and high-water marks, implemented
// by *db.Store: AppendNotice commits the notice, its high-water mark, the
// retention trim and the audit entry in one Store.Commit (a duplicate source
// revision is db.ErrConflict and writes nothing); ListNotices pages history
// with gap detection; GetNoticeHighWater feeds Recovered.
type NoticeStore interface {
	GetNoticeHighWater(ctx context.Context) (map[corenotify.Key]int64, error)
	ListNotices(ctx context.Context, after int64, limit int) (db.NoticePage, error)
	Commit(ctx context.Context, fn func(*db.Tx) error) error
}

// Publisher is the hub side (api.Phase6Hub): PublishNotice is called only after
// the commit succeeded, with IDs and cursors and no content.
type Publisher = api.Phase6Hub

// TaskTransitions is how the task service reports a committed status. The task
// service already publishes tasks.StatusMessage (now with a Revision); an
// adapter turns it into a core/notify.TaskTransition. Hub fan-out of the raw
// message is unchanged.
type TaskTransitions interface {
	TaskTransition(ctx context.Context, t corenotify.TaskTransition) error
}

// VerificationTransitions is the same for a verification result: the adapter
// supplies the run ID and the verification version as the revision, so an old
// run cannot overwrite a new one.
type VerificationTransitions interface {
	VerificationTransition(ctx context.Context, v corenotify.VerificationTransition) error
}

// Deps is the aggregate the notification task builds the producer from.
type Deps struct {
	Store     NoticeStore
	Publisher Publisher
	// Clock and IDs are the shell's time and randomness; the core never makes one.
	Clock func() time.Time
	IDs   func(prefix string) core.NoticeID
}

// *db.Store is the live NoticeStore, and *api.Server the live Publisher.
var (
	_ NoticeStore = (*db.Store)(nil)
	_ Publisher   = (*api.Server)(nil)
)
