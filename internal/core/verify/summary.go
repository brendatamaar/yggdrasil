package verify

// Summary is the task-level verification status shown on the board and the
// task card. It is derived from the repo states, never stored.
type Summary string

// The summaries, listed best first.
const (
	SummaryNone         Summary = "none"
	SummarySkipped      Summary = "skipped"
	SummaryPassed       Summary = "passed"
	SummaryRunning      Summary = "running"
	SummaryCancelled    Summary = "cancelled"
	SummaryUnverifiable Summary = "unverifiable"
	SummaryInterrupted  Summary = "interrupted"
	SummaryFailed       Summary = "failed"
)

// rank orders the summaries worst first: Failed > Interrupted > Unverifiable
// > Cancelled > running > Passed > Skipped > None.
func (s Summary) rank() int {
	switch s {
	case SummaryFailed:
		return 7
	case SummaryInterrupted:
		return 6
	case SummaryUnverifiable:
		return 5
	case SummaryCancelled:
		return 4
	case SummaryRunning:
		return 3
	case SummaryPassed:
		return 2
	case SummarySkipped:
		return 1
	}
	return 0
}

// SummaryOf maps one state to its summary. A nil state counts as None.
func SummaryOf(s State) Summary {
	switch s.(type) {
	case Skipped:
		return SummarySkipped
	case Unverifiable:
		return SummaryUnverifiable
	case Bootstrapping, Verifying:
		return SummaryRunning
	case Passed:
		return SummaryPassed
	case Failed:
		return SummaryFailed
	case Interrupted:
		return SummaryInterrupted
	case Cancelled:
		return SummaryCancelled
	}
	return SummaryNone
}

// Summarize returns the worst summary over the repo states of a task; no
// repos gives None.
func Summarize(states []State) Summary {
	worst := SummaryNone
	for _, s := range states {
		if x := SummaryOf(s); x.rank() > worst.rank() {
			worst = x
		}
	}
	return worst
}
