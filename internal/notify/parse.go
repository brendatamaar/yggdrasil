package notify

import (
	"errors"
	"fmt"
	"strings"

	corenotify "github.com/brendatamaar/yggdrasil/internal/core/notify"
)

// ErrInvalid marks a transition rejected at the boundary: a bad revision, status,
// task or title. It is a domain outcome (nothing was stored or published), not a
// failure of the producer.
var ErrInvalid = errors.New("invalid notification input")

// ErrClosed is returned once the producer has been closed.
var ErrClosed = errors.New("the notification producer is closed")

// ErrUnknownTask is returned when the transition names a task that is not stored.
var ErrUnknownTask = errors.New("the task of this notification is not known")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// parseTask validates a task transition from the task service. Time and the
// notice ID are the shell's, so they are not checked here (the producer sets
// them after parsing).
func parseTask(t corenotify.TaskTransition) error {
	if err := t.Task.Validate(); err != nil {
		return invalid("%v", err)
	}
	if err := t.Status.Validate(); err != nil {
		return invalid("%v", err)
	}
	if t.Revision <= 0 {
		return invalid("a task transition needs a positive revision")
	}
	return parseTitle(t.Title)
}

// parseVerification validates a verification transition.
func parseVerification(v corenotify.VerificationTransition) error {
	if err := v.Task.Validate(); err != nil {
		return invalid("%v", err)
	}
	if err := (corenotify.Source{Kind: corenotify.SourceVerification, RunID: v.RunID}).Validate(); err != nil {
		return invalid("%v", err)
	}
	switch v.Result {
	case corenotify.VerifyInProgress, corenotify.VerifyPassed, corenotify.VerifyFailed:
	default:
		return invalid("unknown verification result %q", string(v.Result))
	}
	if v.Revision <= 0 {
		return invalid("a verification transition needs a positive revision")
	}
	return parseTitle(v.Title)
}

func parseTitle(t string) error {
	if strings.TrimSpace(t) == "" {
		return invalid("a notification needs the task title")
	}
	return nil
}
