//go:build !windows

package platformtest

import "testing"

// A held handle does not block a rename or a delete on Unix, and there are
// no 8.3 names: those clauses skip.
var (
	osLock  func(*testing.T, string) func()
	osShort func(*testing.T, string) string
)
