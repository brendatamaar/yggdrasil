package newrepos

import (
	"errors"
	"fmt"

	corenewrepos "github.com/brendatamaar/yggdrasil/internal/core/newrepos"
)

// Errors the API layer can translate. Their text is plain language.
var (
	// ErrHashMismatch: the confirmed hash is not the one previewed.
	ErrHashMismatch = errors.New("what you confirmed is not what was previewed; preview again and confirm the new version")
	// ErrNotApplicable: the operation is not in a state that takes this step
	// (already running, finished, rejected, or not yet proposed).
	ErrNotApplicable = errors.New("this new repo is not waiting for that step")
	// ErrClosed: the service is shutting down.
	ErrClosed = errors.New("yggdrasil is shutting down; try again after it restarts")
)

// RefusedError says why a request was refused before anything was written.
type RefusedError struct {
	Reason corenewrepos.StopReason
}

func (e *RefusedError) Error() string { return Explain(e.Reason) }

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
