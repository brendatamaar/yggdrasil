package notify

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corenotify "github.com/brendatamaar/yggdrasil/internal/core/notify"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

var errInjected = errors.New("injected store failure")

// flakyStore is a NoticeStore over a real *db.Store with scripted failures.
// A *db.Tx cannot be faked, so the fake decorates the real store: it can fail a
// commit before it runs (nothing written) or after fn ran (the transaction is
// rolled back), which is how the atomicity of notice + mark + audit is shown.
type flakyStore struct {
	*db.Store
	mu           sync.Mutex
	failBefore   int // commits to fail before running fn
	failAfter    int // commits to roll back after fn ran
	commitsTried int
}

func (f *flakyStore) Commit(ctx context.Context, fn func(*db.Tx) error) error {
	f.mu.Lock()
	f.commitsTried++
	before, after := f.failBefore > 0, f.failAfter > 0
	switch {
	case before:
		f.failBefore--
	case after:
		f.failAfter--
	}
	f.mu.Unlock()
	if before {
		return errInjected
	}
	if after {
		return f.Store.Commit(ctx, func(tx *db.Tx) error {
			if err := fn(tx); err != nil {
				return err
			}
			return errInjected
		})
	}
	return f.Store.Commit(ctx, fn)
}

func (f *flakyStore) script(before, after int) {
	f.mu.Lock()
	f.failBefore, f.failAfter = before, after
	f.mu.Unlock()
}

// recPub is a recording Publisher. drop simulates a hub that lost the message;
// onPublish runs at the moment of publication (used to check commit-before-publish).
type recPub struct {
	mu        sync.Mutex
	msgs      []api.NoticeChangedMessage
	drop      bool
	onPublish func(api.NoticeChangedMessage)
}

func (p *recPub) PublishNotice(m api.NoticeChangedMessage) {
	p.mu.Lock()
	hook, drop := p.onPublish, p.drop
	p.mu.Unlock()
	if hook != nil {
		hook(m)
	}
	if drop {
		return
	}
	p.mu.Lock()
	p.msgs = append(p.msgs, m)
	p.mu.Unlock()
}
func (p *recPub) PublishAction(api.ActionChangedMessage)   {}
func (p *recPub) PublishNewRepo(api.NewRepoChangedMessage) {}
func (p *recPub) setDrop(v bool)                           { p.mu.Lock(); p.drop = v; p.mu.Unlock() }
func (p *recPub) messages() []api.NoticeChangedMessage {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]api.NoticeChangedMessage(nil), p.msgs...)
}

type harness struct {
	t     *testing.T
	store *flakyStore
	pub   *recPub
	prod  *Producer
	tasks []core.Task
	deps  Deps
}

func newStore(t *testing.T) *db.Store {
	t.Helper()
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "ygg.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return db.NewStore(d, safety.NewRedactor().Redact)
}

func seedTasks(t *testing.T, s *db.Store, n int) []core.Task {
	t.Helper()
	project := dbtest.Project(t, s, core.Project{Name: "acme-shop", Path: t.TempDir()})
	dir := filepath.Join(project.Path, "shop-api")
	repo := dbtest.Repo(t, s, core.Repo{ProjectID: project.ID, Name: "shop-api", Path: dir})
	var out []core.Task
	for i := 0; i < n; i++ {
		out = append(out, dbtest.Task(t, s, core.Task{ProjectID: project.ID, Scope: core.ScopeRepo, Mode: core.ModeCode, RepoIDs: []core.RepoID{repo.ID}}))
	}
	return out
}

func newHarness(t *testing.T, tasks int) *harness {
	t.Helper()
	h := &harness{t: t, pub: &recPub{}}
	real := newStore(t)
	h.store = &flakyStore{Store: real}
	h.tasks = seedTasks(t, real, tasks)
	var tick, ids atomic.Int64
	h.deps = Deps{
		Store: h.store, Publisher: h.pub,
		Clock: func() time.Time { return time.Unix(1_700_000_000+tick.Add(1), 0) },
		IDs: func(prefix string) core.NoticeID {
			return core.NoticeID(fmt.Sprintf("%s_%06d", prefix, ids.Add(1)))
		},
	}
	h.start()
	return h
}

// start (re)starts the producer on the same store, as a restart of the server does.
func (h *harness) start() {
	h.t.Helper()
	p, err := New(context.Background(), h.deps)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { _ = p.Close() })
	h.prod = p
}

func (h *harness) task(i int) core.TaskID { return h.tasks[i].ID }

func (h *harness) history() []corenotify.Notice {
	h.t.Helper()
	page, err := h.prod.ListNotices(context.Background(), 0, corenotify.MaxPage)
	if err != nil {
		h.t.Fatal(err)
	}
	return page.Notices
}

func (h *harness) send(i int, st core.TaskStatus, rev int64) error {
	return h.prod.TaskStatus(context.Background(), h.task(i), "Fix the build", st, rev)
}
