package tasks

import "fmt"

// OutcomeCode is the stored form of an Outcome, in tasks.idle_outcome and
// task_turns.outcome. The codes are pinned by the CHECK constraints of
// migration 00004: change both together, in a new migration.
func OutcomeCode(o Outcome) string {
	switch v := o.(type) {
	case Answered:
		return "answered"
	case ReadyForReview:
		return "ready_for_review"
	case NeedsYou:
		return "needs_" + string(v.Why)
	case Failed:
		return "failed_" + string(v.Kind)
	case Cancelled:
		return "cancelled"
	case Dismissed:
		return "dismissed"
	}
	return ""
}

// ParseOutcomeCode is the inverse of OutcomeCode. It rejects any other string.
func ParseOutcomeCode(s string) (Outcome, error) {
	switch s {
	case "answered":
		return Answered{}, nil
	case "ready_for_review":
		return ReadyForReview{}, nil
	case "cancelled":
		return Cancelled{}, nil
	case "dismissed":
		return Dismissed{}, nil
	}
	for _, w := range []NeedsWhy{NeedsIntegrityViolated, NeedsCapExceeded} {
		if s == "needs_"+string(w) {
			return NeedsYou{Why: w}, nil
		}
	}
	for _, k := range []FailKind{FailPrepare, FailStart, FailRun, FailInterrupted} {
		if s == "failed_"+string(k) {
			return Failed{Kind: k}, nil
		}
	}
	return nil, fmt.Errorf("unknown task outcome %q", s)
}
