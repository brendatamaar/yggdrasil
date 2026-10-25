//go:build darwin

package platform

// PathsCaseInsensitive reports whether two spellings of a path that differ
// only in case are the same folder. The default macOS file system (APFS,
// HFS+) is case-insensitive, so this is true; a case-sensitive volume is
// treated as insensitive too. That is safe: callers fold with ASCII
// lower-casing, which can only make a guard stricter (it may call two files
// the same that are not, never the reverse).
const PathsCaseInsensitive = true
