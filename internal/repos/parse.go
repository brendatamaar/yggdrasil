package repos

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// relKey is what two repo folders are compared by: on Windows the same folder
// can be spelled in different case.
func relKey(rel corerepos.RelPath) string {
	if platform.PathsCaseInsensitive {
		return strings.ToLower(string(rel))
	}
	return string(rel)
}

// MaxPicks bounds the repos one request can track.
const MaxPicks = 200

func invalid(msg string, err error) error { return apperr.New(apperr.Invalid, msg, err) }

// ParseFolder is the boundary parser of a folder path from HTTP or the CLI.
// It accepts an absolute path of an existing folder and returns its canonical
// form: cleaned, 8.3 short names expanded, the drive letter in upper case and
// the \\?\ prefix stripped. It rejects what platform.CheckPath refuses (WSL
// and device paths), relative paths, control characters and anything that is
// not an existing folder. Rejections are apperr.Invalid with a message that
// says what to do.
func ParseFolder(raw string) (corerepos.CleanPath, error) {
	if strings.TrimSpace(raw) == "" {
		return "", invalid("The folder path is empty. Choose a folder on this computer.", nil)
	}
	if strings.ContainsFunc(raw, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", invalid("The folder path holds a character that isn't allowed. Choose the folder again.", nil)
	}
	if err := platform.CheckPath(raw); err != nil {
		return "", invalid(err.Error(), err)
	}
	p := stripNamespace(raw)
	if !filepath.IsAbs(p) {
		return "", invalid(fmt.Sprintf("%q isn't a full path. Choose a folder on this computer, such as C:\\work\\shop.", raw), nil)
	}
	p = filepath.Clean(p)
	// The folder itself must not be a link: its content would live somewhere
	// else than the path says (invariant 6).
	link, err := platform.IsReparsePoint(p)
	if err != nil {
		return "", invalid(fmt.Sprintf("The folder %q doesn't exist or can't be read. Choose another folder.", raw), err)
	}
	if link {
		return "", invalid(fmt.Sprintf("The folder %q is a link to somewhere else. Choose the real folder.", raw), nil)
	}
	// One spelling per folder: the on-disk case of every segment, long names.
	long, err := platform.CanonicalPath(p)
	if err != nil {
		return "", invalid(fmt.Sprintf("The folder %q doesn't exist or can't be read. Choose another folder.", raw), err)
	}
	long = filepath.Clean(long)
	// The folder is the one the user chose; this only looks at it.
	if st, err := os.Stat(long); err != nil || !st.IsDir() { //nolint:gosec // path chosen by the user
		return "", invalid(fmt.Sprintf("%q isn't a folder. Choose a folder, not a file.", raw), err)
	}
	cp, err := corerepos.NewCleanPath(long)
	if err != nil {
		return "", invalid("This folder path isn't supported. Choose a folder with a normal path.", err)
	}
	return cp, nil
}

// stripNamespace removes the Win32 extended-length prefix so one folder has
// one stored form.
func stripNamespace(p string) string {
	if rest, ok := strings.CutPrefix(p, `\\?\UNC\`); ok {
		return `\\` + rest
	}
	if rest, ok := strings.CutPrefix(p, `\\?\`); ok {
		return rest
	}
	return p
}

// FormatFolder is the text form of a parsed folder: ParseFolder(FormatFolder(p))
// gives p back for an existing folder.
func FormatFolder(p corerepos.CleanPath) string { return string(p) }

// ParseBrowsePath parses the path of the folder browser. An empty path means
// the roots and gives an empty CleanPath.
func ParseBrowsePath(raw string) (corerepos.CleanPath, error) {
	if raw == "" {
		return "", nil
	}
	return ParseFolder(raw)
}

// ParseName parses a project name: trimmed, not empty, at most
// corerepos.MaxNameRunes characters, no control characters.
func ParseName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if err := corerepos.ValidName(name); err != nil {
		return "", invalid("Choose a project name of 1 to 100 characters without control characters.", err)
	}
	return name, nil
}

// ParsePicks parses the repos a user selected. The relative paths must be
// forward-slash paths under the project folder (no "..", no absolute path),
// distinct, and at most MaxPicks; a blank name becomes the folder's name.
// NewRepoID stays empty: the service fills it.
func ParsePicks(path corerepos.CleanPath, raw []corerepos.Pick) ([]corerepos.Pick, error) {
	if len(raw) == 0 {
		return nil, invalid("Select at least one repo to track.", nil)
	}
	if len(raw) > MaxPicks {
		return nil, invalid(fmt.Sprintf("Select at most %d repos at a time.", MaxPicks), nil)
	}
	seen := make(map[string]bool, len(raw))
	out := make([]corerepos.Pick, 0, len(raw))
	for _, pk := range raw {
		rel, err := corerepos.NewRelPath(string(pk.RelPath))
		if err != nil {
			return nil, invalid(fmt.Sprintf("The repo folder %q isn't a folder inside the project.", string(pk.RelPath)), err)
		}
		key := relKey(rel)
		if seen[key] {
			return nil, invalid(fmt.Sprintf("The repo folder %q is selected twice.", string(rel)), nil)
		}
		seen[key] = true
		name := strings.TrimSpace(pk.Name)
		if name == "" {
			name = corerepos.DefaultRepoName(path, rel)
		}
		if err := corerepos.ValidName(name); err != nil {
			return nil, invalid(fmt.Sprintf("The name %q isn't allowed for a repo: use 1 to 100 characters without control characters.", pk.Name), err)
		}
		out = append(out, corerepos.Pick{RelPath: rel, Name: name})
	}
	return out, nil
}
