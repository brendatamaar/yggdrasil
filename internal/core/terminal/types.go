package terminal

import (
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// TokenHash is the SHA-256 of a token, lower-case hex (64 characters).
type TokenHash string

// Folder is a tracked repo or worktree folder, by ID. Never a path.
type Folder struct {
	Repo     core.RepoID
	Worktree core.WorktreeID // empty for a main checkout
}

// Size is a terminal size in cells.
type Size struct{ Cols, Rows int }

// Limits and timing, in one place; tests pin the table.
const (
	TokenTTL    = 30 * time.Second
	MaxPending  = 8
	MaxSessions = 4
	MaxCols     = 500
	MaxRows     = 500
	// OpenDeadline is how long a granted redeem may wait for its socket and
	// shell before the session is given up as open_failed.
	OpenDeadline = 15 * time.Second
)

// FolderCheck is the shell's fact at redeem time: is the folder still tracked.
type FolderCheck string

// The closed set of FolderCheck values.
const (
	FolderTracked   FolderCheck = "tracked"
	FolderUntracked FolderCheck = "untracked"
)

// CloseCause says why a session ended.
type CloseCause string

// The closed set of CloseCause values.
const (
	CauseClientClosed CloseCause = "client_closed"
	CauseShellExited  CloseCause = "shell_exited"
	CauseUserClosed   CloseCause = "user_closed"
	CauseAppShutdown  CloseCause = "app_shutdown"
	CauseOpenFailed   CloseCause = "open_failed"
)

// Refusal says why an issue or redeem was refused.
type Refusal string

// The closed set of Refusal values.
const (
	RefusedTooManyPending  Refusal = "too_many_pending"
	RefusedShuttingDown    Refusal = "shutting_down"
	RefusedCollision       Refusal = "collision"
	RefusedExpired         Refusal = "expired"
	RefusedNotTracked      Refusal = "not_tracked"
	RefusedTooManySessions Refusal = "too_many_sessions"
	RefusedReused          Refusal = "reused"
	RefusedUnknown         Refusal = "unknown"
)

// AuditAction is the closed set of audit actions the terminal writes.
type AuditAction string

// The closed set of AuditAction values.
const (
	AuditOpen       AuditAction = "terminal.open"
	AuditClose      AuditAction = "terminal.close"
	AuditOpenFailed AuditAction = "terminal.open_failed"
)

// WakeKind says why a wake-up was scheduled. The set is closed.
type WakeKind string

// WakeExpire removes expired tokens.
const WakeExpire WakeKind = "expire"

// WakeOpenDeadline gives up on sessions stuck in Opening.
const WakeOpenDeadline WakeKind = "open_deadline"

// Validate reports whether the size is within 1..MaxCols by 1..MaxRows.
func (s Size) Validate() error {
	if s.Cols < 1 || s.Cols > MaxCols || s.Rows < 1 || s.Rows > MaxRows {
		return fmt.Errorf("terminal size %dx%d is outside 1..%d columns and 1..%d rows", s.Cols, s.Rows, MaxCols, MaxRows)
	}
	return nil
}

// Phase is whether the registry accepts new work.
type Phase string

// The closed set of Phase values.
const (
	Serving  Phase = "serving"
	Draining Phase = "draining"
)
