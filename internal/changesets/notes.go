package changesets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/review"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// noteChange is one note's part of a transaction: the row as loaded (for the
// version check), what it becomes, and the audit entries.
type noteChange struct {
	before  *db.NoteRow // nil: a new note
	after   review.Note
	deleted bool
	audits  []review.WriteAudit
}

// commitNotes writes the changed notes and their audit entries in one
// transaction. A note that moved since it was loaded is a conflict.
func (s *Service) commitNotes(ctx context.Context, actor string, changes []noteChange) error {
	return s.d.Store.Commit(ctx, func(tx Tx) error {
		for _, c := range changes {
			row := RowOfNote(c.after)
			switch {
			case c.before == nil:
				if err := tx.InsertNote(ctx, row); err != nil {
					return err
				}
			case c.deleted:
				if err := s.checkNoteVersion(ctx, tx, c.before); err != nil {
					return err
				}
				if err := tx.DeleteNote(ctx, c.before.ID); err != nil {
					return err
				}
			default:
				if err := s.checkNoteVersion(ctx, tx, c.before); err != nil {
					return err
				}
				if _, err := tx.UpdateNote(ctx, row, c.before.Version); err != nil {
					return err
				}
			}
			if err := s.auditNotes(ctx, tx, actor, c.after, c.audits); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Service) checkNoteVersion(ctx context.Context, tx Tx, before *db.NoteRow) error {
	cur, err := tx.LoadNote(ctx, before.ID)
	if err != nil {
		return err
	}
	if cur.Version != before.Version {
		return fmt.Errorf("note %s moved to version %d: %w", before.ID, cur.Version, db.ErrConflict)
	}
	return nil
}

func auditsOf(cmds []review.Command) []review.WriteAudit {
	var out []review.WriteAudit
	for _, c := range cmds {
		if w, ok := c.(review.WriteAudit); ok {
			out = append(out, w)
		}
	}
	return out
}

func (s *Service) publishNotes(task core.TaskID) {
	if s.d.Hub != nil {
		s.d.Hub.PublishNotes(task)
	}
}

// loadNote reads and parses one note of a task.
func (s *Service) loadNote(ctx context.Context, task core.TaskID, id string) (db.NoteRow, review.Note, error) {
	nid := review.NoteID(id)
	if err := nid.Validate(); err != nil {
		return db.NoteRow{}, review.Note{}, fmt.Errorf("note %s: %w", id, db.ErrNotFound)
	}
	row, err := s.d.Store.Note(ctx, id)
	if err != nil {
		return db.NoteRow{}, review.Note{}, err
	}
	if row.Task != task {
		return db.NoteRow{}, review.Note{}, fmt.Errorf("note %s: %w", id, db.ErrNotFound)
	}
	n, err := NoteFromRow(row)
	if err != nil {
		return db.NoteRow{}, review.Note{}, err
	}
	return row, n, nil
}

// ListNotes returns the notes of a task, oldest first. A row that cannot be
// read is logged and left out.
func (s *Service) ListNotes(ctx context.Context, task core.TaskID) ([]review.Note, error) {
	rows, err := s.d.Store.TaskNotes(ctx, task)
	if err != nil {
		return nil, err
	}
	out := make([]review.Note, 0, len(rows))
	for _, r := range rows {
		n, err := NoteFromRow(r)
		if err != nil {
			s.d.Log.Warn("a review note could not be read", "note", r.ID, "err", err)
			continue
		}
		out = append(out, n)
	}
	return out, nil
}

// AddNote stores a note on a line of a task's diff. raw comes from
// DecodeNote; the ID and the task are set here.
func (s *Service) AddNote(ctx context.Context, task core.TaskID, raw review.RawNote, actor string) (review.Note, error) {
	s.nmu.Lock()
	defer s.nmu.Unlock()
	if err := task.Validate(); err != nil {
		return review.Note{}, fmt.Errorf("task %s: %w", task, db.ErrNotFound)
	}
	raw.ID, raw.Task = s.d.IDs("nte"), string(task)
	note, err := review.ParseNote(raw, s.now())
	if err != nil {
		return review.Note{}, userErr(err)
	}
	repos, err := s.d.Store.TaskRepos(ctx, task)
	if err != nil {
		return review.Note{}, err
	}
	if !slices.Contains(repos, note.Repo) {
		return review.Note{}, &UserError{Message: "That repo isn't part of this task."}
	}
	var added review.Note
	err = s.d.Store.Commit(ctx, func(tx Tx) error {
		count, err := tx.CountOpenNotes(ctx, task)
		if err != nil {
			return err
		}
		var cmds []review.Command
		added, cmds = review.StepNote(review.Note{}, review.AddNote{Note: note, OpenCount: count, Now: s.now()})
		if len(cmds) == 0 {
			return &UserError{Message: fmt.Sprintf("This task has %d notes waiting. Send or delete some first.", review.MaxNotesPerTask)}
		}
		if err := tx.InsertNote(ctx, RowOfNote(added)); err != nil {
			return err
		}
		return s.auditNotes(ctx, tx, actor, added, auditsOf(cmds))
	})
	if err != nil {
		return review.Note{}, err
	}
	s.publishNotes(task)
	return added, nil
}

// auditNotes writes the audit entries of one note inside tx.
func (s *Service) auditNotes(ctx context.Context, tx Tx, actor string, n review.Note, ws []review.WriteAudit) error {
	now := s.now()
	for _, w := range ws {
		detail, err := json.Marshal(map[string]string{"task": string(n.Task), "repo": string(n.Repo), "outcome": w.Outcome})
		if err != nil {
			return fmt.Errorf("audit detail: %w", err)
		}
		e := core.AuditEntry{Actor: actor, Action: w.Action, Subject: "note:" + string(w.Note), Detail: detail}
		if err := tx.WriteAudit(ctx, e, now); err != nil {
			return err
		}
	}
	return nil
}

// stepOne loads one note, applies an input and commits the result.
func (s *Service) stepOne(ctx context.Context, task core.TaskID, id, actor string, in review.Input) (review.Note, bool, error) {
	for try := 0; try < maxAttempts; try++ {
		row, n, err := s.loadNote(ctx, task, id)
		if err != nil {
			return review.Note{}, false, err
		}
		after, cmds := review.StepNote(n, in)
		if len(cmds) == 0 {
			return n, false, nil
		}
		del := slices.ContainsFunc(cmds, func(c review.Command) bool { _, ok := c.(review.RemoveNote); return ok })
		err = s.commitNotes(ctx, actor, []noteChange{{before: &row, after: noteForAudit(n, after, del), deleted: del, audits: auditsOf(cmds)}})
		if isConflict(err) {
			continue
		}
		if err != nil {
			return n, false, err
		}
		s.publishNotes(task)
		return after, true, nil
	}
	return review.Note{}, false, fmt.Errorf("note %s changed while it was being updated: %w", id, db.ErrConflict)
}

// noteForAudit: a deleted note steps to the zero Note; the audit still needs
// its task and repo.
func noteForAudit(before, after review.Note, deleted bool) review.Note {
	if deleted {
		return before
	}
	return after
}

// EditNote changes the text of an open note.
func (s *Service) EditNote(ctx context.Context, task core.TaskID, id, text, actor string) (review.Note, error) {
	s.nmu.Lock()
	defer s.nmu.Unlock()
	if err := review.ValidText(text); err != nil {
		return review.Note{}, userErr(err)
	}
	n, changed, err := s.stepOne(ctx, task, id, actor, review.EditNote{Text: normalizeText(text), Now: s.now()})
	if err == nil && !changed {
		if _, open := n.State.(review.NoteOpen); !open {
			return n, &UserError{Message: "Only a note that hasn't been sent can be edited."}
		}
	}
	return n, err
}

// DeleteNote removes an open or outdated note. A sent note is history.
func (s *Service) DeleteNote(ctx context.Context, task core.TaskID, id, actor string) error {
	s.nmu.Lock()
	defer s.nmu.Unlock()
	_, changed, err := s.stepOne(ctx, task, id, actor, review.DeleteNote{Now: s.now()})
	if err == nil && !changed {
		return &UserError{Message: "Only a note that hasn't been sent can be deleted."}
	}
	return err
}

// SendNotes composes the notes into one message, marks them as being sent
// (committed first, so a crash can lose a send but never repeat one silently)
// and hands the message to the task's agent. A refusal returns the notes to
// open and says why.
func (s *Service) SendNotes(ctx context.Context, task core.TaskID, ids []string, actor string) error {
	s.nmu.Lock()
	defer s.nmu.Unlock()
	nids, err := ParseNoteIDs(ids)
	if err != nil {
		return userErr(err)
	}
	rows := make([]db.NoteRow, len(nids))
	notes := make([]review.Note, len(nids))
	for i, id := range nids {
		if rows[i], notes[i], err = s.loadNote(ctx, task, string(id)); err != nil {
			return err
		}
	}
	batch := review.BatchID(s.d.IDs("bat"))
	text, err := review.ComposeNotes(batch, notes)
	if err != nil {
		return userErr(err)
	}
	now := s.now()
	sending, cmds := review.StepBatch(notes, review.SendNotes{Batch: batch, Text: text, Now: now})
	if len(cmds) == 0 {
		return &UserError{Message: "These notes can't be sent. Reload and try again."}
	}
	changes := make([]noteChange, len(sending))
	for i := range sending {
		changes[i] = noteChange{
			before: &rows[i], after: sending[i],
			audits: []review.WriteAudit{{Action: "note.send", Outcome: "ok", Note: sending[i].ID}},
		}
	}
	if err := s.commitNotes(ctx, actor, changes); err != nil {
		return err
	}
	s.publishNotes(task)

	// The notes are marked Sending in the database. From here a failure leaves
	// them for Recover, never for a silent second send.
	turn, sendErr := s.sendMessage(ctx, task, text)
	// Reload the versions the first commit wrote rather than assuming them.
	for i := range changes {
		cur, err := s.d.Store.Note(ctx, string(rows[i].ID))
		if err != nil {
			s.d.Log.Error("could not reload notes after marking them sending", "task", task, "batch", batch, "err", err)
			if sendErr == nil {
				return nil // sent; the notes stay Sending until recovery
			}
			return sendRefusal(sendErr)
		}
		changes[i].before = &cur
	}
	var in review.Input
	if sendErr == nil {
		in = review.SendAccepted{Batch: batch, Turn: review.TurnID(turn), Now: s.now()}
	} else {
		in = review.SendRefused{Batch: batch, Reason: "refused", Now: s.now()}
	}
	if sendErr == nil && review.TurnID(turn).Validate() != nil {
		// Accepted, but the turn ID is unusable: record the notes as sent
		// under an unknown turn rather than offering them for a second send.
		in = review.SendAccepted{Batch: batch, Turn: "unknown", Now: s.now()}
	}
	for i := range sending {
		after, out := review.StepNote(sending[i], in)
		if len(out) == 0 {
			continue
		}
		changes[i] = noteChange{before: changes[i].before, after: after, audits: auditsOf(out)}
	}
	if err := s.commitNotes(ctx, actor, changes); err != nil {
		if ctx.Err() == nil {
			s.d.Log.Error("could not record the result of sending notes", "task", task, "batch", batch, "err", err)
		}
		if sendErr == nil {
			return nil // sent; the notes stay Sending until recovery
		}
	}
	s.publishNotes(task)
	if sendErr != nil {
		return sendRefusal(sendErr)
	}
	return nil
}

func (s *Service) sendMessage(ctx context.Context, task core.TaskID, text string) (turn string, err error) {
	defer func() {
		if r := recover(); r != nil {
			turn, err = "", fmt.Errorf("sending notes panicked: %v", r)
		}
	}()
	return s.d.Messenger.SendMessage(ctx, task, text)
}

func sendRefusal(err error) error {
	var u interface{ UserMessage() string }
	if errors.As(err, &u) && u.UserMessage() != "" {
		return &UserError{Message: u.UserMessage()}
	}
	if errors.Is(err, ErrAgentBusy) || errors.Is(err, ErrQueueFull) {
		return &UserError{Message: "The agent is busy. Send the notes when it finishes."}
	}
	return apperr.New(apperr.Unavailable, "Yggdrasil couldn't send the notes to the agent. Your notes are still here; try again in a moment.", err)
}

func normalizeText(t string) string {
	return strings.ReplaceAll(t, "\r\n", "\n")
}

// RecoverNotes returns notes found in Sending to Open: all of them at startup
// (the process that was sending is gone), later only those that have been
// sending for review.SendTimeout.
func (s *Service) RecoverNotes(ctx context.Context, startup bool) error {
	s.nmu.Lock()
	defer s.nmu.Unlock()
	rows, err := s.d.Store.NotesInState(ctx, "sending")
	if err != nil {
		return err
	}
	var errs []error
	for _, r := range rows {
		if _, _, err := s.stepOne(ctx, r.Task, r.ID, systemActor, review.Recover{Startup: startup, Now: s.now()}); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ReconcileNotes returns timed-out sends to Open and marks open notes whose
// file changed as outdated. It skips a repo whose diff could not be read: an
// empty answer would mark every note outdated.
func (s *Service) ReconcileNotes(ctx context.Context) error {
	if err := s.RecoverNotes(ctx, false); err != nil {
		s.d.Log.Warn("recover review notes failed", "err", err)
	}
	if s.d.Diffs == nil {
		return nil
	}
	s.nmu.Lock()
	defer s.nmu.Unlock()
	tasks, err := s.d.Store.TasksWithNotesInState(ctx, "open")
	if err != nil {
		return err
	}
	var errs []error
	for _, task := range tasks {
		rows, err := s.d.Store.TaskNotes(ctx, task)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		byRepo := map[core.RepoID][]db.NoteRow{}
		for _, r := range rows {
			if r.State == "open" {
				byRepo[r.Repo] = append(byRepo[r.Repo], r)
			}
		}
		for repo, open := range byRepo {
			hashes, err := s.d.Diffs.FileHashes(ctx, task, repo)
			if err != nil {
				s.d.Log.Debug("could not read a diff to check review notes", "task", task, "repo", repo, "err", err)
				continue
			}
			for _, r := range open {
				if _, _, err := s.stepOne(ctx, task, r.ID, systemActor, review.DiffObserved{Hashes: hashes, Now: s.now()}); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	return errors.Join(errs...)
}
