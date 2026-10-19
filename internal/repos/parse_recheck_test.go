package repos

import (
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// A subst or mapped drive, or a junction in an ancestor, can resolve to a
// \\wsl$ path. ParseFolder checks again on the resolved path (PL-04).
func TestParseFolderRechecksCanonicalPath(t *testing.T) {
	dir := t.TempDir()
	old := canonicalPath
	t.Cleanup(func() { canonicalPath = old })
	canonicalPath = func(string) (string, error) { return `\\wsl$\Ubuntu\home\dev\shop`, nil }
	_, err := ParseFolder(dir)
	if err == nil {
		t.Fatal("a folder that resolves to a WSL path was accepted")
	}
	if platformErr := platform.CheckPath(`\\wsl$\Ubuntu\home\dev\shop`); platformErr == nil {
		t.Fatal("CheckPath no longer refuses WSL paths; the test is meaningless")
	}
}
