package review

import "time"

// Input is one event for StepNote (sealed). Every input carries Now.
type Input interface{ isInput() }

// AddNote: the user wrote a note (parsed at the boundary). OpenCount is the
// number of open and outdated notes the task already has, for the cap.
type AddNote struct {
	Note      Note
	OpenCount int
	Now       time.Time
}

// EditNote: the user changed the text of an open note.
type EditNote struct {
	Text string
	Now  time.Time
}

// DeleteNote: the user deleted an open or outdated note.
type DeleteNote struct{ Now time.Time }

// SendNotes: the user sent a batch. Text is composed by the shell with
// ComposeNotes before the step, so the core checks its size.
type SendNotes struct {
	Batch BatchID
	Text  string
	Now   time.Time
}

// SendAccepted: tasks.Message accepted the batch.
type SendAccepted struct {
	Batch BatchID
	Turn  TurnID
	Now   time.Time
}

// SendRefused: tasks.Message refused the batch (task running, queue full).
type SendRefused struct {
	Batch  BatchID
	Reason string
	Now    time.Time
}

// DiffObserved: the current per-file diff hashes of the note's repo.
type DiffObserved struct {
	Hashes map[RelPath]string
	Now    time.Time
}

// Recover: startup or reconcile found a note in NoteSending.
type Recover struct{ Now time.Time }

func (AddNote) isInput()      {}
func (EditNote) isInput()     {}
func (DeleteNote) isInput()   {}
func (SendNotes) isInput()    {}
func (SendAccepted) isInput() {}
func (SendRefused) isInput()  {}
func (DiffObserved) isInput() {}
func (Recover) isInput()      {}
