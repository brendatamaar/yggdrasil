package git

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
	// core.fsmonitor=false: a repo-configured fsmonitor hook would be a
	// program the repo chooses; the read-only snapshot never starts one.
	ro := func(args ...string) (string, error) {
		return g.run(ctx, dir, "snapshot", true, append([]string{"-c", "core.fsmonitor=false"}, args...)...)
	}
	head, err := ro("rev-parse", "--verify", "HEAD")
	if err != nil {
		return guard.Snapshot{}, err
	}
	head = strings.TrimSpace(head)
	if head == "" {
		return guard.Snapshot{}, &Error{Op: "snapshot", Err: ErrInvalidArg, UserMsg: "This repository has no commits yet."}
	}
	status, err := ro("status", "--porcelain=v2", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return guard.Snapshot{}, err
	}
	diff, err := ro("diff", "--binary", "--no-color", "--no-ext-diff", "--no-textconv", "HEAD", "--")
	if err != nil {
		return guard.Snapshot{}, err
	}
	names, err := ro("diff", "--name-only", "-z", "--no-renames", "HEAD", "--")
	if err != nil {
		return guard.Snapshot{}, err
	}
	others, err := ro("ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return guard.Snapshot{}, err
	}
	// Every branch, tag and other ref with its target: a new or moved ref
	// changes the fingerprint even when the tree and HEAD do not.
	refs, err := ro("for-each-ref")
	if err != nil {
		return guard.Snapshot{}, err
	}

	untracked := splitNUL(others)
	sort.Strings(untracked)
	uh := sha256.New()
	budget := &untrackedBudget{ctx: ctx, maxFiles: snapshotMaxFiles, maxBytes: snapshotMaxBytes}
	for _, rel := range untracked {
		if err := ctx.Err(); err != nil {
			return guard.Snapshot{}, err
		}
		if err := hashUntracked(uh, dir, rel, budget); err != nil {
			if errors.Is(err, ErrSnapshotTooLarge) {
				return guard.Snapshot{}, &Error{Op: "snapshot", Err: err, UserMsg: "The working folder has too many or too large new files for Yggdrasil to check that nothing changed. Move build output and downloads out of the repo or add them to .gitignore."}
			}
			if ctx.Err() != nil {
				return guard.Snapshot{}, ctx.Err()
			}
			return guard.Snapshot{}, &Error{Op: "snapshot", Err: err, UserMsg: "Yggdrasil could not read every new file to check that nothing changed."}
		}
	}

	sh := sha256.New()
	sh.Write([]byte(status))
	sh.Write([]byte{0})
	sh.Write(uh.Sum(nil))
	rh := sha256.Sum256([]byte(refs))
	sh.Write(rh[:])
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

// Limits of the untracked-file hashing, so a repo full of build output or
// downloads neither stalls the check nor fills memory.
const (
	snapshotMaxFiles = 50000
	snapshotMaxBytes = 256 << 20
)

// ErrSnapshotTooLarge is wrapped when the untracked files are too many or too
// large to fingerprint. The check then fails closed (SF-07).
var ErrSnapshotTooLarge = errors.New("working tree too large to fingerprint")

type untrackedBudget struct {
	ctx      context.Context
	maxFiles int
	maxBytes int64
	files    int
	bytes    int64
}

func (b *untrackedBudget) addFile() error {
	b.files++
	if b.files > b.maxFiles {
		return fmt.Errorf("%w: more than %d new files", ErrSnapshotTooLarge, b.maxFiles)
	}
	return nil
}

func (b *untrackedBudget) addBytes(n int64) error {
	b.bytes += n
	if b.bytes > b.maxBytes {
		return fmt.Errorf("%w: more than %d MiB of new files", ErrSnapshotTooLarge, b.maxBytes>>20)
	}
	return nil
}

// ctxReader fails reads once its context is done, so io.Copy stops on cancel.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
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
func hashUntracked(h io.Writer, dir, rel string, b *untrackedBudget) error {
	full := filepath.Join(dir, filepath.FromSlash(rel))
	fi, err := os.Lstat(full)
	if err != nil {
		return fmt.Errorf("read untracked file: %w", err)
	}
	if err := b.addFile(); err != nil {
		return err
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
		if err := b.addBytes(fi.Size()); err != nil {
			return err
		}
		fmt.Fprintf(h, "F%d\x00", fi.Size())
		// The reader stops when the context is cancelled and never reads more
		// than the size announced plus one byte (a file growing while read).
		r := &ctxReader{ctx: b.ctx, r: io.LimitReader(f, fi.Size()+1)}
		if _, err := io.Copy(h, r); err != nil {
			return fmt.Errorf("read untracked file: %w", err)
		}
	default:
		fmt.Fprint(h, "D")
	}
	return nil
}
