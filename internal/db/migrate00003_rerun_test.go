package db

import (
	"context"
	"testing"
)

// A crash between the migration's commit and goose's version insert leaves
// the schema without the version row; starting again must not fail.
func TestMigration00003RerunIsSafe(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()
	if _, err := d.write.ExecContext(ctx, `DELETE FROM goose_db_version WHERE version_id = 3`); err != nil {
		t.Fatal(err)
	}
	m, err := NewMigrator(d.write)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Up(ctx); err != nil {
		t.Fatalf("rerun of 00003: %v", err)
	}
	v, err := m.GetDBVersion(ctx)
	if err != nil || v != 3 {
		t.Fatalf("version = %d, %v; want 3", v, err)
	}
	if _, err := NewStore(d, nil).GetWorkspace(ctx); err != nil {
		t.Errorf("workspace after the rerun: %v", err)
	}
}
