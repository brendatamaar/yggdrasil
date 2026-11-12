package db

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"

	"github.com/pressly/goose/v3"
)

//go:embed sqlcschema/00007_phase6.sql
var schema00007 string

// Migration 00007 (docs/design/phase-6-*.md):
//
//   - repos.readiness, tasks provenance columns, repo_reservations,
//     proposal_effects, workspace_action_requests/_proposals, outbox, notices
//     and notice_high_water (all in the embedded sqlcschema file, which sqlc
//     reads);
//   - proposals rebuilt with the kinds 'ignore_entries', 'review_comment',
//     'init_repo' and 'pull_ff' allowed: SQLite cannot alter a CHECK, and the
//     proposals table has a restrictive kind CHECK.
//
// It is a Go migration for the reason 00003 and 00005 are: the rebuild drops
// proposals while other tables point at it (change set members, and the new
// tables), and foreign_keys cannot be switched off inside a transaction.
// rebuildTx pins one connection, switches foreign keys off, checks
// foreign_key_check before it commits and always puts foreign keys back on. A
// plain SQL goose migration could not do that safely, which is why the audited
// 00003/00005 pattern is reused instead of a SQL-only 00007. Old data, indexes
// and foreign keys of proposals are kept: the DDL is the one of 00005 with a
// longer kind list. Rerunning is safe: it checks for the new schema first.
func migration00007() *goose.Migration {
	return goose.NewGoMigration(7,
		&goose.GoFunc{RunDB: upPhase6, Mode: goose.TransactionDisabled},
		&goose.GoFunc{RunDB: downPhase6, Mode: goose.TransactionDisabled},
	)
}

const kindsPhase6 = kindsNew + `, 'ignore_entries', 'review_comment', 'init_repo', 'pull_ff'`

func schema00007Applied(ctx context.Context, tx *sql.Tx) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT
  (SELECT count(*) FROM pragma_table_info('repos') WHERE name = 'readiness') +
  (SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'notices')`).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("look for the phase 6 schema: %w", err)
	}
	return n == 2, nil
}

func rebuildProposals(name, kinds string) []string {
	return []string{
		proposalsDDL(name+"_new", kinds),
		`INSERT INTO ` + name + `_new (` + proposalsCols + `) SELECT ` + proposalsCols + ` FROM proposals`,
		`DROP TABLE proposals`,
		`ALTER TABLE ` + name + `_new RENAME TO proposals`,
		`CREATE INDEX proposals_status ON proposals (status)`,
		`CREATE INDEX proposals_change_set_id ON proposals (change_set_id)`,
		`CREATE INDEX proposals_task_status ON proposals (task_id, status)`,
	}
}

func upPhase6(ctx context.Context, db *sql.DB) error {
	return rebuildTx(ctx, db, func(ctx context.Context, tx *sql.Tx) error {
		if done, err := schema00007Applied(ctx, tx); err != nil || done {
			return err
		}
		// Rebuild first: the new tables point at proposals by name and keep
		// pointing at the rebuilt table.
		if err := execAll(ctx, tx, rebuildProposals("proposals", kindsPhase6)...); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, schema00007); err != nil {
			return fmt.Errorf("add the phase 6 schema: %w", err)
		}
		return nil
	})
}

// downPhase6 removes what 00007 added. Rows that cannot exist under the older
// schema are removed first: proposals of the four new kinds (new-feature rows;
// their audit entries stay in the append-only log), reserved repos with their
// setup drafts and the reservation rows (they never had a directory), and
// imported-issue drafts keep their task but lose their provenance.
func downPhase6(ctx context.Context, db *sql.DB) error {
	return rebuildTx(ctx, db, func(ctx context.Context, tx *sql.Tx) error {
		if done, err := schema00007Applied(ctx, tx); err != nil || !done {
			return err
		}
		stmts := []string{
			`DROP TABLE notice_high_water`,
			`DROP TABLE notices`,
			`DROP TABLE outbox`,
			`DROP TABLE workspace_action_proposals`,
			`DROP TABLE workspace_action_requests`,
			`DROP TABLE proposal_effects`,
			`DROP TABLE repo_reservations`,
			// Foreign keys are off here, so nothing cascades: remove the
			// dependents of the setup drafts and reserved repos by hand.
			`DELETE FROM proposals WHERE task_id IN (SELECT id FROM tasks WHERE source_kind = 'repo_setup')
			    OR repo_id IN (SELECT id FROM repos WHERE readiness = 'reserved')`,
			`DELETE FROM change_sets WHERE task_id IN (SELECT id FROM tasks WHERE source_kind = 'repo_setup')`,
			`DELETE FROM task_repos WHERE task_id IN (SELECT id FROM tasks WHERE source_kind = 'repo_setup')
			    OR repo_id IN (SELECT id FROM repos WHERE readiness = 'reserved')`,
			`DELETE FROM task_turns WHERE task_id IN (SELECT id FROM tasks WHERE source_kind = 'repo_setup')`,
			`DELETE FROM task_events WHERE task_id IN (SELECT id FROM tasks WHERE source_kind = 'repo_setup')`,
			`DELETE FROM task_dependencies WHERE task_id IN (SELECT id FROM tasks WHERE source_kind = 'repo_setup')
			    OR depends_on_task_id IN (SELECT id FROM tasks WHERE source_kind = 'repo_setup')`,
			`DELETE FROM tasks WHERE source_kind = 'repo_setup'`,
			`DELETE FROM repos WHERE readiness = 'reserved'`,
			`DROP TRIGGER task_turns_not_for_setup_draft`,
			`DROP TRIGGER tasks_setup_stays_draft`,
			`DROP INDEX tasks_source_kind`,
			`DROP INDEX tasks_project_dedup`,
			`ALTER TABLE tasks DROP COLUMN dedup_key`,
			`ALTER TABLE tasks DROP COLUMN source_number`,
			`ALTER TABLE tasks DROP COLUMN source_project`,
			`ALTER TABLE tasks DROP COLUMN source_instance`,
			`ALTER TABLE tasks DROP COLUMN source_kind`,
			`DROP INDEX repos_project_readiness`,
			`ALTER TABLE repos DROP COLUMN readiness`,
			`DELETE FROM proposals WHERE kind IN ('ignore_entries', 'review_comment', 'init_repo', 'pull_ff')`,
		}
		stmts = append(stmts, rebuildProposals("proposals_old", kindsNew)...)
		return execAll(ctx, tx, stmts...)
	})
}
