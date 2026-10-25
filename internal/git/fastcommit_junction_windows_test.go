//go:build windows

package git

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// mkJunction creates an NTFS junction (no privilege needed, unlike a symlink).
func mkJunction(t *testing.T, f *fx, link, target string) {
	t.Helper()
	cmd, err := platform.Resolve("cmd", "", f.env)
	if err != nil {
		t.Skipf("cmd.exe not found: %v", err)
	}
	var out bytes.Buffer
	p, err := platform.Start(context.Background(), platform.Spec{
		Argv: []string{cmd, "/c", "mklink", "/J", link, target}, Dir: filepath.Dir(link), Env: f.env, Stdout: &out, Stderr: &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if code, err := p.Wait(); err != nil || code != 0 {
		t.Skipf("mklink /J failed (%d, %v): %s", code, err, out.String())
	}
}

// Invariant 6 (docs/AGENTS.md 6): a junction inside .git could lead the read
// outside the tracked repo. It must be treated like a symlink.
func TestReadCommitDirect_JunctionedGitOrObjectsDeclines(t *testing.T) {
	f := newFx(t)
	for _, which := range []string{"git", "objects"} {
		t.Run(which, func(t *testing.T) {
			dir := f.repo("junction-" + which)
			f.commit(dir, "a.txt", "a\n", "first")
			oid := f.git(dir, "rev-parse", "HEAD")
			oid = oid[:40]
			if _, ok := readCommitDirect(dir, oid); !ok {
				t.Fatal("control: plain repo not answered")
			}
			link := filepath.Join(dir, ".git")
			if which == "objects" {
				link = filepath.Join(link, "objects")
			}
			moved := link + "-real"
			if err := os.Rename(link, moved); err != nil {
				t.Fatal(err)
			}
			mkJunction(t, f, link, moved)
			if fi, err := os.Lstat(link); err != nil || fi.Mode().IsDir() && fi.Mode()&os.ModeIrregular == 0 {
				t.Logf("Lstat of the junction: mode %v err %v", fi.Mode(), err)
			}
			if _, ok := readCommitDirect(dir, oid); ok {
				t.Errorf("followed a junctioned %s", which)
			}
		})
	}
}
