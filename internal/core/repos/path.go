package repos

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// CleanPath is a canonical absolute folder. Equality of two CleanPaths is
// plain ==, so the shell parser (ParseFolder) must produce one canonical form
// per folder: cleaned, drive letter in canonical case, long-path prefix
// stripped. NewCleanPath checks the shape only; whether the folder exists is
// the shell's business.
type CleanPath string

// NewCleanPath accepts an absolute Unix path ("/work/x"), a drive path
// ("C:\work\x" or "C:/work/x") or a UNC path ("\\host\share\x"). It rejects
// empty input, NUL and control characters, device and extended-length
// prefixes (the shell strips them first), "." and ".." segments, empty
// segments and a trailing separator (except on a root).
func NewCleanPath(raw string) (CleanPath, error) {
	if raw == "" {
		return "", errors.New("the path is empty")
	}
	if strings.ContainsFunc(raw, isControl) {
		return "", errors.New("the path holds a control character")
	}
	rest, err := stripRoot(raw)
	if err != nil {
		return "", err
	}
	if rest != "" {
		if strings.HasSuffix(rest, "/") || strings.HasSuffix(rest, `\`) {
			return "", fmt.Errorf("the path %q ends with a separator", raw)
		}
		for _, seg := range strings.FieldsFunc(rest, isSep) {
			if seg == "." || seg == ".." {
				return "", fmt.Errorf("the path %q holds a %q segment", raw, seg)
			}
		}
		if strings.Contains(rest, "//") || strings.Contains(rest, `\\`) || strings.Contains(rest, `/\`) || strings.Contains(rest, `\/`) {
			return "", fmt.Errorf("the path %q holds an empty segment", raw)
		}
	}
	return CleanPath(raw), nil
}

// stripRoot checks the root of an absolute path and returns what follows it.
func stripRoot(p string) (string, error) {
	switch {
	case strings.HasPrefix(p, `\\?\`) || strings.HasPrefix(p, `\\.\`) || strings.HasPrefix(p, `\??\`):
		return "", fmt.Errorf("the path %q is a device or extended-length path", p)
	case strings.HasPrefix(p, `\\`):
		unc := strings.FieldsFunc(p[2:], isSep)
		if len(unc) < 2 {
			return "", fmt.Errorf("the path %q is not a UNC share", p)
		}
		return p[2:], nil
	case p[0] == '/':
		return p[1:], nil
	case len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') &&
		(p[0] >= 'A' && p[0] <= 'Z' || p[0] >= 'a' && p[0] <= 'z'):
		return p[3:], nil
	}
	return "", fmt.Errorf("the path %q isn't absolute", p)
}

func isSep(r rune) bool     { return r == '/' || r == '\\' }
func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

// String returns the path.
func (p CleanPath) String() string { return string(p) }

// RelPath is a path under a project folder with forward slashes only, "." for
// the folder itself.
type RelPath string

// NewRelPath accepts "." or a slash-separated path whose segments are not
// empty, "." or "..", with no leading or trailing slash, backslash, drive
// letter colon or control character.
func NewRelPath(raw string) (RelPath, error) {
	if raw == "" {
		return "", errors.New("the relative path is empty")
	}
	if raw == "." {
		return RelPath("."), nil
	}
	if strings.ContainsFunc(raw, isControl) || strings.ContainsAny(raw, `\:`) {
		return "", fmt.Errorf("the relative path %q holds a character that isn't allowed", raw)
	}
	if strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("the relative path %q is absolute", raw)
	}
	for _, seg := range strings.Split(raw, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("the relative path %q holds a %q segment", raw, seg)
		}
	}
	return RelPath(raw), nil
}

// String returns the path.
func (p RelPath) String() string { return string(p) }

// MaxNameRunes bounds project and repo names.
const MaxNameRunes = 100

// ValidName reports whether s is a usable project or repo name: non-blank, at
// most MaxNameRunes runes, no control characters.
func ValidName(s string) error {
	if strings.TrimSpace(s) == "" {
		return errors.New("the name is empty")
	}
	if utf8.RuneCountInString(s) > MaxNameRunes {
		return fmt.Errorf("the name is longer than %d characters", MaxNameRunes)
	}
	if strings.ContainsFunc(s, isControl) {
		return errors.New("the name holds a control character")
	}
	return nil
}
