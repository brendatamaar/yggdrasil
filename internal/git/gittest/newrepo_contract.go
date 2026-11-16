package gittest

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/git"
)

// InitWorld is what the RepoInitializer and OriginManager contract suites need
// from an implementation: real git in temp folders, or the in-memory fakes.
type InitWorld interface {
	// NewDir returns a fresh, empty, existing directory whose name holds a space.
	NewDir() string
	WriteFile(dir, rel, content string)
	// SetAuthor sets whether git knows the user's name and email.
	SetAuthor(known bool)
	// InstallHooks installs hooks that record when they run in dir's repo
	// (after InitRepo) and returns the probe.
	InstallHooks(dir string) (ran func() bool)
	Initializer() git.RepoInitializer
	Origins() git.OriginManager
}

var firstFiles = []string{"README.md", ".gitignore"}

func writeFirst(w InitWorld, dir, readme string) {
	w.WriteFile(dir, "README.md", readme)
	w.WriteFile(dir, ".gitignore", "node_modules/\n")
}

// RunRepoInitializerContract is the contract of git.RepoInitializer. Run it
// against the fake and the real implementation.
func RunRepoInitializerContract(t *testing.T, newWorld func(t *testing.T) InitWorld) {
	ctx := context.Background()

	t.Run("commits exactly the listed files and reads them back", func(t *testing.T) {
		w := newWorld(t)
		in := w.Initializer()
		dir := w.NewDir()
		if err := in.InitRepo(ctx, dir, "main"); err != nil {
			t.Fatal(err)
		}
		if ok, err := in.HasAuthor(ctx, dir); err != nil || !ok {
			t.Fatalf("HasAuthor = %v, %v; want true", ok, err)
		}
		if _, found, err := in.ReadFirstCommit(ctx, dir, "main"); err != nil || found {
			t.Fatalf("ReadFirstCommit before commit = found %v, %v", found, err)
		}
		writeFirst(w, dir, "# Shop API\n")
		w.WriteFile(dir, "junk.txt", "not listed\n")
		fc, err := in.CommitFirst(ctx, dir, firstFiles, "Initial commit")
		if err != nil {
			t.Fatal(err)
		}
		if fc.Commit == "" || fc.Tree == "" || fc.Author == "" {
			t.Fatalf("incomplete first commit: %+v", fc)
		}
		if fc.MessageHash != git.MessageHash("Initial commit") {
			t.Fatalf("message hash %q, want %q", fc.MessageHash, git.MessageHash("Initial commit"))
		}
		got, found, err := in.ReadFirstCommit(ctx, dir, "main")
		if err != nil || !found || got != fc {
			t.Fatalf("ReadFirstCommit = %+v, %v, %v; want %+v", got, found, err, fc)
		}
		if _, found, err := in.ReadFirstCommit(ctx, dir, "other"); err != nil || found {
			t.Fatalf("other branch: found %v, %v", found, err)
		}

		// The same listed files without the extra file give the same tree.
		dir2 := w.NewDir()
		if err := in.InitRepo(ctx, dir2, "main"); err != nil {
			t.Fatal(err)
		}
		writeFirst(w, dir2, "# Shop API\n")
		fc2, err := in.CommitFirst(ctx, dir2, firstFiles, "Initial commit")
		if err != nil {
			t.Fatal(err)
		}
		if fc2.Tree != fc.Tree {
			t.Fatalf("the unlisted file changed the tree: %s vs %s", fc.Tree, fc2.Tree)
		}
	})

	t.Run("a missing author refuses before any write", func(t *testing.T) {
		w := newWorld(t)
		in := w.Initializer()
		dir := w.NewDir()
		if err := in.InitRepo(ctx, dir, "main"); err != nil {
			t.Fatal(err)
		}
		writeFirst(w, dir, "# Shop API\n")
		w.SetAuthor(false)
		if ok, err := in.HasAuthor(ctx, dir); err != nil || ok {
			t.Fatalf("HasAuthor = %v, %v; want false", ok, err)
		}
		if _, err := in.CommitFirst(ctx, dir, firstFiles, "Initial commit"); !errors.Is(err, git.ErrNoAuthor) {
			t.Fatalf("err = %v, want ErrNoAuthor", err)
		}
		if _, found, _ := in.ReadFirstCommit(ctx, dir, "main"); found {
			t.Fatal("a commit exists after the refusal")
		}
		w.SetAuthor(true)
		if _, err := in.CommitFirst(ctx, dir, firstFiles, "Initial commit"); err != nil {
			t.Fatalf("commit after the author was set: %v", err)
		}
	})

	t.Run("repo hooks do not run", func(t *testing.T) {
		w := newWorld(t)
		in := w.Initializer()
		dir := w.NewDir()
		if err := in.InitRepo(ctx, dir, "main"); err != nil {
			t.Fatal(err)
		}
		ran := w.InstallHooks(dir)
		writeFirst(w, dir, "# Shop API\n")
		if _, err := in.CommitFirst(ctx, dir, firstFiles, "Initial commit"); err != nil {
			t.Fatal(err)
		}
		if ran() {
			t.Fatal("a repo hook ran during the first commit")
		}
	})

	t.Run("a second first commit is refused", func(t *testing.T) {
		w := newWorld(t)
		in := w.Initializer()
		dir := w.NewDir()
		if err := in.InitRepo(ctx, dir, "main"); err != nil {
			t.Fatal(err)
		}
		writeFirst(w, dir, "# Shop API\n")
		if _, err := in.CommitFirst(ctx, dir, firstFiles, "Initial commit"); err != nil {
			t.Fatal(err)
		}
		if _, err := in.CommitFirst(ctx, dir, firstFiles, "Again"); !errors.Is(err, git.ErrNotFirstCommit) {
			t.Fatalf("err = %v, want ErrNotFirstCommit", err)
		}
	})

	t.Run("init refuses a folder that is not empty and a second init", func(t *testing.T) {
		w := newWorld(t)
		in := w.Initializer()
		dir := w.NewDir()
		w.WriteFile(dir, "mine.txt", "someone else's file\n")
		if err := in.InitRepo(ctx, dir, "main"); !errors.Is(err, git.ErrNotEmptyDir) {
			t.Fatalf("non-empty folder: err = %v, want ErrNotEmptyDir", err)
		}
		dir2 := w.NewDir()
		if err := in.InitRepo(ctx, dir2, "main"); err != nil {
			t.Fatal(err)
		}
		if err := in.InitRepo(ctx, dir2, "main"); err == nil {
			t.Fatal("a second init was accepted")
		}
		if err := in.InitRepo(ctx, filepath.Join(dir2, "missing"), "main"); err == nil {
			t.Fatal("init in a folder that does not exist was accepted")
		}
	})

	t.Run("bad branch names, paths and missing files are refused with no commit", func(t *testing.T) {
		w := newWorld(t)
		in := w.Initializer()
		for _, b := range []string{"", "-x", "a..b", "a b"} {
			if err := in.InitRepo(ctx, w.NewDir(), b); err == nil {
				t.Fatalf("branch %q accepted", b)
			}
		}
		dir := w.NewDir()
		if err := in.InitRepo(ctx, dir, "main"); err != nil {
			t.Fatal(err)
		}
		writeFirst(w, dir, "# Shop API\n")
		for name, files := range map[string][]string{
			"empty list": nil,
			"dot":        {"."},
			"parent":     {"../x"},
			"absolute":   {filepath.Join(dir, "README.md")},
			"git dir":    {".git/config"},
			"option":     {"-A"},
			"missing":    {"README.md", "nope.txt"},
		} {
			if _, err := in.CommitFirst(ctx, dir, files, "Initial commit"); err == nil {
				t.Fatalf("%s: accepted", name)
			}
		}
		if _, found, _ := in.ReadFirstCommit(ctx, dir, "main"); found {
			t.Fatal("a refused call left a commit")
		}
	})

	t.Run("content bytes are kept exactly (CRLF)", func(t *testing.T) {
		w := newWorld(t)
		in := w.Initializer()
		tree := func(readme string) string {
			dir := w.NewDir()
			if err := in.InitRepo(ctx, dir, "main"); err != nil {
				t.Fatal(err)
			}
			writeFirst(w, dir, readme)
			fc, err := in.CommitFirst(ctx, dir, firstFiles, "Initial commit")
			if err != nil {
				t.Fatal(err)
			}
			return fc.Tree
		}
		crlf, lf := tree("one\r\ntwo\r\n"), tree("one\ntwo\n")
		if crlf == lf {
			t.Fatal("CRLF and LF content gave the same tree: line endings were changed")
		}
		if again := tree("one\r\ntwo\r\n"); again != crlf {
			t.Fatal("same content gave a different tree")
		}
	})
}

// RunOriginManagerContract is the contract of git.OriginManager.
func RunOriginManagerContract(t *testing.T, newWorld func(t *testing.T) InitWorld) {
	ctx := context.Background()
	const u = "https://git.example.test/acme/shop-api.git"
	repo := func(t *testing.T) (InitWorld, string) {
		w := newWorld(t)
		dir := w.NewDir()
		if err := w.Initializer().InitRepo(ctx, dir, "main"); err != nil {
			t.Fatal(err)
		}
		return w, dir
	}
	t.Run("absent, add, read, idempotent, never replaced", func(t *testing.T) {
		w, dir := repo(t)
		om := w.Origins()
		if _, exists, err := om.Origin(ctx, dir); err != nil || exists {
			t.Fatalf("fresh repo: exists %v, %v", exists, err)
		}
		if err := om.AddOrigin(ctx, dir, u); err != nil {
			t.Fatal(err)
		}
		if got, exists, err := om.Origin(ctx, dir); err != nil || !exists || got != u {
			t.Fatalf("Origin = %q, %v, %v", got, exists, err)
		}
		if err := om.AddOrigin(ctx, dir, u); err != nil {
			t.Fatalf("identical origin: %v", err)
		}
		if err := om.AddOrigin(ctx, dir, "https://git.example.test/acme/other.git"); !errors.Is(err, git.ErrOriginExists) {
			t.Fatalf("different origin: err = %v, want ErrOriginExists", err)
		}
		if got, _, _ := om.Origin(ctx, dir); got != u {
			t.Fatalf("origin was replaced: %q", got)
		}
	})
	t.Run("URLs with credentials or the wrong shape are refused and nothing is written", func(t *testing.T) {
		w, dir := repo(t)
		om := w.Origins()
		for _, bad := range []string{
			"https://user:secret@git.example.test/acme/shop-api.git",
			"https://secret@git.example.test/acme/shop-api.git",
			"https://git.example.test/acme/shop-api.git?token=secret",
			"https://git.example.test/acme/shop-api.git#secret",
			"http://git.example.test/acme/shop-api.git",
			"ssh://git.example.test/acme/shop-api.git",
			"git@git.example.test:acme/shop-api.git",
			"/tmp/shop-api",
			"https://git.example.test/../shop-api.git",
			"",
		} {
			err := om.AddOrigin(ctx, dir, bad)
			if err == nil {
				t.Fatalf("%q accepted", bad)
			}
			if msg := err.Error() + git.UserMessage(err); strings.Contains(msg, "secret") {
				t.Fatalf("error echoes the secret: %q", msg)
			}
		}
		if _, exists, err := om.Origin(ctx, dir); err != nil || exists {
			t.Fatalf("a refused URL left an origin: %v, %v", exists, err)
		}
	})
	t.Run("repo in a path with spaces", func(t *testing.T) {
		w, dir := repo(t)
		if err := w.Origins().AddOrigin(ctx, dir, u); err != nil {
			t.Fatal(err)
		}
	})
}
