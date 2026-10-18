package platform

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsReparsePointPlainEntries(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{dir, file} {
		got, err := IsReparsePoint(p)
		if err != nil || got {
			t.Errorf("IsReparsePoint(%s) = %v, %v; want false, nil", p, got, err)
		}
	}
	if _, err := IsReparsePoint(filepath.Join(dir, "missing")); err == nil {
		t.Error("a missing path must be an error")
	}
}

func TestIsReparsePointSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("can't create symlinks here: %v", err)
	}
	if got, err := IsReparsePoint(link); err != nil || !got {
		t.Errorf("IsReparsePoint(symlink) = %v, %v; want true", got, err)
	}
}

func TestListRoots(t *testing.T) {
	roots, err := ListRoots()
	if err != nil || len(roots) == 0 {
		t.Fatalf("ListRoots = %v, %v; want at least one root", roots, err)
	}
	for _, r := range roots {
		if !filepath.IsAbs(r) {
			t.Errorf("root %q isn't absolute", r)
		}
	}
}
