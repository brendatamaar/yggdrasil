package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Task creation checks tracking inside the write transaction (invariant 6):
// an untracked repo or project is refused and nothing is written.
func TestCreateTaskRefusesUntracked(t *testing.T) {
	d := openTest(t)
	s := NewStore(d, nil)
	ctx := context.Background()
	p, r := seed(t, s)
	untrack := func(table string, id string) {
		t.Helper()
		if _, err := d.write.ExecContext(ctx, `UPDATE `+table+` SET untracked_at = '2026-02-01T00:00:00.000Z' WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
	}
	count := func() (n int) {
		t.Helper()
		if err := d.write.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM tasks) + (SELECT count(*) FROM task_repos)`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	at := time.Unix(10, 0)
	create := func(id core.TaskID) error {
		tk := newTask(p, r, nil)
		tk.ID, tk.CreatedAt, tk.UpdatedAt = id, at, at
		return s.Commit(ctx, func(tx *Tx) error { _, err := tx.CreateTask(ctx, tk); return err })
	}

	untrack("repos", string(r.ID))
	if err := create("t-a"); !errors.Is(err, ErrNotFound) {
		t.Errorf("task in an untracked repo = %v, want ErrNotFound", err)
	}
	if n := count(); n != 0 {
		t.Errorf("rows written for the refused task: %d", n)
	}
	if _, err := d.write.ExecContext(ctx, `UPDATE repos SET untracked_at = NULL`); err != nil {
		t.Fatal(err)
	}
	if err := create("t-ok"); err != nil {
		t.Fatalf("task in a tracked repo: %v", err)
	}
	untrack("projects", string(p.ID))
	before := count()
	if err := create("t-b"); !errors.Is(err, ErrNotFound) {
		t.Errorf("task in an untracked project = %v, want ErrNotFound", err)
	}
	if count() != before {
		t.Error("rows written for the refused task")
	}
}
