package db

import (
	"context"
	"database/sql"
	"os"
	"reflect"
	"testing"
)

// sqlc reads internal/db/sqlcschema/00003_repo_tracking.sql instead of the Go
// migration; the tables it describes must equal the migrated ones.
func TestSqlcSchemaMatchesMigration(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	ref, err := sql.Open(driverName, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer ref.Close()
	ref.SetMaxOpenConns(1)
	p, err := NewMigrator(ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 2); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile("sqlcschema/00003_repo_tracking.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ref.ExecContext(ctx, string(src)); err != nil {
		t.Fatal(err)
	}
	cols := func(db *sql.DB, table string) [][4]string {
		// trust_hash is migration 00004's column on repos; the reference
		// schema stops at 00003, so leave it out of the comparison.
		rows, err := db.QueryContext(ctx, `SELECT name, type, "notnull", coalesce(dflt_value,'') FROM pragma_table_info('`+table+`') WHERE name <> 'trust_hash' ORDER BY name`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out [][4]string
		for rows.Next() {
			var c [4]string
			if err := rows.Scan(&c[0], &c[1], &c[2], &c[3]); err != nil {
				t.Fatal(err)
			}
			out = append(out, c)
		}
		return out
	}
	for _, table := range []string{"projects", "repos", "settings"} {
		if got, want := cols(ref, table), cols(d.write, table); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: sqlc schema %v, migration %v", table, got, want)
		}
	}
	// The two unique indexes carry the "one active name / path" rules.
	indexes := func(db *sql.DB) map[string]string {
		rows, err := db.QueryContext(ctx, `SELECT name, coalesce(sql,'') FROM sqlite_master WHERE type = 'index' AND name IN ('projects_active_name', 'repos_project_rel_path')`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var n, s string
			if err := rows.Scan(&n, &s); err != nil {
				t.Fatal(err)
			}
			out[n] = s
		}
		return out
	}
	got, want := indexes(ref), indexes(d.write)
	if len(want) != 2 || !reflect.DeepEqual(got, want) {
		t.Errorf("unique indexes: sqlc schema %v, migration %v", got, want)
	}
}
