package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/pressly/goose/v3"
)

// Migration 00003 (docs/design/repo-tracking.md, "Persistence and delivery"):
//
//   - soft untrack: projects.untracked_at and repos.untracked_at;
//   - projects.version, for expected-version updates of a decision;
//   - repos.rel_path, the repo's folder under its project, unique per project;
//   - the project name is unique among ACTIVE projects only (partial index),
//     which needs a rebuild of projects: SQLite cannot drop a column UNIQUE;
//   - settings, a small key/value table for user settings such as the editor.
//
// It is a Go migration because the rebuild drops projects while repos, tasks
// and build_presets point at it with ON DELETE CASCADE, and foreign_keys
// cannot be switched off inside a transaction. The migration pins ONE
// connection, switches foreign keys off on it, runs the steps in a
// transaction, checks PRAGMA foreign_key_check before it commits, and always
// puts foreign_keys back on, whether it succeeded or not.
func migration00003() *goose.Migration {
	return goose.NewGoMigration(3,
		&goose.GoFunc{RunDB: upRepoTracking, Mode: goose.TransactionDisabled},
		&goose.GoFunc{RunDB: downRepoTracking, Mode: goose.TransactionDisabled},
	)
}

// rebuildTx runs fn on a pinned connection with foreign keys off, inside one
// transaction that commits only if foreign_key_check finds nothing.
func rebuildTx(ctx context.Context, db *sql.DB, fn func(ctx context.Context, tx *sql.Tx) error) (err error) {
	// goose keeps one connection of the pool pinned while a Go migration
	// runs. The write pool has exactly one, so make room for a second for the
	// duration of the migration.
	if n := db.Stats().MaxOpenConnections; n > 0 && n < 2 {
		db.SetMaxOpenConns(2)
		defer db.SetMaxOpenConns(n)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("pin a connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("switch foreign keys off: %w", err)
	}
	// Runs before conn.Close, and even when ctx is cancelled: a pooled
	// connection must never go back with foreign keys off.
	defer func() {
		if _, perr := conn.ExecContext(context.WithoutCancel(ctx), `PRAGMA foreign_keys = ON`); perr != nil {
			err = errors.Join(err, fmt.Errorf("switch foreign keys back on: %w", perr))
		}
	}()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit
	if err := fn(ctx, tx); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("foreign key check: %w", err)
	}
	broken := rows.Next()
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return fmt.Errorf("foreign key check: %w", err)
	}
	if broken {
		return errors.New("the migration would leave rows that point at missing rows; nothing was changed")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func execAll(ctx context.Context, tx *sql.Tx, stmts ...string) error {
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("%w\n%s", err, s)
		}
	}
	return nil
}

func upRepoTracking(ctx context.Context, db *sql.DB) error {
	return rebuildTx(ctx, db, func(ctx context.Context, tx *sql.Tx) error {
		if err := execAll(ctx, tx,
			`CREATE TABLE projects_new (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    path         TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    untracked_at TEXT,
    version      INTEGER NOT NULL DEFAULT 0
) STRICT`,
			`INSERT INTO projects_new (id, name, path, created_at) SELECT id, name, path, created_at FROM projects`,
			`DROP TABLE projects`,
			`ALTER TABLE projects_new RENAME TO projects`,
			`CREATE UNIQUE INDEX projects_active_name ON projects (name) WHERE untracked_at IS NULL`,
			`ALTER TABLE repos ADD COLUMN untracked_at TEXT`,
			`ALTER TABLE repos ADD COLUMN rel_path TEXT NOT NULL DEFAULT '.'`,
		); err != nil {
			return err
		}
		if err := fillRelPaths(ctx, tx); err != nil {
			return err
		}
		return execAll(ctx, tx,
			`CREATE UNIQUE INDEX repos_project_rel_path ON repos (project_id, rel_path)`,
			`CREATE TABLE settings (
    key     TEXT PRIMARY KEY,
    value   TEXT NOT NULL,
    version INTEGER NOT NULL DEFAULT 0
) STRICT`,
		)
	})
}

// fillRelPaths derives rel_path for the existing repos from their paths. A
// repo outside its project folder gets a slug of its name, with its ID added
// when the slug is taken, so the value always passes the core's rules. Two
// repos that derive the same folder are reported, not repaired.
func fillRelPaths(ctx context.Context, tx *sql.Tx) error {
	type row struct{ id, project, name, path, projectPath string }
	rows, err := tx.QueryContext(ctx, `SELECT r.id, r.project_id, r.name, r.path, p.path
		FROM repos r JOIN projects p ON p.id = r.project_id ORDER BY r.id`)
	if err != nil {
		return fmt.Errorf("read repos: %w", err)
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.project, &r.name, &r.path, &r.projectPath); err != nil {
			_ = rows.Close()
			return fmt.Errorf("read repos: %w", err)
		}
		all = append(all, r)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return fmt.Errorf("read repos: %w", err)
	}
	taken := map[string]map[string]bool{}
	var outside []row
	for _, r := range all {
		if taken[r.project] == nil {
			taken[r.project] = map[string]bool{}
		}
		rel, ok := derivedRel(r.projectPath, r.path)
		if !ok {
			outside = append(outside, r)
			continue
		}
		if taken[r.project][rel] {
			return fmt.Errorf("two repos of project %s lie in the folder %q; stop tracking one of them and try again", r.project, rel)
		}
		taken[r.project][rel] = true
		if err := setRel(ctx, tx, r.id, rel); err != nil {
			return err
		}
	}
	for _, r := range outside {
		if err := setRel(ctx, tx, r.id, fallbackRel(r.name, r.id, taken[r.project])); err != nil {
			return err
		}
	}
	return nil
}

func setRel(ctx context.Context, tx *sql.Tx, id, rel string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE repos SET rel_path = ? WHERE id = ?`, rel, id); err != nil {
		return fmt.Errorf("set the folder of repo %s: %w", id, err)
	}
	return nil
}

func downRepoTracking(ctx context.Context, db *sql.DB) error {
	return rebuildTx(ctx, db, func(ctx context.Context, tx *sql.Tx) error {
		return execAll(ctx, tx,
			`DROP TABLE settings`,
			`DROP INDEX repos_project_rel_path`,
			`ALTER TABLE repos DROP COLUMN rel_path`,
			`ALTER TABLE repos DROP COLUMN untracked_at`,
			// 00001 wants one name per project. Untracked projects may share a
			// name with an active one, so make theirs unique; nothing is deleted.
			`UPDATE projects SET name = name || ' (' || id || ')' WHERE untracked_at IS NOT NULL`,
			`CREATE TABLE projects_old (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    path       TEXT NOT NULL,
    created_at TEXT NOT NULL
) STRICT`,
			`INSERT INTO projects_old (id, name, path, created_at) SELECT id, name, path, created_at FROM projects`,
			`DROP TABLE projects`,
			`ALTER TABLE projects_old RENAME TO projects`,
		)
	})
}
