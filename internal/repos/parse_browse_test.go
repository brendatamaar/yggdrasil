package repos_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/repos"
)

// ParseBrowsePath: "" means the roots; everything else follows the folder rules.
func TestParseBrowsePathRejectsEachRule(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"blank":         "   ",
		"relative":      "work/shop",
		"dot":           ".",
		"NUL":           dir + "\x00x",
		"control":       dir + "\n",
		"missing":       filepath.Join(dir, "nope"),
		"a file":        file,
		"WSL":           `\\wsl$\Ubuntu\home\me`,
		"WSL localhost": `\\wsl.localhost\Ubuntu\home`,
		"device path":   `\\.\GLOBALROOT\Device\HarddiskVolume1\x`,
	} {
		_, err := repos.ParseBrowsePath(raw)
		wantInvalid(t, name, err)
	}
}

func TestParseBrowsePathAcceptsAndRoundTrips(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "with space")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	p, err := repos.ParseBrowsePath(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := corerepos.NewCleanPath(string(p)); err != nil {
		t.Errorf("accepted %q but it isn't a valid CleanPath: %v", p, err)
	}
	if again, err := repos.ParseBrowsePath(repos.FormatFolder(p)); err != nil || again != p {
		t.Errorf("ParseBrowsePath(Format(p)) = %q, %v; want %q", again, err, p)
	}
	// Both parsers agree on a folder.
	if f, err := repos.ParseFolder(dir); err != nil || f != p {
		t.Errorf("ParseFolder = %q, %v; ParseBrowsePath = %q", f, err, p)
	}
}

// Windows spellings of one folder: drive letter case, the long-path prefix,
// forward slashes and (on a case-insensitive disk) name case.
func TestParseFolderWindowsSpellings(t *testing.T) {
	if !platform.PathsCaseInsensitive {
		t.Skip("Windows spellings only")
	}
	dir := filepath.Join(t.TempDir(), "Shop Api")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	want, err := repos.ParseFolder(dir)
	if err != nil {
		t.Fatal(err)
	}
	if v := filepath.VolumeName(string(want)); len(v) != 2 || v[0] < 'A' || v[0] > 'Z' {
		t.Errorf("canonical path %q does not have an upper-case drive letter", want)
	}
	spellings := map[string]string{
		"lower drive":   strings.ToLower(dir[:1]) + dir[1:],
		"upper name":    strings.ToUpper(dir),
		"lower name":    strings.ToLower(dir),
		"long prefix":   `\\?\` + dir,
		"slashes":       filepath.ToSlash(dir),
		"trailing sep":  dir + `\`,
		"dotted":        filepath.Join(dir, ".") + `\.`,
		"redundant sep": strings.ReplaceAll(dir, `\`, `\\`),
	}
	for name, raw := range spellings {
		for parser, parse := range map[string]func(string) (corerepos.CleanPath, error){"folder": repos.ParseFolder, "browse": repos.ParseBrowsePath} {
			got, err := parse(raw)
			if err != nil || got != want {
				t.Errorf("%s %s: %q -> %q, %v; want %q", parser, name, raw, got, err, want)
			}
		}
	}
}

func TestParseFolderLongPath(t *testing.T) {
	deep := t.TempDir()
	for len(deep) < 300 {
		deep = filepath.Join(deep, strings.Repeat("d", 40))
	}
	if err := os.MkdirAll(deep, 0o750); err != nil {
		t.Skipf("this machine can't create long paths: %v", err)
	}
	p, err := repos.ParseFolder(deep)
	if err != nil {
		t.Fatalf("a long path was refused: %v", err)
	}
	if strings.HasPrefix(string(p), `\\?\`) {
		t.Errorf("the long-path prefix must not be stored: %q", p)
	}
	if again, err := repos.ParseFolder(repos.FormatFolder(p)); err != nil || again != p {
		t.Errorf("round trip = %q, %v; want %q", again, err, p)
	}
}

func FuzzParseBrowsePath(f *testing.F) {
	for _, s := range []string{"", ".", "..", `C:\`, `c:/work//x`, `\\?\C:\x`, `\\wsl$\x`, "/", "a\x00b", strings.Repeat("a/", 300)} {
		f.Add(s)
	}
	f.Add(os.TempDir())
	f.Fuzz(func(t *testing.T, raw string) {
		p, err := repos.ParseBrowsePath(raw)
		if err != nil {
			wantInvalid(t, "fuzz", err)
			return
		}
		if p == "" {
			if raw != "" {
				t.Fatalf("%q was read as the roots", raw)
			}
			return
		}
		if _, err := corerepos.NewCleanPath(string(p)); err != nil {
			t.Fatalf("accepted %q -> %q, not a valid CleanPath: %v", raw, p, err)
		}
		if st, err := os.Stat(string(p)); err != nil || !st.IsDir() {
			t.Fatalf("accepted %q -> %q, not a folder", raw, p)
		}
		if again, err := repos.ParseBrowsePath(repos.FormatFolder(p)); err != nil || again != p {
			t.Fatalf("not canonical: %q -> %q -> %q, %v", raw, p, again, err)
		}
	})
}
