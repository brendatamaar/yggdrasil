package review

// StepNote is (note, input) -> (note, commands): the review-note table of
// docs/design/changesets.md. An input it does not handle in the current state
// changes nothing and returns no commands.
//
// TODO(CS1): decision logic. Foundations ship the signature only.
func StepNote(n Note, in Input) (Note, []Command) {
	_ = in
	return n, nil
}

// ComposeNotes builds the one message that sends the given notes to the
// agent: bounded by MaxMessageBytes, repo-relative anchors only, each note's
// text verbatim but escaped so it cannot close the wrapper. Pure.
//
// TODO(CS1): composer. Foundations ship the signature only.
func ComposeNotes(batch BatchID, notes []Note) (string, error) {
	_, _ = batch, notes
	return "", nil
}
