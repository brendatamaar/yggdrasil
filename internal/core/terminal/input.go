package terminal

import (
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// ReplyID correlates a Reply command with the waiting HTTP or socket handler.
type ReplyID string

// Input is one thing that happened. It is a closed set. Time and IDs arrive as
// fields.
type Input interface{ isInput() }

// IssueRequested asks for a token for a folder the shell already resolved.
type IssueRequested struct {
	Hash   TokenHash
	Folder Folder
	Now    time.Time
	Reply  ReplyID
}

// RedeemRequested presents a token and asks for a session.
type RedeemRequested struct {
	Hash    TokenHash
	Folder  Folder // the folder the shell re-checked; must equal the token's
	Session core.TerminalSessionID
	Size    Size
	Check   FolderCheck
	Now     time.Time
	Reply   ReplyID
}

// Opened reports that the shell started.
type Opened struct {
	Session core.TerminalSessionID
	Pid     int
	Now     time.Time
}

// OpenFailed reports that the PTY could not start.
type OpenFailed struct {
	Session core.TerminalSessionID
	Now     time.Time
}

// Resized carries a new size from the client.
type Resized struct {
	Session core.TerminalSessionID
	Size    Size
}

// ClientClosed reports that the socket closed.
type ClientClosed struct {
	Session core.TerminalSessionID
	Now     time.Time
}

// CloseRequested is the UI close button.
type CloseRequested struct {
	Session core.TerminalSessionID
	Now     time.Time
}

// ShellExited reports that the shell ended.
type ShellExited struct {
	Session core.TerminalSessionID
	Code    int
	Now     time.Time
}

// Shutdown drains the registry.
type Shutdown struct{ Now time.Time }

// Wake expires tokens.
type Wake struct {
	Kind WakeKind
	Now  time.Time
}

func (IssueRequested) isInput()  {}
func (RedeemRequested) isInput() {}
func (Opened) isInput()          {}
func (OpenFailed) isInput()      {}
func (Resized) isInput()         {}
func (ClientClosed) isInput()    {}
func (CloseRequested) isInput()  {}
func (ShellExited) isInput()     {}
func (Shutdown) isInput()        {}
func (Wake) isInput()            {}
