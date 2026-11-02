package changesetstest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/changesets"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// APIHarness is a changesets.ProposalsAPI under test with a way to make
// pending proposals and read their status.
type APIHarness struct {
	API changesets.ProposalsAPI
	// NewPending creates a pending proposal and returns its ID and action hash.
	NewPending func(t *testing.T) (core.ProposalID, string)
	// Status reads the proposal's status now.
	Status func(t *testing.T, id core.ProposalID) core.ProposalStatus
	// Settle waits until a confirmed proposal reached a terminal status
	// (executed or failed) and returns it.
	Settle func(t *testing.T, id core.ProposalID) core.ProposalStatus
}

// ProposalsAPIContract is the suite every changesets.ProposalsAPI must pass:
// a wrong hash is refused and changes nothing; a right hash confirms; a second
// confirmation of a decided proposal is refused as already decided; an unknown
// proposal is refused; a confirmed proposal reaches a terminal status.
func ProposalsAPIContract(t *testing.T, mk func(t *testing.T) APIHarness) {
	ctx := context.Background()
	refused := func(t *testing.T, err error) *changesets.Refused {
		t.Helper()
		var r *changesets.Refused
		if !errors.As(err, &r) {
			t.Fatalf("want *Refused, got %T %v", err, err)
		}
		if r.Message == "" {
			t.Fatal("a refusal has no message for the user")
		}
		return r
	}
	t.Run("a wrong hash is refused and nothing changes", func(t *testing.T) {
		h := mk(t)
		id, _ := h.NewPending(t)
		refused(t, h.API.Confirm(ctx, id, Hash(999), "user:test"))
		if got := h.Status(t, id); got != core.ProposalPending {
			t.Fatalf("status after a refusal: %s", got)
		}
	})
	t.Run("the right hash confirms", func(t *testing.T) {
		h := mk(t)
		id, hash := h.NewPending(t)
		if err := h.API.Confirm(ctx, id, hash, "user:test"); err != nil {
			t.Fatal(err)
		}
		if got := h.Status(t, id); got == core.ProposalPending || got == core.ProposalRejected {
			t.Fatalf("status after a confirmation: %s", got)
		}
	})
	t.Run("a second confirmation is refused as already decided", func(t *testing.T) {
		h := mk(t)
		id, hash := h.NewPending(t)
		if err := h.API.Confirm(ctx, id, hash, "user:test"); err != nil {
			t.Fatal(err)
		}
		if r := refused(t, h.API.Confirm(ctx, id, hash, "user:test")); !r.AlreadyDecided {
			t.Fatal("the refusal doesn't say the proposal was already decided")
		}
	})
	t.Run("an unknown proposal is refused", func(t *testing.T) {
		h := mk(t)
		refused(t, h.API.Confirm(ctx, "prp_does_not_exist", Hash(1), "user:test"))
	})
	t.Run("a confirmed proposal reaches a terminal status", func(t *testing.T) {
		h := mk(t)
		id, hash := h.NewPending(t)
		if err := h.API.Confirm(ctx, id, hash, "user:test"); err != nil {
			t.Fatal(err)
		}
		if got := h.Settle(t, id); got != core.ProposalExecuted && got != core.ProposalFailed {
			t.Fatalf("settled at %s", got)
		}
	})
}

// StoreHarness is a changesets.Store under test with seeded data: Set holds
// two proposals of Task (Members, in the repos Repos); OtherSet holds one of
// OtherTask.
type StoreHarness struct {
	Store     changesets.Store
	Task      core.TaskID
	OtherTask core.TaskID
	Repos     [2]core.RepoID
	Set       core.ChangeSetID
	OtherSet  core.ChangeSetID
	Members   [2]core.ProposalID
	Other     core.ProposalID
	// NoteRepo is a repo of Task (notes need an existing task and repo).
	// Audit returns the audit entries of a subject.
	Audit func(subject string) []core.AuditEntry
}

func noteRow(h StoreHarness, id, state string) db.NoteRow {
	n := db.NoteRow{
		ID: id, Task: h.Task, Repo: h.Repos[0], Path: "src/a.go", Line: 3, Side: "new", Text: "rename this",
		DiffHash: Hash(7), State: state, CreatedAt: time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC),
	}
	if state != "open" {
		at := time.Date(2026, 11, 1, 10, 5, 0, 0, time.UTC)
		n.StateAt = &at
	}
	if state == "sending" {
		n.Batch = "bat_1"
	}
	if state == "sent" {
		n.Turn = "trn_1"
	}
	return n
}

// ChangeSetStoreContract is the suite every changesets.Store must pass:
// reads, a version conflict, state and audit written in the same transaction
// (and rolled back together), and note storage rules.
func ChangeSetStoreContract(t *testing.T, mk func(t *testing.T) StoreHarness) {
	ctx := context.Background()
	t.Run("a set is read with version 0 and no run; unknown is not found", func(t *testing.T) {
		h := mk(t)
		row, err := h.Store.Set(ctx, h.Set)
		if err != nil {
			t.Fatal(err)
		}
		if row.Version != 0 || row.Status != core.ChangeSetOpen || row.Task != h.Task {
			t.Fatalf("row %+v", row)
		}
		if _, err := h.Store.Set(ctx, "cs_missing"); !errors.Is(err, db.ErrNotFound) {
			t.Fatalf("unknown set: %v", err)
		}
	})
	t.Run("members are only those of the set", func(t *testing.T) {
		h := mk(t)
		ms, err := h.Store.Members(ctx, h.Set)
		if err != nil || len(ms) != 2 {
			t.Fatalf("members %v %v", ms, err)
		}
		for _, m := range ms {
			if m.ID != h.Members[0] && m.ID != h.Members[1] {
				t.Fatalf("member %s of another set", m.ID)
			}
			if len(m.Hash) != 64 || m.Status != core.ProposalPending {
				t.Fatalf("member %+v", m)
			}
		}
		other, _ := h.Store.Members(ctx, h.OtherSet)
		if len(other) != 1 || other[0].ID != h.Other {
			t.Fatalf("other set members %v", other)
		}
		repos, err := h.Store.TaskRepos(ctx, h.Task)
		if err != nil || len(repos) == 0 {
			t.Fatalf("task repos %v %v", repos, err)
		}
	})
	t.Run("a set is found from its proposal and from its task", func(t *testing.T) {
		h := mk(t)
		row, err := h.Store.SetOfProposal(ctx, h.Members[1])
		if err != nil || row.ID != h.Set {
			t.Fatalf("set of proposal: %v %v", row.ID, err)
		}
		if _, err := h.Store.SetOfProposal(ctx, "prp_missing"); !errors.Is(err, db.ErrNotFound) {
			t.Fatalf("unknown proposal: %v", err)
		}
		id, err := h.Store.SetOfTask(ctx, h.Task)
		if err != nil || id != h.Set {
			t.Fatalf("set of task: %v %v", id, err)
		}
	})
	t.Run("a save at a stale version is a conflict", func(t *testing.T) {
		h := mk(t)
		row, _ := h.Store.Set(ctx, h.Set)
		row.Status, row.MergeOrder = core.ChangeSetShipped, []core.RepoID{h.Repos[1], h.Repos[0]}
		row.RunResult = []byte(`{"kind":"completed"}`)
		save := func(expected int64) error {
			return h.Store.Commit(ctx, func(tx changesets.Tx) error { _, err := tx.SaveSet(ctx, row, expected); return err })
		}
		if err := save(0); err != nil {
			t.Fatal(err)
		}
		if err := save(0); !errors.Is(err, db.ErrConflict) {
			t.Fatalf("stale save: %v", err)
		}
		got, _ := h.Store.Set(ctx, h.Set)
		if got.Version != 1 || got.Status != core.ChangeSetShipped || len(got.MergeOrder) != 2 || got.MergeOrder[0] != h.Repos[1] {
			t.Fatalf("stored %+v", got)
		}
	})
	t.Run("state and audit commit together and roll back together", func(t *testing.T) {
		h := mk(t)
		row, _ := h.Store.Set(ctx, h.Set)
		row.Status = core.ChangeSetClosed
		entry := core.AuditEntry{Actor: "user", Action: "changeset.close", Subject: "changeset:" + string(h.Set), Detail: []byte(`{"outcome":"ok"}`)}
		boom := errors.New("boom")
		err := h.Store.Commit(ctx, func(tx changesets.Tx) error {
			if _, err := tx.SaveSet(ctx, row, 0); err != nil {
				return err
			}
			if err := tx.AppendAudit(ctx, entry, time.Now()); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("commit error: %v", err)
		}
		got, _ := h.Store.Set(ctx, h.Set)
		if got.Version != 0 || got.Status != core.ChangeSetOpen || len(h.Audit(entry.Subject)) != 0 {
			t.Fatalf("a failed commit left state %+v or %d audit entries", got, len(h.Audit(entry.Subject)))
		}
		err = h.Store.Commit(ctx, func(tx changesets.Tx) error {
			if _, err := tx.SaveSet(ctx, row, 0); err != nil {
				return err
			}
			return tx.AppendAudit(ctx, entry, time.Now())
		})
		if err != nil {
			t.Fatal(err)
		}
		got, _ = h.Store.Set(ctx, h.Set)
		if got.Status != core.ChangeSetClosed || len(h.Audit(entry.Subject)) != 1 {
			t.Fatalf("after commit: %+v, %d audit entries", got, len(h.Audit(entry.Subject)))
		}
	})
	t.Run("an audit entry with no subject is refused", func(t *testing.T) {
		h := mk(t)
		err := h.Store.Commit(ctx, func(tx changesets.Tx) error {
			return tx.AppendAudit(ctx, core.AuditEntry{Actor: "user", Action: "x"}, time.Now())
		})
		if err == nil {
			t.Fatal("accepted")
		}
	})
	t.Run("notes: insert, read, conflict, update by version", func(t *testing.T) {
		h := mk(t)
		if err := h.Store.Commit(ctx, func(tx changesets.Tx) error { return tx.InsertNote(ctx, noteRow(h, "nte_1", "open")) }); err != nil {
			t.Fatal(err)
		}
		err := h.Store.Commit(ctx, func(tx changesets.Tx) error { return tx.InsertNote(ctx, noteRow(h, "nte_1", "open")) })
		if !errors.Is(err, db.ErrConflict) {
			t.Fatalf("duplicate insert: %v", err)
		}
		n, err := h.Store.Note(ctx, "nte_1")
		if err != nil || n.State != "open" || n.Version != 0 || n.Text != "rename this" {
			t.Fatalf("note %+v %v", n, err)
		}
		upd := noteRow(h, "nte_1", "sending")
		upd.Version = 0
		if err := h.Store.Commit(ctx, func(tx changesets.Tx) error { _, err := tx.UpdateNote(ctx, upd, 0); return err }); err != nil {
			t.Fatal(err)
		}
		err = h.Store.Commit(ctx, func(tx changesets.Tx) error { _, err := tx.UpdateNote(ctx, upd, 0); return err })
		if !errors.Is(err, db.ErrConflict) {
			t.Fatalf("stale update: %v", err)
		}
		n, _ = h.Store.Note(ctx, "nte_1")
		if n.State != "sending" || n.Batch != "bat_1" || n.Version != 1 || n.StateAt == nil {
			t.Fatalf("after update %+v", n)
		}
		if _, err := h.Store.Note(ctx, "nte_missing"); !errors.Is(err, db.ErrNotFound) {
			t.Fatalf("missing note: %v", err)
		}
	})
	t.Run("notes: only open and outdated notes are deleted", func(t *testing.T) {
		h := mk(t)
		for i, st := range []string{"open", "outdated", "sending", "sent"} {
			id := "nte_d" + string(rune('a'+i))
			if err := h.Store.Commit(ctx, func(tx changesets.Tx) error { return tx.InsertNote(ctx, noteRow(h, id, st)) }); err != nil {
				t.Fatal(err)
			}
			err := h.Store.Commit(ctx, func(tx changesets.Tx) error { return tx.DeleteNote(ctx, id) })
			deletable := st == "open" || st == "outdated"
			if deletable && err != nil {
				t.Fatalf("delete %s note: %v", st, err)
			}
			if !deletable && !errors.Is(err, db.ErrNotFound) {
				t.Fatalf("delete %s note: %v (history must stay)", st, err)
			}
		}
	})
	t.Run("notes in sending are listed for recovery; open ones are counted", func(t *testing.T) {
		h := mk(t)
		for i, st := range []string{"open", "outdated", "sending", "sent"} {
			id := "nte_l" + string(rune('a'+i))
			if err := h.Store.Commit(ctx, func(tx changesets.Tx) error { return tx.InsertNote(ctx, noteRow(h, id, st)) }); err != nil {
				t.Fatal(err)
			}
		}
		sending, err := h.Store.NotesInState(ctx, "sending")
		if err != nil || len(sending) != 1 || sending[0].ID != "nte_lc" {
			t.Fatalf("sending notes %v %v", sending, err)
		}
		var count int
		_ = h.Store.Commit(ctx, func(tx changesets.Tx) error { count, err = tx.CountOpenNotes(ctx, h.Task); return err })
		if err != nil || count != 2 {
			t.Fatalf("open+outdated count %d %v", count, err)
		}
		tasks, err := h.Store.TasksWithNotesInState(ctx, "open")
		if err != nil || len(tasks) != 1 || tasks[0] != h.Task {
			t.Fatalf("tasks with open notes %v %v", tasks, err)
		}
		all, _ := h.Store.TaskNotes(ctx, h.Task)
		if len(all) != 4 {
			t.Fatalf("task notes %d", len(all))
		}
		none, _ := h.Store.TaskNotes(ctx, h.OtherTask)
		if len(none) != 0 {
			t.Fatal("notes of another task listed")
		}
	})
}
