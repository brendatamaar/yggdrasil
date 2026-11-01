package git

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrMergeTreeUnsupported: this git cannot do `merge-tree --write-tree`
// (needs git 2.38 or newer). The caller falls back to merge-then-abort.
var ErrMergeTreeUnsupported = errors.New("git is too old for merge-tree --write-tree")

// Prediction is what a merge would do, found without touching any working tree.
type Prediction struct {
	// Clean is true when the merge has no conflicts.
	Clean bool
	// Files are the conflicted paths, sorted (empty when Clean).
	Files []string
	// Tree is the object ID of the merged top-level tree (also written for a
	// conflicted merge, with markers in the conflicted files).
	Tree string
}

// MergeTreeArgv is the argument list of the prediction (after "git"). The
// command only adds objects to the object store; it changes no ref, index or
// working tree.
func MergeTreeArgv(ours, theirs string) []string {
	return []string{"--no-optional-locks", "-c", "core.fsmonitor=false", "merge-tree", "--write-tree", "--name-only", "-z", ours, theirs}
}

// MergeTreeWriteTree predicts the merge of theirs into ours (commits or refs)
// with `git merge-tree --write-tree`. It returns ErrMergeTreeUnsupported on a
// git older than 2.38.
func (g *Git) MergeTreeWriteTree(ctx context.Context, dir, ours, theirs string) (Prediction, error) {
	if err := checkName("revision", ours); err != nil {
		return Prediction{}, err
	}
	if err := checkName("revision", theirs); err != nil {
		return Prediction{}, err
	}
	out, se, code, err := g.execWith(ctx, dir, "merge-tree", nil, false, MergeTreeArgv(ours, theirs)...)
	if err != nil {
		return Prediction{}, err
	}
	if code == 129 {
		// Usage error: this git does not know --write-tree.
		return Prediction{}, &Error{Op: "merge-tree", Code: code, Stderr: se, Err: ErrMergeTreeUnsupported,
			UserMsg: "This git is too old to check a merge without changing files (needs git 2.38). Update git."}
	}
	p, perr := ParseMergeTree(out, code)
	if perr != nil {
		return Prediction{}, &Error{Op: "merge-tree", Code: code, Stderr: se, Err: perr,
			UserMsg: "Yggdrasil could not check whether the branch merges cleanly. Update git and try again."}
	}
	return p, nil
}

// ParseMergeTree reads the output of `git merge-tree --write-tree --name-only
// -z` and the exit code (0 clean, 1 conflicts, anything else is an error).
//
// Layout: <tree OID> NUL, then one conflicted file name per NUL, then an empty
// entry (a second NUL) and the informational messages, which are ignored.
func ParseMergeTree(out string, exit int) (Prediction, error) {
	if exit != 0 && exit != 1 {
		return Prediction{}, fmt.Errorf("merge-tree exit status %d", exit)
	}
	oid, rest, found := strings.Cut(out, "\x00")
	if !found || !isCommitID(oid) {
		return Prediction{}, errors.New("merge-tree output has no tree id")
	}
	var files []string
	for rest != "" {
		name, tail, _ := strings.Cut(rest, "\x00")
		if name == "" {
			break // start of the messages section
		}
		if strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return Prediction{}, errors.New("merge-tree output has a file name with control characters")
		}
		files = append(files, name)
		rest = tail
	}
	if exit == 0 && len(files) > 0 {
		return Prediction{}, errors.New("merge-tree reported conflicts with a zero exit status")
	}
	sort.Strings(files)
	return Prediction{Clean: exit == 0, Files: files, Tree: oid}, nil
}
