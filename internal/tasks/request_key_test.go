package tasks

import (
	"context"
	"errors"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// A keyed submission stores its digest over the stable key: a retry whose
// prompt was rebuilt from newer data returns the first task (also after a
// restart), a different key under the same ID is refused, and LookupRequestKey
// answers before any prompt exists.
func TestSubmitWithRequestKeyedDigestIsStable(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.start()
	ctx := context.Background()
	const id core.RequestID = "req_keyed"
	const key = "standup-polish/v1|prj|1|2|\"a\"|\"\"|req_keyed"

	if _, err := h.svc.LookupRequestKey(ctx, id, key); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("before submit: %v", err)
	}
	req := h.msg("ask", "summary v1", 0)
	req.RequestKey = key
	first, err := h.svc.SubmitWithRequest(ctx, id, req)
	if err != nil {
		t.Fatal(err)
	}

	// The text changed because new commits landed; the key did not.
	req.Prompt = "summary v2 with a new commit"
	again, err := h.svc.SubmitWithRequest(ctx, id, req)
	if err != nil || again.Task.ID != first.Task.ID {
		t.Fatalf("retry with a rebuilt prompt: %+v %v", again.Task.ID, err)
	}
	h.restart()
	got, err := h.svc.LookupRequestKey(ctx, id, key)
	if err != nil || got != first.Task.ID {
		t.Fatalf("lookup after restart: %v %v", got, err)
	}
	again, err = h.svc.SubmitWithRequest(ctx, id, req)
	if err != nil || again.Task.ID != first.Task.ID {
		t.Fatalf("retry after restart: %v %v", again.Task.ID, err)
	}

	// Another request under the same ID is refused, by lookup and by submit.
	if _, err := h.svc.LookupRequestKey(ctx, id, key+"|other"); !errors.Is(err, db.ErrDigestMismatch) {
		t.Fatalf("lookup with another key: %v", err)
	}
	other := req
	other.RequestKey = key + "|other"
	if _, err := h.svc.SubmitWithRequest(ctx, id, other); !errors.Is(err, db.ErrDigestMismatch) {
		t.Fatalf("submit with another key: %v", err)
	}
	// An unkeyed request keeps the full-payload digest: a changed prompt refuses.
	plain := h.msg("ask", "plain v1", 0)
	if _, err := h.svc.SubmitWithRequest(ctx, "req_plain", plain); err != nil {
		t.Fatal(err)
	}
	plain.Prompt = "plain v2"
	if _, err := h.svc.SubmitWithRequest(ctx, "req_plain", plain); !errors.Is(err, db.ErrDigestMismatch) {
		t.Fatalf("unkeyed changed prompt: %v", err)
	}
	// A keyed request cannot reuse an unkeyed one's ID (and vice versa).
	keyed := h.msg("ask", "plain v1", 0)
	keyed.RequestKey = key
	if _, err := h.svc.SubmitWithRequest(ctx, "req_plain", keyed); !errors.Is(err, db.ErrDigestMismatch) {
		t.Fatalf("keyed over unkeyed: %v", err)
	}
	if n := len(h.listTasks(t)); n != 2 {
		t.Fatalf("tasks = %d, want 2", n)
	}
}

func (h *harness) listTasks(t *testing.T) []core.Task {
	t.Helper()
	list, err := h.store.ListTasks(context.Background(), h.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	return list
}
