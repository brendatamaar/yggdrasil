//go:build !windows

package platform_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/platform/platformtest"
)

// A FIFO named .gitignore must be refused, not block the open.
func TestIgnoreWriterRefusesFIFOWithoutBlocking(t *testing.T) {
	root := platformtest.CanonicalTempDir(t)
	if err := syscall.Mkfifo(filepath.Join(root, ".gitignore"), 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	w := platform.NewIgnoreWriter()
	done := make(chan error, 1)
	go func() {
		_, err := w.ReadIgnore(context.Background(), root)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, platform.ErrUnsafePath) {
			t.Fatalf("FIFO: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ReadIgnore blocked on a FIFO")
	}
}

// Many writers with the same reviewed hash: exactly one can append, the others
// see a change (or a lock) and write nothing.
func TestAppendIgnoreConcurrentWritersAppendOnce(t *testing.T) {
	root := platformtest.CanonicalTempDir(t)
	path := filepath.Join(root, ".gitignore")
	seed := []byte("seed\n")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatal(err)
	}
	w := platform.NewIgnoreWriter()
	expect := platform.IgnoreExpectation{Hash: platform.HashHex(seed)}
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for _, e := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, _, err := w.AppendIgnore(context.Background(), root, []string{e}, expect)
			switch {
			case err == nil && out == platform.IgnoreAppended:
				mu.Lock()
				wins++
				mu.Unlock()
			case errors.Is(err, platform.ErrChanged), errors.Is(err, platform.ErrLocked):
			default:
				t.Errorf("writer %s: %q %v", e, out, err)
			}
		}()
	}
	wg.Wait()
	got, _ := os.ReadFile(path)
	if wins != 1 || len(got) != len("seed\n")+2 {
		t.Fatalf("wins=%d content=%q", wins, got)
	}
}

// A symlink anywhere on the way to a new repo is refused, also one that points
// to a folder that exists and is writable.
func TestSafeInitFSRefusesSymlinkedProjectParent(t *testing.T) {
	base := platformtest.CanonicalTempDir(t)
	real := filepath.Join(base, "real folder")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip(err)
	}
	_, err := platform.NewSafeInitFS().CreateChildDir(context.Background(), link, "shop-api")
	if !errors.Is(err, platform.ErrUnsafePath) {
		t.Fatalf("got %v", err)
	}
	if entries, _ := os.ReadDir(real); len(entries) != 0 {
		t.Fatalf("something was created through the link: %v", entries)
	}
}

// The created files are not world-writable and nothing is removed afterwards.
func TestSafeInitFSCreatedModes(t *testing.T) {
	parent := platformtest.CanonicalTempDir(t)
	s := platform.NewSafeInitFS()
	ctx := context.Background()
	id, err := s.CreateChildDir(ctx, parent, "shop-api")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateFile(ctx, parent, "shop-api", id, "README.md", []byte("x")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(parent, "shop-api"), filepath.Join(parent, "shop-api", "README.md")} {
		fi, err := os.Stat(p)
		if err != nil || fi.Mode().Perm()&0o002 != 0 {
			t.Fatalf("%s: %v %v", p, fi, err)
		}
	}
}
