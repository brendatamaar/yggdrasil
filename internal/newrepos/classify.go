package newrepos

import (
	"context"
	"errors"
	"io/fs"

	corenewrepos "github.com/brendatamaar/yggdrasil/internal/core/newrepos"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/hosts"
	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// Every error of a stage is either definite (the stage did not take effect, with
// a StopReason) or unknown. Unknown is the default: a timeout, a cancelled
// context, a lost reply and any error this file does not recognise may hide an
// effect, so they are never reported as a failure and never retried blindly.

// definite wraps an error that is known not to have changed anything.
type definite struct {
	reason corenewrepos.StopReason
	err    error
}

func (d *definite) Error() string { return d.err.Error() }
func (d *definite) Unwrap() error { return d.err }

func definiteErr(reason corenewrepos.StopReason, err error) error {
	return &definite{reason: reason, err: err}
}

// verdict turns a stage error into the core's classified outcome.
func verdict(err error) (corenewrepos.FailOutcome, corenewrepos.StopReason) {
	var d *definite
	if errors.As(err, &d) {
		return corenewrepos.OutcomeDefiniteFailure, d.reason
	}
	return corenewrepos.OutcomeUnknown, ""
}

func isCtx(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// classifyFS classifies an error of a SafeInitFS mutation.
func classifyFS(err error) error {
	switch {
	case err == nil:
		return nil
	case isCtx(err):
		return err
	case errors.Is(err, platform.ErrExists):
		return definiteErr(corenewrepos.StopPathExists, err)
	case errors.Is(err, platform.ErrUnsupportedPlatform):
		return definiteErr(corenewrepos.StopUnsupported, err)
	case errors.Is(err, platform.ErrChanged), errors.Is(err, platform.ErrUnsafePath),
		errors.Is(err, platform.ErrInvalidArgument), errors.Is(err, fs.ErrNotExist):
		return definiteErr(corenewrepos.StopPreconditions, err)
	}
	return err
}

// classifyGit classifies an error of a local git stage.
func classifyGit(err error) error {
	switch {
	case err == nil:
		return nil
	case isCtx(err):
		return err
	case errors.Is(err, git.ErrNoAuthor):
		return definiteErr(corenewrepos.StopMissingAuthor, err)
	case errors.Is(err, git.ErrNotEmptyDir):
		return definiteErr(corenewrepos.StopPathExists, err)
	case errors.Is(err, git.ErrOriginExists):
		return definiteErr(corenewrepos.StopOriginExists, err)
	case errors.Is(err, git.ErrNotFirstCommit):
		return definiteErr(corenewrepos.StopConflict, err)
	case errors.Is(err, git.ErrInvalidArg):
		return definiteErr(corenewrepos.StopPreconditions, err)
	}
	return err
}

// classifyHost classifies the error of the one create-remote POST. Only
// hosts.ErrOutcomeUnknown (and a cancelled or timed-out context) is unknown;
// the host client reports every definite refusal as another error.
func classifyHost(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, hosts.ErrOutcomeUnknown), isCtx(err):
		return err
	case errors.Is(err, hosts.ErrWriteRejected):
		return definiteErr(corenewrepos.StopNameCollision, err)
	}
	// A refused token or permission, a missing token and every other answer
	// that is not "outcome unknown" mean the host did not create anything.
	return definiteErr(corenewrepos.StopHostRefused, err)
}

// classifyPush classifies the error of the one push.
func classifyPush(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, git.ErrPublishUnknown), isCtx(err):
		return err
	case errors.Is(err, git.ErrRemoteDiverged):
		return definiteErr(corenewrepos.StopConflict, err)
	case errors.Is(err, git.ErrStaleHead), errors.Is(err, git.ErrInvalidArg):
		return definiteErr(corenewrepos.StopPreconditions, err)
	case errors.Is(err, git.ErrUnsupportedFormat):
		return definiteErr(corenewrepos.StopUnsupported, err)
	}
	// The transport reports everything it cannot place as ErrPublishUnknown, so
	// what is left is a refusal the host answered (sign-in, permission, rules).
	return definiteErr(corenewrepos.StopHostRefused, err)
}
