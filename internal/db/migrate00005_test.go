package db

import (
	"context"
	"strings"
	"testing"
)

// Migration 00005 rebuilds worktrees and proposals (wider CHECKs) and adds the
// verification, review-note and build-settings tables. Rows written under the
// 00004 schema, including a build that points at a worktree and proposals of
// every kind it knew, must survive it, in both directions, with the same
// columns in the same order and every foreign key still in place.
func TestMigration00005KeepsDataBothWays(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()
	exec := func(q string, args ...any) error { _, err := d.write.ExecContext(ctx, q, args...); return err }
	must := func(q string, args ...any) {
		t.Helper()
		if err := exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	m, err := NewMigrator(d.write)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.DownTo(ctx, 4); err != nil {
		t.Fatalf("down to 00004: %v", err)
	}

	s := NewStore(d, nil)
	p, r := seed(t, s)
	task, err := s.CreateTask(ctx, newTask(p, r, nil))
	if err != nil {
		t.Fatal(err)
	}
	tid, rid := string(task.ID), string(r.ID)
	must(`INSERT INTO worktrees (id, repo_id, task_id, name, path, branch, status, created_at, base_commit)
	      VALUES ('w1', ?, ?, 'w', 'C:\wt\w', 'ygg/w', 'merged', `+ts+`, 'abc123')`, rid, tid)
	must(`INSERT INTO builds (id, worktree_id, task_id, target, status, created_at)
	      VALUES ('b1', 'w1', ?, 'worktree', 'succeeded', `+ts+`)`, tid)
	must(`INSERT INTO build_steps (build_id, idx, repo_id, name, status, argv) VALUES ('b1', 0, ?, 's', 'succeeded', '["go","test"]')`, rid)
	must(`INSERT INTO change_sets (id, task_id, status, merge_order, created_at) VALUES ('cs1', ?, 'open', '["`+rid+`"]', `+ts+`)`, tid)
	for i, k := range []string{"commit", "push", "merge_request", "install", "publish", "agent_file", "delete_files", "subtasks"} {
		must(`INSERT INTO proposals (id, change_set_id, task_id, repo_id, kind, status, summary, created_at, action_hash, attempt, source, message, retryable, version)
		      VALUES (?, 'cs1', ?, ?, ?, 'pending', 'sum', `+ts+`, ?, 2, 'agent', 'm', 1, 3)`,
			"p"+string(rune('a'+i)), tid, rid, k, strings.Repeat("c", 64))
	}
	before := schemaOf(t, d, "worktrees", "proposals")

	if _, err := m.UpTo(ctx, 5); err != nil {
		t.Fatalf("up to 00005: %v", err)
	}

	// Rows and column values survived.
	if n := count(t, d, `SELECT count(*) FROM proposals WHERE attempt = 2 AND source = 'agent' AND message = 'm' AND retryable = 1 AND version = 3 AND action_hash = ?`, strings.Repeat("c", 64)); n != 8 {
		t.Fatalf("%d proposals kept their data, want 8", n)
	}
	if n := count(t, d, `SELECT count(*) FROM worktrees WHERE id = 'w1' AND base_commit = 'abc123' AND status = 'merged' AND staged_name = ''`); n != 1 {
		t.Fatal("worktree lost or changed")
	}
	if n := count(t, d, `SELECT count(*) FROM builds WHERE id = 'b1' AND worktree_id = 'w1'`); n != 1 {
		t.Fatal("the build lost its worktree")
	}
	if n := count(t, d, `SELECT count(*) FROM build_steps WHERE build_id = 'b1' AND env = '[]' AND argv = '["go","test"]'`); n != 1 {
		t.Fatal("build step lost or has no default env")
	}
	if n := count(t, d, `SELECT count(*) FROM change_sets WHERE id = 'cs1' AND version = 0 AND run_result = '{"kind":"idle"}'`); n != 1 {
		t.Fatal("change set lost or has no default run result")
	}
	// Same columns, same order, plus staged_name on worktrees.
	after := schemaOf(t, d, "worktrees", "proposals")
	if got, want := after["proposals"], before["proposals"]; got != want {
		t.Errorf("proposals columns changed:\n got %s\nwant %s", got, want)
	}
	if got, want := after["worktrees"], before["worktrees"]+",staged_name"; got != want {
		t.Errorf("worktrees columns:\n got %s\nwant %s", got, want)
	}
	// Indexes and foreign keys are back.
	for _, ix := range []string{"worktrees_task_id", "proposals_status", "proposals_change_set_id", "proposals_task_status"} {
		if n := count(t, d, `SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, ix); n != 1 {
			t.Errorf("index %s missing", ix)
		}
	}
	for _, c := range []struct{ child, parent string }{{"builds", "worktrees"}, {"proposals", "change_sets"}, {"worktrees", "tasks"}, {"verifications", "worktrees"}} {
		if n := count(t, d, `SELECT count(*) FROM pragma_foreign_key_list('`+c.child+`') WHERE "table" = ?`, c.parent); n < 1 {
			t.Errorf("%s no longer points at %s", c.child, c.parent)
		}
	}
	if n := count(t, d, `SELECT count(*) FROM pragma_foreign_key_check`); n != 0 {
		t.Errorf("%d broken foreign keys", n)
	}
	if n := count(t, d, `PRAGMA foreign_keys`); n != 1 {
		t.Error("foreign keys left off")
	}

	// The new values are accepted, and the old CHECKs still hold.
	must(`INSERT INTO proposals (id, task_id, repo_id, kind, status, summary, created_at) VALUES ('pm', ?, ?, 'merge_local', 'pending', 's', `+ts+`)`, tid, rid)
	must(`INSERT INTO proposals (id, task_id, repo_id, kind, status, summary, created_at) VALUES ('pr', ?, ?, 'remove_worktree', 'pending', 's', `+ts+`)`, tid, rid)
	must(`UPDATE worktrees SET status = 'removing', staged_name = 'w-r1' WHERE id = 'w1'`)
	bad := map[string]string{
		"unknown kind":                `INSERT INTO proposals (id, kind, status, summary, created_at) VALUES ('x1', 'rm_rf', 'pending', 's', ` + ts + `)`,
		"removing without staged":     `UPDATE worktrees SET status = 'removing', staged_name = '' WHERE id = 'w1'`,
		"unknown worktree status":     `UPDATE worktrees SET status = 'gone' WHERE id = 'w1'`,
		"bad proposal status":         `UPDATE proposals SET status = 'x' WHERE id = 'pm'`,
		"bad run_result":              `UPDATE change_sets SET run_result = '{' WHERE id = 'cs1'`,
		"bad verification state":      `INSERT INTO verifications (id, task_id, repo_id, worktree_id, state, created_at, updated_at) VALUES ('v0', '` + tid + `', '` + rid + `', 'w1', 'nope', ` + ts + `, ` + ts + `)`,
		"review note line 0":          `INSERT INTO review_notes (id, task_id, repo_id, path, line, side, text, diff_hash, created_at) VALUES ('n0', '` + tid + `', '` + rid + `', 'a.go', 0, 'new', 't', '` + strings.Repeat("a", 64) + `', ` + ts + `)`,
		"review note short diff hash": `INSERT INTO review_notes (id, task_id, repo_id, path, line, side, text, diff_hash, created_at) VALUES ('n1', '` + tid + `', '` + rid + `', 'a.go', 1, 'new', 't', 'abc', ` + ts + `)`,
		"settings full_tests 2":       `INSERT INTO repo_build_settings (repo_id, full_tests, updated_at) VALUES ('` + rid + `', 2, ` + ts + `)`,
	}
	for name, q := range bad {
		if err := exec(q); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	must(`INSERT INTO verifications (id, task_id, repo_id, worktree_id, state, created_at, updated_at) VALUES ('v1', ?, ?, 'w1', 'passed', `+ts+`, `+ts+`)`, tid, rid)
	if err := exec(`INSERT INTO verifications (id, task_id, repo_id, worktree_id, created_at, updated_at) VALUES ('v2', ?, ?, 'w1', `+ts+`, `+ts+`)`, tid, rid); err == nil {
		t.Error("a second verification for one (task, repo) was accepted")
	}
	// A worktree with a verification cannot vanish under it.
	if err := exec(`DELETE FROM worktrees WHERE id = 'w1'`); err == nil {
		t.Error("a worktree with builds or verifications was deleted")
	}

	// Down: new objects go, 'removing' becomes 'merged', new-kind proposals go,
	// everything else stays; then up again.
	if _, err := m.DownTo(ctx, 4); err != nil {
		t.Fatalf("down to 00004: %v", err)
	}
	if n := count(t, d, `SELECT count(*) FROM sqlite_master WHERE name IN ('verifications', 'review_notes', 'repo_build_settings')`); n != 0 {
		t.Fatal("00005 tables survived the down migration")
	}
	for _, tc := range [][2]string{{"change_sets", "run_result"}, {"change_sets", "version"}, {"worktrees", "staged_name"}, {"build_steps", "env"}} {
		table, col := tc[0], tc[1]
		if n := count(t, d, `SELECT count(*) FROM pragma_table_info('`+strings.TrimSpace(table)+`') WHERE name = '`+col+`'`); n != 0 {
			t.Fatalf("%s.%s survived the down migration", table, col)
		}
	}
	if n := count(t, d, `SELECT count(*) FROM proposals`); n != 8 {
		t.Fatalf("%d proposals after down, want the 8 old-kind ones", n)
	}
	if n := count(t, d, `SELECT count(*) FROM worktrees WHERE id = 'w1' AND status = 'merged' AND base_commit = 'abc123'`); n != 1 {
		t.Fatal("worktree not restored to merged")
	}
	if got, want := schemaOf(t, d, "worktrees", "proposals"), before; got["worktrees"] != want["worktrees"] || got["proposals"] != want["proposals"] {
		t.Errorf("schema after down differs from before up: %v vs %v", got, want)
	}
	if err := exec(`INSERT INTO proposals (id, kind, status, summary, created_at) VALUES ('x2', 'merge_local', 'pending', 's', ` + ts + `)`); err == nil {
		t.Error("merge_local accepted after the down migration")
	}
	if _, err := m.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if n := count(t, d, `SELECT count(*) FROM proposals`); n != 8 {
		t.Fatalf("%d proposals after up again", n)
	}
	if n := count(t, d, `SELECT count(*) FROM pragma_foreign_key_check`); n != 0 {
		t.Errorf("%d broken foreign keys after up again", n)
	}
}

func schemaOf(t *testing.T, d *DB, tables ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, table := range tables {
		rows, err := d.write.QueryContext(context.Background(), `SELECT name FROM pragma_table_info('`+table+`') ORDER BY cid`)
		if err != nil {
			t.Fatal(err)
		}
		var cols []string
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				t.Fatal(err)
			}
			cols = append(cols, c)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		out[table] = strings.Join(cols, ",")
	}
	return out
}
