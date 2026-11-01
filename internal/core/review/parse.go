package review

import (
	"errors"
	"strings"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// RawNote is a note as it arrives from the API, before parsing.
type RawNote struct {
	ID       string
	Task     string
	Repo     string
	Path     string
	Line     int
	Side     string
	Text     string
	DiffHash string
}

// ParseNote turns a raw note into a valid NoteOpen note created at now, or
// says which rule it broke. CRLF line endings in the text become LF. Pure.
func ParseNote(raw RawNote, now time.Time) (Note, error) {
	n := Note{
		ID:       NoteID(raw.ID),
		Task:     core.TaskID(raw.Task),
		Repo:     core.RepoID(raw.Repo),
		Anchor:   Anchor{Path: RelPath(raw.Path), Line: raw.Line, Side: Side(raw.Side)},
		Text:     strings.ReplaceAll(raw.Text, "\r\n", "\n"),
		DiffHash: raw.DiffHash,
		Created:  now,
		State:    NoteOpen{},
	}
	if err := n.Validate(); err != nil {
		return Note{}, err
	}
	if !plainToken(raw.ID) || !plainToken(raw.Repo) {
		return Note{}, errors.New("a note id or repo has characters that are not allowed")
	}
	return n, nil
}
