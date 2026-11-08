package db

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"

	"github.com/pressly/goose/v3"
)

//go:embed sqlcschema/00005_verify_review_landing.sql
var schema00005 string

// Migration 00005 (docs/design/verify.md, changesets.md, landing.md):
//
//   - verifications, review_notes and repo_build_settings;
//   - change_sets.run_result and version, worktrees.staged_name and
//     build_steps.env (all in the embedded sqlcschema file, which sqlc reads);
//   - worktrees rebuilt with status 'removing' allowed, and proposals rebuilt
//     with the kinds 'merge_local' and 'remove_worktree' allowed: SQLite cannot
//     alter a CHECK.
//
// It is a Go migration for the reason migration 00003 is: the rebuild drops
// worktrees while builds point at it (ON DELETE RESTRICT / SET NULL), and
// foreign_keys cannot be switched off inside a transaction. rebuildTx pins one
// connection, switches foreign keys off, checks foreign_key_check before it
// commits and always puts foreign keys back on. The rebuilt tables keep their
// column order, because the sqlc models use SELECT *.
func migration00005() *goose.Migration {
	return goose.NewGoMigration(5,
		&goose.GoFunc{RunDB: upVerifyReviewLanding, Mode: goose.TransactionDisabled},
		&goose.GoFunc{RunDB: downVerifyReviewLanding, Mode: goose.TransactionDisabled},
	)
}

func schema00005Applied(ctx context.Context, tx *sql.Tx) (bool, error) {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'verifications'`).Scan(&n); err != nil {
		return false, fmt.Errorf("look for the verification schema: %w", err)
	}
	return n == 1, nil
}

const worktreesCols = `id, repo_id, task_id, name, path, branch, status, created_at, base_commit`

const proposalsCols = `id, change_set_id, task_id, repo_id, kind, status, summary, payload, created_at, decided_at,
    version, action_hash, attempt, source, fail_kind, result, message, retryable`

// proposalsDDL and worktreesDDL are the tables as migrations 00001, 00002 and
// 00004 left them; only the kind / status CHECK differs between up and down.
func proposalsDDL(name, kinds string) string {
	return `CREATE TABLE ` + name + ` (
    id            TEXT PRIMARY KEY,
    change_set_id TEXT REFERENCES change_sets (id) ON DELETE CASCADE,
    task_id       TEXT REFERENCES tasks (id) ON DELETE SET NULL,
    repo_id       TEXT REFERENCES repos (id) ON DELETE SET NULL,
    kind          TEXT NOT NULL CHECK (kind IN (` + kinds + `)),
    status        TEXT NOT NULL CHECK (status IN ('pending', 'confirmed', 'rejected', 'executed', 'failed')),
    summary       TEXT NOT NULL,
    payload       TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(payload)),
    created_at    TEXT NOT NULL,
    decided_at    TEXT,
    version       INTEGER NOT NULL DEFAULT 0,
    action_hash   TEXT NOT NULL DEFAULT '' CHECK (action_hash = '' OR length(action_hash) = 64),
    attempt       INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    source        TEXT NOT NULL DEFAULT 'user' CHECK (source IN ('agent', 'user')),
    fail_kind     TEXT CHECK (fail_kind IS NULL OR fail_kind IN
                      ('stale', 'precondition', 'repo_untracked', 'rejected_by_remote', 'unsafe_remote',
                       'interrupted', 'exec_error')),
    result        TEXT CHECK (result IS NULL OR json_valid(result)),
    message       TEXT NOT NULL DEFAULT '',
    retryable     INTEGER NOT NULL DEFAULT 0 CHECK (retryable IN (0, 1))
) STRICT`
}

const (
	kindsOld = `'commit', 'push', 'merge_request', 'install', 'publish', 'agent_file', 'delete_files', 'subtasks'`
	kindsNew = kindsOld + `, 'merge_local', 'remove_worktree'`
)

func worktreesDDL(name string, withStaged bool) string {
	statuses, staged, check := `'active', 'merged', 'removed'`, ``, ``
	if withStaged {
		statuses = `'active', 'merged', 'removing', 'removed'`
		staged = `,
    staged_name TEXT NOT NULL DEFAULT ''`
		check = `,
    CHECK (status <> 'removing' OR staged_name <> '')`
	}
	return `CREATE TABLE ` + name + ` (
    id         TEXT PRIMARY KEY,
    repo_id    TEXT NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
    task_id    TEXT NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    path       TEXT NOT NULL,
    branch     TEXT NOT NULL,
    status     TEXT NOT NULL CHECK (status IN (` + statuses + `)),
    created_at TEXT NOT NULL,
    base_commit TEXT NOT NULL DEFAULT ''` + staged + `,
    UNIQUE (repo_id, name)` + check + `
) STRICT`
}

func upVerifyReviewLanding(ctx context.Context, db *sql.DB) error {
	return rebuildTx(ctx, db, func(ctx context.Context, tx *sql.Tx) error {
		if done, err := schema00005Applied(ctx, tx); err != nil || done {
			return err
		}
		// The additive part first: it adds worktrees.staged_name, which the
		// rebuilt table below keeps.
		if _, err := tx.ExecContext(ctx, schema00005); err != nil {
			return fmt.Errorf("add the verification schema: %w", err)
		}
		return execAll(ctx, tx,
			worktreesDDL("worktrees_new", true),
			`INSERT INTO worktrees_new (`+worktreesCols+`, staged_name) SELECT `+worktreesCols+`, staged_name FROM worktrees`,
			`DROP TABLE worktrees`,
			`ALTER TABLE worktrees_new RENAME TO worktrees`,
			`CREATE INDEX worktrees_task_id ON worktrees (task_id)`,

			proposalsDDL("proposals_new", kindsNew),
			`INSERT INTO proposals_new (`+proposalsCols+`) SELECT `+proposalsCols+` FROM proposals`,
			`DROP TABLE proposals`,
			`ALTER TABLE proposals_new RENAME TO proposals`,
			`CREATE INDEX proposals_status ON proposals (status)`,
			`CREATE INDEX proposals_change_set_id ON proposals (change_set_id)`,
			`CREATE INDEX proposals_task_status ON proposals (task_id, status)`,
		)
	})
}

// downVerifyReviewLanding removes what 00005 added. Data that cannot exist
// under the older schema is changed or dropped first: proposals of the two new
// kinds are deleted (they are new-feature rows; their audit entries stay), and
// a worktree in 'removing' goes back to 'merged'.
func downVerifyReviewLanding(ctx context.Context, db *sql.DB) error {
	return rebuildTx(ctx, db, func(ctx context.Context, tx *sql.Tx) error {
		if done, err := schema00005Applied(ctx, tx); err != nil || !done {
			return err
		}
		return execAll(ctx, tx,
			`DROP TABLE verifications`,
			`DROP TABLE review_notes`,
			`DROP TABLE repo_build_settings`,
			`ALTER TABLE change_sets DROP COLUMN run_result`,
			`ALTER TABLE change_sets DROP COLUMN version`,
			`ALTER TABLE build_steps DROP COLUMN env`,

			`UPDATE worktrees SET status = 'merged' WHERE status = 'removing'`,
			worktreesDDL("worktrees_old", false),
			`INSERT INTO worktrees_old (`+worktreesCols+`) SELECT `+worktreesCols+` FROM worktrees`,
			`DROP TABLE worktrees`,
			`ALTER TABLE worktrees_old RENAME TO worktrees`,
			`CREATE INDEX worktrees_task_id ON worktrees (task_id)`,

			`DELETE FROM proposals WHERE kind IN ('merge_local', 'remove_worktree')`,
			proposalsDDL("proposals_old", kindsOld),
			`INSERT INTO proposals_old (`+proposalsCols+`) SELECT `+proposalsCols+` FROM proposals`,
			`DROP TABLE proposals`,
			`ALTER TABLE proposals_old RENAME TO proposals`,
			`CREATE INDEX proposals_status ON proposals (status)`,
			`CREATE INDEX proposals_change_set_id ON proposals (change_set_id)`,
			`CREATE INDEX proposals_task_status ON proposals (task_id, status)`,
		)
	})
}
