package hosts

import "fmt"

// ParseHead validates the head commit a host reports for a merge request: the
// full commit id (40 lower case hex digits, or 64 for SHA-256 repos), or empty
// when the host did not say. Anything else is rejected, never shortened or
// lower-cased, so a pin made from it names exactly one commit (GH-05, AC-02).
func ParseHead(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	if !contextHead(s) {
		return "", fmt.Errorf("the host sent an invalid head commit %q; expected 40 or 64 lower case hex digits", clipHead(s))
	}
	return s, nil
}

// clipHead keeps a rejected value short in an error message.
func clipHead(s string) string {
	const limit = 80
	if len(s) > limit {
		return s[:limit] + "..."
	}
	return s
}
