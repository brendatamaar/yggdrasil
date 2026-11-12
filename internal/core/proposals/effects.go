package proposals

// Plain data shared by the executors of the Phase 6 proposals and the host
// capabilities they call. Pure values: no behaviour.

// CommentLookup is the answer of a read-only lookup of a review comment by its
// marker (reconciliation after a lost reply). Only a complete read can say
// CommentAbsent; an incomplete one is CommentUnknown.
type CommentLookup struct {
	Outcome CommentLookupOutcome
	// CommentID is the host's ID when Outcome is CommentFound.
	CommentID string
}

// CommentLookupOutcome is a closed set.
type CommentLookupOutcome string

const (
	// CommentFound: exactly one comment matches the marker, body hash and head.
	CommentFound CommentLookupOutcome = "found"
	// CommentAbsent: the whole comment list was read and none matches. A human
	// still decides whether to retry; there is no automatic repost.
	CommentAbsent CommentLookupOutcome = "absent"
	// CommentUnknown: a page failed or pagination was incomplete; never proof
	// of absence.
	CommentUnknown CommentLookupOutcome = "unknown"
	// CommentConflict: more than one match, or a match with another body or head.
	CommentConflict CommentLookupOutcome = "conflict"
)
