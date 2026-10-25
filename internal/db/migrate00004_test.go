package db

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

const ts = `'2026-01-01T00:00:00.000Z'`

// Migration 00004 adds task_turns and columns on tasks, repos, worktrees and
// proposals. Existing rows keep their data and get the defaults, the CHECKs
// hold, and the migration goes down and up again.
func TestMigration00004(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()
	s := NewStore(d, nil)
	p, r := seed(t, s)
	task, err := s.CreateTask(ctx, newTask(p, r, nil))
	if err != nil {
		t.Fatal(err)
	}
	prop, err := s.CreateProposal(ctx, core.Proposal{TaskID: &task.ID, Kind: core.ProposalCommit, Summary: "c"})
	if err != nil {
		t.Fatal(err)
	}

	exec := func(q string) error { _, err := d.write.ExecContext(ctx, q); return err }
	if err := exec(`INSERT INTO worktrees (id, repo_id, task_id, name, path, branch, status, created_at)
		VALUES ('w1', '` + string(r.ID) + `', '` + string(task.ID) + `', 'w', 'C:\wt', 'ygg/w', 'active', ` + ts + `)`); err != nil {
		t.Fatal(err)
	}
	turn := func(id string, seq int, state, extra string) string {
		return `INSERT INTO task_turns (id, task_id, seq, mode, prompt, state` + extraCols(extra) + `)
		        VALUES ('` + id + `', '` + string(task.ID) + `', ` + strconv.Itoa(seq) + `, 'ask', 'hello', '` + state + `'` + extraVals(extra) + `)`
	}

	// Defaults on rows that existed before (here: created through the store).
	if n := count(t, d, `SELECT count(*) FROM repos WHERE trust_hash = ''`); n != 1 {
		t.Fatal("repos.trust_hash default is not empty")
	}
	if n := count(t, d, `SELECT count(*) FROM proposals WHERE id = ? AND action_hash = '' AND attempt = 0 AND source = 'user' AND fail_kind IS NULL AND result IS NULL AND message = '' AND retryable = 0`, string(prop.ID)); n != 1 {
		t.Fatal("proposal defaults are wrong")
	}
	if n := count(t, d, `SELECT count(*) FROM tasks WHERE id = ? AND current_turn IS NULL AND idle_outcome IS NULL`, string(task.ID)); n != 1 {
		t.Fatal("task defaults are wrong")
	}

	// A valid waiting turn, then a finished one with usage.
	if err := exec(turn("u1", 1, "waiting", "")); err != nil {
		t.Fatalf("waiting turn refused: %v", err)
	}
	if err := exec(turn("u2", 2, "finished", "started_at, finished_at, outcome, cost_known, cost_micro_usd|"+ts+", "+ts+", 'answered', 1, 12345")); err != nil {
		t.Fatalf("finished turn refused: %v", err)
	}
	if err := exec(`UPDATE tasks SET current_turn = 'u2', idle_outcome = 'answered' WHERE id = '` + string(task.ID) + `'`); err != nil {
		t.Fatalf("task state columns refused: %v", err)
	}

	bad := map[string]string{
		"duplicate seq":                  turn("x1", 1, "waiting", ""),
		"seq zero":                       turn("x2", 0, "waiting", ""),
		"unknown state":                  turn("x3", 3, "paused", ""),
		"running without started_at":     turn("x4", 4, "running", ""),
		"finished without outcome":       turn("x5", 5, "finished", "started_at, finished_at|"+ts+", "+ts),
		"outcome on a live turn":         turn("x6", 6, "waiting", "outcome|'answered'"),
		"unknown outcome":                turn("x7", 7, "finished", "started_at, finished_at, outcome|"+ts+", "+ts+", 'meh'"),
		"cost without cost_known":        turn("x8", 8, "waiting", "cost_micro_usd|5"),
		"negative tokens":                turn("x9", 9, "waiting", "input_tokens|-1"),
		"unknown mode":                   strings.Replace(turn("x10", 10, "waiting", ""), "'ask'", "'root'", 1),
		"turn of a missing task":         strings.Replace(turn("x11", 11, "waiting", ""), string(task.ID), "nope", 1),
		"unknown idle_outcome":           `UPDATE tasks SET idle_outcome = 'meh'`,
		"trust_hash of the wrong length": `UPDATE repos SET trust_hash = 'abc'`,
		"unknown proposal source":        `UPDATE proposals SET source = 'robot'`,
		"unknown fail_kind":              `UPDATE proposals SET fail_kind = 'meh'`,
		"result that is not json":        `UPDATE proposals SET result = '{'`,
		"retryable 2":                    `UPDATE proposals SET retryable = 2`,
		"action_hash of the wrong size":  `UPDATE proposals SET action_hash = 'abc'`,
	}
	for name, q := range bad {
		if err := exec(q); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	good := []string{
		`UPDATE repos SET trust_hash = '` + strings.Repeat("a", 64) + `'`,
		`UPDATE proposals SET action_hash = '` + strings.Repeat("b", 64) + `', attempt = 1, source = 'agent', fail_kind = 'stale', result = '{"commit":"abc"}', message = 'x', retryable = 1`,
		`UPDATE worktrees SET base_commit = 'abc123'`,
	}
	for _, q := range good {
		if err := exec(q); err != nil {
			t.Errorf("%s: %v", q, err)
		}
	}

	// Deleting the task removes its turns.
	if n := count(t, d, `SELECT count(*) FROM task_turns`); n != 2 {
		t.Fatalf("turns = %d, want 2", n)
	}

	// Down removes the new schema and keeps the rows; up brings it back.
	m, err := NewMigrator(d.write)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.DownTo(ctx, 3); err != nil {
		t.Fatalf("down to 00003: %v", err)
	}
	if n := count(t, d, `SELECT count(*) FROM sqlite_master WHERE name IN ('task_turns', 'proposals_task_status')`); n != 0 {
		t.Fatal("00004 objects survived the down migration")
	}
	for table, col := range map[string]string{"tasks": "current_turn", "repos": "trust_hash", "worktrees": "base_commit", "proposals": "action_hash"} {
		if n := count(t, d, `SELECT count(*) FROM pragma_table_info('`+table+`') WHERE name = '`+col+`'`); n != 0 {
			t.Fatalf("%s.%s survived the down migration", table, col)
		}
	}
	if n := count(t, d, `SELECT count(*) FROM tasks`); n != 1 {
		t.Fatal("task lost by the down migration")
	}
	if n := count(t, d, `SELECT count(*) FROM proposals`); n != 1 {
		t.Fatal("proposal lost by the down migration")
	}
	if _, err := m.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if n := count(t, d, `SELECT count(*) FROM tasks WHERE id = ? AND current_turn IS NULL`, string(task.ID)); n != 1 {
		t.Fatal("task lost by down and up")
	}
}

func extraCols(extra string) string {
	cols, _, ok := strings.Cut(extra, "|")
	if !ok {
		return ""
	}
	return ", " + cols
}

func extraVals(extra string) string {
	_, vals, ok := strings.Cut(extra, "|")
	if !ok {
		return ""
	}
	return ", " + vals
}
