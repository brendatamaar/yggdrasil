//go:build !windows && !darwin

package platform

// PathsCaseInsensitive reports whether two spellings of a path that differ
// only in case are the same folder. Linux file systems are case-sensitive.
const PathsCaseInsensitive = false
