package notify

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corenotify "github.com/brendatamaar/yggdrasil/internal/core/notify"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// Producer turns committed task and verification transitions into notices
// (UI-06). One owner goroutine holds the core state and handles one transition
// at a time, so every Key is serialized: it calls corenotify.Step, commits the
// notice, its high-water mark, the retention trim and the audit entry in one
// Store.Commit, and only then publishes an invalidation (IDs and a cursor, no
// content).
type Producer struct {
	deps Deps

	in     chan request
	run    context.Context
	cancel context.CancelFunc
	group  *errgroup.Group
	done   chan struct{}
	once   sync.Once

	state corenotify.State // owned by the loop goroutine
}

type request struct {
	in    corenotify.Input
	reply chan error
}

var (
	_ TaskTransitions         = (*Producer)(nil)
	_ VerificationTransitions = (*Producer)(nil)
)

// New loads the stored history and high-water marks (the Recovered input) and
// starts the owner goroutine, which stops when ctx is cancelled or Close is
// called.
func New(ctx context.Context, d Deps) (*Producer, error) {
	if d.Store == nil || d.Publisher == nil {
		return nil, errors.New("notify: a store and a publisher are required")
	}
	if d.Clock == nil {
		d.Clock = time.Now
	}
	if d.IDs == nil {
		d.IDs = func(prefix string) core.NoticeID { return core.NoticeID(db.NewID(prefix)) }
	}
	if d.Redactor == nil {
		d.Redactor = safety.NewRedactor()
	}
	hw, err := d.Store.GetNoticeHighWater(ctx)
	if err != nil {
		return nil, fmt.Errorf("load notification high-water marks: %w", err)
	}
	page, err := d.Store.ListNotices(ctx, 0, corenotify.MaxRecent)
	if err != nil {
		return nil, fmt.Errorf("load notification history: %w", err)
	}
	state, _ := corenotify.Step(corenotify.Empty{}, corenotify.Recovered{Recent: page.Notices, HighWater: hw})

	run, cancel := context.WithCancel(ctx)
	g, gctx := errgroup.WithContext(run)
	p := &Producer{deps: d, in: make(chan request), run: gctx, cancel: cancel, group: g, done: make(chan struct{}), state: state}
	g.Go(func() error { p.loop(); return nil })
	return p, nil
}

// Close stops the owner goroutine and waits for it. It is safe to call again.
func (p *Producer) Close() error {
	p.once.Do(func() {
		p.cancel()
		_ = p.group.Wait()
	})
	return nil
}

// loop is the only goroutine that touches state.
func (p *Producer) loop() {
	defer close(p.done)
	defer func() { p.state, _ = corenotify.Step(p.state, corenotify.Stop{}) }()
	for {
		select {
		case <-p.run.Done():
			return
		case r := <-p.in:
			r.reply <- p.handle(p.run, r.in) // reply is buffered: never blocks
		}
	}
}

// submit hands one input to the owner goroutine and waits for its outcome.
func (p *Producer) submit(ctx context.Context, in corenotify.Input) error {
	r := request{in: in, reply: make(chan error, 1)}
	select {
	case p.in <- r:
	case <-p.run.Done():
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-r.reply:
		return err
	case <-p.done:
		select {
		case err := <-r.reply:
			return err
		default:
			return ErrClosed
		}
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TaskTransition implements TaskTransitions. Time and the notice ID are set
// here, and the title is redacted (a token pasted into a title must not reach
// a notice); the task service passes only what it knows.
func (p *Producer) TaskTransition(ctx context.Context, t corenotify.TaskTransition) error {
	if err := parseTask(t); err != nil {
		return err
	}
	t.Title = p.deps.Redactor.Redact(t.Title)
	t.Now, t.ID = p.deps.Clock().UTC(), p.deps.IDs("ntc")
	return p.submit(ctx, t)
}

// VerificationTransition implements VerificationTransitions.
func (p *Producer) VerificationTransition(ctx context.Context, v corenotify.VerificationTransition) error {
	if err := parseVerification(v); err != nil {
		return err
	}
	v.Title = p.deps.Redactor.Redact(v.Title)
	v.Now, v.ID = p.deps.Clock().UTC(), p.deps.IDs("ntc")
	return p.submit(ctx, v)
}

// TaskStatus is the adapter from the task service's status message (task ID,
// title, status and the task row version as the revision) to TaskTransition.
// It takes plain fields so this package does not import internal/tasks.
func (p *Producer) TaskStatus(ctx context.Context, id core.TaskID, title string, status core.TaskStatus, revision int64) error {
	return p.TaskTransition(ctx, corenotify.TaskTransition{Task: id, Title: title, Status: status, Revision: revision})
}

// VerificationResult is the adapter from a verification result (run ID, its
// version as the revision) to VerificationTransition.
func (p *Producer) VerificationResult(ctx context.Context, id core.TaskID, title, runID string, result corenotify.VerifyResult, revision int64) error {
	return p.VerificationTransition(ctx, corenotify.VerificationTransition{Task: id, Title: title, RunID: runID, Result: result, Revision: revision})
}

// ListNotices pages notification history for a client that connects or
// reconnects. after == 0 gives the newest limit notices (newest first); a
// cursor gives the notices after it (oldest first), with Gap set when history
// was trimmed past it so the client must refetch and not replay old toasts.
func (p *Producer) ListNotices(ctx context.Context, after int64, limit int) (db.NoticePage, error) {
	if after < 0 {
		return db.NoticePage{}, invalid("the cursor cannot be negative")
	}
	if limit < 1 || limit > corenotify.MaxPage {
		limit = corenotify.MaxPage
	}
	page, err := p.deps.Store.ListNotices(ctx, after, limit)
	if err != nil {
		return db.NoticePage{}, fmt.Errorf("list notifications: %w", err)
	}
	page.Gap = corenotify.CursorGap(after, page.Oldest)
	return page, nil
}

// handle runs one input: step, persist atomically, then publish. The state
// advances only when every command succeeded, so a failed commit can be retried
// with the same revision and nothing is published for it.
func (p *Producer) handle(ctx context.Context, in corenotify.Input) error {
	next, cmds := corenotify.Step(p.state, in)
	if len(cmds) == 0 {
		return nil
	}
	cursors := map[core.NoticeID]int64{}
	for _, c := range cmds {
		switch c := c.(type) {
		case corenotify.PersistNoticeAndAudit:
			n, err := p.persistNotice(ctx, c)
			switch {
			case errors.Is(err, db.ErrConflict):
				// Another writer already holds this revision: the notice exists or
				// was trimmed. A duplicate is a no-op, not a failure.
			case err != nil:
				return err
			default:
				cursors[n.ID] = n.Cursor
			}
		case corenotify.PersistHighWater:
			if err := p.persistHighWater(ctx, c); err != nil {
				return err
			}
		case corenotify.PublishNotice:
			// Only after the commit above succeeded, and only for a notice this
			// call wrote.
			if cur, ok := cursors[c.ID]; ok {
				p.deps.Publisher.PublishNotice(api.NoticeChangedMessage{NoticeID: c.ID, Cursor: cur})
			}
		}
	}
	p.state = next
	return nil
}
