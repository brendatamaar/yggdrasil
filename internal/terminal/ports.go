package terminal

import (
	"context"
	"errors"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	ct "github.com/brendatamaar/yggdrasil/internal/core/terminal"
	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// ErrNotTracked is what Folders returns for a repo or worktree that is not
// tracked (never added, removed since, or a worktree of another repo). The
// terminal only ever opens in tracked folders (DV-05, invariant 6).
var ErrNotTracked = errors.New("terminal: folder is not tracked")

// Folders resolves repo and worktree IDs to tracked folders. The directory
// never leaves this package: it goes to the PTY and nowhere else (not the
// audit log, not a log line, not a reply).
type Folders interface {
	// Resolve turns a request target into the folder it names. A worktree
	// resolves to its own repo. It returns ErrNotTracked when the repo or
	// worktree is not tracked.
	Resolve(ctx context.Context, t ct.Target) (ct.Folder, error)
	// Dir returns the directory of a tracked folder, or ErrNotTracked. It is
	// called again at redeem time and just before the shell starts.
	Dir(ctx context.Context, f ct.Folder) (string, error)
}

// Socket is the browser side of a session. The WebSocket adapter lives in
// internal/api; this package only sees frames. Output goes out as binary
// frames, the exit notice as a close with a reason.
type Socket interface {
	// Read returns the next client frame. It returns an error when the
	// client is gone or ctx is done.
	Read(ctx context.Context) (ct.FrameKind, []byte, error)
	// Write sends one binary output frame.
	Write(ctx context.Context, data []byte) error
	// Close ends the socket with a short plain-language reason. It is safe
	// to call more than once; later calls do nothing.
	Close(reason string) error
}

// TokenSource makes raw tokens: 32 random bytes as unpadded base64url.
type TokenSource interface {
	New() (string, error)
}

// AuditEntry is what the terminal records. It has the session ID, folder IDs,
// cause and exit code, and the time. It never carries keystrokes, command
// lines, paths or the token.
type AuditEntry struct {
	Action  ct.AuditAction
	Session core.TerminalSessionID
	Folder  ct.Folder
	Cause   ct.CloseCause
	Code    int
	At      time.Time
}

// AuditSink stores audit entries. The real one writes to the audit log
// through the store; tests use an in-memory one.
type AuditSink interface {
	Record(ctx context.Context, e AuditEntry) error
}

// PTYStarter starts a process on a pseudo-terminal. platform.StartPTY is the
// real one.
type PTYStarter func(ctx context.Context, spec platform.PTYSpec) (platform.PTY, error)

// RefusedError says why a token request was refused. Message is for the user.
type RefusedError struct{ Reason ct.Refusal }

func (e *RefusedError) Error() string { return "terminal: refused: " + string(e.Reason) }

// Message is a plain-language explanation with the next step.
func (e *RefusedError) Message() string {
	switch e.Reason {
	case ct.RefusedTooManyPending:
		return "Too many terminals are being opened at once. Wait a few seconds and try again."
	case ct.RefusedShuttingDown:
		return "Yggdrasil is shutting down, so no new terminal can open."
	case ct.RefusedExpired:
		return "The terminal link expired. Open the terminal again."
	case ct.RefusedNotTracked:
		return "That folder is not tracked any more, so a terminal cannot open there."
	case ct.RefusedTooManySessions:
		return "Four terminals are already open. Close one and try again."
	case ct.RefusedReused, ct.RefusedUnknown:
		return "The terminal link is not valid. Open the terminal again."
	default:
		return "The terminal could not be opened. Try again."
	}
}
