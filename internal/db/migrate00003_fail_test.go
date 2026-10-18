package db

import (
	"context"
	"testing"

	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
)

// A repo outside its project folder gets a slug of its name; a name like
// "a:b" must not become a folder the core rejects, and a taken slug gets the
// repo ID.
func TestMigration00003FallbackFolderIsSafe(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()
	m, err := NewMigrator(d.write)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.DownTo(ctx, 2); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO projects (id, name, path, created_at) VALUES ('p1', 'acme', '/work/acme', '2026-01-01T00:00:00.000Z')`,
		`INSERT INTO repos (id, project_id, name, path, created_at) VALUES
		   ('r1', 'p1', 'a:b', '/elsewhere/one', '2026-01-01T00:00:00.000Z'),
		   ('r2', 'p1', 'a-b', '/elsewhere/two', '2026-01-01T00:00:00.000Z'),
		   ('r3', 'p1', 'a-b-r1', '/work/acme/a-b-r1', '2026-01-01T00:00:00.000Z')`,
	} {
		if _, err := d.write.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Up(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}
	got := relPaths(t, d)
	seen := map[string]bool{}
	for id, rel := range got {
		if _, err := corerepos.NewRelPath(rel); err != nil {
			t.Errorf("rel_path of %s = %q, which the core rejects: %v", id, rel, err)
		}
		if seen[rel] {
			t.Errorf("rel_path %q used twice", rel)
		}
		seen[rel] = true
	}
	if got["r3"] != "a-b-r1" {
		t.Errorf("a derived folder was changed: %q", got["r3"])
	}
	if _, err := NewStore(d, nil).GetWorkspace(ctx); err != nil {
		t.Errorf("the workspace doesn't load after the migration: %v", err)
	}
}

// Two repos that derive the same folder make the migration fail: the error is
// reported, nothing changes, and foreign keys are on again on that connection.
func TestMigration00003FailureChangesNothing(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()
	m, err := NewMigrator(d.write)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.DownTo(ctx, 2); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO projects (id, name, path, created_at) VALUES ('p1', 'acme', 'C:\Work\acme', '2026-01-01T00:00:00.000Z')`,
		`INSERT INTO repos (id, project_id, name, path, created_at) VALUES
		   ('r1', 'p1', 'one', 'C:\Work\acme\shop', '2026-01-01T00:00:00.000Z'),
		   ('r2', 'p1', 'two', 'C:\Work\acme/shop', '2026-01-01T00:00:00.000Z')`,
	} {
		if _, err := d.write.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Up(ctx); err == nil {
		t.Fatal("the migration succeeded although two repos share a folder")
	}
	if n := count(t, d, `SELECT count(*) FROM projects`); n != 1 {
		t.Errorf("projects = %d after the failed migration", n)
	}
	if n := count(t, d, `SELECT count(*) FROM repos`); n != 2 {
		t.Errorf("repos = %d after the failed migration", n)
	}
	if n := count(t, d, `SELECT count(*) FROM pragma_table_info('repos') WHERE name IN ('rel_path', 'untracked_at')`); n != 0 {
		t.Error("the failed migration left new columns behind")
	}
	if n := count(t, d, `SELECT count(*) FROM pragma_table_info('projects') WHERE name = 'version'`); n != 0 {
		t.Error("the failed migration left the rebuilt projects table behind")
	}
	if n := count(t, d, `PRAGMA foreign_keys`); n != 1 {
		t.Errorf("foreign_keys = %d after the failed migration, want 1", n)
	}
	// And the foreign keys really are enforced on the pool's connection.
	if _, err := d.write.ExecContext(ctx, `INSERT INTO repos (id, project_id, name, path, created_at) VALUES ('x', 'nope', 'n', '/n', '2026-01-01T00:00:00.000Z')`); err == nil {
		t.Error("a repo of a missing project was accepted after the failed migration")
	}
}
