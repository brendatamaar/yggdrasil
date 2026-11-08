package git

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// File status letters of a BaseFile.
const (
	FileAdded    = "A"
	FileModified = "M"
	FileDeleted  = "D"
	FileRenamed  = "R" // never produced: renames are off, a rename is a delete plus an add
)

const (
	defaultPatchLimit = 256 << 10 // bytes of patch kept per file
	defaultMaxFiles   = maxStatusFiles
	// untrackedPatchMax: an untracked file larger than this gets no patch (git
	// would build the whole patch in memory).
	untrackedPatchMax = 1 << 20
)

// BaseFile is one file the agent changed relative to the worktree's base
// commit. (It is not FileChange, the status entry of a repo.)
type BaseFile struct {
	Path   string `json:"path"` // slash-separated, relative to the worktree
	Status string `json:"status"`
	Binary bool   `json:"binary"`
	// Added and Deleted are line counts (0 for a binary file, and for an
	// untracked file too big to diff: see PatchTruncated).
	Added   int `json:"added"`
	Deleted int `json:"deleted"`
	// EOLOnly: the whole change is a line-ending change (PL-03).
	EOLOnly bool `json:"eolOnly"`
	// Hash fingerprints this file's change. For a text file whose patch fits
	// the limit it is the lowercase hex SHA-256 of exactly the bytes of Patch
	// (nothing else mixed in), so a client holding the patch computes the same
	// value. When the patch is not usable (binary, cut at the limit, or an
	// untracked file too big to diff) it is made from the git hash of the
	// file's current content instead (never equal to a client's hash of the
	// cut patch). It changes when the change changes, so a review note can
	// tell it has gone stale. A line-ending-only change alters the patch, so
	// it alters the hash.
	Hash string `json:"hash"`
	// Patch is the unified diff of the file, cut at the limit. Empty for a
	// binary file.
	Patch          string `json:"patch"`
	PatchTruncated bool   `json:"patchTruncated"`
}

// BaseDiff is the work in a worktree relative to its base commit: commits made
// since the base, uncommitted changes and untracked files together.
type BaseDiff struct {
	HasChanges bool       `json:"hasChanges"`
	Files      []BaseFile `json:"files"` // sorted by path
	// Truncated: more files changed than MaxFiles; the rest are not listed.
	// It is also set when a patch was cut; see FilesTruncated.
	Truncated bool `json:"truncated"`
	// FilesTruncated: only the file list was cut at MaxFiles. A cut patch
	// does not set it.
	FilesTruncated bool `json:"filesTruncated"`
}

// BaseDiffOptions bound the size of the answer. Zero values mean the defaults.
type BaseDiffOptions struct {
	// PatchLimit caps the patch of one file in bytes (default 256 KiB).
	PatchLimit int
	// MaxFiles caps the number of files listed (default 5000).
	MaxFiles int
}

// DiffAgainstBase compares the worktree at dir (its root) with the commit base.
// Ignored files are left out; untracked files appear as added files. Renames
// are off. It only reads, and writes no file or config.
func (g *Git) DiffAgainstBase(ctx context.Context, dir, base string, opts BaseDiffOptions) (BaseDiff, error) {
	if err := checkCommitID("base", base); err != nil {
		return BaseDiff{}, err
	}
	limit := opts.PatchLimit
	if limit <= 0 {
		limit = defaultPatchLimit
	}
	maxFiles := opts.MaxFiles
	if maxFiles <= 0 {
		maxFiles = defaultMaxFiles
	}
	diffArgs := func(extra ...string) []string {
		a := []string{"-c", "core.fsmonitor=false", "diff", "--no-color", "--no-ext-diff", "--no-textconv", "--no-renames"}
		return append(append(a, extra...), base, "--")
	}
	statusOut, err := g.run(ctx, dir, "diff base", true, diffArgs("--name-status", "-z")...)
	if err != nil {
		return BaseDiff{}, err
	}
	plain, err := g.run(ctx, dir, "diff base", true, diffArgs("--numstat", "-z")...)
	if err != nil {
		return BaseDiff{}, err
	}
	ignoring, err := g.run(ctx, dir, "diff base", true, diffArgs("--numstat", "-z", "--ignore-cr-at-eol")...)
	if err != nil {
		return BaseDiff{}, err
	}
	others, err := g.run(ctx, dir, "diff base", true, "-c", "core.fsmonitor=false", "ls-files", "--others", "--exclude-standard", "-z", "--")
	if err != nil {
		return BaseDiff{}, err
	}

	stats, err := ParseNumstat(plain)
	if err != nil {
		return BaseDiff{}, badDiffOutput(err)
	}
	counts := make(map[string]NumstatEntry, len(stats))
	for _, e := range stats {
		counts[e.Path] = e
	}
	eol, err := ClassifyEOL(plain, ignoring)
	if err != nil {
		return BaseDiff{}, badDiffOutput(err)
	}
	isEOL := make(map[string]bool, len(eol))
	for _, p := range eol {
		isEOL[p] = true
	}
	tracked, err := parseNameStatus(statusOut)
	if err != nil {
		return BaseDiff{}, badDiffOutput(err)
	}

	var files []BaseFile
	for _, t := range tracked {
		f := BaseFile{Path: t.path, Status: t.status}
		c := counts[t.path]
		f.Binary, f.Added, f.Deleted = c.Binary, c.Added, c.Deleted
		f.EOLOnly = f.Status == FileModified && isEOL[t.path]
		files = append(files, f)
	}
	for _, rel := range splitNUL(others) {
		files = append(files, BaseFile{Path: rel, Status: FileAdded})
	}
	sort.SliceStable(files, func(i, j int) bool { return files[i].Path < files[j].Path })

	res := BaseDiff{HasChanges: len(files) > 0}
	if len(files) > maxFiles {
		files, res.Truncated = files[:maxFiles], true
		res.FilesTruncated = true
	}
	trackedSet := make(map[string]bool, len(tracked))
	for _, t := range tracked {
		trackedSet[t.path] = true
	}
	for i := range files {
		if err := g.fillPatch(ctx, dir, base, &files[i], trackedSet[files[i].Path], limit); err != nil {
			return BaseDiff{}, err
		}
		if files[i].PatchTruncated {
			res.Truncated = true
		}
	}
	res.Files = files
	return res, nil
}

func badDiffOutput(err error) error {
	return &Error{Op: "diff base", Err: err, UserMsg: "Yggdrasil could not read the output of git diff. Update git and try again."}
}

type nameStatus struct{ status, path string }

// parseNameStatus reads `git diff --name-status -z --no-renames`: status NUL
// path NUL. Type changes and unmerged entries count as modified.
func parseNameStatus(out string) ([]nameStatus, error) {
	toks := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	if out == "" {
		return nil, nil
	}
	var res []nameStatus
	for i := 0; i < len(toks); i += 2 {
		if i+1 >= len(toks) || toks[i+1] == "" {
			return nil, fmt.Errorf("bad name-status entry %q", toks[i])
		}
		var st string
		switch toks[i] {
		case "A":
			st = FileAdded
		case "M", "T", "U":
			st = FileModified
		case "D":
			st = FileDeleted
		default:
			return nil, fmt.Errorf("unexpected status %q", toks[i])
		}
		res = append(res, nameStatus{st, toks[i+1]})
	}
	return res, nil
}

// fillPatch sets Patch, PatchTruncated, Hash and, for an untracked file, the
// counts of f.
func (g *Git) fillPatch(ctx context.Context, dir, base string, f *BaseFile, tracked bool, limit int) error {
	var patch string
	switch {
	case strings.HasSuffix(f.Path, "/"):
		// An untracked folder git did not enter (a nested repo): name only.
		f.Hash = fileHash(f.Status, f.Path, "dir")
		return nil
	case tracked:
		out, err := g.runLimited(ctx, dir, "diff base", true, limit+1,
			"--literal-pathspecs", "-c", "core.fsmonitor=false", "diff", "--no-color", "--no-ext-diff", "--no-textconv", "--no-renames", base, "--", f.Path)
		if err != nil {
			return err
		}
		patch = out
	default:
		full := filepath.Join(dir, filepath.FromSlash(f.Path))
		if fi, err := os.Lstat(full); err == nil && fi.Mode().IsRegular() && fi.Size() > untrackedPatchMax {
			f.PatchTruncated = true
			f.Hash = g.contentHash(ctx, dir, f, "")
			return nil
		}
		out, se, code, err := g.execWith(ctx, dir, "diff base", nil, false,
			"--no-optional-locks", "-c", "core.fsmonitor=false", "diff", "--no-index", "--no-color", "--no-ext-diff", "--no-textconv", "--", "/dev/null", f.Path)
		if err != nil {
			return err
		}
		if code != 0 && code != 1 {
			return &Error{Op: "diff base", Code: code, Stderr: se, Err: fmt.Errorf("exit status %d", code), UserMsg: classify("diff base", se)}
		}
		patch = out
		f.Added, f.Deleted, f.Binary = countPatch(patch)
	}
	if len(patch) > limit {
		patch, f.PatchTruncated = patch[:limit], true
	}
	if f.Binary {
		f.Hash = g.contentHash(ctx, dir, f, "")
		return nil
	}
	f.Patch = patch
	if f.PatchTruncated {
		f.Hash = g.contentHash(ctx, dir, f, patch)
		return nil
	}
	sum := sha256.Sum256([]byte(patch))
	f.Hash = hex.EncodeToString(sum[:])
	return nil
}

// contentHash is the hash of a file whose patch cannot stand for it (binary,
// or cut at the limit): it uses the git hash of the file's current content. A
// deleted file's content is the base's, fixed for a fixed base, so status and
// path are enough.
func (g *Git) contentHash(ctx context.Context, dir string, f *BaseFile, patch string) string {
	if f.Status == FileDeleted {
		return fileHash(f.Status, f.Path, "gone")
	}
	oid, err := g.run(ctx, dir, "hash file", true, "hash-object", "--no-filters", "--", f.Path)
	if err != nil {
		// Not a plain file (for example a link to nowhere): the cut patch and
		// the counts are the best fingerprint left.
		return fileHash(f.Status, f.Path, fmt.Sprintf("cut:%d:%d:%s", f.Added, f.Deleted, patch))
	}
	return fileHash(f.Status, f.Path, "blob:"+strings.TrimSpace(oid))
}

func fileHash(status, path, payload string) string {
	h := sha256.New()
	fmt.Fprintf(h, "ygg-basediff-v1\x00%s\x00%s\x00%s", status, path, payload)
	return hex.EncodeToString(h.Sum(nil))
}

// countPatch counts added and deleted lines of one file's patch and tells a
// binary patch.
func countPatch(patch string) (added, deleted int, binary bool) {
	inHunk := false
	for line := range strings.SplitSeq(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "Binary files ") || strings.HasPrefix(line, "GIT binary patch"):
			return 0, 0, true
		case strings.HasPrefix(line, "@@ "):
			inHunk = true
		case !inHunk:
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			deleted++
		}
	}
	return added, deleted, false
}

// HasChanges says whether the worktree at dir differs from the commit base in
// any way: committed since, uncommitted or untracked (ignored files do not
// count). A line-ending-only change counts. It builds no patch.
func (g *Git) HasChanges(ctx context.Context, dir, base string) (bool, error) {
	if err := checkCommitID("base", base); err != nil {
		return false, err
	}
	_, se, code, err := g.exec(ctx, dir, "diff base", nil,
		"--no-optional-locks", "-c", "core.fsmonitor=false", "diff", "--quiet", "--no-ext-diff", "--no-textconv", "--no-renames", base, "--")
	if err != nil {
		return false, err
	}
	switch code {
	case 1:
		return true, nil
	case 0:
	default:
		return false, &Error{Op: "diff base", Code: code, Stderr: se, Err: errors.New("exit status " + fmt.Sprint(code)), UserMsg: classify("diff base", se)}
	}
	// One byte of output is enough to know there is an untracked file.
	out, err := g.runLimited(ctx, dir, "diff base", true, 1,
		"-c", "core.fsmonitor=false", "ls-files", "--others", "--exclude-standard", "-z", "--")
	if err != nil {
		return false, err
	}
	return out != "", nil
}
