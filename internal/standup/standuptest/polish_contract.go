package standuptest

import (
	"context"
	"errors"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/standup"
)

// PolishRig is the subject of PolisherContract: a standup.Polisher and the means
// to look at what it stored.
type PolishRig struct {
	Polisher standup.Polisher
	// Project is a tracked project with at least one ready repo; Unknown is not.
	Project core.ProjectID
	Unknown core.ProjectID
	// Tasks counts the tasks the polisher made.
	Tasks func() int
	// Mode is the mode of a task it made (nil when the fake cannot tell).
	Mode func(ctx context.Context, id core.TaskID) (core.TaskMode, error)
}

// PolisherContract states what every standup.Polisher promises (AU-02):
//
//   - FindPolish of an unused request ID is "none yet" and creates nothing;
//   - SubmitPolish makes one task; a repeat with the same request ID and key
//     returns the same task whatever the rebuilt text (a lost reply or a restart
//     never makes a second task), and FindPolish then finds it;
//   - the same request ID with another key is ErrRequestReused, from FindPolish
//     and from SubmitPolish, and makes nothing;
//   - another request ID makes another task;
//   - an unknown project is db.ErrNotFound and makes nothing;
//   - the task is an Ask task (the standup text is never a way to write);
//   - a cancelled context returns an error.
func PolisherContract(t *testing.T, mk func(t *testing.T) PolishRig) {
	ctx := context.Background()
	const key, otherKey = "key-one", "key-two"

	t.Run("find before submit is none and creates nothing", func(t *testing.T) {
		r := mk(t)
		id, found, err := r.Polisher.FindPolish(ctx, "req_a", key)
		if err != nil || found || id != "" || r.Tasks() != 0 {
			t.Fatalf("FindPolish = %q, %v, %v with %d tasks; want none", id, found, err, r.Tasks())
		}
	})

	t.Run("a request is bound to its first task", func(t *testing.T) {
		r := mk(t)
		first, err := r.Polisher.SubmitPolish(ctx, "req_a", key, r.Project, "text v1", "mock", "")
		if err != nil || first == "" {
			t.Fatalf("submit: %q, %v", first, err)
		}
		again, err := r.Polisher.SubmitPolish(ctx, "req_a", key, r.Project, "text v2 rebuilt from newer data", "mock", "")
		if err != nil || again != first || r.Tasks() != 1 {
			t.Fatalf("repeat = %q, %v with %d tasks; want %q and one task", again, err, r.Tasks(), first)
		}
		found, ok, err := r.Polisher.FindPolish(ctx, "req_a", key)
		if err != nil || !ok || found != first {
			t.Fatalf("FindPolish = %q, %v, %v; want %q", found, ok, err, first)
		}
		if r.Mode != nil {
			if m, err := r.Mode(ctx, first); err != nil || m != core.ModeAsk {
				t.Fatalf("the polish task mode = %q, %v; want ask", m, err)
			}
		}
	})

	t.Run("a reused request ID is refused", func(t *testing.T) {
		r := mk(t)
		if _, err := r.Polisher.SubmitPolish(ctx, "req_a", key, r.Project, "text", "mock", ""); err != nil {
			t.Fatal(err)
		}
		if _, _, err := r.Polisher.FindPolish(ctx, "req_a", otherKey); !errors.Is(err, standup.ErrRequestReused) {
			t.Fatalf("FindPolish with another key = %v, want ErrRequestReused", err)
		}
		if _, err := r.Polisher.SubmitPolish(ctx, "req_a", otherKey, r.Project, "text", "mock", ""); !errors.Is(err, standup.ErrRequestReused) {
			t.Fatalf("SubmitPolish with another key = %v, want ErrRequestReused", err)
		}
		if r.Tasks() != 1 {
			t.Fatalf("%d tasks, want 1", r.Tasks())
		}
	})

	t.Run("another request ID is another task", func(t *testing.T) {
		r := mk(t)
		a, err := r.Polisher.SubmitPolish(ctx, "req_a", key, r.Project, "text", "mock", "")
		if err != nil {
			t.Fatal(err)
		}
		b, err := r.Polisher.SubmitPolish(ctx, "req_b", key, r.Project, "text", "mock", "")
		if err != nil || a == b || r.Tasks() != 2 {
			t.Fatalf("a %q, b %q, %v, %d tasks; want two tasks", a, b, err, r.Tasks())
		}
	})

	t.Run("unknown project", func(t *testing.T) {
		r := mk(t)
		if id, err := r.Polisher.SubmitPolish(ctx, "req_a", key, r.Unknown, "text", "mock", ""); !errors.Is(err, db.ErrNotFound) || id != "" {
			t.Fatalf("SubmitPolish = %q, %v; want ErrNotFound", id, err)
		}
		if r.Tasks() != 0 {
			t.Fatalf("%d tasks after a refusal", r.Tasks())
		}
	})

	t.Run("cancelled context", func(t *testing.T) {
		r := mk(t)
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		if id, err := r.Polisher.SubmitPolish(cctx, "req_a", key, r.Project, "text", "mock", ""); err == nil {
			t.Fatalf("a cancelled context made task %q", id)
		}
		if r.Tasks() != 0 {
			t.Fatalf("%d tasks after a cancelled call", r.Tasks())
		}
	})
}
