package worktreeenv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	env "github.com/brendatamaar/yggdrasil/internal/core/worktreeenv"
	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// OSFileSystem is the FileSystem on the real disk. It looks at every path
// component with Lstat and treats symlinks and Windows reparse points
// (junctions, mount points) as links.
type OSFileSystem struct{}

type entryKind int

const (
	kindMissing entryKind = iota
	kindFile
	kindDir
	kindLink
	kindOther
	kindError
)

// lstat classifies one path without following it.
func lstat(path string) (entryKind, fs.FileInfo) {
	fi, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return kindMissing, nil
		}
		return kindError, nil
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return kindLink, fi
	}
	if reparse, err := platform.IsReparsePoint(path); err != nil || reparse {
		return kindLink, fi // an unreadable attribute is treated as a link: refuse
	}
	switch {
	case fi.IsDir():
		return kindDir, fi
	case fi.Mode().IsRegular():
		return kindFile, fi
	}
	return kindOther, fi
}

// valid reports whether rel is a path the profile may name; it guards the
// interpreter even when a caller skipped the parser.
func valid(rel env.RelPath) bool { return rel.Validate() == nil }

func join(root string, rel env.RelPath) string {
	return filepath.Join(root, filepath.FromSlash(string(rel)))
}

// walk lstats every component of rel below root and returns the kind of the
// last one, or the first problem on the way: a link, a missing part, or a
// parent that is not a folder.
func walk(root string, rel env.RelPath) (entryKind, fs.FileInfo) {
	parts := strings.Split(string(rel), "/")
	cur := root
	for i, p := range parts {
		cur = filepath.Join(cur, p)
		k, fi := lstat(cur)
		if i == len(parts)-1 || k == kindDir {
			if i == len(parts)-1 {
				return k, fi
			}
			continue
		}
		if k == kindFile {
			return kindOther, fi // a file where a folder should be
		}
		return k, fi
	}
	return kindMissing, nil
}

// Observe implements FileSystem.
func (OSFileSystem) Observe(_ context.Context, srcRoot, dstRoot string, rel env.RelPath) (env.Source, env.Dest, error) {
	if !valid(rel) || !filepath.IsAbs(srcRoot) || !filepath.IsAbs(dstRoot) {
		return env.SourceUnreadable{}, env.DestExists, nil
	}
	var src env.Source
	switch k, fi := walk(srcRoot, rel); k {
	case kindFile:
		src = env.SourceFile{Size: fi.Size()}
	case kindMissing:
		src = env.SourceMissing{}
	case kindDir, kindOther:
		src = env.SourceDirectory{}
	case kindLink:
		src = env.SourceLink{}
	default:
		src = env.SourceUnreadable{}
	}
	dest := env.DestExists
	// Absent only when nothing at all is there and no parent is a link.
	if k, _ := walk(dstRoot, rel); k == kindMissing {
		dest = env.DestAbsent
	}
	return src, dest, nil
}

// CopyExclusive implements FileSystem.
func (OSFileSystem) CopyExclusive(ctx context.Context, srcRoot, dstRoot string, rel env.RelPath, maxBytes int64) error {
	if !valid(rel) || !filepath.IsAbs(srcRoot) || !filepath.IsAbs(dstRoot) {
		return ErrNotRegular
	}
	srcPath := join(srcRoot, rel)
	k, lfi := walk(srcRoot, rel)
	if k != kindFile {
		return ErrNotRegular
	}
	if lfi.Size() > maxBytes {
		return ErrTooLarge
	}
	// The destination's parents must be real folders (or missing, and then
	// made here); a link would send the write outside the worktree.
	dstPath := join(dstRoot, rel)
	if err := makeParents(dstRoot, rel); err != nil {
		return err
	}
	in, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("open profile file: %w", err)
	}
	defer in.Close()
	ofi, err := in.Stat()
	if err != nil {
		return fmt.Errorf("inspect profile file: %w", err)
	}
	if !os.SameFile(ofi, lfi) || !ofi.Mode().IsRegular() {
		return ErrNotRegular // swapped between the check and the open
	}
	// O_EXCL: an existing file, a link or a file made by a racing writer fails
	// with "exists" and is never overwritten.
	out, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ErrDestExists
		}
		return fmt.Errorf("create worktree file: %w", err)
	}
	n, copyErr := io.Copy(out, io.LimitReader(&ctxReader{ctx: ctx, r: in}, maxBytes+1))
	closeErr := out.Close()
	switch {
	case copyErr != nil:
		return fmt.Errorf("copy profile file: %w", copyErr) // the partial file stays (invariant 1)
	case n > maxBytes:
		return ErrTooLarge
	case closeErr != nil:
		return fmt.Errorf("finish worktree file: %w", closeErr)
	}
	return nil
}

// makeParents creates the missing folders above rel in root, one level at a
// time, and refuses when an existing level is a link or a file.
func makeParents(root string, rel env.RelPath) error {
	parts := strings.Split(string(rel), "/")
	cur := root
	for _, p := range parts[:len(parts)-1] {
		cur = filepath.Join(cur, p)
		switch k, _ := lstat(cur); k {
		case kindDir:
		case kindMissing:
			if err := os.Mkdir(cur, 0o750); err != nil && !errors.Is(err, fs.ErrExist) {
				return fmt.Errorf("create worktree folder: %w", err)
			}
			if k2, _ := lstat(cur); k2 != kindDir { // lost a race to a link
				return ErrNotRegular
			}
		default:
			return ErrNotRegular
		}
	}
	return nil
}

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
