package platform_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/platform/platformtest"
)

// canonDir makes a temp folder and returns its canonical spelling (t.TempDir
// can be an 8.3 path on Windows).
func canonDir(t *testing.T) string {
	t.Helper()
	d, err := platform.CanonicalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestRealPathsMeetPathResolverContract(t *testing.T) {
	root := filepath.Join(canonDir(t), "proj with spaces")
	long := filepath.Join(root, "Long Directory Name")
	if err := os.MkdirAll(long, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := canonDir(t)
	fx := platformtest.PathFixture{Root: root, Long: long, Short: shortName(t, long), Outside: outside}
	link := filepath.Join(root, "link out")
	if makeLink(t, link, outside) {
		fx.LinkOut = link
	}
	platformtest.PathResolverContract(t, platform.RealPaths{}, fx)
}

func TestResolveRootLongPath(t *testing.T) {
	deep := canonDir(t)
	for len(deep) < 300 {
		deep = filepath.Join(deep, "segment with spaces "+strings.Repeat("d", 20))
	}
	mkdirAll(t, deep)
	got, err := platform.ResolveRoot(deep)
	if err != nil || got != deep {
		t.Fatalf("ResolveRoot(long) = %q, %v; want %q", got, err, deep)
	}
	missing := filepath.Join(deep, "missing", "leaf.txt")
	if got, err := platform.ResolveRoot(missing); err != nil || got != missing {
		t.Fatalf("ResolveRoot(long, missing leaf) = %q, %v; want %q", got, err, missing)
	}
}

func TestResolveRootOnDiskCase(t *testing.T) {
	if !platform.FoldCase() {
		t.Skip("case-sensitive file system")
	}
	root := filepath.Join(canonDir(t), "MixedCase")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := platform.ResolveRoot(strings.ToLower(root))
	if err != nil || got != root {
		t.Errorf("ResolveRoot(lower) = %q, %v; want on-disk case %q", got, err, root)
	}
	got, err = platform.ResolveRoot(strings.ToLower(filepath.Join(root, "Missing")))
	if err != nil || !strings.HasPrefix(got, root) {
		t.Errorf("existing part keeps on-disk case: %q, %v", got, err)
	}
}

func TestResolveRootBrokenLinkRefused(t *testing.T) {
	root := canonDir(t)
	link := filepath.Join(root, "dangling")
	if err := os.Symlink(filepath.Join(root, "nowhere"), link); err != nil {
		t.Skipf("can't create symlinks here: %v", err)
	}
	if _, err := platform.ResolveRoot(filepath.Join(link, "f.txt")); err == nil {
		t.Error("a path through a dangling symlink was accepted")
	}
}

func TestResolveRootSymlinkOut(t *testing.T) {
	root, outside := canonDir(t), canonDir(t)
	link := filepath.Join(root, "sym")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("can't create symlinks here: %v", err)
	}
	got, err := platform.ResolveRoot(filepath.Join(link, "x"))
	if err != nil || got != filepath.Join(outside, "x") {
		t.Errorf("ResolveRoot(symlink) = %q, %v; want %q", got, err, filepath.Join(outside, "x"))
	}
}
