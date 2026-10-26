package platform_test

import (
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/platform/platformtest"
)

// fakeRoot is an absolute path that exists on no disk, valid on every OS.
func fakeRoot(parts ...string) string {
	base := filepath.VolumeName(os.TempDir()) + string(filepath.Separator)
	return filepath.Join(append([]string{base, "fake", "proj with spaces"}, parts...)...)
}

func TestFakePathsMeetPathResolverContract(t *testing.T) {
	root := fakeRoot()
	f := platformtest.NewFakePaths(platform.FoldCase())
	f.Add(filepath.Dir(root), filepath.Dir(root))
	f.Add(root, root)
	long, short := fakeRoot("Long Directory Name"), fakeRoot("LONGDI~1")
	f.Add(long, long).Add(short, long)
	outside := fakeRoot("..", "outside")
	f.Add(outside, outside)
	link := fakeRoot("link")
	f.Add(link, outside)
	platformtest.PathResolverContract(t, f, platformtest.PathFixture{
		Root: root, Long: long, Short: short, LinkOut: link, Outside: outside,
	})
}

func TestFakePathsScriptedError(t *testing.T) {
	boom := errors.New("boom")
	f := platformtest.NewFakePaths(false).Add(fakeRoot(), fakeRoot()).Fail(fakeRoot("bad"), boom)
	if _, err := f.ResolveRoot(fakeRoot("bad")); !errors.Is(err, boom) {
		t.Errorf("err = %v, want boom", err)
	}
}

// FoldCase is true on Windows and macOS (case-insensitive by default), false
// on Linux. Folding only makes the guard stricter when the caller folds with
// ASCII lower-casing.
func TestFoldCaseMatchesOS(t *testing.T) {
	want := runtime.GOOS == "windows" || runtime.GOOS == "darwin"
	if got := platform.FoldCase(); got != want {
		t.Errorf("FoldCase() = %v, want %v", got, want)
	}
}

// Off Windows a ".." segment is refused: the kernel resolves it physically, so
// link/.. is not the parent of the lexical path.
func TestCleanRootPathRefusesDotDotOffWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows collapses .. lexically, like Win32")
	}
	root := string(filepath.Separator) + "repo"
	for _, in := range []string{
		root + "/link/../x", root + "/..", root + "/a/b/../../c", "/../x", root + "/./a/../b",
	} {
		_, err := platform.CleanRootPath(in)
		if !errors.Is(err, platform.ErrPathDotDot) {
			t.Errorf("CleanRootPath(%q) = %v, want ErrPathDotDot", in, err)
		}
		if _, err := platform.ResolveRoot(in); !errors.Is(err, platform.ErrPathDotDot) {
			t.Errorf("ResolveRoot(%q) = %v, want ErrPathDotDot", in, err)
		}
	}
	if strings.Contains(platform.ErrPathDotDot.Error(), "/") || strings.Contains(platform.ErrPathDotDot.Error(), "repo") {
		t.Errorf("error text should be path-free: %v", platform.ErrPathDotDot)
	}
	// Names that merely contain dots are fine.
	for _, in := range []string{root + "/..a", root + "/a..", root + "/...", root + "/a/./b"} {
		if _, err := platform.CleanRootPath(in); err != nil {
			t.Errorf("CleanRootPath(%q) = %v, want nil", in, err)
		}
	}
}

func TestResolveRootDotDotThroughSymlinkOffWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows collapses .. lexically, like Win32")
	}
	root, outside := canonDir(t), canonDir(t)
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("can't create symlinks here: %v", err)
	}
	if got, err := platform.ResolveRoot(root + "/link/../x"); err == nil {
		t.Errorf("link/../x accepted as %q", got)
	}
}

func TestCleanRootPathErrorsAreSentinels(t *testing.T) {
	cases := map[string]error{
		"":         platform.ErrPathEmpty,
		"rel/path": platform.ErrPathNotAbsolute,
		"a\x00b":   platform.ErrPathNUL,
	}
	for in, want := range cases {
		if _, err := platform.CleanRootPath(in); !errors.Is(err, want) {
			t.Errorf("CleanRootPath(%q) = %v, want %v", in, err, want)
		}
	}
}

func TestCleanRootPathWindowsForms(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path forms")
	}
	ok := map[string]string{
		`C:\a\..\b`:            `C:\b`,
		`c:/a/b`:               `c:\a\b`,
		`\\?\C:\a b\c`:         `C:\a b\c`,
		`\\.\C:\x`:             `C:\x`,
		`\\?\UNC\host\share\x`: `\\host\share\x`,
		`\\host\share`:         `\\host\share\`,
	}
	for in, want := range ok {
		if got, err := platform.CleanRootPath(in); err != nil || got != want {
			t.Errorf("CleanRootPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := map[string]error{
		`C:`:                        platform.ErrPathNotAbsolute,
		`C:foo`:                     platform.ErrPathNotAbsolute,
		`\foo`:                      platform.ErrPathNotAbsolute,
		`\\host`:                    platform.ErrPathNotAbsolute,
		`\\wsl$\Ubuntu\home`:        platform.ErrWSLPath,
		`\\?\UNC\WSL.localhost\x`:   platform.ErrWSLPath,
		`\\.\pipe\x`:                platform.ErrPathDevice,
		`\\?\GLOBALROOT\Device\Mup`: platform.ErrPathDevice,
		`C:\a\file.txt:s`:           platform.ErrPathStream,
		`C:\a\NUL`:                  platform.ErrPathDevice,
		`C:\a\com1.txt`:             platform.ErrPathDevice,
		`C:\a\x. `:                  platform.ErrPathTrailing,
		`C:\a\x.`:                   platform.ErrPathTrailing,
		`C:\a\*`:                    platform.ErrPathChars,
		`C:\a\CONIN$.txt`:           platform.ErrPathDevice,
		`C:\a\conout$ .txt`:         platform.ErrPathDevice,
		`C:\a\CLOCK$.a.b`:           platform.ErrPathDevice,
		`C:\a\CONIN$`:               platform.ErrPathDevice,
		`C:\a\CONIN$ `:              platform.ErrPathTrailing,
		`C:\a\CLOCK$.`:              platform.ErrPathTrailing,
	}
	for in, want := range bad {
		if _, err := platform.CleanRootPath(in); !errors.Is(err, want) {
			t.Errorf("CleanRootPath(%q) = %v, want %v", in, err, want)
		}
	}
}

// within reports whether p is root or under it, folding case where the OS does.
func within(root, p string) bool {
	if platform.FoldCase() {
		root, p = strings.ToLower(root), strings.ToLower(p)
	}
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}

// For a temp root and any generated relative path, the resolved path is inside
// the resolved root iff the lexical join is.
func TestResolveRootInsideIffLexicalJoinInside(t *testing.T) {
	root, err := platform.ResolveRoot(filepath.Join(t.TempDir(), "proj with spaces"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "a", "b"), 0o700); err != nil {
		t.Fatal(err)
	}
	segs := []string{"a", "b", "..", "..", ".", "c d", "file.txt", "A", "x"}
	rng := rand.New(rand.NewSource(1))
	for range 2000 {
		n := rng.Intn(8) + 1
		parts := make([]string, n)
		for i := range parts {
			parts[i] = segs[rng.Intn(len(segs))]
		}
		joined := filepath.Join(append([]string{root}, parts...)...) // lexical, cleaned
		got, err := platform.ResolveRoot(filepath.Join(root, filepath.Join(parts...)))
		if err != nil {
			t.Fatalf("ResolveRoot(%v) failed: %v", parts, err)
		}
		if within(root, got) != within(root, joined) {
			t.Fatalf("parts %v: resolved %q inside=%v, lexical %q inside=%v", parts, got, within(root, got), joined, within(root, joined))
		}
	}
}

func FuzzResolveRoot(f *testing.F) {
	for _, s := range []string{
		"", "x", fakeRoot(), fakeRoot("..", ".."), `C:\a\..\b`, `\\?\C:\x`, `C:\a\NUL`, `C:\a\f:s`, `C:\a\CONIN$.txt`, `C:\a\CONOUT$ `, `C:\a\CLOCK$.x`, `\\wsl$\u`,
		`C:\Program Files\x`, "/tmp/a/../b", os.TempDir(), "C:\\a\\b\x00",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		got, err := platform.ResolveRoot(in)
		if err != nil {
			return
		}
		if !filepath.IsAbs(got) {
			t.Fatalf("accepted %q, result %q is not absolute", in, got)
		}
		vol := filepath.VolumeName(got)
		rest := strings.TrimPrefix(got, vol)
		for _, seg := range strings.FieldsFunc(rest, func(r rune) bool { return r == '/' || r == filepath.Separator }) {
			if seg == ".." {
				t.Fatalf("accepted %q, result %q has a .. segment", in, got)
			}
			if runtime.GOOS == "windows" {
				if strings.Contains(seg, ":") {
					t.Fatalf("accepted %q, result %q has ':' outside the drive", in, got)
				}
				if base, _, _ := strings.Cut(strings.ToUpper(seg), "."); base == "CON" || base == "NUL" || base == "PRN" || base == "AUX" {
					t.Fatalf("accepted %q, result %q has a device name", in, got)
				}
			}
		}
		if strings.ContainsRune(got, 0) {
			t.Fatalf("result has NUL")
		}
	})
}
