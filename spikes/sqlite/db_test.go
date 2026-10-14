package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/spikes/sqlite/gen"
)

func openTemp(t *testing.T, dir string) *DB {
	t.Helper()
	d, err := Open(context.Background(), filepath.Join(dir, "ygg.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return d
}

func seedProject(t *testing.T, d *DB) gen.Project {
	t.Helper()
	p, err := d.Writer().CreateProject(context.Background(), "acme-shop")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return p
}

func pragma(t *testing.T, db *sql.DB, name string) string {
	t.Helper()
	var v string
	if err := db.QueryRow("PRAGMA " + name).Scan(&v); err != nil {
		t.Fatalf("PRAGMA %s: %v", name, err)
	}
	return v
}

func TestMigrateInsertSelect(t *testing.T) {
	ctx := context.Background()
	d := openTemp(t, t.TempDir())

	for name, want := range map[string]string{
		"journal_mode": "wal",
		"foreign_keys": "1",
		"synchronous":  "1", // NORMAL
		"busy_timeout": "5000",
	} {
		if got := pragma(t, d.Write, name); got != want {
			t.Errorf("writer PRAGMA %s = %q, want %q", name, got, want)
		}
		if got := pragma(t, d.Read, name); got != want {
			t.Errorf("reader PRAGMA %s = %q, want %q", name, got, want)
		}
	}

	p := seedProject(t, d)
	if p.ID == 0 || p.CreatedAt == 0 {
		t.Fatalf("CreateProject returned %+v", p)
	}
	for _, name := range []string{"shop-web", "shop-api"} {
		if _, err := d.Writer().CreateRepo(ctx, gen.CreateRepoParams{
			ProjectID: p.ID, Name: name, Path: `C:\code\` + name,
		}); err != nil {
			t.Fatalf("CreateRepo %s: %v", name, err)
		}
	}

	got, err := d.Reader().GetProjectByName(ctx, "acme-shop")
	if err != nil || got != p {
		t.Fatalf("GetProjectByName = %+v, %v; want %+v", got, err, p)
	}
	repos, err := d.Reader().ListReposByProject(ctx, p.ID)
	if err != nil {
		t.Fatalf("ListReposByProject: %v", err)
	}
	if len(repos) != 2 || repos[0].Name != "shop-api" || repos[1].Name != "shop-web" {
		t.Fatalf("ListReposByProject = %+v", repos)
	}

	if _, err := d.Reader().CreateProject(ctx, "nope"); err == nil {
		t.Fatal("write through the read pool succeeded, want query_only error")
	}
}

func TestForeignKeyEnforced(t *testing.T) {
	ctx := context.Background()
	d := openTemp(t, t.TempDir())

	_, err := d.Writer().CreateRepo(ctx, gen.CreateRepoParams{ProjectID: 999, Name: "shop-api", Path: "x"})
	if err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Fatalf("CreateRepo with unknown project: err = %v, want FOREIGN KEY constraint error", err)
	}

	p := seedProject(t, d)
	if _, err := d.Writer().CreateRepo(ctx, gen.CreateRepoParams{ProjectID: p.ID, Name: "shop-api", Path: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := d.Writer().DeleteProject(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := d.Reader().CountRepos(ctx); err != nil || n != 0 {
		t.Fatalf("CountRepos after cascade = %d, %v; want 0", n, err)
	}
}

func TestPathWithSpaces(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "My Projects", "ygg data")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	d := openTemp(t, dir)
	seedProject(t, d)

	if _, err := os.Stat(filepath.Join(dir, "ygg.db")); err != nil {
		t.Fatalf("database not created at the path with spaces: %v", err)
	}
	if _, err := Open(context.Background(), filepath.Join(dir, "bad?.db")); err == nil {
		t.Fatal("Open accepted a path with '?'")
	}
}

func TestConcurrentWriters(t *testing.T) {
	const writers, perWriter = 8, 200
	ctx := context.Background()
	d := openTemp(t, t.TempDir())
	p := seedProject(t, d)

	start := time.Now()
	var wg sync.WaitGroup
	errs := make(chan error, writers*perWriter)
	stop := make(chan struct{})
	readerDone := make(chan int)
	var readErr error

	// A reader runs the whole time to show WAL readers do not block writers.
	go func() {
		reads := 0
		for readErr == nil {
			select {
			case <-stop:
				readerDone <- reads
				return
			default:
			}
			_, readErr = d.Reader().CountRepos(ctx)
			reads++
		}
		<-stop
		readerDone <- reads
	}()

	for w := range writers {
		wg.Go(func() {
			for i := range perWriter {
				name := fmt.Sprintf("shop-api-%d-%d", w, i)
				arg := gen.CreateRepoParams{ProjectID: p.ID, Name: name, Path: name}
				var err error
				if i%2 == 0 {
					_, err = d.Writer().CreateRepo(ctx, arg)
				} else {
					err = d.WithTx(ctx, func(q *gen.Queries) error {
						_, err := q.CreateRepo(ctx, arg)
						return err
					})
				}
				if err != nil {
					errs <- fmt.Errorf("writer %d insert %d: %w", w, i, err)
				}
			}
		})
	}
	wg.Wait()
	close(stop)
	reads := <-readerDone
	close(errs)

	if readErr != nil {
		t.Errorf("reader: %v", readErr)
	}
	for err := range errs {
		t.Error(err)
	}
	n, err := d.Reader().CountRepos(ctx)
	if err != nil || n != writers*perWriter {
		t.Fatalf("CountRepos = %d, %v; want %d", n, err, writers*perWriter)
	}
	t.Logf("%d writers x %d inserts in %v, %d concurrent reads", writers, perWriter, time.Since(start), reads)
}

func TestUpTwiceIsNoop(t *testing.T) {
	ctx := context.Background()
	d := openTemp(t, t.TempDir()) // first Up

	p, err := NewMigrator(d.Write)
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Up(ctx)
	if err != nil {
		t.Fatalf("second Up: %v", err)
	}
	if len(res) != 0 {
		t.Fatalf("second Up applied %d migrations, want 0", len(res))
	}
	if v, err := p.GetDBVersion(ctx); err != nil || v != 2 {
		t.Fatalf("GetDBVersion = %d, %v; want 2", v, err)
	}
}

func TestDownMigration(t *testing.T) {
	ctx := context.Background()
	d := openTemp(t, t.TempDir())
	seedProject(t, d)

	p, err := NewMigrator(d.Write)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Down(ctx); err != nil {
		t.Fatalf("Down: %v", err)
	}
	if v, err := p.GetDBVersion(ctx); err != nil || v != 1 {
		t.Fatalf("GetDBVersion after Down = %d, %v; want 1", v, err)
	}
	if hasTable(t, d.Write, "repos") || !hasTable(t, d.Write, "projects") {
		t.Fatal("Down did not drop exactly the repos table")
	}

	// Rolling back to 0 drops everything; Up brings it back.
	if _, err := p.DownTo(ctx, 0); err != nil {
		t.Fatalf("DownTo(0): %v", err)
	}
	if hasTable(t, d.Write, "projects") {
		t.Fatal("projects table still exists at version 0")
	}
	if res, err := p.Up(ctx); err != nil || len(res) != 2 {
		t.Fatalf("Up after DownTo(0) = %d results, %v; want 2", len(res), err)
	}

	// The provider is not closed (Close would close d.Write), so the pool
	// is still usable.
	if err := d.Write.PingContext(ctx); err != nil {
		t.Fatalf("write pool unusable after migrations: %v", err)
	}
}

func hasTable(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return true
}
