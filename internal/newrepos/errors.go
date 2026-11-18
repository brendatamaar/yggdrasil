package newrepos

import (
	"errors"
	"fmt"

	corenewrepos "github.com/brendatamaar/yggdrasil/internal/core/newrepos"
)

// refusal is an error that says nothing was written: the request was refused
// before any state or effect changed. It carries plain-language text and the
// NothingWritten marker the proposal executors read to tell a definite refusal
// from an unknown outcome (internal/proposals.definiteRefusal). Callers match
// the exported variables with errors.Is.
type refusal struct{ msg string }

func (e *refusal) Error() string        { return e.msg }
func (e *refusal) UserMessage() string  { return e.msg }
func (e *refusal) NothingWritten() bool { return true }

// Errors the API layer can translate. Their text is plain language.
var (
	// ErrHashMismatch: the confirmed hash is not the one previewed. Nothing was written.
	ErrHashMismatch error = &refusal{"what you confirmed is not what was previewed; preview again and confirm the new version"}
	// ErrNotApplicable: the operation is not in a state that takes this step
	// (already running, finished, rejected, stopped for good or not yet
	// proposed). Nothing was written.
	ErrNotApplicable error = &refusal{"this new repo is not waiting for that step"}
	// ErrClosed: the service is shutting down. It is not marked as "nothing was
	// written": a shutdown can interrupt a call after its state was committed.
	ErrClosed = errors.New("yggdrasil is shutting down; try again after it restarts")
)

// RefusedError says why a request was refused before anything was written.
type RefusedError struct {
	Reason corenewrepos.StopReason
}

func (e *RefusedError) Error() string        { return Explain(e.Reason) }
func (e *RefusedError) UserMessage() string  { return Explain(e.Reason) }
func (e *RefusedError) NothingWritten() bool { return true }

// Explain is the plain-language sentence for a stop reason.
func Explain(r corenewrepos.StopReason) string {
	switch r {
	case corenewrepos.StopRejected:
		return "You rejected this new repo, so nothing was created."
	case corenewrepos.StopUntracked:
		return "The project is not tracked any more, so the new repo was stopped. Nothing was deleted."
	case corenewrepos.StopPathExists:
		return "A folder or file with that name already exists, so Yggdrasil will not touch it. Choose another name."
	case corenewrepos.StopMissingAuthor:
		return `Git does not know who you are. Run: git config --global user.name "Your Name" and git config --global user.email "you@example.com", then try again.`
	case corenewrepos.StopStageFailed:
		return "A step failed and did not take effect. Nothing was deleted; you can retry after reading the details."
	case corenewrepos.StopHostRefused:
		return "The git host refused the request. Check the account, its permissions and the token, then try again."
	case corenewrepos.StopNameCollision:
		return "The git host says that repository name is taken or not allowed. Choose another name."
	case corenewrepos.StopOriginExists:
		return "This repo already has an origin, so Yggdrasil will not replace it."
	case corenewrepos.StopConflict:
		return "What Yggdrasil found does not match what it created. Nothing was changed or deleted; please look at the folder or repository yourself."
	case corenewrepos.StopUnsupported:
		return "This repository cannot be published from here. The local repo is fine; publish it by hand."
	case corenewrepos.StopPreconditions:
		return "Something changed since you confirmed (the project folder, the first commit or the remote). Preview and confirm again."
	case corenewrepos.StopNotImplemented:
		return "This step is not available yet."
	}
	return fmt.Sprintf("The new repo stopped (%s).", string(r))
}
