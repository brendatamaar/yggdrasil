package review

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Caps.
const (
	MaxNotesPerTask = 200
	MaxNoteRunes    = 2000
	MaxMessageBytes = 32 * 1024
	MaxLine         = 1_000_000
	MaxPathBytes    = 1024
	// SendTimeout is how long a note may stay in NoteSending before Recover
	// returns it to NoteOpen.
	SendTimeout = 5 * time.Minute
)

// NoteID identifies a note.
type NoteID string

// Validate reports whether the ID is well formed.
func (id NoteID) Validate() error { return core.TaskID(id).Validate() }

// BatchID identifies one "send notes" action.
type BatchID string

// Validate reports whether the ID is well formed.
func (id BatchID) Validate() error { return core.TaskID(id).Validate() }

// TurnID mirrors core/tasks.TurnID; the shell converts.
type TurnID string

// Validate reports whether the ID is well formed.
func (id TurnID) Validate() error { return core.TaskID(id).Validate() }

// RelPath is a repo-relative, slash-separated path of a file in the diff.
type RelPath string

// Validate rejects absolute paths, "..", drives, streams (":"), backslashes
// and control characters, so a note can never name a path outside its repo.
func (p RelPath) Validate() error {
	s := string(p)
	if s == "" || len(s) > MaxPathBytes {
		return errors.New("a note path must have 1 to 1024 bytes")
	}
	if strings.HasPrefix(s, "/") || strings.ContainsAny(s, `\:`) || strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return fmt.Errorf("the note path %q isn't a repo-relative path", s)
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("the note path %q isn't a repo-relative path", s)
		}
	}
	return nil
}

// Side says which side of the diff a line belongs to.
type Side string

const (
	SideOld Side = "old"
	SideNew Side = "new"
)

// Validate reports whether s is known.
func (s Side) Validate() error {
	switch s {
	case SideOld, SideNew:
		return nil
	}
	return fmt.Errorf("unknown diff side %q", string(s))
}

// Anchor is where a note sits in a repo's diff.
type Anchor struct {
	Path RelPath
	Line int
	Side Side
}

// Validate checks path, line and side.
func (a Anchor) Validate() error {
	if a.Line < 1 || a.Line > MaxLine {
		return fmt.Errorf("the line must be between 1 and %d", MaxLine)
	}
	return errors.Join(a.Path.Validate(), a.Side.Validate())
}

// ValidText reports whether text is 1 to MaxNoteRunes runes with no control
// characters other than newline and tab.
func ValidText(text string) error {
	if strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > MaxNoteRunes || !utf8.ValidString(text) {
		return fmt.Errorf("a note must have 1 to %d characters", MaxNoteRunes)
	}
	if strings.ContainsFunc(text, func(r rune) bool { return (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f }) {
		return errors.New("a note can't contain control characters")
	}
	return nil
}

// Note is one review note with its state. DiffHash is the hash of the file's
// diff when the note was written (64 hex).
type Note struct {
	ID       NoteID
	Task     core.TaskID
	Repo     core.RepoID
	Anchor   Anchor
	Text     string
	DiffHash string
	Created  time.Time
	State    NoteState
}

// Validate checks every field and the state.
func (n Note) Validate() error {
	if n.State == nil {
		return errors.New("note has no state")
	}
	if len(n.DiffHash) != 64 || strings.Trim(n.DiffHash, "0123456789abcdef") != "" {
		return errors.New("note diff hash must be 64 lowercase hex characters")
	}
	return errors.Join(n.ID.Validate(), n.Task.Validate(), n.Repo.Validate(), n.Anchor.Validate(), ValidText(n.Text), n.State.Validate())
}

// NoteState is the state of a note (sealed).
type NoteState interface {
	Validate() error
	isNoteState()
}

// NoteOpen: written, not sent.
type NoteOpen struct{}

// NoteSending: composed into a message, waiting for the task to accept it.
type NoteSending struct {
	Batch BatchID
	At    time.Time
}

// NoteSent: the task accepted the message; final.
type NoteSent struct {
	Turn TurnID
	At   time.Time
}

// NoteOutdated: the file changed after the note was written, before it was
// sent.
type NoteOutdated struct{ At time.Time }

func (NoteOpen) isNoteState()     {}
func (NoteSending) isNoteState()  {}
func (NoteSent) isNoteState()     {}
func (NoteOutdated) isNoteState() {}

// Validate always succeeds.
func (NoteOpen) Validate() error { return nil }

// Validate checks the batch.
func (s NoteSending) Validate() error { return s.Batch.Validate() }

// Validate checks the turn.
func (s NoteSent) Validate() error { return s.Turn.Validate() }

// Validate always succeeds.
func (NoteOutdated) Validate() error { return nil }
