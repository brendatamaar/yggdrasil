package review

import (
	"slices"
	"strings"
)

func changed(n NoteID, action string) []Command {
	return []Command{WriteAudit{Action: action, Outcome: "ok", Note: n}, Publish{}}
}

// StepNote is (note, input) -> (note, commands): the review-note table of
// docs/design/changesets.md. An input it does not handle in the current state
// changes nothing and returns no commands. Every change emits one WriteAudit
// and one Publish. A deleted note is returned as the zero Note.
//
// SendNotes on one note emits that note's SendMessage; for several notes in
// one batch use StepBatch, which sends one message.
func StepNote(n Note, in Input) (Note, []Command) {
	if n.State == nil {
		return addNote(n, in)
	}
	switch v := in.(type) {
	case EditNote:
		if _, ok := n.State.(NoteOpen); ok && ValidText(v.Text) == nil && v.Text != n.Text {
			n.Text = v.Text
			return n, changed(n.ID, "note.edit")
		}
	case DeleteNote:
		switch n.State.(type) {
		case NoteOpen, NoteOutdated:
			return Note{}, append(changed(n.ID, "note.delete"), RemoveNote{Note: n.ID})
		}
	case SendNotes:
		if sendable(n, v) {
			n.State = NoteSending{Batch: v.Batch, At: v.Now}
			return n, append(changed(n.ID, "note.send"), SendMessage{Batch: v.Batch, Text: v.Text})
		}
	case SendAccepted:
		if s, ok := n.State.(NoteSending); ok && s.Batch == v.Batch && v.Turn.Validate() == nil {
			n.State = NoteSent{Turn: v.Turn, At: v.Now}
			return n, changed(n.ID, "note.sent")
		}
	case SendRefused:
		if s, ok := n.State.(NoteSending); ok && s.Batch == v.Batch {
			n.State = NoteOpen{}
			return n, changed(n.ID, "note.send_refused")
		}
	case Recover:
		if s, ok := n.State.(NoteSending); ok && (v.Startup || v.Now.Sub(s.At) >= SendTimeout) {
			n.State = NoteOpen{}
			return n, changed(n.ID, "note.recovered")
		}
	case DiffObserved:
		if _, ok := n.State.(NoteOpen); ok {
			if h, present := v.Hashes[n.Anchor.Path]; !present || h != n.DiffHash {
				n.State = NoteOutdated{At: v.Now}
				return n, changed(n.ID, "note.outdated")
			}
		}
	}
	return n, nil
}

func addNote(n Note, in Input) (Note, []Command) {
	v, ok := in.(AddNote)
	if !ok || n.ID != "" || v.OpenCount < 0 || v.OpenCount >= MaxNotesPerTask {
		return n, nil
	}
	added := v.Note
	switch added.State.(type) {
	case nil, NoteOpen:
	default:
		return n, nil
	}
	added.State = NoteOpen{}
	if added.Created.IsZero() {
		added.Created = v.Now
	}
	if added.Validate() != nil {
		return n, nil
	}
	return added, changed(added.ID, "note.add")
}

// sendable reports whether a note may move to NoteSending with this input: it
// is open, the batch and text are valid, and the text lists the note's ID.
func sendable(n Note, v SendNotes) bool {
	_, open := n.State.(NoteOpen)
	return open && v.Batch.Validate() == nil && v.Text != "" && len(v.Text) <= MaxMessageBytes &&
		strings.Contains(v.Text, string(n.ID))
}

// StepBatch sends several notes in one batch: all of them must be sendable,
// or nothing changes. It emits one WriteAudit per note, one SendMessage for the
// batch and one Publish.
func StepBatch(notes []Note, in SendNotes) ([]Note, []Command) {
	if len(notes) == 0 || len(notes) > MaxNotesPerTask {
		return notes, nil
	}
	seen := map[NoteID]bool{}
	for _, n := range notes {
		if !sendable(n, in) || seen[n.ID] {
			return notes, nil
		}
		seen[n.ID] = true
	}
	out := slices.Clone(notes)
	var cmds []Command
	for i := range out {
		out[i].State = NoteSending{Batch: in.Batch, At: in.Now}
		cmds = append(cmds, WriteAudit{Action: "note.send", Outcome: "ok", Note: out[i].ID})
	}
	return out, append(cmds, SendMessage{Batch: in.Batch, Text: in.Text}, Publish{})
}
