package proposals

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// This file is the only place the proposals shell touches the file system for
// AgentFile and DeleteFiles. There is no remove call anywhere in the package
// (invariant 1, archtest "proposals and git never delete files"): files are
// moved into _to_delete/ or replaced by a rename.

// toDeleteDir is the folder DeleteFiles moves files into.
const toDeleteDir = "_to_delete"

// FileHash is the hash an AgentFile proposal carries in ExpectHash: the
// lower-case hex SHA-256 of the file content.
func FileHash(content []byte) string {
	h := sha256.Sum256(content)
	return hex.EncodeToString(h[:])
}

func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// insideRoot resolves rel under root the way the guard resolves paths (long
// names, links and junctions followed) and fails unless the result is still
// inside root. It returns the resolved path. Errors never hold the path.
func insideRoot(root, rel string) (string, error) {
	rootAbs, err := platform.ResolveRoot(root)
	if err != nil {
		return "", fmt.Errorf("the folder of this change cannot be used: %w", err)
	}
	full, err := platform.ResolveRoot(filepath.Join(rootAbs, filepath.FromSlash(rel)))
	if err != nil {
		return "", fmt.Errorf("a file path cannot be used: %w", err)
	}
	a, b := rootAbs, full
	if platform.FoldCase() {
		a, b = asciiLower(a), asciiLower(b)
	}
	r, err := filepath.Rel(a, b)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) || filepath.IsAbs(r) {
		return "", errors.New("a file path leads outside the folder of this change")
	}
	return full, nil
}

// writeAtomic replaces the content of path: it writes a temporary file in the
// same folder and renames it over path, so a reader sees the old or the new
// content, never half of it. A failed rename leaves the temporary file, which
// is named so the user can tell it from their own files.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("make folder: %w", err)
	}
	perm := fs.FileMode(0o644)
	if fi, err := os.Lstat(path); err == nil {
		if !fi.Mode().IsRegular() {
			return errors.New("the target is not a regular file")
		}
		perm = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(dir, ".ygg-write-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("flush temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	if err := os.Chmod(tmp.Name(), perm); err != nil {
		return fmt.Errorf("set permissions: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("replace file: %w", err)
	}
	return nil
}

// moveAside moves root/rel to root/_to_delete/rel (a numeric suffix is added
// when the name is taken) and returns the new path relative to root. It never
// deletes anything.
func moveAside(root, rel string) (string, error) {
	slash := strings.Trim(filepath.ToSlash(rel), "/")
	if slash == "" || slash == toDeleteDir || strings.HasPrefix(slash, toDeleteDir+"/") {
		return "", errors.New("this path is already in " + toDeleteDir)
	}
	src := filepath.Join(root, filepath.FromSlash(slash))
	if _, err := os.Lstat(src); err != nil {
		return "", err
	}
	dest := filepath.Join(root, toDeleteDir, filepath.FromSlash(slash))
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return "", fmt.Errorf("make %s: %w", toDeleteDir, err)
	}
	candidate := dest
	for n := 1; ; n++ {
		if _, err := os.Lstat(candidate); errors.Is(err, fs.ErrNotExist) {
			break
		} else if err != nil {
			return "", err
		}
		if n > 10000 {
			return "", errors.New("too many files with this name in " + toDeleteDir)
		}
		candidate = dest + "." + strconv.Itoa(n)
	}
	if err := os.Rename(src, candidate); err != nil {
		return "", err
	}
	out, err := filepath.Rel(root, candidate)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(out), nil
}

// moveLog is the file in _to_delete that records, per proposal, where each
// file went ("<path>\t<destination>" per line). A retry or a reconcile trusts
// only this record, so a file that was in _to_delete before cannot count.
func moveLog(root, proposal string) string {
	return filepath.Join(root, toDeleteDir, ".ygg-moves-"+proposal+".log")
}

// recordMove appends one move to the proposal's record (append only).
func recordMove(root, proposal, rel, dest string) error {
	f, err := os.OpenFile(moveLog(root, proposal), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // inside the task folder
	if err != nil {
		return err
	}
	_, werr := f.WriteString(strings.Trim(filepath.ToSlash(rel), "/") + "\t" + dest + "\n")
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// movedAlready returns where this proposal moved rel to: the move is in its
// record, the source is gone and the destination exists.
func movedAlready(root, proposal, rel string) (string, bool) {
	slash := strings.Trim(filepath.ToSlash(rel), "/")
	if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(slash))); err == nil {
		return "", false
	}
	b, err := os.ReadFile(moveLog(root, proposal)) //nolint:gosec // inside the task folder
	if err != nil {
		return "", false
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		path, dest, ok := strings.Cut(line, "\t")
		if !ok || path != slash {
			continue
		}
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(dest))); err == nil {
			return dest, true
		}
	}
	return "", false
}
