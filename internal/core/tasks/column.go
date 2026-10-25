package tasks

// BoardColumn is where a task card sits on the agent board (AG-12).
type BoardColumn string

const (
	ColumnWorking  BoardColumn = "working"
	ColumnNeedsYou BoardColumn = "needs_you"
	ColumnReview   BoardColumn = "review"
	ColumnDone     BoardColumn = "done"
)

// Column maps a state and the number of pending proposals onto a column:
//
//   - Working: Waiting, Starting, Running and Stopping, whatever the number of
//     pending proposals (the card shows a badge). Working holds exactly when a
//     run could be live (K14).
//   - NeedsYou: Idle{NeedsYou}, or any Idle task with pending proposals. The
//     pending-proposal test comes before ReadyForReview: a finished turn whose
//     proposals still wait for a confirmation needs a decision, and a
//     proposal is the one thing the user must act on.
//   - Review: Idle{ReadyForReview} with no pending proposals.
//   - Done: the rest of Idle.
//   - Draft: Done. The four columns are fixed by AG-12 and a draft is neither
//     live nor waiting for a decision; it has not started, so it gets the
//     least prominent column. The board query should leave drafts out; this
//     is only the total answer for a draft that reaches the function.
func Column(s State, pendingProposals int) BoardColumn {
	switch st := s.(type) {
	case Waiting, Starting, Running, Stopping:
		return ColumnWorking
	case Idle:
		if _, ok := st.Outcome.(NeedsYou); ok || pendingProposals > 0 {
			return ColumnNeedsYou
		}
		if _, ok := st.Outcome.(ReadyForReview); ok {
			return ColumnReview
		}
		return ColumnDone
	}
	return ColumnDone
}
