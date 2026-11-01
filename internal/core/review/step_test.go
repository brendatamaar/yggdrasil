package review

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func withState(s NoteState) Note {
	n := validNote("n1")
	n.State = s
	return n
}

func TestStepTable(t *testing.T) {
	sendAt := epoch
	later := epoch.Add(SendTimeout)
	tests := []struct {
		name   string
		from   Note
		in     Input
		want   NoteState // nil: the note is deleted (zero Note)
		action string    // "" means no change and no commands
	}{
		{"add", Note{}, AddNote{Note: validNote("n1"), Now: epoch}, NoteOpen{}, "note.add"},
		{"add over the cap", Note{}, AddNote{Note: validNote("n1"), OpenCount: MaxNotesPerTask, Now: epoch}, nil, ""},
		{"add invalid", Note{}, AddNote{Note: func() Note { n := validNote("n1"); n.Anchor.Line = 0; return n }(), Now: epoch}, nil, ""},
		{"add on an existing note", withState(NoteOpen{}), AddNote{Note: validNote("n2"), Now: epoch}, NoteOpen{}, ""},
		{"edit", withState(NoteOpen{}), EditNote{Text: "other", Now: epoch}, NoteOpen{}, "note.edit"},
		{"edit empty", withState(NoteOpen{}), EditNote{Text: " ", Now: epoch}, NoteOpen{}, ""},
		{"edit outdated", withState(NoteOutdated{}), EditNote{Text: "other", Now: epoch}, NoteOutdated{}, ""},
		{"delete open", withState(NoteOpen{}), DeleteNote{Now: epoch}, nil, "note.delete"},
		{"delete outdated", withState(NoteOutdated{}), DeleteNote{Now: epoch}, nil, "note.delete"},
		{"delete sent", withState(NoteSent{Turn: "t1"}), DeleteNote{Now: epoch}, NoteSent{Turn: "t1"}, ""},
		{"delete sending", withState(NoteSending{Batch: "b1", At: sendAt}), DeleteNote{Now: epoch}, NoteSending{Batch: "b1", At: sendAt}, ""},
		{"send", withState(NoteOpen{}), SendNotes{Batch: "b1", Text: "n1 says", Now: epoch}, NoteSending{Batch: "b1", At: epoch}, "note.send"},
		{"send text without the id", withState(NoteOpen{}), SendNotes{Batch: "b1", Text: "other", Now: epoch}, NoteOpen{}, ""},
		{"send oversize", withState(NoteOpen{}), SendNotes{Batch: "b1", Text: "n1" + strings.Repeat("x", MaxMessageBytes), Now: epoch}, NoteOpen{}, ""},
		{"send outdated", withState(NoteOutdated{}), SendNotes{Batch: "b1", Text: "n1", Now: epoch}, NoteOutdated{}, ""},
		{"accepted", withState(NoteSending{Batch: "b1", At: sendAt}), SendAccepted{Batch: "b1", Turn: "t9", Now: later}, NoteSent{Turn: "t9", At: later}, "note.sent"},
		{"accepted other batch", withState(NoteSending{Batch: "b1", At: sendAt}), SendAccepted{Batch: "b2", Turn: "t9", Now: later}, NoteSending{Batch: "b1", At: sendAt}, ""},
		{"accepted on open", withState(NoteOpen{}), SendAccepted{Batch: "b1", Turn: "t9", Now: later}, NoteOpen{}, ""},
		{"refused", withState(NoteSending{Batch: "b1", At: sendAt}), SendRefused{Batch: "b1", Reason: "busy", Now: later}, NoteOpen{}, "note.send_refused"},
		{"refused other batch", withState(NoteSending{Batch: "b1", At: sendAt}), SendRefused{Batch: "b2", Now: later}, NoteSending{Batch: "b1", At: sendAt}, ""},
		{"recover after the timeout", withState(NoteSending{Batch: "b1", At: sendAt}), Recover{Now: later}, NoteOpen{}, "note.recovered"},
		{"recover too early", withState(NoteSending{Batch: "b1", At: sendAt}), Recover{Now: later.Add(-time.Second)}, NoteSending{Batch: "b1", At: sendAt}, ""},
		{"recover at startup", withState(NoteSending{Batch: "b1", At: sendAt}), Recover{Startup: true, Now: epoch}, NoteOpen{}, "note.recovered"},
		{"diff changed", withState(NoteOpen{}), DiffObserved{Hashes: map[RelPath]string{"a.go": hash("b")}, Now: later}, NoteOutdated{At: later}, "note.outdated"},
		{"file left the diff", withState(NoteOpen{}), DiffObserved{Hashes: map[RelPath]string{}, Now: later}, NoteOutdated{At: later}, "note.outdated"},
		{"diff same", withState(NoteOpen{}), DiffObserved{Hashes: map[RelPath]string{"a.go": hash("a")}, Now: later}, NoteOpen{}, ""},
		{"diff on sent", withState(NoteSent{Turn: "t1"}), DiffObserved{Hashes: nil, Now: later}, NoteSent{Turn: "t1"}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, cmds := StepNote(tc.from, tc.in)
			if tc.action == "" {
				if !reflect.DeepEqual(got, tc.from) || len(cmds) != 0 {
					t.Fatalf("expected no change, got %#v with %v", got.State, cmds)
				}
				return
			}
			if tc.want == nil {
				if !reflect.DeepEqual(got, Note{}) {
					t.Fatalf("expected a deleted note, got %#v", got)
				}
			} else if !reflect.DeepEqual(got.State, tc.want) {
				t.Fatalf("state = %#v, want %#v", got.State, tc.want)
			}
			a, ok := cmds[0].(WriteAudit)
			if !ok || a.Action != tc.action || a.Note != "n1" && tc.action != "note.add" {
				t.Fatalf("first command = %#v, want audit %s", cmds[0], tc.action)
			}
		})
	}
}

func TestEditChangesText(t *testing.T) {
	got, _ := StepNote(withState(NoteOpen{}), EditNote{Text: "new words", Now: epoch})
	if got.Text != "new words" {
		t.Fatalf("text = %q", got.Text)
	}
}

func TestAddNormalizesState(t *testing.T) {
	in := validNote("n1")
	in.Created = time.Time{}
	got, _ := StepNote(Note{}, AddNote{Note: in, Now: epoch})
	if got.Created != epoch {
		t.Fatalf("created = %v", got.Created)
	}
}

func TestDeleteEmitsRemove(t *testing.T) {
	_, cmds := StepNote(withState(NoteOpen{}), DeleteNote{Now: epoch})
	if count[RemoveNote](cmds) != 1 {
		t.Fatalf("commands = %v", cmds)
	}
}

func TestBatchSendsOnce(t *testing.T) {
	a, b := withState(NoteOpen{}), validNote("n2")
	out, cmds := StepBatch([]Note{a, b}, SendNotes{Batch: "b1", Text: "n1 n2", Now: epoch})
	if count[SendMessage](cmds) != 1 || len(out) != 2 {
		t.Fatalf("commands = %v", cmds)
	}
	for _, n := range out {
		if s, ok := n.State.(NoteSending); !ok || s.Batch != "b1" {
			t.Fatalf("state = %#v", n.State)
		}
	}
}
