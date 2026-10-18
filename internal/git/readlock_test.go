package git

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A status read must never take index.lock or rewrite the index: it would
// block the user's own git or an agent's (docs/design/repo-status.md, S3).
// Git refreshes stale stat data in the index during a plain `git status`; with
// --no-optional-locks it doesn't, so the index file stays byte for byte equal.
func TestStatusTakesNoIndexLock(t *testing.T) {
	f := newFx(t)
	dir := f.repo("lockless")
	f.commit(dir, "a.txt", "one\n", "add a")

	// Make the cached stat data of a.txt stale without changing its content.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "a.txt"), old, old); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(dir, ".git", "index")
	before, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}

	st, err := f.g.Status(bg(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Dirty() {
		t.Fatalf("content did not change, but status says dirty: %+v", st)
	}
	after, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("Status rewrote the index: it ran without --no-optional-locks")
	}
	if _, err := os.Stat(index + ".lock"); !os.IsNotExist(err) {
		t.Errorf("index.lock exists after Status (stat error: %v)", err)
	}

	// The premise: a plain status does refresh the index. If it does not on
	// this git version, the check above proved nothing, so say so.
	f.git(dir, "status", "--short")
	refreshed, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(before, refreshed) {
		t.Skip("this git version does not rewrite the index on a plain status; the check above is vacuous here")
	}
}

// Another git process holding index.lock must not make a read fail.
func TestStatusWhileIndexIsLocked(t *testing.T) {
	f := newFx(t)
	dir := f.repo("locked")
	f.commit(dir, "a.txt", "one\n", "add a")
	lock := filepath.Join(dir, ".git", "index.lock")
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.g.Status(bg(), dir); err != nil {
		t.Fatalf("Status with index.lock held by someone else: %v", err)
	}
	if _, err := os.Stat(lock); err != nil {
		t.Fatalf("Status touched somebody else's index.lock: %v", err)
	}
}
