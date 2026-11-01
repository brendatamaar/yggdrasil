package review

// Command is plain data that asks the shell for an effect (sealed).
type Command interface{ isCommand() }

// WriteAudit records a note change. It runs in the same transaction as the
// state write. Note text is never audited.
type WriteAudit struct {
	Action  string
	Outcome string
	Note    NoteID
}

// SendMessage hands the composed text to the task. NOT idempotent: guarded by
// the NoteSending state written first.
type SendMessage struct {
	Batch BatchID
	Text  string
}

// RemoveNote deletes the note row. Idempotent. (The design's "(deleted)"
// transition needs a command to say so.)
type RemoveNote struct{ Note NoteID }

// Publish tells subscribers that notes changed. Idempotent.
type Publish struct{}

func (WriteAudit) isCommand()  {}
func (SendMessage) isCommand() {}
func (RemoveNote) isCommand()  {}
func (Publish) isCommand()     {}
