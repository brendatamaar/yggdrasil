package terminal

import (
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Token is a registry entry. It is a closed set: Issued and Consumed.
type Token interface{ isToken() }

// Issued can be redeemed once before ExpiresAt.
type Issued struct {
	Folder    Folder
	ExpiresAt time.Time
}

// Consumed was redeemed (or burned by a refused redeem); kept until expiry so
// a replay is told apart from an unknown token.
type Consumed struct {
	At        time.Time
	ExpiresAt time.Time
}

func (Issued) isToken()   {}
func (Consumed) isToken() {}

// Session is a registry entry. It is a closed set: Opening, Open and Closing.
// A closed session is removed after its audit.
type Session interface{ isSession() }

// Opening waits for the PTY to start.
type Opening struct {
	Folder Folder
	Size   Size
	Since  time.Time
}

// Open has a running shell.
type Open struct {
	Folder Folder
	Pid    int
	Size   Size
	Since  time.Time
}

// Closing has asked the PTY to close.
type Closing struct {
	Folder Folder
	Cause  CloseCause
	Since  time.Time
}

func (Opening) isSession() {}
func (Open) isSession()    {}
func (Closing) isSession() {}

// Registry is all tokens and sessions, because the limits are global.
type Registry struct {
	Tokens   map[TokenHash]Token
	Sessions map[core.TerminalSessionID]Session
	Phase    Phase
}

// NewRegistry returns an empty, serving registry.
func NewRegistry() Registry {
	return Registry{Tokens: map[TokenHash]Token{}, Sessions: map[core.TerminalSessionID]Session{}, Phase: Serving}
}

// FolderOf returns the folder of an Issued token: the one source of truth for
// the shell's tracked-folder check at redeem time.
func (r Registry) FolderOf(h TokenHash) (Folder, bool) {
	t, ok := r.Tokens[h].(Issued)
	return t.Folder, ok
}
