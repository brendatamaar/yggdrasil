package changesets_test

import (
	"errors"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/changesets/changesetstest"
	"github.com/brendatamaar/yggdrasil/internal/core"
	cs "github.com/brendatamaar/yggdrasil/internal/core/changesets"
)

func changesetsHash(n int) string { return changesetstest.Hash(n) }

func TestAddNoteRejectsARepoOutsideTheTask(t *testing.T) {
	r := newRig(t)
	other := raw("a.go", 1, "x")
	other.Repo = "rep_other"
	if _, err := r.svc.AddNote(ctx, task, other, "user"); userMessage(err) == "" {
		t.Fatalf("a note on a repo outside the task was accepted: %v", err)
	}
	// A task with no repos accepts no note at all.
	r.w.AddTask("tsk_empty")
	if _, err := r.svc.AddNote(ctx, "tsk_empty", raw("a.go", 1, "x"), "user"); userMessage(err) == "" {
		t.Fatalf("a note on a task without repos was accepted: %v", err)
	}
}

// Another writer bumps the note's stored version while the message is out:
// the second commit must use the version now stored, not version+1.
func TestSendRecordsItsResultWhenTheVersionMovedMeanwhile(t *testing.T) {
	r := newRig(t)
	a := r.addNote("a.go", 1, "first")
	r.msg.During = func() { r.w.BumpNoteVersion(string(a.ID)) }
	if err := r.svc.SendNotes(ctx, task, []string{string(a.ID)}, "user"); err != nil {
		t.Fatal(err)
	}
	if got := stateName(r.notes()[string(a.ID)]); got != "sent" {
		t.Fatalf("state %s, want sent", got)
	}
}

// The hash changed between the first pass and the resend: the run halts and
// nothing is confirmed.
func TestResendChecksTheHashTheUserSaw(t *testing.T) {
	r := newRig(t)
	ids, seen := threeProposals(r)
	r.api.Fail, r.api.FailTimes = errors.New("killed"), 1
	if _, err := r.confirmAll(seen); err != nil {
		t.Fatal(err)
	}
	r.w.SetHash(ids[0], changesetsHash(99))
	r.svc = r.newService()
	r.reconcile()
	if _, ok := openOf(r.view().State).LastRun.(cs.RunHalted); !ok {
		t.Fatalf("a changed hash did not halt the run: %#v", r.view().State)
	}
	if got := r.confirmedIDs(); len(got) != 0 {
		t.Fatalf("confirmed %v after the hash changed", got)
	}
}

// A late error after the proposal moved on must not halt the run.
func TestLateConfirmErrorDoesNotHaltWhenTheProposalMovedOn(t *testing.T) {
	r := newRig(t)
	ids, seen := threeProposals(r)
	r.api.Fail, r.api.FailTimes = errors.New("database is locked"), 5
	r.api.OnFail = func(id core.ProposalID, n int) {
		if n == 2 {
			r.w.SetStatus(id, core.ProposalConfirmed, "")
		}
	}
	if _, err := r.confirmAll(seen); err != nil {
		t.Fatal(err)
	}
	r.reconcile()
	if _, ok := r.view().State.(cs.Confirming); !ok {
		t.Fatalf("a late error halted the run: %#v", r.view().State)
	}
	if got := r.w.Status(ids[0]); got != core.ProposalConfirmed {
		t.Fatalf("status %s", got)
	}
}
