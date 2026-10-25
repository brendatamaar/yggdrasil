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
// Working for Waiting, Starting, Running and Stopping (a running task with
// pending proposals stays Working); NeedsYou for Idle{NeedsYou} or an Idle
// task with pending proposals; Review for Idle{ReadyForReview}; Done for the
// rest of Idle and for a Draft.
//
// The order of the Idle cases (pending proposals before ReadyForReview) is
// the literal reading of the design; K1 confirms it with the table test.
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
