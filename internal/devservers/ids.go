package devservers

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	dsc "github.com/brendatamaar/yggdrasil/internal/core/devservers"
)

// Target is a tracked repo (its main checkout) or one of its worktrees. It is
// always named by IDs, never by a path (invariant 6, DV-05).
type Target struct {
	Repo     core.RepoID
	Worktree core.WorktreeID // empty for the main checkout
}

// Key names the server for a command of the target.
func (t Target) Key(command string) dsc.Key {
	return dsc.Key{Repo: t.Repo, Worktree: t.Worktree, CommandKey: command}
}

// IDOf is the stable ID of a server. It is safe as a file name and as a
// platform process ID (letters, digits and '_' only, 28 characters).
func IDOf(k dsc.Key) string {
	sum := sha256.Sum256([]byte(string(k.Repo) + "\x00" + string(k.Worktree) + "\x00" + k.CommandKey))
	return "dsv_" + hex.EncodeToString(sum[:12])
}

const maxCommandKey = 64

// ParseStart is the boundary parser of a start, stop or restart request: it
// turns the target IDs and the command name into a Key. Targets are IDs; an
// ID or command that looks like a path is refused.
func ParseStart(repo, worktree, command string) (dsc.Key, error) {
	bad := func(msg string) (dsc.Key, error) {
		return dsc.Key{}, apperr.New(apperr.Invalid, msg, nil)
	}
	if err := core.RepoID(repo).Validate(); err != nil {
		return bad("Choose a repo from the list.")
	}
	if worktree != "" {
		if err := core.WorktreeID(worktree).Validate(); err != nil {
			return bad("Choose a worktree from the list.")
		}
	}
	if command == "" || len(command) > maxCommandKey {
		return bad("Choose one of the commands offered for this repo.")
	}
	for _, r := range command {
		if !(r == '.' || r == '_' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return bad("Choose one of the commands offered for this repo.")
		}
	}
	if strings.HasPrefix(command, ".") {
		return bad("Choose one of the commands offered for this repo.")
	}
	return dsc.Key{Repo: core.RepoID(repo), Worktree: core.WorktreeID(worktree), CommandKey: command}, nil
}

// RejectedError is a refused request: the core said no (already running, bad
// template). It is an apperr.Error with the class Conflict or Invalid.
func rejectError(r dsc.Reject) error {
	switch r.Reason {
	case dsc.RejectAlreadyRunning:
		return apperr.New(apperr.Conflict, "This server is already running or stopping. Stop it first, or use restart.", nil)
	case dsc.RejectBadTemplate:
		return apperr.New(apperr.Invalid, "This server's command cannot be started: "+plainTemplateMessage(r.Message), nil)
	}
	return apperr.New(apperr.Invalid, "The request was refused.", fmt.Errorf("reject %q", string(r.Reason)))
}

func plainTemplateMessage(m string) string {
	return strings.TrimPrefix(m, dsc.ErrBadTemplate.Error()+": ")
}
