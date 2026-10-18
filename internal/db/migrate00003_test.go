package db

import (
	"context"
	"testing"
)

// seed00002 fills every table migration 00003 touches or rebuilds, or that
// points at projects/repos, under the 00002 schema.
func seed00002(t *testing.T, d *DB) {
	t.Helper()
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO projects (id, name, path, created_at) VALUES
		   ('p1', 'acme-shop', 'C:\Work\acme-shop', '2026-01-01T00:00:00.000Z'),
		   ('p2', 'acme-docs', '/work/acme-docs', '2026-01-01T00:00:00.000Z'),
		   ('p3', 'root-proj', '/', '2026-01-01T00:00:00.000Z')`,
		`INSERT INTO repos (id, project_id, name, path, created_at) VALUES
		   ('r1', 'p1', 'shop-api', 'C:\Work\acme-shop\services\shop-api', '2026-01-01T00:00:00.000Z'),
		   ('r2', 'p1', 'shop-web', 'c:/work/ACME-SHOP/shop-web/', '2026-01-01T00:00:00.000Z'),
		   ('r3', 'p1', 'self',     'C:\Work\acme-shop', '2026-01-01T00:00:00.000Z'),
		   ('r4', 'p1', 'elsewhere','D:\Other\place', '2026-01-01T00:00:00.000Z'),
		   ('r5', 'p2', 'docs',     '/work/acme-docs/docs', '2026-01-01T00:00:00.000Z'),
		   ('r6', 'p3', 'top',      '/top', '2026-01-01T00:00:00.000Z')`,
		`INSERT INTO tasks (id, project_id, title, prompt, scope, mode, agent, status, created_at, updated_at)
		 VALUES ('t1', 'p1', 't', 'p', 'repo', 'ask', 'mock', 'running', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`,
		`INSERT INTO task_repos (task_id, repo_id) VALUES ('t1', 'r1')`,
		`INSERT INTO worktrees (id, repo_id, task_id, name, path, branch, status, created_at)
		 VALUES ('w1', 'r1', 't1', 'w', '/wt', 'ygg/w', 'active', '2026-01-01T00:00:00.000Z')`,
		`INSERT INTO builds (id, task_id, worktree_id, target, status, created_at) VALUES ('b1', 't1', 'w1', 'worktree', 'succeeded', '2026-01-02T00:00:00.000Z')`,
		`INSERT INTO build_steps (build_id, idx, repo_id, name, status) VALUES ('b1', 0, 'r1', 'verify', 'succeeded')`,
		`INSERT INTO build_artifacts (build_id, repo_id, path, size) VALUES ('b1', 'r1', 'target/a.jar', 10)`,
		`INSERT INTO build_presets (id, project_id, name, steps, created_at) VALUES ('bp1', 'p1', 'all', '[]', '2026-01-02T00:00:00.000Z')`,
		`INSERT INTO proposals (id, task_id, repo_id, kind, status, summary, created_at)
		 VALUES ('pr1', 't1', 'r1', 'commit', 'pending', 'c', '2026-01-02T00:00:00.000Z')`,
	} {
		if _, err := d.write.ExecContext(ctx, q); err != nil {
			t.Fatalf("seed 00002 data: %v\n%s", err, q)
		}
	}
}

// dataCounts are the rows that must survive the rebuild of projects: a
// DROP TABLE with foreign keys on would cascade them away.
var dataCounts = map[string]int{
	"projects": 3, "repos": 6, "tasks": 1, "task_repos": 1, "worktrees": 1, "builds": 1,
	"build_steps": 1, "build_artifacts": 1, "build_presets": 1, "proposals": 1,
}

func checkCounts(t *testing.T, d *DB, when string) {
	t.Helper()
	for table, want := range dataCounts {
		if n := count(t, d, `SELECT count(*) FROM `+table); n != want {
			t.Errorf("%s: %s has %d rows, want %d (a cascade deleted them?)", when, table, n, want)
		}
	}
}

func relPaths(t *testing.T, d *DB) map[string]string {
	t.Helper()
	rows, err := d.write.QueryContext(context.Background(), `SELECT id, rel_path FROM repos`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, rel string
		if err := rows.Scan(&id, &rel); err != nil {
			t.Fatal(err)
		}
		out[id] = rel
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMigration00003KeepsData(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()
	m, err := NewMigrator(d.write)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.DownTo(ctx, 2); err != nil {
		t.Fatalf("down to 00002: %v", err)
	}
	seed00002(t, d)

	if _, err := m.Up(ctx); err != nil {
		t.Fatalf("up to 00003: %v", err)
	}
	checkCounts(t, d, "after up")
	if n := count(t, d, `PRAGMA foreign_keys`); n != 1 {
		t.Fatalf("foreign_keys = %d after the migration, want 1: the pragma was not restored", n)
	}

	want := map[string]string{
		"r1": "services/shop-api", // backslashes become slashes
		"r2": "shop-web",          // case-insensitive prefix, trailing slash trimmed
		"r3": ".",                 // the project folder itself
		"r4": "elsewhere",         // outside the project: the repo name
		"r5": "docs",
		"r6": "top", // a project at the root
	}
	got := relPaths(t, d)
	for id, rel := range want {
		if got[id] != rel {
			t.Errorf("rel_path of %s = %q, want %q", id, got[id], rel)
		}
	}

	// The rebuilt projects table kept its foreign keys on both sides.
	for name, q := range map[string]string{
		"repo of a missing project":         `INSERT INTO repos (id, project_id, name, path, rel_path, created_at) VALUES ('x', 'nope', 'n', '/n', 'n', '2026-01-01T00:00:00.000Z')`,
		"deleting a repo with builds":       `DELETE FROM repos WHERE id = 'r1'`,
		"duplicate rel_path in a project":   `INSERT INTO repos (id, project_id, name, path, rel_path, created_at) VALUES ('x', 'p1', 'other', '/o', 'shop-web', '2026-01-01T00:00:00.000Z')`,
		"duplicate repo name":               `INSERT INTO repos (id, project_id, name, path, rel_path, created_at) VALUES ('x', 'p1', 'shop-web', '/o', 'o', '2026-01-01T00:00:00.000Z')`,
		"duplicate active project name":     `INSERT INTO projects (id, name, path, created_at) VALUES ('x', 'acme-shop', '/x', '2026-01-01T00:00:00.000Z')`,
		"build preset of a missing project": `INSERT INTO build_presets (id, project_id, name, steps, created_at) VALUES ('x', 'nope', 'n', '[]', '2026-01-01T00:00:00.000Z')`,
	} {
		if _, err := d.write.ExecContext(ctx, q); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}

	// An untracked project frees its name; an untracked repo keeps its rel_path.
	if _, err := d.write.ExecContext(ctx, `UPDATE projects SET untracked_at = '2026-02-01T00:00:00.000Z' WHERE id = 'p2'`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.write.ExecContext(ctx, `INSERT INTO projects (id, name, path, created_at) VALUES ('p4', 'acme-docs', '/work/acme-docs', '2026-02-02T00:00:00.000Z')`); err != nil {
		t.Fatalf("active project reusing an untracked name refused: %v", err)
	}
	if _, err := d.write.ExecContext(ctx, `INSERT INTO projects (id, name, path, created_at) VALUES ('p5', 'acme-docs', '/work/x', '2026-02-02T00:00:00.000Z')`); err == nil {
		t.Error("two active projects named acme-docs accepted")
	}

	// settings
	s := NewStore(d, nil)
	if _, err := d.write.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('editor', 'code')`); err != nil {
		t.Fatalf("settings: %v", err)
	}
	if got, err := s.GetSetting(ctx, "editor"); err != nil || got.Value != "code" || got.Version != 0 {
		t.Fatalf("setting = %+v, %v", got, err)
	}
}

func TestMigration00003DownAndUpAgain(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()
	m, err := NewMigrator(d.write)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.DownTo(ctx, 2); err != nil {
		t.Fatal(err)
	}
	seed00002(t, d)
	if _, err := m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	// State only 00003 can hold: an untracked project sharing a name with an
	// active one, an untracked repo, a setting.
	for _, q := range []string{
		`UPDATE projects SET untracked_at = '2026-02-01T00:00:00.000Z', version = 3 WHERE id = 'p2'`,
		`INSERT INTO projects (id, name, path, created_at) VALUES ('p4', 'acme-docs', '/work/acme-docs', '2026-02-02T00:00:00.000Z')`,
		`UPDATE repos SET untracked_at = '2026-02-01T00:00:00.000Z' WHERE id = 'r2'`,
		`INSERT INTO settings (key, value) VALUES ('editor', 'code')`,
	} {
		if _, err := d.write.ExecContext(ctx, q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}

	if _, err := m.DownTo(ctx, 2); err != nil {
		t.Fatalf("down with data: %v", err)
	}
	dataCounts["projects"]++ // p4
	defer func() { dataCounts["projects"]-- }()
	checkCounts(t, d, "after down")
	if n := count(t, d, `PRAGMA foreign_keys`); n != 1 {
		t.Fatalf("foreign_keys = %d after down, want 1", n)
	}
	if n := count(t, d, `SELECT count(*) FROM pragma_table_info('repos') WHERE name IN ('untracked_at', 'rel_path')`); n != 0 {
		t.Error("repo columns survived the down migration")
	}
	if n := count(t, d, `SELECT count(*) FROM pragma_table_info('projects') WHERE name IN ('untracked_at', 'version')`); n != 0 {
		t.Error("project columns survived the down migration")
	}
	if n := count(t, d, `SELECT count(*) FROM sqlite_master WHERE name IN ('settings', 'projects_active_name', 'repos_project_rel_path')`); n != 0 {
		t.Error("00003 objects survived the down migration")
	}
	// 00002 wants unique names again; the untracked duplicate was renamed, not deleted.
	if _, err := d.write.ExecContext(ctx, `INSERT INTO projects (id, name, path, created_at) VALUES ('x', 'acme-shop', '/x', '2026-02-02T00:00:00.000Z')`); err == nil {
		t.Error("duplicate project name accepted after down")
	}
	if n := count(t, d, `SELECT count(*) FROM projects WHERE id = 'p2' AND name = 'acme-docs (p2)'`); n != 1 {
		t.Error("the untracked project was not given a unique name")
	}
	if _, err := d.write.ExecContext(ctx, `INSERT INTO repos (id, project_id, name, path, created_at) VALUES ('x', 'nope', 'n', '/n', '2026-01-01T00:00:00.000Z')`); err == nil {
		t.Error("repo of a missing project accepted after down: foreign key lost")
	}

	if _, err := m.Up(ctx); err != nil {
		t.Fatalf("up after down: %v", err)
	}
	checkCounts(t, d, "after down and up")
	if got := relPaths(t, d); got["r1"] != "services/shop-api" || got["r3"] != "." {
		t.Errorf("rel_path after down and up = %v", got)
	}
	if n := count(t, d, `PRAGMA foreign_keys`); n != 1 {
		t.Fatalf("foreign_keys = %d after up, want 1", n)
	}
}
