// Package db is a throwaway spike for the Phase 1 internal/db package:
// pure-Go SQLite (modernc.org/sqlite, no cgo), sqlc queries and goose
// migrations embedded in the binary.
package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"runtime"
	"strings"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"github.com/brendatamaar/yggdrasil/spikes/sqlite/gen"
)

//go:embed migrations/*.sql
var migrations embed.FS

// driverName is the name modernc.org/sqlite registers with database/sql.
const driverName = "sqlite"

// busyTimeout is how long a connection waits for a lock before SQLITE_BUSY.
const busyTimeout = 5 * time.Second

// DB holds two pools on the same file. SQLite allows one writer at a time,
// so the write pool has exactly one connection and writers queue in
// database/sql instead of spinning on busy_timeout. WAL lets readers run
// next to that writer, so the read pool has several query_only connections.
type DB struct {
	Write *sql.DB
	Read  *sql.DB
}

// DSN returns the modernc.org/sqlite data source name for path. The path is
// passed to SQLite as is (no "file:" URI), so spaces and backslashes need no
// escaping; only '?' is rejected because it starts the parameter list.
func DSN(path string, readOnly bool) (string, error) {
	if path == "" {
		return "", errors.New("database path is empty")
	}
	if strings.Contains(path, "?") {
		return "", fmt.Errorf("database path %q must not contain '?'", path)
	}
	q := url.Values{}
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeout.Milliseconds()))
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(NORMAL)")
	if readOnly {
		q.Set("_query_only", "1")
	} else {
		// BEGIN IMMEDIATE takes the write lock up front, so a transaction
		// never fails later when it upgrades from read to write.
		q.Set("_txlock", "immediate")
	}
	return path + "?" + q.Encode(), nil
}

// Open opens both pools, runs pending migrations on the write pool and
// returns a DB ready to use.
func Open(ctx context.Context, path string) (*DB, error) {
	w, err := openPool(ctx, path, false, 1)
	if err != nil {
		return nil, fmt.Errorf("open database for writing: %w", err)
	}
	if err := Migrate(ctx, w); err != nil {
		w.Close()
		return nil, err
	}
	r, err := openPool(ctx, path, true, max(4, runtime.NumCPU()))
	if err != nil {
		w.Close()
		return nil, fmt.Errorf("open database for reading: %w", err)
	}
	return &DB{Write: w, Read: r}, nil
}

func openPool(ctx context.Context, path string, readOnly bool, conns int) (*sql.DB, error) {
	dsn, err := DSN(path, readOnly)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(conns)
	db.SetMaxIdleConns(conns)
	// Keep connections for the life of the process: every new one replays
	// the pragmas, and closing the last one checkpoints the WAL.
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// Close closes both pools.
func (d *DB) Close() error {
	return errors.Join(d.Read.Close(), d.Write.Close())
}

// Writer returns queries bound to the single-connection write pool.
func (d *DB) Writer() *gen.Queries { return gen.New(d.Write) }

// Reader returns queries bound to the read-only pool.
func (d *DB) Reader() *gen.Queries { return gen.New(d.Read) }

// WithTx runs fn in a write transaction (BEGIN IMMEDIATE). fn must only use
// the queries it gets: calling d.Writer() inside it would wait forever for
// the one write connection the transaction holds.
func (d *DB) WithTx(ctx context.Context, fn func(*gen.Queries) error) error {
	tx, err := d.Write.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	if err := fn(gen.New(d.Write).WithTx(tx)); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// NewMigrator returns a goose provider for the embedded migrations. Do not
// call its Close method: it closes db, which the caller still owns.
func NewMigrator(db *sql.DB) (*goose.Provider, error) {
	fsys, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, db, fsys)
	if err != nil {
		return nil, fmt.Errorf("load migrations: %w", err)
	}
	return p, nil
}

// Migrate applies all pending migrations.
func Migrate(ctx context.Context, db *sql.DB) error {
	p, err := NewMigrator(db)
	if err != nil {
		return err
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	return nil
}
