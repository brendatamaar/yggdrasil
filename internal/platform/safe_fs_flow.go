package platform

// The OS-independent sequence of SafeInitFS. The OS files supply only the
// primitives (fsDir): walk to a folder without following links, create a child
// exclusively, open a child without following links, create or open a file
// relative to a folder handle. Every operation re-resolves the parent path
// from the root, so a swap between calls is seen.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
)

// fsDir is an open folder handle (a directory file descriptor on Unix, a
// directory handle on Windows) with the identity it had when opened.
type fsDir interface {
	ID() DirIdentity
	// Mkdir creates the child exclusively and returns it open. An existing
	// entry is ErrExists.
	Mkdir(leaf string) (fsDir, error)
	// OpenDir opens the child without following links. A missing child is
	// fs.ErrNotExist; a link, reparse point or non-folder is ErrUnsafePath.
	OpenDir(leaf string) (fsDir, error)
	// CreateFile creates a file exclusively, for writing only. ErrExists if
	// something is there.
	CreateFile(name string) (*os.File, error)
	// OpenFile opens an existing regular file for reading without following
	// links. fs.ErrNotExist if absent; ErrUnsafePath if not a plain file.
	OpenFile(name string) (*os.File, error)
	Close()
}

// dirWalker opens an absolute folder path component by component without
// following links, rejecting every link, reparse point and unsafe alias on the
// way (ErrUnsafePath), and returns the final folder.
type dirWalker interface {
	OpenParent(parent string) (fsDir, error)
}

type safeInitFS struct{ w dirWalker }

func (s safeInitFS) openParent(ctx context.Context, parent string) (fsDir, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("the operation was cancelled: %w", err)
	}
	return s.w.OpenParent(parent)
}

// recheck re-resolves parent and requires the same folder as before.
func (s safeInitFS) recheck(ctx context.Context, parent string, want DirIdentity) error {
	p, err := s.openParent(ctx, parent)
	if err != nil {
		return fmt.Errorf("the project folder %q changed while it was being used: %w", parent, errors.Join(ErrChanged, err))
	}
	defer p.Close()
	if p.ID() != want {
		return fmt.Errorf("the project folder %q was replaced while it was being used: %w", parent, ErrChanged)
	}
	return nil
}

// openChild opens parent/leaf and requires identity id. A missing, linked or
// swapped child is ErrChanged.
func (s safeInitFS) openChild(p fsDir, leaf string, id DirIdentity) (fsDir, error) {
	if id == "" {
		return nil, fmt.Errorf("no folder identity was given for %q: %w", leaf, ErrInvalidArgument)
	}
	c, err := p.OpenDir(leaf)
	switch {
	case errors.Is(err, fs.ErrNotExist) || errors.Is(err, ErrUnsafePath):
		return nil, fmt.Errorf("the folder %q is no longer the one Yggdrasil created: %w", leaf, ErrChanged)
	case err != nil:
		return nil, fmt.Errorf("open the folder %q: %w", leaf, err)
	}
	if c.ID() != id {
		c.Close()
		return nil, fmt.Errorf("the folder %q was replaced by a different one: %w", leaf, ErrChanged)
	}
	return c, nil
}

func (s safeInitFS) CreateChildDir(ctx context.Context, parent, leaf string) (DirIdentity, error) {
	if err := ValidateChildName(leaf); err != nil {
		return "", err
	}
	p, err := s.openParent(ctx, parent)
	if err != nil {
		return "", err
	}
	defer p.Close()
	c, err := p.Mkdir(leaf)
	if err != nil {
		if errors.Is(err, ErrExists) {
			return "", fmt.Errorf("%q already exists in %q; Yggdrasil never reuses an existing folder: %w", leaf, parent, ErrExists)
		}
		return "", fmt.Errorf("create the folder %q in %q: %w", leaf, parent, err)
	}
	defer c.Close()
	if err := s.recheck(ctx, parent, p.ID()); err != nil {
		return "", err
	}
	return c.ID(), nil
}

func (s safeInitFS) VerifyChildDir(ctx context.Context, parent, leaf string, id DirIdentity) error {
	if err := ValidateChildName(leaf); err != nil {
		return err
	}
	p, err := s.openParent(ctx, parent)
	if err != nil {
		return err
	}
	defer p.Close()
	c, err := s.openChild(p, leaf, id)
	if err != nil {
		return err
	}
	c.Close()
	return nil
}

func (s safeInitFS) CreateFile(ctx context.Context, parent, leaf string, id DirIdentity, name string, content []byte) (string, error) {
	if err := errors.Join(ValidateChildName(leaf), ValidateChildName(name)); err != nil {
		return "", err
	}
	p, err := s.openParent(ctx, parent)
	if err != nil {
		return "", err
	}
	defer p.Close()
	c, err := s.openChild(p, leaf, id)
	if err != nil {
		return "", err
	}
	defer c.Close()
	f, err := c.CreateFile(name)
	if err != nil {
		if errors.Is(err, ErrExists) {
			return "", fmt.Errorf("%q already exists in %q and is never overwritten: %w", name, leaf, ErrExists)
		}
		return "", fmt.Errorf("create %q in %q: %w", name, leaf, err)
	}
	_, werr := f.Write(content)
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		return "", fmt.Errorf("write %q in %q (the partial file is left for you to inspect; nothing was deleted): %w", name, leaf, err)
	}
	// The handle kept the write in the folder Yggdrasil owns; make sure that
	// is still the folder at the path, or report the swap.
	if err := s.recheck(ctx, parent, p.ID()); err != nil {
		return "", err
	}
	if err := s.VerifyChildDir(ctx, parent, leaf, id); err != nil {
		return "", err
	}
	return HashHex(content), nil
}

func (s safeInitFS) FileHash(ctx context.Context, parent, leaf string, id DirIdentity, name string) (string, bool, error) {
	if err := errors.Join(ValidateChildName(leaf), ValidateChildName(name)); err != nil {
		return "", false, err
	}
	p, err := s.openParent(ctx, parent)
	if err != nil {
		return "", false, err
	}
	defer p.Close()
	c, err := s.openChild(p, leaf, id)
	if err != nil {
		return "", false, err
	}
	defer c.Close()
	f, err := c.OpenFile(name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("read %q in %q: %w", name, leaf, err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxInitFileSize+1))
	if err != nil {
		return "", false, fmt.Errorf("read %q in %q: %w", name, leaf, err)
	}
	if len(data) > maxInitFileSize {
		return "", true, fmt.Errorf("%q in %q is larger than Yggdrasil will read: %w", name, leaf, ErrTooLarge)
	}
	return HashHex(data), true, nil
}
