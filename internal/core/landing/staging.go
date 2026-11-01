package landing

import (
	"errors"
	"strings"
)

// StagingDir is the folder under the worktree root that holds worktrees being
// removed. Only a StagePath leaf under it is ever deleted.
const StagingDir = ".removing"

const maxStageNameRunes = 40

// StagePath names a staged folder as data: the shell joins it with
// filepath.Join(Root, StagingDir, Leaf). The core never builds a path string,
// so it never concatenates separators (docs/AGENTS.md 5.2).
type StagePath struct {
	Root string
	Leaf string
}

// StagingPath derives the staged folder for a worktree (R9). The leaf is the
// worktree name reduced to [A-Za-z0-9._-] (anything else becomes "_", leading
// dots are dropped, at most 40 characters, "wt" when nothing is left) plus "-"
// and the id. It therefore holds no separator, is never "." or "..", and is
// unique per id. id must be 1 to 64 characters of [A-Za-z0-9_-]; root must be
// non-empty and free of NUL.
func StagingPath(root, name, id string) (StagePath, error) {
	if root == "" || strings.ContainsRune(root, 0) {
		return StagePath{}, errors.New("the worktree root is empty or invalid")
	}
	if id == "" || len(id) > 64 || strings.Trim(id, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") != "" {
		return StagePath{}, errors.New("the removal ID must be 1 to 64 letters, digits, '_' or '-'")
	}
	var b strings.Builder
	n := 0
	for _, r := range name {
		if n >= maxStageNameRunes {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == '.':
			if b.Len() == 0 {
				continue // no leading dot: never "." or ".."
			}
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
		n++
	}
	clean := b.String()
	if clean == "" {
		clean = "wt"
	}
	return StagePath{Root: root, Leaf: clean + "-" + id}, nil
}
