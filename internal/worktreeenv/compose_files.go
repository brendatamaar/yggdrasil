package worktreeenv

import (
	"errors"
	"fmt"
	"io"
	"os"

	env "github.com/brendatamaar/yggdrasil/internal/core/worktreeenv"
)

// MaxComposeFile is the largest compose file that is read and hashed.
const MaxComposeFile = 1 << 20

// ErrComposeFile means the compose file is missing, too large, a link, or
// reaches outside the worktree through a link.
var ErrComposeFile = errors.New("worktreeenv: compose file is not a regular file inside the worktree")

// ComposeFiles reads the compose file of a worktree.
type ComposeFiles interface {
	// Read returns the bytes of rel below root. A missing file, a folder, a
	// symlink or reparse point at any level, or a file over MaxComposeFile is
	// ErrComposeFile. Nothing is written or removed.
	Read(root string, rel env.RelPath) ([]byte, error)
}

// OSComposeFiles is ComposeFiles on the real disk, with the same link and
// reparse-point checks as the profile copy.
type OSComposeFiles struct{}

// Read implements ComposeFiles.
func (OSComposeFiles) Read(root string, rel env.RelPath) ([]byte, error) {
	if rel.Validate() != nil {
		return nil, ErrComposeFile
	}
	k, lfi := walk(root, rel)
	if k != kindFile || lfi.Size() > MaxComposeFile {
		return nil, ErrComposeFile
	}
	f, err := os.Open(join(root, rel)) //nolint:gosec // rel validated by RelPath.Validate and walked with Lstat
	if err != nil {
		return nil, fmt.Errorf("open compose file: %w", err)
	}
	defer f.Close()
	ofi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect compose file: %w", err)
	}
	if !os.SameFile(ofi, lfi) || !ofi.Mode().IsRegular() {
		return nil, ErrComposeFile // swapped between the check and the open
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxComposeFile+1))
	if err != nil {
		return nil, fmt.Errorf("read compose file: %w", err)
	}
	if len(data) > MaxComposeFile {
		return nil, ErrComposeFile
	}
	return data, nil
}
