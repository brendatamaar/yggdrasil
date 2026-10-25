package guard

import "fmt"

// ParsePolicy turns the policy file written by the tasks service into a
// Policy and rejects what Policy.Validate rejects.
//
// STUB (task G1 implements it): always an error, so a hook that cannot parse
// its policy denies every call (fail closed).
func ParsePolicy(data []byte) (Policy, error) {
	_ = data
	return Policy{}, fmt.Errorf("parse policy: %w", ErrNotImplemented)
}

// FormatPolicy is the inverse of ParsePolicy: ParsePolicy(FormatPolicy(p))
// equals p for every valid p.
//
// STUB (task G1 implements it): returns nil.
func FormatPolicy(p Policy) []byte {
	_ = p
	return nil
}
