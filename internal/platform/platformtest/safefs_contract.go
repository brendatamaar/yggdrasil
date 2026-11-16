package platformtest

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/platform"
)

func mustFixture(t *testing.T, v any) FSFixture {
	t.Helper()
	p, ok := v.(FixtureProvider)
	if !ok {
		t.Fatalf("%T does not provide a fixture (wrap it with OSSafeInitFS/OSIgnoreWriter or use a Fake)", v)
	}
	return p.Fixture()
}

func wantErr(t *testing.T, what string, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: got %v, want an error matching %v", what, err, want)
	}
}

// RunSafeInitFSContract is the contract suite of platform.SafeInitFS, run
// against the real implementation and FakeSafeInitFS (docs/AGENTS.md 5.11).
// newFS returns a fresh SafeInitFS that also implements FixtureProvider, and
// an existing, canonical, empty parent folder it may use. Every clause checks
// that nothing pre-existing was changed or removed.
func RunSafeInitFSContract(t *testing.T, newFS func(t *testing.T) (platform.SafeInitFS, string)) {
	ctx := context.Background()
	setup := func(t *testing.T) (platform.SafeInitFS, FSFixture, string) {
		t.Helper()
		s, parent := newFS(t)
		fx := mustFixture(t, s)
		return s, fx, parent
	}
	create := func(t *testing.T, s platform.SafeInitFS, parent, leaf string) platform.DirIdentity {
		t.Helper()
		id, err := s.CreateChildDir(ctx, parent, leaf)
		if err != nil {
			t.Fatalf("CreateChildDir(%q): %v", leaf, err)
		}
		if id == "" {
			t.Fatal("CreateChildDir returned an empty identity")
		}
		return id
	}
	swap := func(t *testing.T, fx FSFixture, dir string) {
		t.Helper()
		fx.Move(t, dir, dir+".swapped")
		fx.Mkdir(t, dir)
	}

	t.Run("create child dir and verify it", func(t *testing.T) {
		s, _, parent := setup(t)
		id := create(t, s, parent, "shop-api")
		if err := s.VerifyChildDir(ctx, parent, "shop-api", id); err != nil {
			t.Fatalf("VerifyChildDir: %v", err)
		}
	})

	t.Run("existing entries are never reused", func(t *testing.T) {
		s, fx, parent := setup(t)
		id := create(t, s, parent, "shop-api")
		_, err := s.CreateChildDir(ctx, parent, "shop-api")
		wantErr(t, "second create", err, platform.ErrExists)
		if err := s.VerifyChildDir(ctx, parent, "shop-api", id); err != nil {
			t.Fatalf("the first folder must be untouched: %v", err)
		}
		fx.Mkdir(t, filepath.Join(parent, "empty-folder"))
		_, err = s.CreateChildDir(ctx, parent, "empty-folder")
		wantErr(t, "existing empty folder", err, platform.ErrExists)
		fx.WriteFile(t, filepath.Join(parent, "a-file"), []byte("keep"))
		_, err = s.CreateChildDir(ctx, parent, "a-file")
		wantErr(t, "existing file", err, platform.ErrExists)
		if got, _ := fx.ReadFile(t, filepath.Join(parent, "a-file")); string(got) != "keep" {
			t.Fatalf("an existing file was changed: %q", got)
		}
	})

	t.Run("verify detects other identity, swap, missing and bad input", func(t *testing.T) {
		s, fx, parent := setup(t)
		id := create(t, s, parent, "shop-web")
		other := create(t, s, parent, "shop-admin")
		wantErr(t, "wrong identity", s.VerifyChildDir(ctx, parent, "shop-web", other), platform.ErrChanged)
		wantErr(t, "empty identity", s.VerifyChildDir(ctx, parent, "shop-web", ""), platform.ErrInvalidArgument)
		wantErr(t, "missing folder", s.VerifyChildDir(ctx, parent, "no-such", id), platform.ErrChanged)
		swap(t, fx, filepath.Join(parent, "shop-web"))
		wantErr(t, "swapped folder", s.VerifyChildDir(ctx, parent, "shop-web", id), platform.ErrChanged)
	})

	t.Run("a link in place of the folder is a change", func(t *testing.T) {
		s, fx, parent := setup(t)
		id := create(t, s, parent, "shop-web")
		dir := filepath.Join(parent, "shop-web")
		fx.Move(t, dir, dir+".old")
		if !fx.Symlink(t, dir, dir+".old") {
			t.Skip("symlinks are not available here")
		}
		wantErr(t, "verify", s.VerifyChildDir(ctx, parent, "shop-web", id), platform.ErrChanged)
		_, err := s.CreateFile(ctx, parent, "shop-web", id, "README.md", []byte("x"))
		wantErr(t, "create file", err, platform.ErrChanged)
		if _, ok := fx.ReadFile(t, filepath.Join(dir+".old", "README.md")); ok {
			t.Fatal("a file was written through the link")
		}
	})

	t.Run("create file is exclusive and exact", func(t *testing.T) {
		s, fx, parent := setup(t)
		id := create(t, s, parent, "shop-api")
		content := []byte("# shop-api\r\nline two\n\x00binary tail \xff é\n")
		hash, err := s.CreateFile(ctx, parent, "shop-api", id, "README.md", content)
		if err != nil {
			t.Fatalf("CreateFile: %v", err)
		}
		if hash != platform.HashHex(content) {
			t.Fatalf("hash %s, want the sha256 of the content", hash)
		}
		got, ok := fx.ReadFile(t, filepath.Join(parent, "shop-api", "README.md"))
		if !ok || !bytes.Equal(got, content) {
			t.Fatalf("file content %q (exists %v), want exactly %q", got, ok, content)
		}
		_, err = s.CreateFile(ctx, parent, "shop-api", id, "README.md", []byte("other"))
		wantErr(t, "second create", err, platform.ErrExists)
		got, _ = fx.ReadFile(t, filepath.Join(parent, "shop-api", "README.md"))
		if !bytes.Equal(got, content) {
			t.Fatal("an existing file was overwritten")
		}
		fx.WriteFile(t, filepath.Join(parent, "shop-api", "LICENSE"), []byte("foreign"))
		_, err = s.CreateFile(ctx, parent, "shop-api", id, "LICENSE", []byte("ours"))
		wantErr(t, "file made by another process", err, platform.ErrExists)
		got, _ = fx.ReadFile(t, filepath.Join(parent, "shop-api", "LICENSE"))
		if string(got) != "foreign" {
			t.Fatal("a file supplied by another process was overwritten")
		}
		h, err := s.CreateFile(ctx, parent, "shop-api", id, "empty.txt", nil)
		if err != nil || h != platform.HashHex(nil) {
			t.Fatalf("empty file: %q, %v", h, err)
		}
	})

	t.Run("create file after a swap is refused and writes nothing", func(t *testing.T) {
		s, fx, parent := setup(t)
		id := create(t, s, parent, "shop-api")
		swap(t, fx, filepath.Join(parent, "shop-api"))
		_, err := s.CreateFile(ctx, parent, "shop-api", id, "README.md", []byte("x"))
		wantErr(t, "swapped folder", err, platform.ErrChanged)
		if _, ok := fx.ReadFile(t, filepath.Join(parent, "shop-api", "README.md")); ok {
			t.Fatal("a file was written into the swapped folder")
		}
		_, _, err = s.FileHash(ctx, parent, "shop-api", id, "README.md")
		wantErr(t, "FileHash on swapped folder", err, platform.ErrChanged)
	})

	t.Run("file hash reports absent and present", func(t *testing.T) {
		s, _, parent := setup(t)
		id := create(t, s, parent, "shop-api")
		if _, exists, err := s.FileHash(ctx, parent, "shop-api", id, "README.md"); err != nil || exists {
			t.Fatalf("absent file: exists=%v err=%v", exists, err)
		}
		hash, err := s.CreateFile(ctx, parent, "shop-api", id, "README.md", []byte("hello\n"))
		if err != nil {
			t.Fatal(err)
		}
		got, exists, err := s.FileHash(ctx, parent, "shop-api", id, "README.md")
		if err != nil || !exists || got != hash {
			t.Fatalf("FileHash = %q, %v, %v; want %q, true, nil", got, exists, err, hash)
		}
	})

	t.Run("a symlink ancestor is refused", func(t *testing.T) {
		s, fx, parent := setup(t)
		real := filepath.Join(parent, "real")
		fx.Mkdir(t, real)
		link := filepath.Join(parent, "link")
		if !fx.Symlink(t, link, real) {
			t.Skip("symlinks are not available here")
		}
		_, err := s.CreateChildDir(ctx, link, "shop-api")
		wantErr(t, "create below a link", err, platform.ErrUnsafePath)
		deeper := filepath.Join(link, "sub")
		fx.Mkdir(t, filepath.Join(real, "sub"))
		_, err = s.CreateChildDir(ctx, deeper, "shop-api")
		wantErr(t, "create two levels below a link", err, platform.ErrUnsafePath)
		if _, ok := fx.ReadFile(t, filepath.Join(real, "shop-api")); ok {
			t.Fatal("something was created through the link")
		}
		id := create(t, s, real, "ok")
		wantErr(t, "verify below a link", s.VerifyChildDir(ctx, link, "ok", id), platform.ErrUnsafePath)
	})

	t.Run("unsafe names and paths are refused", func(t *testing.T) {
		s, fx, parent := setup(t)
		id := create(t, s, parent, "shop-api")
		for _, name := range []string{"", ".", "..", "a/b", `a\b`, "a:b", "NUL", "com1.txt", "x ", "x.", "PROGRA~1", "a\x00b", "a*b"} {
			_, err := s.CreateChildDir(ctx, parent, name)
			wantErr(t, "leaf "+name, err, platform.ErrUnsafePath)
			_, err = s.CreateFile(ctx, parent, "shop-api", id, name, []byte("x"))
			wantErr(t, "file name "+name, err, platform.ErrUnsafePath)
		}
		_, err := s.CreateChildDir(ctx, "relative/dir", "x")
		wantErr(t, "relative parent", err, platform.ErrUnsafePath)
		_, err = s.CreateChildDir(ctx, filepath.Join(parent, "does-not-exist"), "x")
		wantErr(t, "missing parent", err, fs.ErrNotExist)
		fx.WriteFile(t, filepath.Join(parent, "plain-file"), []byte("x"))
		if _, err = s.CreateChildDir(ctx, filepath.Join(parent, "plain-file"), "x"); err == nil {
			t.Fatal("a file as the parent was accepted")
		}
	})

	t.Run("spaces, non-ASCII and long paths", func(t *testing.T) {
		s, fx, parent := setup(t)
		odd := filepath.Join(parent, "my projects", "ünï côdé")
		fx.Mkdir(t, odd)
		id := create(t, s, odd, "répo with space")
		if _, err := s.CreateFile(ctx, odd, "répo with space", id, "ünï.txt", []byte("ok")); err != nil {
			t.Fatalf("non-ASCII names: %v", err)
		}
		long := filepath.Join(parent, strings.Repeat("d", 90), strings.Repeat("e", 90), strings.Repeat("f", 90))
		fx.Mkdir(t, long)
		lid := create(t, s, long, "deep-repo")
		if _, err := s.CreateFile(ctx, long, "deep-repo", lid, "README.md", []byte("deep")); err != nil {
			t.Fatalf("long path: %v", err)
		}
		if err := s.VerifyChildDir(ctx, long, "deep-repo", lid); err != nil {
			t.Fatalf("long path verify: %v", err)
		}
	})

	t.Run("a cancelled context does nothing", func(t *testing.T) {
		s, _, parent := setup(t)
		id := create(t, s, parent, "shop-api")
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		_, err := s.CreateChildDir(cctx, parent, "late")
		wantErr(t, "create", err, context.Canceled)
		_, err = s.CreateFile(cctx, parent, "shop-api", id, "README.md", []byte("x"))
		wantErr(t, "create file", err, context.Canceled)
		wantErr(t, "verify", s.VerifyChildDir(cctx, parent, "shop-api", id), context.Canceled)
		if _, err = s.CreateChildDir(ctx, parent, "late"); err != nil {
			t.Fatalf("the cancelled call left something behind: %v", err)
		}
	})
}
