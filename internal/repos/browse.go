package repos

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// Dir is one folder in the folder browser.
type Dir struct {
	Name string
	// Path is the full path to browse deeper or to preview.
	Path string
	// IsRepo: the folder has a .git folder or file.
	IsRepo bool
}

// DirLister lists folders for the folder browser. It only reads.
type DirLister interface {
	// Roots returns the starting points: the drives on Windows, "/" elsewhere.
	Roots() ([]string, error)
	// ListDirs returns the subfolders of path, sorted by name without case.
	// It lists only folders, flags repos and leaves out links (symlinks,
	// junctions), so the browser can't be led out of the tree it shows.
	ListDirs(ctx context.Context, path corerepos.CleanPath) ([]Dir, error)
}

// FSDirs is the live DirLister.
type FSDirs struct{}

var _ DirLister = FSDirs{}

// Roots implements DirLister.
func (FSDirs) Roots() ([]string, error) { return platform.ListRoots() }

// ListDirs implements DirLister.
func (FSDirs) ListDirs(ctx context.Context, path corerepos.CleanPath) ([]Dir, error) {
	entries, err := os.ReadDir(string(path)) //nolint:gosec // browsing is read-only and chosen by the user
	if err != nil {
		return nil, apperr.New(apperr.Invalid, fmt.Sprintf("The folder %q can't be read. Choose another folder.", string(path)), err)
	}
	out := make([]Dir, 0, len(entries))
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("browse stopped: %w", err)
		}
		if !e.IsDir() {
			continue
		}
		full := filepath.Join(string(path), e.Name())
		if link, err := platform.IsReparsePoint(full); err != nil || link {
			continue
		}
		_, gitErr := os.Stat(filepath.Join(full, ".git")) //nolint:gosec // read-only
		out = append(out, Dir{Name: e.Name(), Path: full, IsRepo: gitErr == nil})
	}
	slices.SortFunc(out, func(a, b Dir) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return out, nil
}

// parentOf is the folder above path, or "" at a root (so the browser goes
// back to the list of roots).
func parentOf(path corerepos.CleanPath) string {
	p := string(path)
	dir := filepath.Dir(p)
	if dir == p || dir == "." {
		return ""
	}
	return dir
}
