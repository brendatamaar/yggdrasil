//go:build windows

package platform

import (
	"os"
	"slices"
	"testing"
)

// On Windows the job object replaces pid files: the orphan functions write
// nothing and list nothing.
func TestOrphanFunctionsAreNoOps(t *testing.T) {
	dataDir := t.TempDir()
	if err := RecordProcess(dataDir, "dev1", os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if got, err := ListOrphans(dataDir); err != nil || len(got) != 0 {
		t.Fatalf("ListOrphans = %v, %v", got, err)
	}
	if err := StopOrphan(dataDir, "dev1"); err != nil {
		t.Fatal(err)
	}
	if err := ForgetProcess(dataDir, "dev1"); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dataDir)
	if len(entries) != 0 {
		t.Errorf("data dir not empty: %v", entries)
	}
}

func TestDefaultShellIsPowerShell(t *testing.T) {
	got := DefaultShell()
	if len(got) == 0 || got[0] != "powershell.exe" {
		t.Fatalf("DefaultShell = %v", got)
	}
	if !slices.Contains(got, "-NoLogo") {
		t.Errorf("DefaultShell = %v, want -NoLogo", got)
	}
}
