package platformtest

import (
	"bytes"
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// RunIgnoreWriterContract is the contract suite of platform.IgnoreWriter, run
// against the real implementation and FakeIgnoreWriter. newW returns a fresh
// IgnoreWriter that also implements FixtureProvider and an existing, empty
// repo root it may use. Every clause checks the original bytes survive.
func RunIgnoreWriterContract(t *testing.T, newW func(t *testing.T) (platform.IgnoreWriter, string)) {
	ctx := context.Background()
	setup := func(t *testing.T) (platform.IgnoreWriter, FSFixture, string, string) {
		t.Helper()
		w, root := newW(t)
		return w, mustFixture(t, w), root, filepath.Join(root, ".gitignore")
	}
	hashOf := func(s string) platform.IgnoreExpectation {
		return platform.IgnoreExpectation{Hash: platform.HashHex([]byte(s))}
	}
	absent := platform.IgnoreExpectation{Absent: true}
	content := func(t *testing.T, fx FSFixture, path string) string {
		t.Helper()
		b, _ := fx.ReadFile(t, path)
		return string(b)
	}
	// appendTo seeds .gitignore with seed and appends entries to it.
	appendTo := func(t *testing.T, seed string, entries ...string) (platform.IgnoreOutcome, platform.IgnoreSnapshot, string) {
		t.Helper()
		w, fx, root, path := setup(t)
		fx.WriteFile(t, path, []byte(seed))
		out, snap, err := w.AppendIgnore(ctx, root, entries, hashOf(seed))
		if err != nil {
			t.Fatalf("AppendIgnore: %v", err)
		}
		got := content(t, fx, path)
		if snap.Hash != platform.HashHex([]byte(got)) || snap.Size != int64(len(got)) {
			t.Fatalf("snapshot %+v does not describe the resulting bytes %q", snap, got)
		}
		if !strings.HasPrefix(got, seed) {
			t.Fatalf("existing bytes were changed: %q -> %q", seed, got)
		}
		return out, snap, got
	}

	t.Run("read absent, empty and filled", func(t *testing.T) {
		w, fx, root, path := setup(t)
		snap, err := w.ReadIgnore(ctx, root)
		if err != nil || !snap.Absent || snap.Hash != "" {
			t.Fatalf("absent: %+v, %v", snap, err)
		}
		fx.WriteFile(t, path, nil)
		snap, err = w.ReadIgnore(ctx, root)
		if err != nil || snap.Absent || snap.Hash != platform.HashHex(nil) || snap.Size != 0 {
			t.Fatalf("empty file must differ from absent: %+v, %v", snap, err)
		}
		fx.WriteFile(t, path, []byte("a\n"))
		snap, err = w.ReadIgnore(ctx, root)
		if err != nil || snap.Hash != platform.HashHex([]byte("a\n")) || snap.Size != 2 {
			t.Fatalf("filled: %+v, %v", snap, err)
		}
	})

	t.Run("absent file is created exclusively", func(t *testing.T) {
		w, fx, root, path := setup(t)
		out, snap, err := w.AppendIgnore(ctx, root, []string{"node_modules/", "dist/", "node_modules/"}, absent)
		if err != nil || out != platform.IgnoreCreated {
			t.Fatalf("outcome %q, %v", out, err)
		}
		if got := content(t, fx, path); got != "node_modules/\ndist/\n" || snap.Hash != platform.HashHex([]byte(got)) {
			t.Fatalf("created %q (%+v)", got, snap)
		}
	})

	t.Run("expectation mismatches are a change and write nothing", func(t *testing.T) {
		w, fx, root, path := setup(t)
		_, _, err := w.AppendIgnore(ctx, root, []string{"x"}, hashOf("was here"))
		wantErr(t, "hash expected but file absent", err, platform.ErrChanged)
		if _, ok := fx.ReadFile(t, path); ok {
			t.Fatal("a file was created although a hash was expected")
		}
		fx.WriteFile(t, path, []byte("a\n"))
		_, _, err = w.AppendIgnore(ctx, root, []string{"x"}, absent)
		wantErr(t, "absent expected but file exists", err, platform.ErrChanged)
		_, _, err = w.AppendIgnore(ctx, root, []string{"x"}, hashOf("stale\n"))
		wantErr(t, "stale hash", err, platform.ErrChanged)
		if got := content(t, fx, path); got != "a\n" {
			t.Fatalf("a refused append changed the file: %q", got)
		}
	})

	t.Run("bad arguments are refused", func(t *testing.T) {
		w, fx, root, path := setup(t)
		fx.WriteFile(t, path, []byte("a\n"))
		for _, bad := range [][]string{nil, {""}, {"a\nb"}, {"a\rb"}, {"a\x00"}} {
			_, _, err := w.AppendIgnore(ctx, root, bad, hashOf("a\n"))
			wantErr(t, "entries "+strings.Join(bad, "|"), err, platform.ErrInvalidArgument)
		}
		for _, exp := range []platform.IgnoreExpectation{{}, {Absent: true, Hash: "x"}} {
			_, _, err := w.AppendIgnore(ctx, root, []string{"x"}, exp)
			wantErr(t, "expectation", err, platform.ErrInvalidArgument)
		}
		if got := content(t, fx, path); got != "a\n" {
			t.Fatalf("file changed: %q", got)
		}
	})

	t.Run("LF with final newline", func(t *testing.T) {
		out, _, got := appendTo(t, "a\nb\n", "c", "d")
		if out != platform.IgnoreAppended || got != "a\nb\nc\nd\n" {
			t.Fatalf("%q %q", out, got)
		}
	})

	t.Run("LF without final newline keeps it that way", func(t *testing.T) {
		_, _, got := appendTo(t, "a\nb", "c")
		if got != "a\nb\nc" {
			t.Fatalf("%q", got)
		}
	})

	t.Run("CRLF style is kept", func(t *testing.T) {
		_, _, got := appendTo(t, "a\r\nb\r\n", "c", "d")
		if got != "a\r\nb\r\nc\r\nd\r\n" {
			t.Fatalf("%q", got)
		}
		_, _, got = appendTo(t, "a\r\nb", "c")
		if got != "a\r\nb\r\nc" {
			t.Fatalf("CRLF without final newline: %q", got)
		}
	})

	t.Run("empty file gets entries with final newlines", func(t *testing.T) {
		_, _, got := appendTo(t, "", "a")
		if got != "a\n" {
			t.Fatalf("%q", got)
		}
	})

	t.Run("only missing entries are appended, by exact line", func(t *testing.T) {
		out, _, got := appendTo(t, "dist/\r\n# build\nnode_modules\n", "dist/", "node_modules/", "build", "build")
		if out != platform.IgnoreAppended || got != "dist/\r\n# build\nnode_modules\nnode_modules/\nbuild\n" {
			t.Fatalf("%q %q", out, got)
		}
	})

	t.Run("already present writes nothing", func(t *testing.T) {
		w, fx, root, path := setup(t)
		seed := "a\r\nb\r\n"
		fx.WriteFile(t, path, []byte(seed))
		out, snap, err := w.AppendIgnore(ctx, root, []string{"b", "a"}, hashOf(seed))
		if err != nil || out != platform.IgnoreAlreadyPresent || snap.Hash != platform.HashHex([]byte(seed)) {
			t.Fatalf("%q %+v %v", out, snap, err)
		}
		if got := content(t, fx, path); got != seed {
			t.Fatalf("file changed: %q", got)
		}
	})

	t.Run("repeat with the new hash is a no-op", func(t *testing.T) {
		w, fx, root, path := setup(t)
		_, snap, err := w.AppendIgnore(ctx, root, []string{"dist/"}, absent)
		if err != nil {
			t.Fatal(err)
		}
		first := content(t, fx, path)
		out, _, err := w.AppendIgnore(ctx, root, []string{"dist/"}, platform.IgnoreExpectation{Hash: snap.Hash})
		if err != nil || out != platform.IgnoreAlreadyPresent {
			t.Fatalf("%q %v", out, err)
		}
		if content(t, fx, path) != first {
			t.Fatal("repeat changed the file")
		}
	})

	t.Run("unicode and metacharacters are written verbatim", func(t *testing.T) {
		_, _, got := appendTo(t, "x\n", "ünï/*.log", "!keep.me", "[ab]?", `\#hash`, "sp ace/")
		if got != "x\nünï/*.log\n!keep.me\n[ab]?\n\\#hash\nsp ace/\n" {
			t.Fatalf("%q", got)
		}
	})

	t.Run("a huge file is refused", func(t *testing.T) {
		w, fx, root, path := setup(t)
		big := bytes.Repeat([]byte("x\n"), (1<<20)/2+10)
		fx.WriteFile(t, path, big)
		_, err := w.ReadIgnore(ctx, root)
		wantErr(t, "read", err, platform.ErrTooLarge)
		_, _, err = w.AppendIgnore(ctx, root, []string{"y"}, platform.IgnoreExpectation{Hash: platform.HashHex(big)})
		wantErr(t, "append", err, platform.ErrTooLarge)
		if got, _ := fx.ReadFile(t, path); !bytes.Equal(got, big) {
			t.Fatal("a refused file was changed")
		}
	})

	t.Run("a symlinked or folder .gitignore is refused", func(t *testing.T) {
		w, fx, root, path := setup(t)
		other := filepath.Join(root, "outside.txt")
		fx.WriteFile(t, other, []byte("secret\n"))
		if fx.Symlink(t, path, other) {
			_, err := w.ReadIgnore(ctx, root)
			wantErr(t, "read through link", err, platform.ErrUnsafePath)
			_, _, err = w.AppendIgnore(ctx, root, []string{"x"}, hashOf("secret\n"))
			wantErr(t, "append through link", err, platform.ErrUnsafePath)
			_, _, err = w.AppendIgnore(ctx, root, []string{"x"}, absent)
			if err == nil {
				t.Fatal("append through a link with an absent expectation succeeded")
			}
			if got := content(t, fx, other); got != "secret\n" {
				t.Fatalf("the link target was changed: %q", got)
			}
		} else {
			t.Log("symlinks are not available here; link clause skipped")
		}
		w2, fx2, root2, path2 := setup(t)
		fx2.Mkdir(t, path2)
		_, err := w2.ReadIgnore(ctx, root2)
		wantErr(t, "folder named .gitignore", err, platform.ErrUnsafePath)
		_, _, err = w2.AppendIgnore(ctx, root2, []string{"x"}, absent)
		if err == nil {
			t.Fatal("a folder named .gitignore was accepted")
		}
	})

	t.Run("a locked file is a retryable error", func(t *testing.T) {
		w, fx, root, path := setup(t)
		if fx.Lock == nil {
			t.Skip("this disk cannot lock files")
		}
		fx.WriteFile(t, path, []byte("a\n"))
		release := fx.Lock(t, path)
		_, _, err := w.AppendIgnore(ctx, root, []string{"x"}, hashOf("a\n"))
		release()
		wantErr(t, "locked", err, platform.ErrLocked)
		if got := content(t, fx, path); got != "a\n" {
			t.Fatalf("a locked file was changed: %q", got)
		}
		out, _, err := w.AppendIgnore(ctx, root, []string{"x"}, hashOf("a\n"))
		if err != nil || out != platform.IgnoreAppended {
			t.Fatalf("retry after release: %q %v", out, err)
		}
	})

	t.Run("paths with spaces, non-ASCII and long names", func(t *testing.T) {
		w, fx, root, _ := setup(t)
		for _, dir := range []string{
			filepath.Join(root, "my projects", "ünï côdé"),
			filepath.Join(root, strings.Repeat("d", 90), strings.Repeat("e", 90), strings.Repeat("f", 90)),
		} {
			fx.Mkdir(t, dir)
			out, _, err := w.AppendIgnore(ctx, dir, []string{"dist/"}, absent)
			if err != nil || out != platform.IgnoreCreated {
				t.Fatalf("%q: %q %v", dir, out, err)
			}
			snap, err := w.ReadIgnore(ctx, dir)
			if err != nil || snap.Absent {
				t.Fatalf("%q: read %+v %v", dir, snap, err)
			}
		}
	})

	t.Run("a missing root and a cancelled context", func(t *testing.T) {
		w, fx, root, path := setup(t)
		_, err := w.ReadIgnore(ctx, filepath.Join(root, "no-such"))
		if err == nil {
			t.Fatal("a missing root was accepted")
		}
		wantErr(t, "missing root", err, fs.ErrNotExist)
		fx.WriteFile(t, path, []byte("a\n"))
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		_, _, err = w.AppendIgnore(cctx, root, []string{"x"}, hashOf("a\n"))
		wantErr(t, "cancelled", err, context.Canceled)
		if got := content(t, fx, path); got != "a\n" {
			t.Fatalf("a cancelled call wrote: %q", got)
		}
	})
}
