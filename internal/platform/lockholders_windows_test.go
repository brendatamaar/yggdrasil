//go:build windows

package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanHolderNames(t *testing.T) {
	in := []string{"Code.exe", "code.EXE", "", "  ", "Bad\x00Name\n.exe", "a", "b", "c", "d", "e", "f"}
	got := cleanHolderNames(in)
	want := []string{"Code.exe", "BadName.exe", "a", "b", "c"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("cleanHolderNames = %q, want %q", got, want)
	}
}

func TestSampleFilesIsBounded(t *testing.T) {
	dir := t.TempDir()
	for i := range 300 {
		name := "f" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := sampleFiles(dir); len(got) != maxSampleFiles {
		t.Errorf("sampled %d files, want %d", len(got), maxSampleFiles)
	}
	if got := sampleFiles(filepath.Join(dir, "missing")); got != nil {
		t.Errorf("missing path sampled %v", got)
	}
}

// This process holds the file open, so the Restart Manager should name it.
// The answer depends on the machine (policy, services), so the names are
// logged and bounded, not required; the manual checklist covers a real editor.
func TestLockHoldersWithAnOpenFile(t *testing.T) {
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "held.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got := LockHolders(dir)
	if len(got) > maxHolders {
		t.Fatalf("too many names: %v", got)
	}
	t.Logf("LockHolders = %q", got)
	for _, n := range got {
		if strings.ContainsFunc(n, func(r rune) bool { return r < 0x20 }) {
			t.Errorf("control character in %q", n)
		}
	}
	// A second call must work: the first session was ended.
	if again := LockHolders(f.Name()); len(again) > maxHolders {
		t.Errorf("second call: %v", again)
	}
}
