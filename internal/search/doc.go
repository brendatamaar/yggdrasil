// Package search is the shell of code search (XR-01, docs/design/code-search.md).
// The decisions (merge, caps, ordering, editor links) are pure functions in
// internal/core/search; this package parses outside data, runs git grep
// through platform.Start and feeds the answers back to the core. A search
// reads only: it persists nothing and writes no audit entry.
package search
