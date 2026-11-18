package platform

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// The Windows path rules are pure, so they are tested on every OS.
func TestParseWindowsDir(t *testing.T) {
	ok := map[string]string{
		`C:\work\acme-shop`:               `C:\work\acme-shop`,
		`c:/work/acme-shop/`:              `C:\work\acme-shop`,
		`C:\`:                             `C:\`,
		`\\?\C:\work\acme shop`:           `C:\work\acme shop`,
		`D:\ünï côdé\répo`:                `D:\ünï côdé\répo`,
		`C:\` + strings.Repeat(`d\`, 200): `C:\` + strings.TrimSuffix(strings.Repeat(`d\`, 200), `\`),
	}
	for in, want := range ok {
		d, err := parseWindowsDir(in, true)
		if err != nil || d.String() != want {
			t.Errorf("parseWindowsDir(%q) = %q, %v; want %q", in, d.String(), err, want)
		}
	}
	unsafe := []string{
		`\\wsl$\Ubuntu\home\me`, `\\wsl.localhost\Ubuntu\home`, `//WSL$/Ubuntu/x`,
		`\\.\C:\x`, `\\?\GLOBALROOT\Device\x`, `\\?\Volume{1234}\x`,
		`work\acme`, `C:work`, `\work`, `C:\a\..\b`, `C:\a\.\b`, ``,
		`C:\PROGRA~1\x`, `C:\work\NUL`, `C:\work\con.txt`, `C:\work\a:stream`, `C:\work\trailing.`, `C:\work\trailing `,
		`C:\work\a*b`,
	}
	for _, in := range unsafe {
		if _, err := parseWindowsDir(in, true); !errors.Is(err, ErrUnsafePath) {
			t.Errorf("parseWindowsDir(%q) = %v; want ErrUnsafePath", in, err)
		}
	}
	for _, in := range []string{`\\server\share\x`, `\\?\UNC\server\share\x`} {
		if _, err := parseWindowsDir(in, true); !errors.Is(err, ErrUnsupportedPlatform) {
			t.Errorf("parseWindowsDir(%q) = %v; want ErrUnsupportedPlatform", in, err)
		}
	}
	// A checkout root is not name-checked (only link-free, local, full).
	if _, err := parseWindowsDir(`C:\PROGRA~1\repo`, false); err != nil {
		t.Errorf("non-strict root with a short-looking name: %v", err)
	}
	if _, err := parseWindowsDir(`\\wsl$\x`, false); !errors.Is(err, ErrUnsafePath) {
		t.Errorf("WSL root must be refused even when not strict: %v", err)
	}
}

func TestValidateChildName(t *testing.T) {
	for _, n := range []string{"shop-api", ".gitignore", "README.md", "répo with space", "a.b.c", "x~y", "LICENSE"} {
		if err := ValidateChildName(n); err != nil {
			t.Errorf("%q refused: %v", n, err)
		}
	}
	for _, n := range []string{"", ".", "..", "a/b", `a\b`, "a:b", "aux", "LPT1", "x.", "x ", "PROGRA~1", "LONGDI~1.TXT", "a\x00", "a\n", "a?", strings.Repeat("a", 256), "bad\xffutf"} {
		if err := ValidateChildName(n); !errors.Is(err, ErrUnsafePath) {
			t.Errorf("%q = %v; want ErrUnsafePath", n, err)
		}
	}
}

// Accepted names are always valid single components (accepted implies valid).
func FuzzValidateChildName(f *testing.F) {
	for _, s := range []string{"a", "..", "a/b", "NUL", "x.", "répo"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		if ValidateChildName(name) != nil {
			return
		}
		if name == "" || name == "." || name == ".." || !utf8.ValidString(name) || strings.ContainsAny(name, "/\\:\x00*?\"<>|") {
			t.Fatalf("accepted %q", name)
		}
	})
}

func FuzzParseWindowsDir(f *testing.F) {
	for _, s := range []string{`C:\a`, `\\wsl$\x`, `\\?\C:\x`, `a\b`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		d, err := parseWindowsDir(in, true)
		if err != nil {
			return
		}
		if len(d.Drive) != 1 || strings.ContainsAny(d.String(), "/*?<>|\"") {
			t.Fatalf("accepted %q as %q", in, d.String())
		}
		for _, c := range d.Comps {
			if badComponent(c) != "" {
				t.Fatalf("accepted bad component %q in %q", c, in)
			}
		}
		again, err := parseWindowsDir(d.String(), true)
		if err != nil || again.String() != d.String() {
			t.Fatalf("not stable: %q -> %q -> %q, %v", in, d.String(), again.String(), err)
		}
	})
}
