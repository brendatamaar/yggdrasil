package platform

// The OS-independent sequence of IgnoreWriter. The OS files supply only
// ignoreOps: open the top-level .gitignore of root without following links,
// and create it exclusively.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
)

// ignoreHandle is an open top-level .gitignore.
type ignoreHandle interface {
	File() *os.File
	// SameAsPath reports whether root/.gitignore still names this very file.
	SameAsPath() (bool, error)
	Close()
}

type ignoreOps interface {
	// OpenIgnore opens root/.gitignore without following links. A missing
	// file is exists=false. A link or reparse point is ErrUnsafePath, a plain
	// non-file likewise. With write, the handle is opened append-only (no
	// overwrite, no truncate) and held so that other writers are kept out as
	// far as the OS allows; a file already held open is ErrLocked.
	OpenIgnore(root string, write bool) (h ignoreHandle, exists bool, err error)
	// CreateIgnore creates root/.gitignore exclusively with exactly content.
	// An existing entry is ErrExists.
	CreateIgnore(root string, content []byte) error
}

type ignoreWriter struct{ ops ignoreOps }

// readHandle reads the whole file, refusing one over the size limit.
func readHandle(h ignoreHandle) ([]byte, os.FileInfo, error) {
	st, err := h.File().Stat()
	if err != nil {
		return nil, nil, fmt.Errorf("read the .gitignore: %w", err)
	}
	if !st.Mode().IsRegular() {
		return nil, nil, fmt.Errorf(".gitignore is not a plain file: %w", ErrUnsafePath)
	}
	if st.Size() > maxIgnoreSize {
		return nil, nil, fmt.Errorf(".gitignore is larger than 1 MiB and is not edited by Yggdrasil: %w", ErrTooLarge)
	}
	data, err := io.ReadAll(io.NewSectionReader(h.File(), 0, maxIgnoreSize+1))
	if err != nil {
		return nil, nil, fmt.Errorf("read the .gitignore: %w", err)
	}
	if len(data) > maxIgnoreSize {
		return nil, nil, fmt.Errorf(".gitignore is larger than 1 MiB and is not edited by Yggdrasil: %w", ErrTooLarge)
	}
	return data, st, nil
}

func snapshotOf(data []byte) IgnoreSnapshot {
	return IgnoreSnapshot{Hash: HashHex(data), Size: int64(len(data))}
}

func (w ignoreWriter) ReadIgnore(ctx context.Context, root string) (IgnoreSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return IgnoreSnapshot{}, fmt.Errorf("the operation was cancelled: %w", err)
	}
	h, exists, err := w.ops.OpenIgnore(root, false)
	if err != nil {
		return IgnoreSnapshot{}, err
	}
	if !exists {
		return IgnoreSnapshot{Absent: true}, nil
	}
	defer h.Close()
	data, _, err := readHandle(h)
	if err != nil {
		return IgnoreSnapshot{}, err
	}
	return snapshotOf(data), nil
}

func (w ignoreWriter) AppendIgnore(ctx context.Context, root string, entries []string, expect IgnoreExpectation) (IgnoreOutcome, IgnoreSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return "", IgnoreSnapshot{}, fmt.Errorf("the operation was cancelled: %w", err)
	}
	if expect.Absent == (expect.Hash != "") {
		return "", IgnoreSnapshot{}, fmt.Errorf("exactly one of \"absent\" or a hash must be expected: %w", ErrInvalidArgument)
	}
	if len(entries) == 0 {
		return "", IgnoreSnapshot{}, fmt.Errorf("no ignore entries were given: %w", ErrInvalidArgument)
	}
	h, exists, err := w.ops.OpenIgnore(root, true)
	if err != nil {
		return "", IgnoreSnapshot{}, err
	}
	if !exists {
		return w.create(root, entries, expect)
	}
	defer h.Close()
	if expect.Absent {
		return "", IgnoreSnapshot{}, fmt.Errorf(".gitignore now exists but was absent when you reviewed the change: %w", ErrChanged)
	}
	data, before, err := readHandle(h)
	if err != nil {
		return "", IgnoreSnapshot{}, err
	}
	if HashHex(data) != expect.Hash {
		return "", IgnoreSnapshot{}, fmt.Errorf(".gitignore changed since you reviewed the change: %w", ErrChanged)
	}
	add, err := PlanIgnoreAppend(data, entries)
	if err != nil {
		return "", IgnoreSnapshot{}, err
	}
	if add == nil {
		return IgnoreAlreadyPresent, snapshotOf(data), nil
	}
	// Narrow the window between the check and the write: the file must be the
	// same size and age and still be the file at the path.
	if st, err := h.File().Stat(); err != nil || st.Size() != before.Size() || !st.ModTime().Equal(before.ModTime()) {
		return "", IgnoreSnapshot{}, fmt.Errorf(".gitignore changed while it was being edited: %w", ErrChanged)
	}
	if same, err := h.SameAsPath(); err != nil || !same {
		return "", IgnoreSnapshot{}, fmt.Errorf(".gitignore was replaced while it was being edited: %w", ErrChanged)
	}
	n, werr := h.File().Write(add)
	if werr != nil || n != len(add) {
		return "", IgnoreSnapshot{}, fmt.Errorf("write the .gitignore (nothing was removed; check the file before retrying): %w", errors.Join(werr, io.ErrShortWrite))
	}
	_ = h.File().Sync()
	// Verify what is on disk: the original bytes untouched, then our bytes.
	got, _, err := readHandle(h)
	if err != nil {
		return "", IgnoreSnapshot{}, err
	}
	if !bytes.Equal(got, append(append([]byte(nil), data...), add...)) {
		return "", IgnoreSnapshot{}, fmt.Errorf(".gitignore was changed by another program during the edit: %w", ErrChanged)
	}
	return IgnoreAppended, snapshotOf(got), nil
}

func (w ignoreWriter) create(root string, entries []string, expect IgnoreExpectation) (IgnoreOutcome, IgnoreSnapshot, error) {
	if !expect.Absent {
		return "", IgnoreSnapshot{}, fmt.Errorf(".gitignore is gone since you reviewed the change: %w", ErrChanged)
	}
	content, err := PlanIgnoreAppend(nil, entries)
	if err != nil {
		return "", IgnoreSnapshot{}, err
	}
	if err := w.ops.CreateIgnore(root, content); err != nil {
		if errors.Is(err, ErrExists) {
			return "", IgnoreSnapshot{}, fmt.Errorf(".gitignore appeared while it was being created: %w", ErrChanged)
		}
		return "", IgnoreSnapshot{}, err
	}
	return IgnoreCreated, snapshotOf(content), nil
}
