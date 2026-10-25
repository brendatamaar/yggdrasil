package platform

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Errors of ResolveRoot. Their text is plain language and never holds the
// path, so it can be shown or logged as is (docs/design/guard.md "Failures").
var (
	ErrPathEmpty       = errors.New("the path is empty")
	ErrPathNUL         = errors.New("the path contains a NUL character")
	ErrPathNotAbsolute = errors.New("the path is not absolute; use a full path such as C:\\code\\repo")
	ErrPathStream      = errors.New("the path names an alternate data stream, which is not allowed")
	ErrPathDevice      = errors.New("the path names a device or uses a device namespace, which is not allowed")
	ErrPathTrailing    = errors.New("a part of the path ends with a dot or a space, which is not allowed")
	ErrPathChars       = errors.New("the path contains characters that are not allowed in file names")
	ErrPathUnreadable  = errors.New("the path could not be read; check that it exists and that access is allowed")
	ErrPathBrokenLink  = errors.New("the path goes through a link whose target does not exist")
)

// PathResolver is what the guard needs from the platform to judge a path
// (docs/design/guard.md). Fake: platformtest.FakePaths; contract:
// platformtest.PathResolverContract.
type PathResolver interface {
	// ResolveRoot returns the absolute canonical form of path (see ResolveRoot).
	ResolveRoot(path string) (string, error)
	// FoldCase reports whether paths that differ only in case are the same.
	FoldCase() bool
}

// RealPaths is the PathResolver of this machine.
type RealPaths struct{}

// ResolveRoot implements PathResolver.
func (RealPaths) ResolveRoot(path string) (string, error) { return ResolveRoot(path) }

// FoldCase implements PathResolver.
func (RealPaths) FoldCase() bool { return FoldCase() }

// FoldCase reports whether two spellings of a path that differ only in case
// name the same file: true on Windows, false elsewhere. It is the only OS
// check the guard needs.
func FoldCase() bool { return PathsCaseInsensitive }

// ResolveRoot returns the absolute canonical form of path: ".." collapsed
// lexically first, then the deepest existing part resolved on disk (long
// names instead of 8.3, links and junctions followed, on-disk case); the part
// that does not exist yet is appended as written, so a read of a missing file
// is still judged. It refuses what could name something other than it seems:
// alternate data streams, device names, WSL and device namespaces, trailing
// dots or spaces, NUL characters, empty and relative input. Errors never
// contain the path.
func ResolveRoot(path string) (string, error) {
	p, err := CleanRootPath(path)
	if err != nil {
		return "", err
	}
	var rest []string
	cur := p
	for {
		canon, err := canonicalExisting(cur)
		if err == nil {
			out := canon
			for i := len(rest) - 1; i >= 0; i-- {
				out = filepath.Join(out, rest[i])
			}
			// The target of a link can be somewhere the lexical rules refuse
			// (a junction into \\wsl$, a volume without a drive letter).
			if _, verr := CleanRootPath(out); verr != nil {
				return "", verr
			}
			return out, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", ErrPathUnreadable
		}
		if _, lerr := os.Lstat(cur); lerr == nil {
			return "", ErrPathBrokenLink
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", ErrPathUnreadable
		}
		rest = append(rest, filepath.Base(cur))
		cur = parent
	}
}

// CleanRootPath applies the lexical rules of ResolveRoot without touching the
// disk and returns the cleaned absolute path, with the \\?\ and \\.\ drive
// and UNC forms rewritten to plain ones. On Windows the Windows file-name
// rules apply; elsewhere only NUL, empty and relative input are refused.
func CleanRootPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", ErrPathEmpty
	}
	if strings.ContainsRune(path, 0) {
		return "", ErrPathNUL
	}
	if !PathsCaseInsensitive {
		if !filepath.IsAbs(path) {
			return "", ErrPathNotAbsolute
		}
		return filepath.Clean(path), nil
	}
	vol, rest, err := splitWindowsRoot(path)
	if err != nil {
		return "", err
	}
	for _, seg := range strings.Split(rest, `\`) {
		if err := checkWindowsSegment(seg, true); err != nil {
			return "", err
		}
	}
	return filepath.Clean(vol + `\` + rest), nil
}

// splitWindowsRoot strips namespace prefixes and splits path into its volume
// (X: or \\host\share) and the rest, with / turned into \.
func splitWindowsRoot(path string) (vol, rest string, err error) {
	p := strings.ReplaceAll(path, "/", `\`)
	for _, prefix := range []string{`\\?\`, `\\.\`, `\??\`} {
		if len(p) < len(prefix) || !strings.EqualFold(p[:len(prefix)], prefix) {
			continue
		}
		r := p[len(prefix):]
		switch {
		case len(r) >= 4 && strings.EqualFold(r[:4], `UNC\`):
			p = `\\` + r[4:]
		case len(r) >= 2 && isDriveLetter(r[0]) && r[1] == ':':
			p = r
		default:
			return "", "", ErrPathDevice
		}
		break
	}
	if isWSLHost(strings.ToLower(p)) {
		return "", "", ErrWSLPath
	}
	if strings.HasPrefix(p, `\\`) {
		parts := strings.Split(p[2:], `\`)
		if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
			return "", "", ErrPathNotAbsolute
		}
		for _, s := range parts[:2] {
			if err := checkWindowsSegment(s, false); err != nil {
				return "", "", err
			}
		}
		return `\\` + parts[0] + `\` + parts[1], strings.Join(parts[2:], `\`), nil
	}
	if len(p) >= 3 && isDriveLetter(p[0]) && p[1] == ':' && p[2] == '\\' {
		return p[:2], p[3:], nil
	}
	return "", "", ErrPathNotAbsolute
}

func isDriveLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// checkWindowsSegment applies the file-name rules to one path segment. names
// is false for UNC host and share, which are not file names.
func checkWindowsSegment(seg string, names bool) error {
	if seg == "" || seg == "." || seg == ".." {
		return nil
	}
	if strings.Contains(seg, ":") {
		return ErrPathStream
	}
	if strings.ContainsAny(seg, `<>"|?*`) {
		return ErrPathChars
	}
	for _, r := range seg {
		if r < 0x20 {
			return ErrPathChars
		}
	}
	if !names {
		return nil
	}
	if strings.HasSuffix(seg, ".") || strings.HasSuffix(seg, " ") {
		return ErrPathTrailing
	}
	if isReservedName(seg) {
		return ErrPathDevice
	}
	switch strings.ToUpper(seg) {
	case "CONIN$", "CONOUT$", "CLOCK$":
		return ErrPathDevice
	}
	return nil
}
