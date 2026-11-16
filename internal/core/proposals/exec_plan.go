package proposals

import (
	"crypto/sha256"
	"encoding/hex"
)

// Pure decisions of the Phase 6 executors (IgnoreEntries, ReviewComment,
// PullFF). The shell reads the facts (an MR head, an effect record, a file
// checksum, a branch tip), calls one of these and does what the answer says.
// None of them performs an effect.

// CommentBodyHash is the hash a host compares when it looks for a posted
// comment: the lower-case hex SHA-256 of the comment body without the marker.
func CommentBodyHash(body string) string {
	h := sha256.Sum256([]byte(body))
	return hex.EncodeToString(h[:])
}

// EffectStatus is the stored state of one earlier attempt's effect record. The
// values are those of the shell's effect states.
type EffectStatus string

const (
	EffectStarted   EffectStatus = "intent"
	EffectDone      EffectStatus = "succeeded"
	EffectRefused   EffectStatus = "failed"
	EffectUncertain EffectStatus = "unknown"
)

// CommentPrior says what earlier attempts of one comment proposal may have
// done on the host.
type CommentPrior string

const (
	// PriorNone: no attempt reached the point of sending.
	PriorNone CommentPrior = "none"
	// PriorNotPosted: every earlier attempt was definitely refused by the host.
	PriorNotPosted CommentPrior = "not_posted"
	// PriorMaybePosted: an earlier attempt started (or lost its reply) and was
	// never settled; the comment may exist.
	PriorMaybePosted CommentPrior = "maybe_posted"
	// PriorPosted: an earlier attempt recorded a posted comment.
	PriorPosted CommentPrior = "posted"
)

// PriorOfEffects folds the statuses of the earlier effects of a proposal. An
// unsettled or uncertain effect always wins over a refused one: it may have
// posted, so nothing is sent before the host has been asked.
func PriorOfEffects(states []EffectStatus) CommentPrior {
	out := PriorNone
	for _, s := range states {
		switch s {
		case EffectDone:
			return PriorPosted
		case EffectRefused:
			if out == PriorNone {
				out = PriorNotPosted
			}
		default: // started, uncertain and anything not understood
			out = PriorMaybePosted
		}
	}
	return out
}

// CommentPlan is the next step of a comment execution.
type CommentPlan string

const (
	// PlanCommentDone: an earlier attempt already posted it; send nothing.
	PlanCommentDone CommentPlan = "done"
	// PlanCommentStale: the MR head is not the reviewed one; send nothing.
	PlanCommentStale CommentPlan = "stale"
	// PlanCommentLookup: ask the host for the marker first (read-only); send
	// nothing yet.
	PlanCommentLookup CommentPlan = "lookup"
	// PlanCommentPost: send exactly one POST.
	PlanCommentPost CommentPlan = "post"
)

// PlanComment decides the first step of executing a ReviewComment. actualHead is
// the MR head read just now; it is only used (and only needs reading) when the
// answer could be a POST, that is when prior is neither PriorMaybePosted nor
// PriorPosted. A POST needs the head to equal the reviewed head, and prior to
// rule out an unreconciled earlier send (never a blind retry).
func PlanComment(a ReviewComment, prior CommentPrior, actualHead string) CommentPlan {
	switch prior {
	case PriorPosted:
		return PlanCommentDone
	case PriorMaybePosted:
		return PlanCommentLookup
	case PriorNone, PriorNotPosted:
		if actualHead == "" || actualHead != a.ExpectedHead {
			return PlanCommentStale
		}
		return PlanCommentPost
	}
	return PlanCommentLookup // a prior that is not understood is treated as uncertain
}

// CommentVerdict is what a lookup of a comment by its marker proved.
type CommentVerdict string

const (
	// CommentPosted: exactly this comment (marker, body hash, head) exists.
	CommentPosted CommentVerdict = "posted"
	// CommentMayRetry: the whole list was read and the comment is not there. A
	// human may retry; nothing is sent automatically.
	CommentMayRetry CommentVerdict = "may_retry"
	// CommentBlocked: anything else (unknown read, a conflicting or duplicate
	// match, an invalid answer). Nothing is sent until a human looks.
	CommentBlocked CommentVerdict = "blocked"
)

// JudgeCommentLookup turns the host's answer into a verdict. When the verdict is
// CommentPosted the receipt is complete and valid; otherwise it is empty.
func JudgeCommentLookup(a ReviewComment, l CommentLookup) (CommentVerdict, CommentReceipt) {
	switch l.Outcome {
	case CommentFound:
		r := CommentReceipt{CommentID: l.CommentID, Marker: a.Marker, BodyHash: CommentBodyHash(a.Body), Head: a.ExpectedHead}
		if r.Validate() != nil {
			return CommentBlocked, CommentReceipt{}
		}
		return CommentPosted, r
	case CommentAbsent:
		return CommentMayRetry, CommentReceipt{}
	}
	return CommentBlocked, CommentReceipt{}
}

// IgnoreFile is the state of the top-level .gitignore as the shell read it.
type IgnoreFile struct {
	Absent bool
	Hash   string
}

// IgnoreMatches reports whether the file is still exactly what the user
// reviewed (absent, or the same checksum).
func IgnoreMatches(a IgnoreEntries, f IgnoreFile) bool {
	if a.ExpectedAbsent {
		return f.Absent
	}
	return !f.Absent && f.Hash == a.ExpectedHash
}

// ReconcileIgnore judges a crashed ignore write without reading the file's
// text. The file as reviewed means the write never happened. A changed file
// proves nothing about who changed it, so it is Unknown and reported for manual
// inspection; Yggdrasil never repairs it.
func ReconcileIgnore(a IgnoreEntries, f IgnoreFile) ReconcileOutcome {
	if IgnoreMatches(a, f) {
		return ReconcileNotDone
	}
	return ReconcileUnknown
}

// PullPlan is the next step of executing a PullFF.
type PullPlan string

const (
	// PlanPullGo: the checkout is as proposed; hand it to the pull runner.
	PlanPullGo PullPlan = "go"
	// PlanPullStale: the branch tip is not the expected one; change nothing.
	PlanPullStale PullPlan = "stale"
	// PlanPullWrongBranch: another branch is checked out; change nothing.
	PlanPullWrongBranch PullPlan = "wrong_branch"
)

// PlanPull checks the facts the shell read read-only (checked-out branch and
// its tip) against the action. A pull never runs on a checkout that is not the
// one the user confirmed.
func PlanPull(a PullFF, branch, head string) PullPlan {
	switch {
	case branch != a.Branch:
		return PlanPullWrongBranch
	case head == "" || head != a.ExpectHead:
		return PlanPullStale
	}
	return PlanPullGo
}

// ReconcilePull judges a crashed pull. A tip that is still the expected one
// means nothing was pulled. Any other tip may be our fast-forward or someone
// else's commit, so it is Unknown.
func ReconcilePull(a PullFF, head string) ReconcileOutcome {
	if head != "" && head == a.ExpectHead {
		return ReconcileNotDone
	}
	return ReconcileUnknown
}
