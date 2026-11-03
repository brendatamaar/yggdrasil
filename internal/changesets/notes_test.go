package changesets_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/changesets"
	"github.com/brendatamaar/yggdrasil/internal/changesets/changesetstest"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/review"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

var ctx = context.Background()

func raw(path string, line int, text string) review.RawNote {
	return review.RawNote{Repo: string(rep1), Path: path, Line: line, Side: "new", Text: text, DiffHash: changesetstest.Hash(5)}
}

func (r *rig) addNote(path string, line int, text string) review.Note {
	r.t.Helper()
	n, err := r.svc.AddNote(ctx, task, raw(path, line, text), "user")
	if err != nil {
		r.t.Fatal(err)
	}
	return n
}

func (r *rig) notes() map[string]review.Note {
	r.t.Helper()
	ns, err := r.svc.ListNotes(ctx, task)
	if err != nil {
		r.t.Fatal(err)
	}
	out := map[string]review.Note{}
	for _, n := range ns {
		out[string(n.ID)] = n
	}
	return out
}

func TestNoteLifecycleIsAudited(t *testing.T) {
	r := newRig(t)
	n := r.addNote("src/a.go", 3, "rename this\r\nplease")
	if n.Text != "rename this\nplease" {
		t.Fatalf("text %q", n.Text)
	}
	if _, err := r.svc.EditNote(ctx, task, string(n.ID), "rename it", "user"); err != nil {
		t.Fatal(err)
	}
	if got := r.notes()[string(n.ID)].Text; got != "rename it" {
		t.Fatalf("edited text %q", got)
	}
	if err := r.svc.DeleteNote(ctx, task, string(n.ID), "user"); err != nil {
		t.Fatal(err)
	}
	if len(r.notes()) != 0 {
		t.Fatal("note not deleted")
	}
	entries := r.w.Audit("note:" + string(n.ID))
	var actions []string
	for _, e := range entries {
		actions = append(actions, e.Action)
		if strings.Contains(string(e.Detail), "rename") {
			t.Fatalf("note text in the audit log: %s", e.Detail)
		}
	}
	if strings.Join(actions, ",") != "note.add,note.edit,note.delete" {
		t.Fatalf("audit actions %v", actions)
	}
}

func TestNoteInputIsRejectedAtTheBoundary(t *testing.T) {
	r := newRig(t)
	bad := []review.RawNote{
		raw("../secret", 1, "x"), raw("/etc/passwd", 1, "x"), raw("C:/x", 1, "x"), raw("a:stream", 1, "x"), raw("NUL", 1, "x"),
		raw("a.go", 0, "x"), raw("a.go", 1, ""), raw("a.go", 1, "bad\x00"), raw("a.go", 1, strings.Repeat("x", 2001)),
	}
	other := raw("a.go", 1, "x")
	other.Repo = "rep_other"
	noSide := raw("a.go", 1, "x")
	noSide.Side = "middle"
	noHash := raw("a.go", 1, "x")
	noHash.DiffHash = "abc"
	bad = append(bad, other, noSide, noHash)
	for i, b := range bad {
		if _, err := r.svc.AddNote(ctx, task, b, "user"); err == nil || userMessage(err) == "" {
			t.Errorf("bad note %d accepted or not explained: %v", i, err)
		}
	}
	if len(r.notes()) != 0 {
		t.Fatal("a rejected note was stored")
	}
}

func TestNoteCapPerTask(t *testing.T) {
	r := newRig(t)
	for i := 0; i < review.MaxNotesPerTask; i++ {
		if _, err := r.svc.AddNote(ctx, task, raw("a.go", i+1, "x"), "user"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.svc.AddNote(ctx, task, raw("a.go", 999, "x"), "user"); err == nil || userMessage(err) == "" {
		t.Fatalf("over the cap: %v", err)
	}
}

func TestSendNotesMarksThemSendingBeforeTheMessageAndSentAfter(t *testing.T) {
	r := newRig(t)
	a := r.addNote("a.go", 1, "first")
	b := r.addNote("b.go", 2, "second")
	var duringStates []string
	r.msg.During = func() {
		for _, n := range r.notes() {
			duringStates = append(duringStates, stateName(n))
		}
	}
	if err := r.svc.SendNotes(ctx, task, []string{string(a.ID), string(b.ID)}, "user", false); err != nil {
		t.Fatal(err)
	}
	if strings.Join(duringStates, ",") != "sending,sending" {
		t.Fatalf("while the message was sent: %v", duringStates)
	}
	msgs := r.msg.Messages()
	if len(msgs) != 1 || !strings.Contains(msgs[0].Text, string(a.ID)) || !strings.Contains(msgs[0].Text, string(b.ID)) || msgs[0].Task != task {
		t.Fatalf("messages %+v", msgs)
	}
	for _, n := range r.notes() {
		if s, ok := n.State.(review.NoteSent); !ok || s.Turn != "trn_1" {
			t.Fatalf("note state %#v", n.State)
		}
	}
	// A sent note is final.
	if err := r.svc.SendNotes(ctx, task, []string{string(a.ID)}, "user", false); err == nil {
		t.Fatal("a sent note was sent again")
	}
	if err := r.svc.DeleteNote(ctx, task, string(a.ID), "user"); err == nil {
		t.Fatal("a sent note was deleted")
	}
	if _, err := r.svc.EditNote(ctx, task, string(a.ID), "changed", "user"); err == nil {
		t.Fatal("a sent note was edited")
	}
	if len(r.msg.Messages()) != 1 {
		t.Fatal("a second message went out")
	}
}

func stateName(n review.Note) string {
	switch n.State.(type) {
	case review.NoteOpen:
		return "open"
	case review.NoteSending:
		return "sending"
	case review.NoteSent:
		return "sent"
	case review.NoteOutdated:
		return "outdated"
	}
	return "?"
}

type userMsgErr struct{}

func (userMsgErr) Error() string       { return "task busy" }
func (userMsgErr) UserMessage() string { return "The agent is busy. Send the notes when it finishes." }

func TestBusyAgentReturnsNotesToOpen(t *testing.T) {
	r := newRig(t)
	a := r.addNote("a.go", 1, "first")
	r.msg.Err = userMsgErr{}
	err := r.svc.SendNotes(ctx, task, []string{string(a.ID)}, "user", false)
	if userMessage(err) != "The agent is busy. Send the notes when it finishes." {
		t.Fatalf("error %v", err)
	}
	if stateName(r.notes()[string(a.ID)]) != "open" {
		t.Fatalf("state %s", stateName(r.notes()[string(a.ID)]))
	}
	r.msg.Err = fmt.Errorf("send: %w", changesets.ErrQueueFull)
	if err := r.svc.SendNotes(ctx, task, []string{string(a.ID)}, "user", false); userMessage(err) != "The agent is busy. Send the notes when it finishes." {
		t.Fatalf("a full queue is a busy agent: %v", err)
	}
	r.msg.Err = nil
	if err := r.svc.SendNotes(ctx, task, []string{string(a.ID)}, "user", false); err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, e := range r.w.Audit("note:" + string(a.ID)) {
		actions = append(actions, e.Action)
	}
	if strings.Join(actions, ",") != "note.add,note.send,note.send_refused,note.send,note.send_refused,note.send,note.sent" {
		t.Fatalf("audit %v", actions)
	}
}

func TestUnknownSendOutcomeLeavesNotesSending(t *testing.T) {
	r := newRig(t)
	a := r.addNote("a.go", 1, "first")
	r.msg.Err = errors.New("timeout waiting for the agent")
	err := r.svc.SendNotes(ctx, task, []string{string(a.ID)}, "user", false)
	if m := userMessage(err); !strings.Contains(m, "couldn't tell") || strings.Contains(strings.ToLower(m), "try again") {
		t.Fatalf("message %q", m)
	}
	if got := stateName(r.notes()[string(a.ID)]); got != "sending" {
		t.Fatalf("state %s, want sending (never invite a second send)", got)
	}
	r.msg.Err = nil
	if err := r.svc.SendNotes(ctx, task, []string{string(a.ID)}, "user", false); err == nil {
		t.Fatal("notes in Sending must not be sent again")
	}
	if len(r.msg.Messages()) != 0 {
		t.Fatalf("messages %v", r.msg.Messages())
	}
}

func TestCrashWhileSendingNeverSendsTwiceAndIsRecovered(t *testing.T) {
	r := newRig(t)
	a := r.addNote("a.go", 1, "first")
	// The process dies inside the call: the second commit never happens.
	r.msg.During = func() { r.w.FailCommits = 1 }
	err := r.svc.SendNotes(ctx, task, []string{string(a.ID)}, "user", false)
	if err != nil {
		t.Fatalf("the message was sent; the user must not be told it failed: %v", err)
	}
	if stateName(r.notes()[string(a.ID)]) != "sending" {
		t.Fatalf("state %s", stateName(r.notes()[string(a.ID)]))
	}
	// A note in Sending cannot be sent again.
	if err := r.svc.SendNotes(ctx, task, []string{string(a.ID)}, "user", false); err == nil || len(r.msg.Messages()) != 1 {
		t.Fatalf("sent a note that is being sent: %v", err)
	}
	// Reconcile before the timeout keeps it; the startup pass returns it.
	if err := r.svc.RecoverNotes(ctx, false); err != nil || stateName(r.notes()[string(a.ID)]) != "sending" {
		t.Fatalf("early recover: %v %s", err, stateName(r.notes()[string(a.ID)]))
	}
	r.advance(review.SendTimeout + time.Second)
	if err := r.svc.RecoverNotes(ctx, false); err != nil || stateName(r.notes()[string(a.ID)]) != "open" {
		t.Fatalf("timed recover: %v %s", err, stateName(r.notes()[string(a.ID)]))
	}
}

func TestStartupRecoverReturnsSendingNotesAtOnce(t *testing.T) {
	r := newRig(t)
	a := r.addNote("a.go", 1, "first")
	r.msg.During = func() { r.w.FailCommits = 1 }
	_ = r.svc.SendNotes(ctx, task, []string{string(a.ID)}, "user", false)
	r.svc = r.newService()
	if err := r.svc.RecoverNotes(ctx, true); err != nil {
		t.Fatal(err)
	}
	if stateName(r.notes()[string(a.ID)]) != "open" {
		t.Fatal("not recovered at startup")
	}
	found := false
	for _, e := range r.w.Audit("note:" + string(a.ID)) {
		found = found || e.Action == "note.recovered"
	}
	if !found {
		t.Fatal("recovery is not audited")
	}
}

func TestNotesGoStaleWhenTheFileChangesAndAreLeftAloneOnReadFailure(t *testing.T) {
	r := newRig(t)
	a := r.addNote("a.go", 1, "first")
	b := r.addNote("b.go", 1, "second")
	// The diff cannot be read: nothing is judged.
	if err := r.svc.ReconcileNotes(ctx); err != nil {
		t.Fatal(err)
	}
	for _, n := range r.notes() {
		if stateName(n) != "open" {
			t.Fatalf("a failed diff read changed %s", n.ID)
		}
	}
	// a.go still has the hash the note was written against; b.go changed.
	r.diffs.Set(task, rep1, map[review.RelPath]string{"a.go": changesetstest.Hash(5), "b.go": changesetstest.Hash(6)})
	if err := r.svc.ReconcileNotes(ctx); err != nil {
		t.Fatal(err)
	}
	ns := r.notes()
	if stateName(ns[string(a.ID)]) != "open" || stateName(ns[string(b.ID)]) != "outdated" {
		t.Fatalf("states %s %s", stateName(ns[string(a.ID)]), stateName(ns[string(b.ID)]))
	}
	// An outdated note is not sendable but can be deleted.
	if err := r.svc.SendNotes(ctx, task, []string{string(b.ID)}, "user", false); err == nil {
		t.Fatal("an outdated note was sent")
	}
	if err := r.svc.DeleteNote(ctx, task, string(b.ID), "user"); err != nil {
		t.Fatal(err)
	}
}

func TestNotesOfAnotherTaskAreNotFound(t *testing.T) {
	r := newRig(t)
	a := r.addNote("a.go", 1, "first")
	if err := r.svc.DeleteNote(ctx, "tsk_other", string(a.ID), "user"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("deleted through another task: %v", err)
	}
	if err := r.svc.SendNotes(ctx, "tsk_other", []string{string(a.ID)}, "user", false); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("sent through another task: %v", err)
	}
	if _, err := r.svc.ListNotes(ctx, core.TaskID("tsk_other")); err != nil {
		t.Fatal(err)
	}
}

func TestSendNotesCarriesTheOptIn(t *testing.T) {
	for _, optIn := range []bool{false, true} {
		r := newRig(t)
		a := r.addNote("a.go", 1, "first")
		if err := r.svc.SendNotes(ctx, task, []string{string(a.ID)}, "user", optIn); err != nil {
			t.Fatal(err)
		}
		if m := r.msg.Messages(); len(m) != 1 || m[0].OptIn != optIn {
			t.Fatalf("optIn=%v: messages %+v", optIn, m)
		}
		seen := 0
		for _, e := range r.w.Audit("note:" + string(a.ID)) {
			if e.Action != "note.send" {
				continue
			}
			var d map[string]any
			if err := json.Unmarshal(e.Detail, &d); err != nil {
				t.Fatal(err)
			}
			if got, ok := d["optIn"].(bool); !ok || got != optIn {
				t.Errorf("audit detail %s, want optIn=%v", e.Detail, optIn)
			}
			seen++
		}
		if seen != 1 {
			t.Errorf("%d note.send entries", seen)
		}
	}
}

type needsOptInErr struct{}

func (needsOptInErr) Error() string { return "rejected" }
func (needsOptInErr) Unwrap() error { return apperr.ErrNeedsOptIn }
func (needsOptInErr) UserMessage() string {
	return "This mode edits your main checkout directly. Turn on the opt-in to use it."
}

// Without the opt-in the notes return to Open and the error says why, with
// the mark the API turns into a 409 needs_opt_in. A second try with it works.
func TestSendNotesNeedsOptInReturnsNotesToOpen(t *testing.T) {
	r := newRig(t)
	a := r.addNote("a.go", 1, "first")
	r.msg.Err = needsOptInErr{}
	err := r.svc.SendNotes(ctx, task, []string{string(a.ID)}, "user", false)
	if !errors.Is(err, apperr.ErrNeedsOptIn) {
		t.Fatalf("err = %v", err)
	}
	if stateName(r.notes()[string(a.ID)]) != "open" {
		t.Fatalf("state %s", stateName(r.notes()[string(a.ID)]))
	}
	r.msg.Err = nil
	if err := r.svc.SendNotes(ctx, task, []string{string(a.ID)}, "user", true); err != nil {
		t.Fatal(err)
	}
}
