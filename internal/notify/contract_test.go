package notify

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corenotify "github.com/brendatamaar/yggdrasil/internal/core/notify"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// A *db.Tx cannot be faked, so the NoticeStore fake is flakyStore: the real
// store with scripted failures. The suite runs against the real *db.Store and
// the decorated one with no failure scripted, so the decorator provably changes
// nothing when it does not fail.
type contractEnv struct {
	Store NoticeStore
	Tasks []core.TaskID
}

func TestNoticeStoreContract(t *testing.T) {
	makers := map[string]func(t *testing.T) contractEnv{
		"db.Store": func(t *testing.T) contractEnv {
			s := newStore(t)
			return contractEnv{Store: s, Tasks: ids(seedTasks(t, s, 2))}
		},
		"flakyStore": func(t *testing.T) contractEnv {
			s := newStore(t)
			return contractEnv{Store: &flakyStore{Store: s}, Tasks: ids(seedTasks(t, s, 2))}
		},
	}
	for name, mk := range makers {
		t.Run(name, func(t *testing.T) { noticeStoreContract(t, mk) })
	}
}

func ids(ts []core.Task) []core.TaskID {
	var out []core.TaskID
	for _, t := range ts {
		out = append(out, t.ID)
	}
	return out
}

func noticeStoreContract(t *testing.T, mk func(t *testing.T) contractEnv) {
	ctx := context.Background()
	at := time.Unix(1_700_000_000, 0).UTC()
	mkNotice := func(task core.TaskID, i int, src corenotify.Source, rev int64) corenotify.Notice {
		st := corenotify.Done
		if src.Kind == corenotify.SourceVerification {
			st = corenotify.VerificationFailed
		}
		return corenotify.Notice{ID: core.NoticeID(fmt.Sprintf("ntc_%05d", i)), Task: task, TaskTitle: "Fix the build", Status: st,
			Source: src, SourceRevision: rev, CreatedAt: at}
	}
	taskSrc := corenotify.Source{Kind: corenotify.SourceTask}
	append1 := func(s NoticeStore, n corenotify.Notice) (corenotify.Notice, error) {
		var out corenotify.Notice
		err := s.Commit(ctx, func(tx *db.Tx) error {
			var err error
			out, err = tx.AppendNotice(ctx, n)
			return err
		})
		return out, err
	}

	t.Run("append assigns increasing cursors and lists newest first", func(t *testing.T) {
		e := mk(t)
		a, err := append1(e.Store, mkNotice(e.Tasks[0], 1, taskSrc, 1))
		if err != nil {
			t.Fatal(err)
		}
		b, err := append1(e.Store, mkNotice(e.Tasks[0], 2, taskSrc, 2))
		if err != nil || b.Cursor <= a.Cursor || a.Cursor == 0 {
			t.Fatalf("cursors %d %d, %v", a.Cursor, b.Cursor, err)
		}
		page, err := e.Store.ListNotices(ctx, 0, 10)
		if err != nil || len(page.Notices) != 2 || page.Notices[0].ID != b.ID || page.Oldest != a.Cursor || page.Newest != b.Cursor || page.Gap {
			t.Fatalf("page %+v %v", page, err)
		}
		after, err := e.Store.ListNotices(ctx, a.Cursor, 10)
		if err != nil || len(after.Notices) != 1 || after.Notices[0].ID != b.ID {
			t.Fatalf("after %+v %v", after, err)
		}
	})

	t.Run("a duplicate or older revision is ErrConflict and writes nothing", func(t *testing.T) {
		e := mk(t)
		if _, err := append1(e.Store, mkNotice(e.Tasks[0], 1, taskSrc, 5)); err != nil {
			t.Fatal(err)
		}
		for i, rev := range []int64{5, 4} {
			_, err := append1(e.Store, mkNotice(e.Tasks[0], 10+i, taskSrc, rev))
			if !errors.Is(err, db.ErrConflict) {
				t.Fatalf("revision %d: %v", rev, err)
			}
		}
		if page, _ := e.Store.ListNotices(ctx, 0, 10); len(page.Notices) != 1 {
			t.Fatalf("%d notices", len(page.Notices))
		}
	})

	t.Run("sources and tasks have separate marks", func(t *testing.T) {
		e := mk(t)
		run := corenotify.Source{Kind: corenotify.SourceVerification, RunID: "run_1"}
		for i, n := range []corenotify.Notice{
			mkNotice(e.Tasks[0], 1, taskSrc, 3), mkNotice(e.Tasks[0], 2, run, 1),
			mkNotice(e.Tasks[1], 3, taskSrc, 1), mkNotice(e.Tasks[0], 4, corenotify.Source{Kind: corenotify.SourceVerification, RunID: "run_2"}, 1),
		} {
			if _, err := append1(e.Store, n); err != nil {
				t.Fatalf("notice %d: %v", i, err)
			}
		}
		hw, err := e.Store.GetNoticeHighWater(ctx)
		if err != nil || len(hw) != 4 || hw[corenotify.Key{Task: e.Tasks[0], Source: taskSrc}] != 3 {
			t.Fatalf("marks %v %v", hw, err)
		}
	})

	t.Run("the mark never moves back and a failed commit changes nothing", func(t *testing.T) {
		e := mk(t)
		k := corenotify.Key{Task: e.Tasks[0], Source: taskSrc}
		advance := func(rev int64, fail bool) (bool, error) {
			var moved bool
			err := e.Store.Commit(ctx, func(tx *db.Tx) error {
				var err error
				moved, err = tx.AdvanceNoticeHighWater(ctx, k, rev, at)
				if err == nil && fail {
					return errInjected
				}
				return err
			})
			return moved, err
		}
		if moved, err := advance(4, false); !moved || err != nil {
			t.Fatalf("%v %v", moved, err)
		}
		if moved, err := advance(2, false); moved || err != nil {
			t.Fatalf("a lower revision moved the mark: %v %v", moved, err)
		}
		if _, err := advance(9, true); !errors.Is(err, errInjected) {
			t.Fatalf("%v", err)
		}
		if hw, _ := e.Store.GetNoticeHighWater(ctx); hw[k] != 4 {
			t.Fatalf("a rolled-back advance changed the mark: %v", hw)
		}
	})

	t.Run("retention keeps the newest and never re-enables a trimmed revision", func(t *testing.T) {
		e := mk(t)
		var first corenotify.Notice
		for i := 1; i <= corenotify.MaxRecent+5; i++ {
			n, err := append1(e.Store, mkNotice(e.Tasks[0], i, taskSrc, int64(i)))
			if err != nil {
				t.Fatal(err)
			}
			if i == 1 {
				first = n
			}
		}
		page, err := e.Store.ListNotices(ctx, 0, corenotify.MaxPage)
		if err != nil || len(page.Notices) != corenotify.MaxRecent {
			t.Fatalf("%d notices, %v", len(page.Notices), err)
		}
		if _, err := append1(e.Store, mkNotice(e.Tasks[0], 9999, taskSrc, 1)); !errors.Is(err, db.ErrConflict) {
			t.Fatalf("a trimmed revision came back: %v", err)
		}
		gap, err := e.Store.ListNotices(ctx, first.Cursor, 10)
		if err != nil || !gap.Gap {
			t.Fatalf("gap %+v %v", gap.Gap, err)
		}
	})

	t.Run("an unknown task is ErrNotFound", func(t *testing.T) {
		e := mk(t)
		if _, err := append1(e.Store, mkNotice("tsk_missing", 1, taskSrc, 1)); !errors.Is(err, db.ErrNotFound) {
			t.Fatalf("%v", err)
		}
	})
}
