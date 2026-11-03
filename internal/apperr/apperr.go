// Package apperr defines the error classes that cross from the owning
// packages to the API. An owning package (tasks, repos, builds, git, hosts)
// translates its own errors into an Error; the API turns the class into a
// status code and shows the Message. The API never matches another package's
// sentinels, so a new package adds no case to the API.
package apperr

import "errors"

// Class says what kind of problem it is, in terms the API can act on.
type Class int

const (
	// NotFound: the thing asked for does not exist (any more).
	NotFound Class = iota + 1
	// Conflict: the request collides with the current state (a taken name, a
	// row that changed in the meantime).
	Conflict
	// Invalid: the request itself is wrong.
	Invalid
	// Unavailable: something Yggdrasil needs is not available right now.
	Unavailable
)

// String names the class for logs.
func (c Class) String() string {
	switch c {
	case NotFound:
		return "not found"
	case Conflict:
		return "conflict"
	case Invalid:
		return "invalid"
	case Unavailable:
		return "unavailable"
	}
	return "unknown"
}

// Error is an error with a class and a message for people. Message is plain
// language, says what to do next where it can, and never holds paths, tokens
// or internal error text. Err is the internal cause, for logs only.
type Error struct {
	Class   Class
	Message string
	Err     error
}

// Error returns the message and the cause, for logs.
func (e *Error) Error() string {
	if e.Err == nil {
		return e.Message
	}
	return e.Message + ": " + e.Err.Error()
}

// Unwrap returns the internal cause.
func (e *Error) Unwrap() error { return e.Err }

// New returns an *Error. err may be nil.
func New(class Class, message string, err error) *Error {
	return &Error{Class: class, Message: message, Err: err}
}

// ClassOf returns the class of the first *Error in err's chain.
func ClassOf(err error) (Class, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e.Class, true
	}
	return 0, false
}

// MessageOf returns the user-facing message of the first *Error in err's chain.
func MessageOf(err error) (string, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e.Message, true
	}
	return "", false
}

// ErrNeedsOptIn marks a refusal because the task's mode needs the person's
// opt-in and the request did not carry it. The API answers 409 with the
// needs_opt_in code so the UI asks for the confirmation and sends again.
var ErrNeedsOptIn = errors.New("the mode needs the opt-in")

// NeedsOptIn is the Conflict that says to confirm the project-mode warning.
func NeedsOptIn(message string, cause error) *Error {
	return New(Conflict, message, errors.Join(ErrNeedsOptIn, cause))
}
