package terminal

import (
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Command is an effect Step asks the shell for. It is a closed set.
type Command interface{ isCommand() }

// OpenPTY starts the shell in the folder. Not idempotent: protected by the
// single-use token (invariant T1).
type OpenPTY struct {
	Session core.TerminalSessionID
	Folder  Folder
	Size    Size
}

// ResizePTY resizes the terminal. Idempotent.
type ResizePTY struct {
	Session core.TerminalSessionID
	Size    Size
}

// ClosePTY closes the terminal and kills its process tree. Idempotent.
type ClosePTY struct{ Session core.TerminalSessionID }

// ScheduleWake asks for a Wake input at At. Idempotent.
type ScheduleWake struct {
	Kind WakeKind
	At   time.Time
}

// ReplyKind says which request a Reply answers.
type ReplyKind string

// The closed set of ReplyKind values.
const (
	ReplyIssue  ReplyKind = "issue"
	ReplyRedeem ReplyKind = "redeem"
)

// Reply answers an issue or redeem. Granted is true with Refusal empty, or
// false with the Refusal. Session is set on a granted redeem.
type Reply struct {
	ID      ReplyID
	Kind    ReplyKind
	Granted bool
	Refusal Refusal
	Session core.TerminalSessionID
}

// WriteAudit is committed with the transition. It carries the session ID,
// folder IDs, cause and exit code only: never keystrokes, command lines,
// paths or the token.
type WriteAudit struct {
	Action  AuditAction
	Session core.TerminalSessionID
	Folder  Folder
	Cause   CloseCause
	Code    int
}

func (OpenPTY) isCommand()      {}
func (ResizePTY) isCommand()    {}
func (ClosePTY) isCommand()     {}
func (ScheduleWake) isCommand() {}
func (Reply) isCommand()        {}
func (WriteAudit) isCommand()   {}
