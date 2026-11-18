package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corenotify "github.com/brendatamaar/yggdrasil/internal/core/notify"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
	"github.com/brendatamaar/yggdrasil/internal/notify"
)

// Notifications end to end through the real feeder, producer, store and HTTP
// handler (UI-06, docs/AGENTS.md 5.9): only the task name and a status leave the
// server; history pages with a gap; a replayed announcement makes no second
// notice.

const (
	secretPrompt = "SECRET_PROMPT_acme_pricing_formula"
	secretFile   = "SECRET_FILE_LINE_do_not_leak"
)

func (r *p6Rig) announce(job func(ctx context.Context, p *notify.Producer) error) {
	r.a.phase6.feeder.enqueue(job)
}

func (r *p6Rig) notices(t *testing.T, query string) (api.NoticesResponse, []byte) {
	t.Helper()
	code, data := apiDo(t, http.MethodGet, r.base+"/api/notifications"+query, nil, appClient)
	if code != 200 {
		t.Fatalf("GET /api/notifications%s = %d %s", query, code, data)
	}
	var out api.NoticesResponse
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out, data
}

func (r *p6Rig) waitNotices(t *testing.T, want int) api.NoticesResponse {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		got, _ := r.notices(t, "")
		if len(got.Notices) >= want {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d notices, want %d", len(got.Notices), want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestPhase6NoticesCarryOnlyTheTaskNameAndStatus(t *testing.T) {
	r := newP6Rig(t)
	p := r.newRepo(t)
	if err := os.WriteFile(filepath.Join(p.dir, "pricing.go"), []byte("// "+secretFile+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.refresh(t, p)
	mk := func(title string, status core.TaskStatus) core.Task {
		return dbtest.Task(t, r.a.store, core.Task{ProjectID: p.project, Scope: core.ScopeRepo, RepoIDs: []core.RepoID{p.id}, Mode: core.ModeAsk,
			Status: status, Title: title, Prompt: "Look at " + filepath.Join(p.dir, "pricing.go") + ": " + secretPrompt})
	}
	review, failed, done, needs := mk("Fix the cart total", core.TaskReview), mk("Update the readme", core.TaskFailed), mk("Tidy the logs", core.TaskDone), mk("Pick a currency", core.TaskNeedsYou)
	verify := mk("Verify the cart", core.TaskReview)
	r.announce(func(ctx context.Context, pr *notify.Producer) error {
		return pr.TaskStatus(ctx, review.ID, review.Title, core.TaskReview, 5)
	})
	r.announce(func(ctx context.Context, pr *notify.Producer) error {
		return pr.TaskStatus(ctx, failed.ID, failed.Title, core.TaskFailed, 5)
	})
	r.announce(func(ctx context.Context, pr *notify.Producer) error {
		return pr.TaskStatus(ctx, done.ID, done.Title, core.TaskDone, 5)
	})
	r.announce(func(ctx context.Context, pr *notify.Producer) error {
		return pr.TaskStatus(ctx, needs.ID, needs.Title, core.TaskNeedsYou, 5)
	})
	r.announce(func(ctx context.Context, pr *notify.Producer) error {
		return pr.VerificationResult(ctx, verify.ID, verify.Title, "run_1", corenotify.VerifyPassed, 1)
	})
	r.announce(func(ctx context.Context, pr *notify.Producer) error {
		return pr.VerificationResult(ctx, verify.ID, verify.Title, "run_2", corenotify.VerifyFailed, 2)
	})
	got := r.waitNotices(t, 6)
	_, raw := r.notices(t, "")

	// Only the allowed fields exist on the wire.
	var generic struct {
		Notices []map[string]any `json:"notices"`
	}
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"id": true, "taskId": true, "taskTitle": true, "status": true, "revision": true, "createdAt": true, "cursor": true}
	for _, n := range generic.Notices {
		for k := range n {
			if !allowed[k] {
				t.Errorf("a notice carries the field %q", k)
			}
		}
	}
	// None of the private text reaches a notice.
	for _, bad := range []string{secretPrompt, secretFile, "pricing.go", p.dir, p.bare, filepath.Base(p.dir), r.cfg.dataDir, "SELECT", "diff --git"} {
		if bad != "" && strings.Contains(string(raw), bad) {
			t.Errorf("the notice history contains %q: %s", bad, raw)
		}
	}
	// Status and title are exactly the task's name and the status.
	statuses := map[api.NoticeStatus][]string{}
	for _, n := range got.Notices {
		statuses[n.Status] = append(statuses[n.Status], n.TaskTitle)
	}
	for status, title := range map[api.NoticeStatus]string{
		api.NoticeReadyForReview: "Fix the cart total", api.NoticeFailed: "Update the readme", api.NoticeDone: "Tidy the logs", api.NoticeNeedsYou: "Pick a currency",
		api.NoticeVerificationPassed: "Verify the cart", api.NoticeVerificationFailed: "Verify the cart",
	} {
		found := false
		for _, got := range statuses[status] {
			found = found || got == title
		}
		if !found {
			t.Errorf("no %s notice titled %q: %v", status, title, statuses)
		}
	}
}

// A task title is the only free text of a notice, and it is bounded.
func TestPhase6NoticeTitleIsBounded(t *testing.T) {
	r := newP6Rig(t)
	p := r.newRepo(t)
	task := dbtest.Task(t, r.a.store, core.Task{ProjectID: p.project, Scope: core.ScopeRepo, RepoIDs: []core.RepoID{p.id}, Mode: core.ModeAsk, Status: core.TaskReview, Title: "T"})
	long := strings.Repeat("é", 300)
	r.announce(func(ctx context.Context, pr *notify.Producer) error {
		return pr.TaskStatus(ctx, task.ID, long, core.TaskReview, 3)
	})
	got := r.waitNotices(t, 1)
	if n := got.Notices[0].TaskTitle; len(n) > corenotify.MaxTitle || !strings.HasPrefix(long, n) {
		t.Fatalf("the title has %d bytes, want a clipped prefix of at most %d", len(n), corenotify.MaxTitle)
	}
}

// A client that reconnects asks for what came after its cursor: it gets the
// newer notices oldest first, and nothing twice. A replayed announcement (the
// same task, status and revision) makes no second notice, and an older revision
// never comes back after a newer one.
func TestPhase6NoticesDedupAfterReconnect(t *testing.T) {
	r := newP6Rig(t)
	p := r.newRepo(t)
	task := dbtest.Task(t, r.a.store, core.Task{ProjectID: p.project, Scope: core.ScopeRepo, RepoIDs: []core.RepoID{p.id}, Mode: core.ModeAsk, Status: core.TaskReview, Title: "Fix the cart total"})
	send := func(status core.TaskStatus, rev int64) {
		r.announce(func(ctx context.Context, pr *notify.Producer) error {
			return pr.TaskStatus(ctx, task.ID, task.Title, status, rev)
		})
	}
	send(core.TaskReview, 3)
	first := r.waitNotices(t, 1)
	cursor := first.Newest

	// The same announcement again, and an older one: nothing new.
	send(core.TaskReview, 3)
	send(core.TaskReview, 2)
	// A newer revision with a new status is a new notice.
	send(core.TaskDone, 4)
	got := r.waitNotices(t, 2)
	if len(got.Notices) != 2 {
		t.Fatalf("%d notices after replays, want 2: %+v", len(got.Notices), got.Notices)
	}
	// Reconnect with the held cursor: exactly the newer notice, once.
	after, _ := r.notices(t, fmt.Sprintf("?after=%d", cursor))
	if len(after.Notices) != 1 || after.Notices[0].Status != api.NoticeDone || after.Gap {
		t.Fatalf("after %d: %+v", cursor, after)
	}
	// A client that is up to date gets an empty page.
	if upToDate, _ := r.notices(t, fmt.Sprintf("?after=%d", after.Newest)); len(upToDate.Notices) != 0 {
		t.Fatalf("an up to date client got %+v", upToDate.Notices)
	}
	// Restart-like replay: the same transitions handed over again change nothing.
	send(core.TaskReview, 3)
	send(core.TaskDone, 4)
	time.Sleep(300 * time.Millisecond)
	if again, _ := r.notices(t, ""); len(again.Notices) != 2 {
		t.Fatalf("%d notices after a replay, want 2", len(again.Notices))
	}
}

// History keeps the newest MaxRecent notices. A client whose cursor is older
// than the oldest kept notice is told so with Gap and must not replay toasts.
func TestPhase6NoticeHistoryReportsAGap(t *testing.T) {
	r := newP6Rig(t)
	p := r.newRepo(t)
	task := dbtest.Task(t, r.a.store, core.Task{ProjectID: p.project, Scope: core.ScopeRepo, RepoIDs: []core.RepoID{p.id}, Mode: core.ModeAsk, Status: core.TaskReview, Title: "Fix the cart total"})
	total := corenotify.MaxRecent + 25
	for i := 1; i <= total; i++ {
		rev := int64(i)
		status := core.TaskReview
		if i%2 == 0 {
			status = core.TaskNeedsYou
		}
		r.announce(func(ctx context.Context, pr *notify.Producer) error {
			return pr.TaskStatus(ctx, task.ID, task.Title, status, rev)
		})
	}
	deadline := time.Now().Add(30 * time.Second)
	var head api.NoticesResponse
	for {
		head, _ = r.notices(t, "")
		if head.Newest >= int64(total) || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if head.Newest < int64(total) {
		t.Fatalf("newest cursor %d, want %d", head.Newest, total)
	}
	if len(head.Notices) > corenotify.MaxPage || head.Notices[0].Cursor != head.Newest {
		t.Fatalf("the newest page is %d notices starting at %d", len(head.Notices), head.Notices[0].Cursor)
	}
	stale, _ := r.notices(t, "?after=1&limit=10")
	if !stale.Gap {
		t.Fatalf("a cursor older than the kept history had no gap: oldest %d, %+v", stale.Oldest, stale)
	}
	fresh, _ := r.notices(t, fmt.Sprintf("?after=%d", head.Newest-5))
	if fresh.Gap || len(fresh.Notices) != 5 || fresh.Notices[0].Cursor >= fresh.Notices[4].Cursor {
		t.Fatalf("a recent cursor gave gap %v and %d notices (oldest first expected)", fresh.Gap, len(fresh.Notices))
	}
	// Limits are refused at the boundary.
	for _, q := range []string{"?limit=0", "?limit=201", "?after=-1", "?after=x"} {
		if code, _ := apiDo(t, http.MethodGet, r.base+"/api/notifications"+q, nil, appClient); code != http.StatusBadRequest {
			t.Errorf("GET /api/notifications%s = %d, want 400", q, code)
		}
	}
}
