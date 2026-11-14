package db

import (
	"context"
	"database/sql"
	_ "embed"

	"github.com/pressly/goose/v3"
)

//go:embed sqlcschema/00008_submission_replay.sql
var schema00008 string

// Migration 00008 uses an embedded SQL mirror so sqlc applies it after the
// Go-backed 00007 schema. Goose runs each direction and its version row in one
// transaction. Down removes only 00008 data/columns; upgrading again leaves
// surviving action rows explicitly legacy, never eligible for replay.
func migration00008() *goose.Migration {
	return goose.NewGoMigration(8,
		&goose.GoFunc{RunTx: func(ctx context.Context, tx *sql.Tx) error { _, err := tx.ExecContext(ctx, schema00008); return err }},
		&goose.GoFunc{RunTx: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `DROP TABLE task_submissions;
ALTER TABLE workspace_action_requests DROP COLUMN model;
ALTER TABLE workspace_action_requests DROP COLUMN agent;`)
			return err
		}},
	)
}
