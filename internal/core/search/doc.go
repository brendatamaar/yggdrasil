// Package search is the functional core of code search (XR-01,
// docs/design/code-search.md): the query, the merge of per-repo results under
// caps, a stable ordering, and pure editor links. Nothing here does IO, reads
// the clock or logs; the shell in internal/search runs git grep and feeds the
// answers back as inputs. The types carry no JSON tags: wire DTOs live in
// internal/api.
package search
