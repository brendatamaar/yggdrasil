package platform

import "errors"

// Additional outcomes of the safe filesystem capabilities (safe_fs.go). They
// are sentinels so callers use errors.Is and show their own plain-language
// text; the wrapped messages here are written for the user as well.

// ErrUnsafePath means the path or name cannot be used without risk: a link,
// junction or reparse point on the way, a \\wsl$ or device path, a device
// name, an 8.3 short-name alias, or a name that is not one plain component.
var ErrUnsafePath = errors.New("the path is not safe to write to")

// ErrLocked means another program holds the file open. It is retryable: close
// the program and try again with the same preview.
var ErrLocked = errors.New("the file is in use by another program")

// ErrTooLarge means the file is bigger than Yggdrasil will read or edit.
var ErrTooLarge = errors.New("the file is too large")

// ErrInvalidArgument means the caller passed a value the capability refuses
// (an empty identity, a malformed entry, an unusable expectation).
var ErrInvalidArgument = errors.New("invalid value")
