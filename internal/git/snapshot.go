package git

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/core/guard"
)

// Snapshot fingerprints the working tree of dir for the Ask-mode integrity
// check (SF-07). It only reads. Status is a hash of `git status` plus the
// content of every untracked file (so editing a file that was already
// untracked changes it); DiffHash hashes `git diff --binary HEAD` (content,
// modes, line endings of tracked files). Ignored files are excluded. Paths
// lists the changed and untracked files for the user.
func (g *Git) Snapshot(ctx context.Context, dir string) (guard.Snapshot, error) {
	head, err := g.run(ctx, dir, "snapshot", true, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return guard.Snapshot{}, err
	}
	head = strings.TrimSpace(head)
	if head == "" {
		return guard.Snapshot{}, &Error{Op: "snapshot", Err: ErrInvalidArg, UserMsg: "This repository has no commits yet."}
	}
	status, err := g.run(ctx, dir, "snapshot", true, "status", "--porcelain=v2", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return guard.Snapshot{}, err
	}
	diff, err := g.run(ctx, dir, "snapshot", true, "diff", "--binary", "--no-color", "--no-ext-diff", "--no-textconv", "HEAD", "--")
	if err != nil {
		return guard.Snapshot{}, err
	}
	names, err := g.run(ctx, dir, "snapshot", true, "diff", "--name-only", "-z", "--no-renames", "HEAD", "--")
	if err != nil {
		return guard.Snapshot{}, err
	}
	others, err := g.run(ctx, dir, "snapshot", true, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return guard.Snapshot{}, err
	}

	untracked := splitNUL(others)
	sort.Strings(untracked)
	uh := sha256.New()
	for _, rel := range untracked {
		if err := ctx.Err(); err != nil {
			return guard.Snapshot{}, err
		}
		if err := hashUntracked(uh, dir, rel); err != nil {
			return guard.Snapshot{}, &Error{Op: "snapshot", Err: err, UserMsg: "Yggdrasil could not read every new file to check that nothing changed."}
		}
	}

	sh := sha256.New()
	sh.Write([]byte(status))
	sh.Write([]byte{0})
	sh.Write(uh.Sum(nil))
	dh := sha256.Sum256([]byte(diff))

	set := map[string]bool{}
	for _, p := range splitNUL(names) {
		set[p] = true
	}
	for _, p := range untracked {
		set[p] = true
	}
	paths := make([]string, 0, len(set))
	for p := range set {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return guard.Snapshot{
		Head:     head,
		Status:   hex.EncodeToString(sh.Sum(nil)),
		DiffHash: hex.EncodeToString(dh[:]),
		Paths:    paths,
	}, nil
}

func splitNUL(s string) []string {
	var out []string
	for _, p := range strings.Split(s, "\x00") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// hashUntracked adds the path and content of one untracked file. A link is
// hashed by its target text, never followed; a folder (a nested repo) is
// hashed by name only.
func hashUntracked(h io.Writer, dir, rel string) error {
	full := filepath.Join(dir, filepath.FromSlash(rel))
	fi, err := os.Lstat(full)
	if err != nil {
		return fmt.Errorf("read untracked file: %w", err)
	}
	fmt.Fprintf(h, "%d:%s\x00", len(rel), rel)
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(full)
		if err != nil {
			return fmt.Errorf("read link: %w", err)
		}
		fmt.Fprintf(h, "L%s\x00", target)
	case fi.Mode().IsRegular():
		f, err := os.Open(full) //nolint:gosec // inside the repo, listed by git
		if err != nil {
			return fmt.Errorf("open untracked file: %w", err)
		}
		defer f.Close()
		fmt.Fprintf(h, "F%d\x00", fi.Size())
		if _, err := io.Copy(h, f); err != nil {
			return fmt.Errorf("read untracked file: %w", err)
		}
	default:
		fmt.Fprint(h, "D")
	}
	return nil
}
