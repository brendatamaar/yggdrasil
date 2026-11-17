package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corenotify "github.com/brendatamaar/yggdrasil/internal/core/notify"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

func TestTransitionCommitsPublishesAndAudits(t *testing.T) {
	h := newHarness(t, 1)
	if err := h.send(0, core.TaskNeedsYou, 2); err != nil {
		t.Fatal(err)
	}
	hist := h.history()
	if len(hist) != 1 || hist[0].Status != corenotify.NeedsYou || hist[0].TaskTitle != "Fix the build" || hist[0].SourceRevision != 2 {
		t.Fatalf("history %+v", hist)
	}
	msgs := h.pub.messages()
	if len(msgs) != 1 || msgs[0].NoticeID != hist[0].ID || msgs[0].Cursor != hist[0].Cursor || msgs[0].Cursor == 0 {
		t.Fatalf("published %+v for %+v", msgs, hist)
	}
	entries, err := h.store.ListAudit(context.Background(), "task:"+string(h.task(0)), 10)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range entries {
		if e.Action != "notice.create" {
			continue
		}
		found = true
		var detail map[string]any
		if err := json.Unmarshal(e.Detail, &detail); err != nil {
			t.Fatal(err)
		}
		if len(detail) != 4 || strings.Contains(string(e.Detail), "Fix the build") {
			t.Errorf("audit detail %s carries more than ids, status and revision", e.Detail)
		}
	}
	if !found {
		t.Error("no audit entry for the notice (invariant 8)")
	}
}

func TestVerificationNotices(t *testing.T) {
	h := newHarness(t, 1)
	ctx := context.Background()
	steps := []struct {
		run string
		res corenotify.VerifyResult
		rev int64
	}{
		{"run_1", corenotify.VerifyInProgress, 1},
		{"run_1", corenotify.VerifyFailed, 2},
		{"run_1", corenotify.VerifyPassed, 1}, // older: ignored
		{"run_2", corenotify.VerifyPassed, 1},
	}
	for _, s := range steps {
		if err := h.prod.VerificationResult(ctx, h.task(0), "Fix the build", s.run, s.res, s.rev); err != nil {
			t.Fatal(err)
		}
	}
	hist := h.history() // newest first
	if len(hist) != 2 || hist[0].Status != corenotify.VerificationPassed || hist[1].Status != corenotify.VerificationFailed {
		t.Fatalf("history %+v", hist)
	}
}

func TestDuplicateAndOlderRevisionsAreNoOps(t *testing.T) {
	h := newHarness(t, 1)
	if err := h.send(0, core.TaskDone, 5); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		st  core.TaskStatus
		rev int64
	}{{core.TaskDone, 5}, {core.TaskFailed, 4}, {core.TaskRunning, 3}} {
		if err := h.send(0, c.st, c.rev); err != nil {
			t.Fatalf("a stale transition is a no-op, not an error: %v", err)
		}
	}
	if len(h.history()) != 1 || len(h.pub.messages()) != 1 {
		t.Fatalf("history %d, published %d", len(h.history()), len(h.pub.messages()))
	}
}

func TestUninterestingStatusRaisesMarkOnly(t *testing.T) {
	h := newHarness(t, 1)
	if err := h.send(0, core.TaskRunning, 3); err != nil {
		t.Fatal(err)
	}
	if len(h.history()) != 0 || len(h.pub.messages()) != 0 {
		t.Fatal("a running status made a notice")
	}
	hw, err := h.store.GetNoticeHighWater(context.Background())
	if err != nil || hw[corenotify.Key{Task: h.task(0), Source: corenotify.Source{Kind: corenotify.SourceTask}}] != 3 {
		t.Fatalf("high water %v, %v", hw, err)
	}
	// The older done of revision 2 cannot notify after revision 3 was seen.
	if err := h.send(0, core.TaskDone, 2); err != nil || len(h.history()) != 0 {
		t.Fatalf("an older transition notified: %v", err)
	}
}

// TestNoticeCommitBeforePublish: when the hub is called the notice, its mark
// and its audit entry are already visible to a separate read.
func TestNoticeCommitBeforePublish(t *testing.T) {
	h := newHarness(t, 1)
	var checked bool
	h.pub.onPublish = func(m api.NoticeChangedMessage) {
		page, err := h.store.ListNotices(context.Background(), 0, 10)
		if err != nil || len(page.Notices) != 1 || page.Notices[0].ID != m.NoticeID || page.Notices[0].Cursor != m.Cursor {
			t.Errorf("publish before commit: %+v %v", page, err)
		}
		hw, _ := h.store.GetNoticeHighWater(context.Background())
		if len(hw) != 1 {
			t.Errorf("high water not committed at publish: %v", hw)
		}
		checked = true
	}
	if err := h.send(0, core.TaskDone, 1); err != nil {
		t.Fatal(err)
	}
	if !checked {
		t.Fatal("nothing was published")
	}
}

func TestCommitFailureWritesAndPublishesNothingThenRetrySucceeds(t *testing.T) {
	for name, script := range map[string]func(*flakyStore){
		"fails before running": func(s *flakyStore) { s.script(1, 0) },
		"rolled back after fn": func(s *flakyStore) { s.script(0, 1) },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, 1)
			script(h.store)
			err := h.send(0, core.TaskFailed, 4)
			if !errors.Is(err, errInjected) {
				t.Fatalf("error %v", err)
			}
			hw, _ := h.store.GetNoticeHighWater(context.Background())
			if len(h.history()) != 0 || len(h.pub.messages()) != 0 || len(hw) != 0 {
				t.Fatalf("a failed commit left history %d, published %d, marks %v", len(h.history()), len(h.pub.messages()), hw)
			}
			entries, _ := h.store.ListAudit(context.Background(), "task:"+string(h.task(0)), 10)
			for _, e := range entries {
				if e.Action == "notice.create" {
					t.Fatal("the audit entry outlived the rolled-back notice")
				}
			}
			// The same revision is retried: the state did not advance, so it notifies now.
			if err := h.send(0, core.TaskFailed, 4); err != nil {
				t.Fatal(err)
			}
			if len(h.history()) != 1 || len(h.pub.messages()) != 1 {
				t.Fatalf("retry: history %d, published %d", len(h.history()), len(h.pub.messages()))
			}
		})
	}
}

// A hub that loses the event after a committed notice: replaying the same
// transition creates no second notice and publishes nothing; the client finds
// the notice through the history query.
func TestLostPublishDoesNotDuplicateOnRetry(t *testing.T) {
	h := newHarness(t, 1)
	h.pub.setDrop(true)
	if err := h.send(0, core.TaskDone, 1); err != nil {
		t.Fatal(err)
	}
	h.pub.setDrop(false)
	if err := h.send(0, core.TaskDone, 1); err != nil {
		t.Fatal(err)
	}
	h.start() // and again after a restart
	if err := h.send(0, core.TaskDone, 1); err != nil {
		t.Fatal(err)
	}
	if len(h.history()) != 1 || len(h.pub.messages()) != 0 {
		t.Fatalf("history %d, published %d", len(h.history()), len(h.pub.messages()))
	}
}

// Another writer raised the mark after this producer recovered: the commit
// conflicts, which is a no-op (nothing written, nothing published, no error).
func TestConflictFromAnotherWriterIsNoOp(t *testing.T) {
	h := newHarness(t, 1)
	k := corenotify.Key{Task: h.task(0), Source: corenotify.Source{Kind: corenotify.SourceTask}}
	if err := h.store.Commit(context.Background(), func(tx *db.Tx) error {
		_, err := tx.AdvanceNoticeHighWater(context.Background(), k, 9, h.deps.Clock())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.send(0, core.TaskDone, 4); err != nil {
		t.Fatalf("a conflict must not be a failure: %v", err)
	}
	if len(h.history()) != 0 || len(h.pub.messages()) != 0 {
		t.Fatal("a conflicting revision produced a notice")
	}
	// The producer adopted the mark: revision 9 and below stay quiet, 10 notifies.
	if err := h.send(0, core.TaskDone, 9); err != nil || len(h.history()) != 0 {
		t.Fatalf("%v", err)
	}
	if err := h.send(0, core.TaskDone, 10); err != nil || len(h.history()) != 1 {
		t.Fatalf("%v", err)
	}
}

func TestRestartRecoversAndReplaysProduceNothing(t *testing.T) {
	h := newHarness(t, 2)
	var all [][2]int64
	for i := 0; i < 2; i++ {
		for rev := int64(1); rev <= 4; rev++ {
			st := []core.TaskStatus{core.TaskRunning, core.TaskNeedsYou, core.TaskRunning, core.TaskDone}[rev-1]
			if err := h.send(i, st, rev); err != nil {
				t.Fatal(err)
			}
			all = append(all, [2]int64{int64(i), rev})
		}
	}
	before := len(h.history())
	if before != 4 {
		t.Fatalf("%d notices", before)
	}
	_ = h.prod.Close()
	h.start()
	for _, a := range all {
		st := []core.TaskStatus{core.TaskRunning, core.TaskNeedsYou, core.TaskRunning, core.TaskDone}[a[1]-1]
		if err := h.send(int(a[0]), st, a[1]); err != nil {
			t.Fatal(err)
		}
	}
	if len(h.history()) != before || len(h.pub.messages()) != before {
		t.Fatalf("replay after restart: history %d, published %d", len(h.history()), len(h.pub.messages()))
	}
}

func TestInvalidInputIsRejectedAtTheBoundary(t *testing.T) {
	h := newHarness(t, 1)
	ctx := context.Background()
	good := corenotify.TaskTransition{Task: h.task(0), Title: "t", Status: core.TaskDone, Revision: 1}
	cases := map[string]func(*corenotify.TaskTransition){
		"no task":     func(x *corenotify.TaskTransition) { x.Task = "" },
		"bad status":  func(x *corenotify.TaskTransition) { x.Status = "wow" },
		"revision 0":  func(x *corenotify.TaskTransition) { x.Revision = 0 },
		"revision <0": func(x *corenotify.TaskTransition) { x.Revision = -3 },
		"blank title": func(x *corenotify.TaskTransition) { x.Title = " \t" },
	}
	for name, f := range cases {
		x := good
		f(&x)
		if err := h.prod.TaskTransition(ctx, x); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	vgood := corenotify.VerificationTransition{Task: h.task(0), Title: "t", RunID: "r", Result: corenotify.VerifyFailed, Revision: 1}
	vcases := map[string]func(*corenotify.VerificationTransition){
		"no run":     func(x *corenotify.VerificationTransition) { x.RunID = "" },
		"long run":   func(x *corenotify.VerificationTransition) { x.RunID = strings.Repeat("r", 101) },
		"bad result": func(x *corenotify.VerificationTransition) { x.Result = "meh" },
		"revision 0": func(x *corenotify.VerificationTransition) { x.Revision = 0 },
		"no title":   func(x *corenotify.VerificationTransition) { x.Title = "" },
		"no task":    func(x *corenotify.VerificationTransition) { x.Task = "" },
	}
	for name, f := range vcases {
		x := vgood
		f(&x)
		if err := h.prod.VerificationTransition(ctx, x); !errors.Is(err, ErrInvalid) {
			t.Errorf("verification %s: %v", name, err)
		}
	}
	if len(h.history()) != 0 || len(h.pub.messages()) != 0 {
		t.Fatal("an invalid input had an effect")
	}
	// Accepted implies valid: a passing input produced a valid notice.
	if err := h.prod.TaskTransition(ctx, good); err != nil {
		t.Fatal(err)
	}
	if n := h.history()[0]; n.Validate() != nil {
		t.Fatalf("accepted input made an invalid notice: %v", n.Validate())
	}
}

func TestUnknownTaskIsRejected(t *testing.T) {
	h := newHarness(t, 1)
	err := h.prod.TaskStatus(context.Background(), "tsk_missing", "t", core.TaskDone, 1)
	if !errors.Is(err, ErrUnknownTask) {
		t.Fatalf("error %v", err)
	}
	err = h.prod.TaskStatus(context.Background(), "tsk_missing", "t", core.TaskRunning, 1)
	if !errors.Is(err, ErrUnknownTask) {
		t.Fatalf("high-water path: %v", err)
	}
	if len(h.pub.messages()) != 0 {
		t.Fatal("published for an unknown task")
	}
}

// A title is the only free text of a notice, and it is redacted: a token pasted
// into a task title does not reach the history or the audit log.
func TestTitleIsRedacted(t *testing.T) {
	h := newHarness(t, 1)
	secret := "ghp_" + strings.Repeat("a1B2", 10)
	if err := h.prod.TaskStatus(context.Background(), h.task(0), "deploy with "+secret, core.TaskDone, 1); err != nil {
		t.Fatal(err)
	}
	if got := h.history()[0].TaskTitle; strings.Contains(got, secret) {
		t.Fatalf("title %q holds the token", got)
	}
}

// Random statuses of both sources: everything stored is the title, a status of
// the closed set and ids, and the audit detail never holds the title. The
// core test pins the Notice fields.
func TestPropNoticeNeverContainsContent(t *testing.T) {
	h := newHarness(t, 1)
	r := rand.New(rand.NewSource(3))
	for i := 1; i <= 60; i++ {
		st := []core.TaskStatus{core.TaskNeedsYou, core.TaskReview, core.TaskDone, core.TaskFailed, core.TaskCancelled}[r.Intn(5)]
		var err error
		if r.Intn(2) == 0 {
			err = h.prod.TaskStatus(context.Background(), h.task(0), fmt.Sprintf("Task %d", i), st, int64(i))
		} else {
			err = h.prod.VerificationResult(context.Background(), h.task(0), fmt.Sprintf("Task %d", i), fmt.Sprintf("run-%d", i), corenotify.VerifyFailed, 1)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range h.history() {
		if !strings.HasPrefix(n.TaskTitle, "Task ") || n.Status.Validate() != nil || n.Validate() != nil {
			t.Fatalf("notice %+v", n)
		}
	}
	entries, _ := h.store.ListAudit(context.Background(), "task:"+string(h.task(0)), 500)
	for _, e := range entries {
		if e.Action == "notice.create" && strings.Contains(string(e.Detail), "Task ") {
			t.Fatalf("audit detail holds the title: %s", e.Detail)
		}
	}
}

func TestHistoryBoundedAndNoReplayAfterTrim(t *testing.T) {
	h := newHarness(t, 1)
	n := corenotify.MaxRecent + 30
	for i := 1; i <= n; i++ {
		if err := h.send(0, core.TaskDone, int64(i)); err != nil {
			t.Fatal(err)
		}
	}
	page, err := h.prod.ListNotices(context.Background(), 0, 500)
	if err != nil || len(page.Notices) != corenotify.MaxRecent {
		t.Fatalf("%d notices, %v", len(page.Notices), err)
	}
	// Revision 1 was trimmed from history; replaying it makes nothing, also after a restart.
	h.start()
	if err := h.send(0, core.TaskDone, 1); err != nil {
		t.Fatal(err)
	}
	if page, _ = h.prod.ListNotices(context.Background(), 0, 500); len(page.Notices) != corenotify.MaxRecent || len(h.pub.messages()) != n {
		t.Fatalf("trimmed notice came back: %d notices, %d published", len(page.Notices), len(h.pub.messages()))
	}
}

func TestListNoticesGapAndBounds(t *testing.T) {
	h := newHarness(t, 1)
	for i := 1; i <= corenotify.MaxRecent+10; i++ {
		if err := h.send(0, core.TaskDone, int64(i)); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	if p, err := h.prod.ListNotices(ctx, 1, 20); err != nil || !p.Gap {
		t.Fatalf("a client at cursor 1 missed trimmed notices: %+v %v", p.Gap, err)
	}
	newest := h.pub.messages()[len(h.pub.messages())-1].Cursor
	if p, err := h.prod.ListNotices(ctx, newest, 20); err != nil || p.Gap || len(p.Notices) != 0 {
		t.Fatalf("up to date: %+v %v", p, err)
	}
	if p, err := h.prod.ListNotices(ctx, newest-3, 20); err != nil || p.Gap || len(p.Notices) != 3 || p.Notices[0].Cursor != newest-2 {
		t.Fatalf("catch up: %d notices, gap %v, %v", len(p.Notices), p.Gap, err)
	}
	if p, err := h.prod.ListNotices(ctx, 0, 100000); err != nil || len(p.Notices) != corenotify.MaxPage {
		t.Fatalf("limit not clamped: %d %v", len(p.Notices), err)
	}
	if _, err := h.prod.ListNotices(ctx, -1, 10); !errors.Is(err, ErrInvalid) {
		t.Fatalf("negative cursor: %v", err)
	}
}

func TestCloseIsIdempotentAndStopsTheProducer(t *testing.T) {
	h := newHarness(t, 1)
	if err := h.prod.Close(); err != nil {
		t.Fatal(err)
	}
	if err := h.prod.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.prod.done:
	default:
		t.Fatal("the owner goroutine is still running after Close")
	}
	if err := h.send(0, core.TaskDone, 1); !errors.Is(err, ErrClosed) {
		t.Fatalf("after Close: %v", err)
	}
}

func TestParentContextCancellationStopsTheProducer(t *testing.T) {
	h := newHarness(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	p, err := New(ctx, h.deps)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	<-p.done
	if err := p.TaskStatus(context.Background(), h.task(0), "t", core.TaskDone, 1); !errors.Is(err, ErrClosed) {
		t.Fatalf("after cancel: %v", err)
	}
	_ = p.Close()
}

func TestCallerContextCancellationReturns(t *testing.T) {
	h := newHarness(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := h.prod.TaskStatus(ctx, h.task(0), "t", core.TaskDone, 1); err == nil {
		// The loop may have accepted it before noticing the cancel; either is fine,
		// but it must not hang and must not duplicate later.
		t.Log("accepted before the cancel was seen")
	}
}

// Many goroutines report the same and different revisions: each (task, revision)
// notifies exactly once. Run with -race.
func TestConcurrentTransitionsNotifyOnce(t *testing.T) {
	h := newHarness(t, 3)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			r := rand.New(rand.NewSource(int64(g)))
			for i := 0; i < 15; i++ {
				_ = h.send(r.Intn(3), core.TaskDone, int64(1+r.Intn(6)))
			}
		}(g)
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, n := range h.history() {
		k := fmt.Sprintf("%s/%d", n.Task, n.SourceRevision)
		if seen[k] {
			t.Fatalf("duplicate notice %s", k)
		}
		seen[k] = true
	}
	// A task's accepted revisions strictly increase with the cursor.
	last := map[core.TaskID]int64{}
	page, _ := h.prod.ListNotices(context.Background(), 0, corenotify.MaxPage)
	for i := len(page.Notices) - 1; i >= 0; i-- { // oldest first
		n := page.Notices[i]
		if n.SourceRevision <= last[n.Task] {
			t.Fatalf("an older revision was stored after a newer one for %s", n.Task)
		}
		last[n.Task] = n.SourceRevision
	}
	if len(h.pub.messages()) != len(page.Notices) {
		t.Fatalf("published %d for %d notices", len(h.pub.messages()), len(page.Notices))
	}
}

func TestNewRequiresPorts(t *testing.T) {
	if _, err := New(context.Background(), Deps{}); err == nil {
		t.Fatal("New accepted empty deps")
	}
}

func FuzzParseTransitions(f *testing.F) {
	f.Add("tsk_1", "title", "done", int64(1), "run", "failed")
	f.Add("", "", "", int64(-1), "", "")
	f.Fuzz(func(t *testing.T, task, title, status string, rev int64, run, result string) {
		if err := parseTask(corenotify.TaskTransition{Task: core.TaskID(task), Title: title, Status: core.TaskStatus(status), Revision: rev}); err == nil {
			// accepted implies valid
			x := corenotify.TaskTransition{Task: core.TaskID(task), Status: core.TaskStatus(status), Revision: rev}
			if x.Task.Validate() != nil || x.Status.Validate() != nil || rev <= 0 || strings.TrimSpace(title) == "" {
				t.Fatalf("accepted %q %q %q %d", task, title, status, rev)
			}
		} else if !errors.Is(err, ErrInvalid) {
			t.Fatalf("rejection is not ErrInvalid: %v", err)
		}
		v := corenotify.VerificationTransition{Task: core.TaskID(task), Title: title, RunID: run, Result: corenotify.VerifyResult(result), Revision: rev}
		if err := parseVerification(v); err == nil {
			if (corenotify.Source{Kind: corenotify.SourceVerification, RunID: run}).Validate() != nil || rev <= 0 || v.Task.Validate() != nil {
				t.Fatalf("accepted verification %+v", v)
			}
		} else if !errors.Is(err, ErrInvalid) {
			t.Fatalf("rejection is not ErrInvalid: %v", err)
		}
	})
}
