package platform

import (
	"errors"
	"strings"
	"testing"
)

// One case per rejection rule of CheckPath.
func TestCheckPathRejectionRules(t *testing.T) {
	rules := map[string]string{
		"plain \\\\wsl$":             `\\wsl$\Ubuntu\home`,
		"plain \\\\wsl.localhost":    `\\wsl.localhost\Ubuntu\home`,
		"upper case":                 `\\WSL$\Ubuntu`,
		"forward slashes":            `//wsl.localhost/Ubuntu/home`,
		"host alone":                 `\\wsl$`,
		"\\\\?\\UNC\\ prefix":        `\\?\UNC\wsl.localhost\Ubuntu\home`,
		"\\\\?\\UNC\\ prefix, wsl$":  `\\?\UNC\wsl$\Ubuntu`,
		"\\\\.\\UNC\\ prefix":        `\\.\UNC\wsl$\Ubuntu\home`,
		"\\\\.\\UNC\\ prefix, lower": `\\.\unc\wsl.localhost\Ubuntu`,
		"plain \\\\?\\ before host":  `\\?\\wsl$\Ubuntu`,
		"plain \\\\.\\ before host":  `\\.\\wsl.localhost\Ubuntu`,
		"\\??\\UNC\\ NT prefix":      `\??\UNC\wsl$\Ubuntu`,
		"mixed slashes with prefix":  `//?/UNC/wsl$/Ubuntu`,
		"mixed case prefix":          `\\?\uNc\WSL$\Ubuntu`,
	}
	for name, p := range rules {
		if err := CheckPath(p); !errors.Is(err, ErrWSLPath) {
			t.Errorf("%s: CheckPath(%q) = %v, want ErrWSLPath", name, p, err)
		}
	}
	if err := CheckPath(""); err == nil || errors.Is(err, ErrWSLPath) {
		t.Errorf("empty path: %v, want the empty-path error", err)
	}
	for _, ok := range []string{`\\?\C:\code\repo`, `\\.\C:\code`, `\\?\UNC\server\share\repo`, `\\.\UNC\server\share`, `\\wsl$x\share`} {
		if err := CheckPath(ok); err != nil {
			t.Errorf("CheckPath(%q) = %v, want nil", ok, err)
		}
	}
}

func FuzzCheckPath(f *testing.F) {
	for _, s := range []string{
		`\\wsl$\x`, `\\?\UNC\wsl$\x`, `\\.\UNC\wsl.localhost\x`, `\\?\\wsl$`, `//wsl.localhost/x`,
		`C:\code`, `\\?\C:\x`, `\\server\share`, `\??\UNC\wsl$`, "",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, path string) {
		if CheckPath(path) != nil {
			return
		}
		n := normalizeUNC(path)
		for _, host := range []string{`\\wsl$`, `\\wsl.localhost`} {
			// A host is the whole component; \\wsl$x is some other server.
			if n == host || strings.HasPrefix(n, host+`\`) {
				t.Fatalf("CheckPath(%q) accepted a WSL path (normalised %q)", path, n)
			}
		}
	})
}
