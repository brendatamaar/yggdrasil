package platform

// Capability declarations for the Phase 6 safe filesystem operations
// (docs/design/phase-6-newrepos.md and phase-6-noise.md). Interfaces and value
// types only: the OS-specific implementations (handle-relative, no-follow,
// exclusive create, identity checks, reparse-point and junction rejection)
// belong in this package behind build tags and come with the new-repo task (N)
// and the proposal-extension task (P). No caller outside this package may
// approximate them with a path check made at preview time: a platform that
// cannot give the guarantee returns ErrUnsupportedPlatform and the feature
// reports it as a blocker.

import (
	"context"
	"errors"
)

// ErrUnsupportedPlatform means the safe operation cannot be guaranteed here.
var ErrUnsupportedPlatform = errors.New("this operation cannot be done safely on this platform")

// ErrChanged means what the operation was bound to (a folder identity, a file
// hash, a parent) is no longer the same.
var ErrChanged = errors.New("the target changed since it was checked")

// ErrExists means an exclusive create found something already there. It is
// never overwritten.
var ErrExists = errors.New("something already exists there")

// DirIdentity is an opaque, platform-owned identity of a directory (volume and
// file ID, or the Unix device and inode). It proves a resumed stage sees the
// same folder and not a swapped or reparsed one. The empty value is invalid.
type DirIdentity string

// SafeInitFS creates a new repo's folder and files without following links and
// without overwriting. Every operation re-resolves the canonical project parent
// and rejects junction, symlink and reparse-point ancestors, \\wsl$ paths,
// device and short-name aliases and a parent whose identity changed.
type SafeInitFS interface {
	// CreateChildDir creates parent/leaf exclusively. An existing entry, even
	// an empty folder, is ErrExists. It returns the new folder's identity.
	CreateChildDir(ctx context.Context, parent, leaf string) (DirIdentity, error)
	// VerifyChildDir checks that parent/leaf is still the folder with identity
	// id (ErrChanged otherwise). Resume is allowed only after it succeeds.
	VerifyChildDir(ctx context.Context, parent, leaf string, id DirIdentity) error
	// CreateFile creates dir/name exclusively with exactly content (ErrExists if
	// present) after VerifyChildDir-style identity checks, and returns the
	// sha256 hex of what was written. It runs no repo hook.
	CreateFile(ctx context.Context, parent, leaf string, id DirIdentity, name string, content []byte) (contentHash string, err error)
	// FileHash returns the sha256 hex of dir/name if it exists, for
	// reconciliation; exists is false when absent.
	FileHash(ctx context.Context, parent, leaf string, id DirIdentity, name string) (hash string, exists bool, err error)
}

// IgnoreSnapshot is the state of a repo's top-level .gitignore, read without
// following links. Absent is explicit and distinct from an empty file's hash.
type IgnoreSnapshot struct {
	Absent bool
	// Hash is the sha256 hex of the raw bytes ("" when Absent).
	Hash string
	// Size is the byte length; files over 1 MiB are refused.
	Size int64
}

// IgnoreExpectation is what the executor requires of the file before it
// appends: Absent, or a Hash.
type IgnoreExpectation struct {
	Absent bool
	Hash   string
}

// IgnoreOutcome says what AppendIgnore did.
type IgnoreOutcome string

const (
	// IgnoreAppended: the missing entries were added after the existing text.
	IgnoreAppended IgnoreOutcome = "appended"
	// IgnoreCreated: the file was absent and was created exclusively.
	IgnoreCreated IgnoreOutcome = "created"
	// IgnoreAlreadyPresent: every entry was already there; nothing was written.
	IgnoreAlreadyPresent IgnoreOutcome = "already_present"
)

// IgnoreWriter appends exact entries to the repo's own top-level .gitignore and
// nothing else. It opens the file without following links, checks the same-file
// identity and the expected hash through the write, preserves the raw prefix,
// the line-ending style and the final newline convention, appends only the
// entries not already present, and never renames over or truncates the file. A
// changed file is ErrChanged; a link or reparse point is refused; a locked file
// is a retryable error. root is the main checkout the caller holds the shared
// main lock for.
type IgnoreWriter interface {
	ReadIgnore(ctx context.Context, root string) (IgnoreSnapshot, error)
	AppendIgnore(ctx context.Context, root string, entries []string, expect IgnoreExpectation) (IgnoreOutcome, IgnoreSnapshot, error)
}
