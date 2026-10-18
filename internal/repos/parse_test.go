package repos_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/repos"
)

func wantInvalid(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: accepted", what)
		return
	}
	if c, ok := apperr.ClassOf(err); !ok || c != apperr.Invalid {
		t.Errorf("%s: error %v is not an apperr.Invalid", what, err)
	}
	if m, _ := apperr.MessageOf(err); m == "" {
		t.Errorf("%s: no user message", what)
	}
}

func TestParseFolderAcceptsAndRoundTrips(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "with space")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	p, err := repos.ParseFolder(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Accepted implies valid.
	if _, err := corerepos.NewCleanPath(string(p)); err != nil {
		t.Errorf("accepted %q but it isn't a valid CleanPath: %v", p, err)
	}
	if st, err := os.Stat(string(p)); err != nil || !st.IsDir() {
		t.Errorf("accepted %q but it isn't a folder", p)
	}
	// Round trip.
	again, err := repos.ParseFolder(repos.FormatFolder(p))
	if err != nil || again != p {
		t.Errorf("ParseFolder(Format(p)) = %q, %v; want %q", again, err, p)
	}
	// Other spellings of the same folder give one canonical form.
	for _, alt := range []string{dir + string(filepath.Separator), filepath.Join(dir, "."), filepath.Join(dir, "..", "with space")} {
		if got, err := repos.ParseFolder(alt); err != nil || got != p {
			t.Errorf("ParseFolder(%q) = %q, %v; want %q", alt, got, err, p)
		}
	}
}

func TestParseFolderRejectsEachRule(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"empty":         "",
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
	}
	for name, raw := range cases {
		_, err := repos.ParseFolder(raw)
		wantInvalid(t, name, err)
	}
}

func TestParseBrowsePath(t *testing.T) {
	if p, err := repos.ParseBrowsePath(""); err != nil || p != "" {
		t.Errorf("empty = %q, %v; want the roots", p, err)
	}
	if _, err := repos.ParseBrowsePath(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("a missing folder was accepted")
	}
	if p, err := repos.ParseBrowsePath(t.TempDir()); err != nil || p == "" {
		t.Errorf("a folder = %q, %v", p, err)
	}
}

func TestParseName(t *testing.T) {
	if got, err := repos.ParseName("  acme-shop \t"); err != nil || got != "acme-shop" {
		t.Errorf("ParseName = %q, %v", got, err)
	}
	if again, err := repos.ParseName("acme-shop"); err != nil || again != "acme-shop" {
		t.Errorf("round trip failed: %q, %v", again, err)
	}
	for name, raw := range map[string]string{
		"empty": "", "blank": " \t ", "too long": strings.Repeat("x", 101), "control": "a\x07b", "newline": "a\nb",
	} {
		_, err := repos.ParseName(raw)
		wantInvalid(t, name, err)
	}
	if _, err := repos.ParseName(strings.Repeat("é", 100)); err != nil {
		t.Errorf("100 runes must be accepted: %v", err)
	}
}

func TestParsePicks(t *testing.T) {
	const root = corerepos.CleanPath("/work/shop")
	got, err := repos.ParsePicks(root, []corerepos.Pick{
		{RelPath: "api", Name: " shop-api "}, {RelPath: "libs/web"}, {RelPath: "."},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []corerepos.Pick{{RelPath: "api", Name: "shop-api"}, {RelPath: "libs/web", Name: "web"}, {RelPath: ".", Name: "shop"}}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pick %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	// Parsing parsed picks changes nothing (round trip).
	again, err := repos.ParsePicks(root, got)
	if err != nil || len(again) != len(got) {
		t.Fatalf("second parse: %v", err)
	}
	for i := range got {
		if again[i] != got[i] {
			t.Errorf("not idempotent: %+v vs %+v", again[i], got[i])
		}
	}

	many := make([]corerepos.Pick, repos.MaxPicks+1)
	for i := range many {
		many[i] = corerepos.Pick{RelPath: corerepos.RelPath("r" + strings.Repeat("x", i%5) + string(rune('a'+i%26)) + string(rune('a'+i/26)))}
	}
	for name, picks := range map[string][]corerepos.Pick{
		"none":           nil,
		"duplicate":      {{RelPath: "a"}, {RelPath: "a"}},
		"dotdot":         {{RelPath: "../x"}},
		"inner dotdot":   {{RelPath: "a/../b"}},
		"absolute":       {{RelPath: "/etc"}},
		"drive":          {{RelPath: `C:\x`}},
		"backslash":      {{RelPath: `a\b`}},
		"empty path":     {{RelPath: ""}},
		"empty segment":  {{RelPath: "a//b"}},
		"trailing slash": {{RelPath: "a/"}},
		"too many":       many,
		"name too long":  {{RelPath: "a", Name: strings.Repeat("n", 101)}},
		"name with ctrl": {{RelPath: "a", Name: "x\ty"}},
	} {
		_, err := repos.ParsePicks(root, picks)
		wantInvalid(t, name, err)
	}
}

func FuzzParseFolder(f *testing.F) {
	for _, s := range []string{"", ".", "..", `C:\`, `c:/work//x`, `\\?\C:\x`, `\\wsl$\x`, "/", "a\x00b", strings.Repeat("a/", 300)} {
		f.Add(s)
	}
	f.Add(os.TempDir())
	f.Fuzz(func(t *testing.T, raw string) {
		p, err := repos.ParseFolder(raw)
		if err != nil {
			wantInvalid(t, "fuzz", err)
			return
		}
		if _, err := corerepos.NewCleanPath(string(p)); err != nil {
			t.Fatalf("accepted %q -> %q, not a valid CleanPath: %v", raw, p, err)
		}
		if st, err := os.Stat(string(p)); err != nil || !st.IsDir() {
			t.Fatalf("accepted %q -> %q, not a folder", raw, p)
		}
		if again, err := repos.ParseFolder(repos.FormatFolder(p)); err != nil || again != p {
			t.Fatalf("not canonical: %q -> %q -> %q, %v", raw, p, again, err)
		}
	})
}

func FuzzParseName(f *testing.F) {
	for _, s := range []string{"", "a", " a ", "a\x00", strings.Repeat("é", 101), "tab\tname"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		n, err := repos.ParseName(raw)
		if err != nil {
			wantInvalid(t, "fuzz", err)
			return
		}
		if err := corerepos.ValidName(n); err != nil || n != strings.TrimSpace(n) {
			t.Fatalf("accepted %q -> %q but it isn't valid: %v", raw, n, err)
		}
		if again, err := repos.ParseName(n); err != nil || again != n {
			t.Fatalf("not idempotent: %q -> %q -> %q", raw, n, again)
		}
	})
}

func FuzzParsePicks(f *testing.F) {
	f.Add("api", "shop-api", "libs/web", "")
	f.Add("..", "x", "a", "b")
	f.Add("a", "n", "a", "n")
	f.Add(`C:\x`, "\x00", "/", " ")
	f.Fuzz(func(t *testing.T, r1, n1, r2, n2 string) {
		const root = corerepos.CleanPath("/work/shop")
		in := []corerepos.Pick{{RelPath: corerepos.RelPath(r1), Name: n1}, {RelPath: corerepos.RelPath(r2), Name: n2}}
		out, err := repos.ParsePicks(root, in)
		if err != nil {
			wantInvalid(t, "fuzz", err)
			return
		}
		seen := map[corerepos.RelPath]bool{}
		for _, p := range out {
			if _, err := corerepos.NewRelPath(string(p.RelPath)); err != nil {
				t.Fatalf("accepted a bad path %q", p.RelPath)
			}
			if corerepos.ValidName(p.Name) != nil {
				t.Fatalf("accepted a bad name %q", p.Name)
			}
			if seen[p.RelPath] {
				t.Fatalf("accepted %q twice", p.RelPath)
			}
			seen[p.RelPath] = true
		}
		if again, err := repos.ParsePicks(root, out); err != nil || len(again) != len(out) {
			t.Fatalf("not idempotent: %v", err)
		}
	})
}
