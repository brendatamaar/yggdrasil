package platform

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// ValidateChildName checks that name is one plain file or folder name that is
// valid on every supported OS: no separators, no "." or "..", no alternate
// data stream colon, no device name, no 8.3 short-name alias, no trailing
// space or dot. The rules are the Windows ones on every platform so a repo
// created on one OS stays portable and the fake and real implementations
// agree. The error wraps ErrUnsafePath.
func ValidateChildName(name string) error {
	if why := badComponent(name); why != "" {
		return fmt.Errorf("%q cannot be used as a file or folder name (%s): %w", name, why, ErrUnsafePath)
	}
	return nil
}

// badComponent returns why name is not a safe single path component, or "".
func badComponent(name string) string {
	switch {
	case name == "":
		return "it is empty"
	case name == "." || name == "..":
		return "it is a relative reference"
	case len(name) > 255:
		return "it is too long"
	case !utf8.ValidString(name):
		return "it is not valid text"
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`<>:"/\|?*`, r) {
			return "it contains a character that is not allowed"
		}
	}
	switch {
	case strings.HasSuffix(name, " ") || strings.HasSuffix(name, "."):
		return "it ends in a space or a dot"
	case isReservedName(name):
		return "it is a reserved device name"
	case HasShortNameSegment(name):
		return "it looks like a Windows short (8.3) name"
	}
	return ""
}

// winDir is a Windows directory path taken apart: a drive letter and the
// components below the drive root.
type winDir struct {
	Drive string // "C"
	Comps []string
}

// parseWindowsDir splits an absolute, drive-letter Windows folder path (also
// in the \\?\ form) into its parts. It rejects what the no-follow walk cannot
// guarantee: \\wsl$ and \\wsl.localhost paths, device namespaces, other UNC
// paths (ErrUnsupportedPlatform), relative and drive-relative paths and ".."
// segments. With strict, each component must also pass the name rules (device
// names, ADS colon, trailing dot or space, 8.3 aliases); the project parent of
// a new repo is strict, an existing checkout root is not.
func parseWindowsDir(path string, strict bool) (winDir, error) {
	p := strings.ReplaceAll(path, "/", `\`)
	for _, prefix := range []string{`\\?\`, `\??\`} {
		if rest, ok := strings.CutPrefix(p, prefix); ok {
			if strings.HasPrefix(strings.ToUpper(rest), `UNC\`) {
				return winDir{}, fmt.Errorf("network share paths are not supported for new files (%q): %w", path, ErrUnsupportedPlatform)
			}
			if len(rest) < 2 || rest[1] != ':' {
				return winDir{}, fmt.Errorf("%q is a device path and cannot be used: %w", path, ErrUnsafePath)
			}
			p = rest
			break
		}
	}
	switch {
	case strings.HasPrefix(p, `\\.\`):
		return winDir{}, fmt.Errorf("%q is a device path and cannot be used: %w", path, ErrUnsafePath)
	case strings.HasPrefix(p, `\\`):
		host, _, _ := strings.Cut(strings.TrimPrefix(p, `\\`), `\`)
		if strings.EqualFold(host, "wsl$") || strings.EqualFold(host, "wsl.localhost") {
			return winDir{}, fmt.Errorf("folders inside WSL (%q) are not supported; use a Windows folder: %w", path, ErrUnsafePath)
		}
		return winDir{}, fmt.Errorf("network share paths are not supported for new files (%q): %w", path, ErrUnsupportedPlatform)
	}
	if len(p) < 3 || p[1] != ':' || p[2] != '\\' || !isASCIILetter(p[0]) {
		return winDir{}, fmt.Errorf("%q is not a full path starting with a drive letter such as C:\\: %w", path, ErrUnsafePath)
	}
	out := winDir{Drive: strings.ToUpper(p[:1])}
	for _, c := range strings.Split(p[3:], `\`) {
		if c == "" {
			continue
		}
		if c == "." || c == ".." {
			return winDir{}, fmt.Errorf("%q has a relative segment: %w", path, ErrUnsafePath)
		}
		if strict {
			if why := badComponent(c); why != "" {
				return winDir{}, fmt.Errorf("the folder name %q in %q is not safe (%s): %w", c, path, why, ErrUnsafePath)
			}
		}
		out.Comps = append(out.Comps, c)
	}
	return out, nil
}

func isASCIILetter(b byte) bool { return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' }

// String is the canonical spelling: "C:\a\b".
func (d winDir) String() string {
	return d.Drive + `:\` + strings.Join(d.Comps, `\`)
}
