package review

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

func plainToken(s string) bool {
	return !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) || r == ' ' || r == ' ' })
}

// fenceFor returns a backtick fence longer than any backtick run in text, so
// the text cannot close it.
func fenceFor(text string) string {
	longest, run := 0, 0
	for _, r := range text {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	return strings.Repeat("`", max(3, longest+1))
}

// ComposeNotes builds the one message that sends the given notes to the
// agent. The header lists the batch and every note ID (so a double send is
// visible in the transcript); each note is a line naming its repo, its
// repo-relative file and line, then its text verbatim inside a code fence
// longer than any backtick run in the text, so the text cannot close the
// wrapper. Only repo-relative anchor paths appear. Every note must be Open
// and valid. The result is at most MaxMessageBytes. Pure.
func ComposeNotes(batch BatchID, notes []Note) (string, error) {
	if err := batch.Validate(); err != nil || !plainToken(string(batch)) {
		return "", errors.New("these notes can't be sent: the batch has no valid ID")
	}
	if len(notes) == 0 {
		return "", errors.New("choose at least one note to send")
	}
	if len(notes) > MaxNotesPerTask {
		return "", fmt.Errorf("at most %d notes can be sent at once", MaxNotesPerTask)
	}
	seen := make(map[NoteID]bool, len(notes))
	ids := make([]string, len(notes))
	for i, n := range notes {
		if _, open := n.State.(NoteOpen); !open {
			return "", fmt.Errorf("note %s has already been sent or is out of date; send only open notes", n.ID)
		}
		if err := errors.Join(n.ID.Validate(), n.Repo.Validate(), n.Anchor.Validate(), ValidText(n.Text)); err != nil ||
			!plainToken(string(n.ID)) || !plainToken(string(n.Repo)) || seen[n.ID] {
			return "", fmt.Errorf("note %s can't be sent: it is not valid", n.ID)
		}
		seen[n.ID] = true
		ids[i] = string(n.ID)
	}
	var b strings.Builder
	b.WriteString("Review notes on your changes (batch " + string(batch) + "). Address each note, then say what you changed.\n")
	b.WriteString("Note IDs: " + strings.Join(ids, ", ") + "\n")
	for _, n := range notes {
		fence := fenceFor(n.Text)
		b.WriteString("\nNote " + string(n.ID) + ": repo " + string(n.Repo) + ", file " + string(n.Anchor.Path) +
			", line " + strconv.Itoa(n.Anchor.Line) + " (" + string(n.Anchor.Side) + " side)\n")
		b.WriteString(fence + "\n" + n.Text + "\n" + fence + "\n")
	}
	if b.Len() > MaxMessageBytes {
		return "", fmt.Errorf("these notes are too long to send in one message (at most %d bytes); send fewer notes", MaxMessageBytes)
	}
	return b.String(), nil
}
