package review

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// N1: a note is sent at most once: NoteSent and NoteSending accept no
// SendNotes, and SendMessage is only emitted from NoteOpen.
func TestPropNoteSentOnce(t *testing.T) {
	prop(t, func(t *testing.T, before Note, in Input, after Note, cmds []Command) {
		if _, ok := in.(SendNotes); !ok {
			return
		}
		switch before.State.(type) {
		case NoteSent, NoteSending:
			if changedNote(before, after) || len(cmds) != 0 {
				t.Errorf("SendNotes changed a %T note or emitted %d commands", before.State, len(cmds))
			}
		}
	})
}

// N3: NoteSent is final: no input changes it or emits a command.
func TestPropSentIsFinal(t *testing.T) {
	prop(t, func(t *testing.T, before Note, in Input, after Note, cmds []Command) {
		if _, ok := before.State.(NoteSent); !ok {
			return
		}
		if changedNote(before, after) || len(cmds) != 0 {
			t.Errorf("%T changed a sent note or emitted %d commands", in, len(cmds))
		}
	})
}

// N4: DiffObserved never changes a Sent or Sending note; it reaches Outdated
// only from Open.
func TestPropOutdatedOnlyFromOpen(t *testing.T) {
	prop(t, func(t *testing.T, before Note, in Input, after Note, cmds []Command) {
		if _, ok := in.(DiffObserved); !ok {
			return
		}
		switch before.State.(type) {
		case NoteSent, NoteSending, NoteOutdated:
			if changedNote(before, after) || len(cmds) != 0 {
				t.Errorf("DiffObserved changed a %T note", before.State)
			}
		}
		if _, out := after.State.(NoteOutdated); out && changedNote(before, after) {
			if _, open := before.State.(NoteOpen); !open {
				t.Errorf("Outdated reached from %T", before.State)
			}
		}
	})
}

// N5: every SendMessage is paired with exactly one transition into
// NoteSending for the same batch, and every such transition emits one.
func TestPropSendCommandMatchesState(t *testing.T) {
	prop(t, func(t *testing.T, before Note, in Input, after Note, cmds []Command) {
		_, wasSending := before.State.(NoteSending)
		s, isSending := after.State.(NoteSending)
		entered := isSending && !wasSending
		n := count[SendMessage](cmds)
		if !entered {
			if n != 0 {
				t.Errorf("%d SendMessage without entering NoteSending", n)
			}
			return
		}
		if n != 1 {
			t.Fatalf("%d SendMessage on entering NoteSending", n)
		}
		for _, c := range cmds {
			if m, ok := c.(SendMessage); ok && m.Batch != s.Batch {
				t.Errorf("SendMessage batch %s, state batch %s", m.Batch, s.Batch)
			}
		}
		if _, open := before.State.(NoteOpen); !open {
			t.Errorf("NoteSending entered from %T", before.State)
		}
	})
}

// Every change emits exactly one WriteAudit and one Publish; no change emits
// nothing. The audit never carries note text.
func TestPropAuditPerChange(t *testing.T) {
	prop(t, func(t *testing.T, before Note, in Input, after Note, cmds []Command) {
		if !changedNote(before, after) {
			if len(cmds) != 0 {
				t.Errorf("no change but %d commands", len(cmds))
			}
			return
		}
		if count[WriteAudit](cmds) != 1 || count[Publish](cmds) != 1 {
			t.Errorf("%T change emitted %d audits and %d publishes", in, count[WriteAudit](cmds), count[Publish](cmds))
		}
		for _, c := range cmds {
			if a, ok := c.(WriteAudit); ok && (strings.Contains(a.Action, before.Text) && before.Text != "") {
				t.Errorf("audit action carries note text")
			}
		}
	})
}

// A step never leaves an invalid note behind (a non-zero note validates), and
// Recover returns Sending to Open only after the timeout or at startup.
func TestPropStepKeepsNoteValid(t *testing.T) {
	prop(t, func(t *testing.T, before Note, in Input, after Note, cmds []Command) {
		if after.State != nil {
			if err := after.Validate(); err != nil {
				t.Errorf("%T produced an invalid note: %v", in, err)
			}
		}
		if rc, ok := in.(Recover); ok {
			if s, was := before.State.(NoteSending); was {
				_, nowOpen := after.State.(NoteOpen)
				due := rc.Startup || rc.Now.Sub(s.At) >= SendTimeout
				if nowOpen != due {
					t.Errorf("recover: open=%v, due=%v", nowOpen, due)
				}
			}
		}
	})
}

// StepBatch is all or nothing and emits one SendMessage for the batch.
func TestPropBatchAllOrNothing(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		var notes []Note
		for i := 0; i < 1+r.Intn(4); i++ {
			n := validNote(fmt.Sprintf("n%d", i))
			switch r.Intn(4) {
			case 0:
				n.State = NoteSent{Turn: "t"}
			case 1:
				n.State = NoteOutdated{}
			}
			notes = append(notes, n)
		}
		if r.Intn(5) == 0 {
			notes = append(notes, notes[0]) // duplicate
		}
		text := "ids"
		for _, n := range notes {
			text += " " + string(n.ID)
		}
		if r.Intn(5) == 0 {
			text = ""
		}
		out, cmds := StepBatch(notes, SendNotes{Batch: "b1", Text: text, Now: epoch})
		sending := 0
		for _, n := range out {
			if _, ok := n.State.(NoteSending); ok {
				sending++
			}
		}
		if sending != 0 && (sending != len(notes) || count[SendMessage](cmds) != 1 || count[WriteAudit](cmds) != len(notes) || count[Publish](cmds) != 1) {
			t.Fatalf("partial batch: %d/%d sending, %d messages", sending, len(notes), count[SendMessage](cmds))
		}
		if sending == 0 && (len(cmds) != 0 || !equalNotes(out, notes)) {
			t.Fatal("refused batch changed something")
		}
		ids := map[NoteID]bool{}
		for _, n := range notes {
			if ids[n.ID] && sending != 0 {
				t.Fatalf("batch with a repeated note %s was sent", n.ID)
			}
			ids[n.ID] = true
			if _, ok := n.State.(NoteOpen); !ok && sending != 0 {
				t.Fatalf("batch sent a %T note", n.State)
			}
		}
	})
}

func equalNotes(a, b []Note) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if changedNote(a[i], b[i]) {
			return false
		}
	}
	return true
}
