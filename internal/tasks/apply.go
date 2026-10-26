package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/audit"
	"github.com/brendatamaar/yggdrasil/internal/core"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// entry is the in-memory copy of one task between inputs. It is kept for a
// task that is not idle, so the part of the state that is not stored (the
// stop phase of a cancel during Starting, sub-agents, live usage) survives
// between inputs (tasks.md, Gate 1 round 3 notes). An idle task is reloaded.
type entry struct {
	mu    sync.Mutex
	valid bool
	st    db.TaskState
}

func (s *Service) entryFor(id core.TaskID) *entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[id]
	if !ok {
		e = &entry{}
		s.entries[id] = e
	}
	return e
}

// applyOpts are the extras of one apply.
type applyOpts struct {
	// actor of the audit entries: "user" or "system".
	actor string
	// create makes the task (as a draft) in the same transaction as the first
	// step. Used for a new task's first message.
	create *core.Task
	// extra audit entries (guard denies, integrity verdicts) committed with
	// the state change. They are committed even when the step is a no-op for
	// the task state.
	extra []auditSpec
	// finishUsage replaces the usage a FinishTurn carries (recovery recomputes
	// it from the stored events).
	finishUsage *coretasks.Usage
}

// auditSpec is an audit entry before it gets its ID and time.
type auditSpec struct {
	Action string
	Detail map[string]any
}

// applyResult is what an apply decided.
type applyResult struct {
	Before coretasks.Task
	After  coretasks.Task
	Row    core.Task
	Cmds   []coretasks.Command
	// Committed is true when the state was written (and not only held in
	// memory).
	Committed bool
	Reject    *coretasks.Reject
}

// apply runs one input through the core for one task: load (or take the
// cached copy), Step, persist state and audit in ONE Store.Commit with the
// expected version, and return the commands for the caller to interpret. It
// retries on a version conflict, reloading the task each time (the commit
// changed nothing, no command ran yet). mk builds the input from the loaded
// state; nil means nothing to do.
func (s *Service) apply(ctx context.Context, id core.TaskID, mk func(db.TaskState) coretasks.Input, o applyOpts) (applyResult, error) {
	if o.actor == "" {
		o.actor = "system"
	}
	e := s.entryFor(id)
	e.mu.Lock()
	defer e.mu.Unlock()

	creating := o.create != nil
	var lastErr error
	for try := 0; try < 3; try++ {
		if creating {
			e.st = db.TaskState{Row: *o.create, Task: coretasks.Task{ID: id, Mode: o.create.Mode, State: coretasks.Draft{}}}
			e.valid = false
		} else if !e.valid {
			st, err := s.c.Store.GetTaskState(ctx, id)
			if err != nil {
				return applyResult{}, err
			}
			e.st, e.valid = st, true
		}
		in := mk(e.st)
		if in == nil {
			return applyResult{Before: e.st.Task, After: e.st.Task, Row: e.st.Row}, nil
		}
		cur := e.st.Task
		cur.Caps = s.c.Caps // caps are not stored; the service's caps are the previous turn's
		next, cmds := coretasks.Step(cur, in)
		res := applyResult{Before: cur, After: next, Row: e.st.Row, Cmds: cmds}
		if rj := rejection(cmds); rj != nil {
			res.Reject, res.After, res.Cmds = rj, cur, nil
			return res, nil
		}
		if len(cmds) == 0 && len(o.extra) == 0 {
			// Nothing to persist (usage, sub-agents, stale or ignored
			// input): keep the new in-memory state.
			if !creating {
				e.st.Task = next
				e.st.Task.Caps = coretasks.Caps{}
			}
			res.After = next
			return res, nil
		}
		w, err := s.plan(e.st, next, cmds, o)
		if err != nil {
			return applyResult{}, err
		}
		var version int64
		err = s.c.Store.Commit(ctx, func(tx *db.Tx) error {
			expected := e.st.Row.Version
			if creating {
				created, err := tx.CreateTask(ctx, *o.create)
				if err != nil {
					return err
				}
				expected = created.Version
			}
			v, err := tx.SaveTask(ctx, id, w.taskRow, next.SessionID, expected, w.now)
			if err != nil {
				return err
			}
			version = v
			if w.turn != nil {
				if err := tx.SaveTurn(ctx, id, *w.turn, w.insertTurn); err != nil {
					return err
				}
			}
			for _, a := range w.audits {
				if err := tx.AppendAudit(ctx, a); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			if errors.Is(err, db.ErrConflict) && !creating {
				e.valid = false
				lastErr = err
				continue
			}
			e.valid = false
			return applyResult{}, fmt.Errorf("save task %s: %w", id, err)
		}
		// The new state is stored. Keep it for the next input.
		e.st.Row.Version = version
		e.st.Row.Status = next.State.Status()
		e.st.Task = next
		e.st.Task.Caps = coretasks.Caps{}
		if w.turn != nil {
			t := *w.turn
			t.Prompt = ""
			e.st.Turn = &t
		}
		if _, idle := next.State.(coretasks.Idle); idle {
			e.valid = false // an idle task is reloaded
		} else {
			e.valid = true
		}
		res.Committed = true
		res.Row = e.st.Row
		return res, nil
	}
	return applyResult{}, fmt.Errorf("the task %s kept changing while it was saved: %w", id, lastErr)
}

func rejection(cmds []coretasks.Command) *coretasks.Reject {
	for _, c := range cmds {
		if r, ok := c.(coretasks.Reject); ok {
			return &r
		}
	}
	return nil
}

// write is what one step stores.
type write struct {
	now        time.Time
	taskRow    coretasks.TaskRow
	turn       *coretasks.TurnRow
	insertTurn bool
	audits     []core.AuditEntry
}

// plan turns the next state and its commands into rows and audit entries.
// It checks that the rows it will write load back as a state (the same
// check a restart makes), so a bug here fails loudly and not at the next
// start.
func (s *Service) plan(prev db.TaskState, next coretasks.Task, cmds []coretasks.Command, o applyOpts) (write, error) {
	w := write{now: s.c.Now().UTC()}
	row, turn := coretasks.RowsOf(next.State)
	w.taskRow = row
	cur := prev.Turn
	var persist *coretasks.PersistTurn
	var finish *coretasks.FinishTurn
	for _, c := range cmds {
		switch v := c.(type) {
		case coretasks.PersistTurn:
			persist = &v
		case coretasks.FinishTurn:
			finish = &v
		}
	}
	switch st := next.State.(type) {
	case coretasks.Draft:
	case coretasks.Idle:
		if finish != nil {
			if cur == nil || cur.ID != finish.Turn {
				return w, fmt.Errorf("task %s finished turn %s but has no row for it", prev.Row.ID, finish.Turn)
			}
			t := *cur
			fin := st.At
			if fin.IsZero() {
				fin = w.now
			}
			u := finish.Usage
			if o.finishUsage != nil {
				u = *o.finishUsage
			}
			t.State, t.FinishedAt, t.Outcome = coretasks.TurnFinished, &fin, coretasks.OutcomeCode(st.Outcome)
			t.Attempt, t.Usage = finish.Attempt, u
			w.turn = &t
			w.taskRow.IdleAt = &fin
		}
	default:
		if turn == nil {
			return w, fmt.Errorf("task %s has no turn row for state %T", prev.Row.ID, st)
		}
		t := *turn
		switch {
		case persist != nil:
			t.Seq, t.Mode, t.Prompt = next.Turns, persist.Mode, persist.Prompt
			w.insertTurn = true
		case cur != nil && cur.ID == t.ID:
			t.Seq, t.Mode = cur.Seq, cur.Mode
		default:
			return w, fmt.Errorf("task %s has no stored turn %s", prev.Row.ID, t.ID)
		}
		w.turn = &t
	}
	if w.turn != nil {
		if _, err := coretasks.StateFromRows(w.taskRow, w.turn); err != nil {
			return w, fmt.Errorf("task %s: rows to write do not load back: %w", prev.Row.ID, err)
		}
	} else if _, ok := next.State.(coretasks.Idle); !ok {
		if _, err := coretasks.StateFromRows(w.taskRow, nil); err != nil {
			return w, fmt.Errorf("task %s: rows to write do not load back: %w", prev.Row.ID, err)
		}
	}
	for _, c := range cmds {
		a, ok := c.(coretasks.Audit)
		if !ok {
			continue
		}
		detail := map[string]any{"outcome": a.Entry.Outcome}
		if a.Entry.Turn != "" {
			detail["turn"] = string(a.Entry.Turn)
		}
		if persist != nil && a.Entry.Action == "task.turn" {
			detail["mode"] = string(persist.Mode)
			detail["promptChars"] = utf8.RuneCountInString(persist.Prompt) // never the prompt
		}
		e, err := s.auditEntry(o.actor, a.Entry.Action, prev.Row.ID, detail)
		if err != nil {
			return w, err
		}
		w.audits = append(w.audits, e)
	}
	for _, x := range o.extra {
		e, err := s.auditEntry("system", x.Action, prev.Row.ID, x.Detail)
		if err != nil {
			return w, err
		}
		w.audits = append(w.audits, e)
	}
	return w, nil
}

// auditEntry prepares an audit entry about a task: the ID and time are chosen
// here, in the shell, and Prepare redacts it (invariant 4).
func (s *Service) auditEntry(actor, action string, task core.TaskID, detail map[string]any) (core.AuditEntry, error) {
	raw := json.RawMessage("{}")
	if detail != nil {
		b, err := json.Marshal(detail)
		if err != nil {
			return core.AuditEntry{}, fmt.Errorf("encode audit detail: %w", err)
		}
		raw = b
	}
	e, err := audit.Prepare(core.AuditEntry{Actor: actor, Action: action, Subject: "task:" + string(task), Detail: raw},
		s.c.Redactor, core.AuditID(s.c.NewID("aud")), s.c.Now())
	if err != nil {
		return core.AuditEntry{}, fmt.Errorf("prepare audit entry %s: %w", action, err)
	}
	return e, nil
}

// clearTrust forgets the stored trust of a repo with its audit entry (SF-09:
// a scan that could not read the config completely, or a changed config).
func (s *Service) clearTrust(ctx context.Context, repo core.RepoID, outcome string) error {
	// The subject is the repo; the entry holds no path.
	raw, err := json.Marshal(map[string]any{"outcome": outcome})
	if err != nil {
		return fmt.Errorf("encode audit detail: %w", err)
	}
	e, err := audit.Prepare(core.AuditEntry{Actor: "system", Action: "guard.trust", Subject: "repo:" + string(repo), Detail: raw},
		s.c.Redactor, core.AuditID(s.c.NewID("aud")), s.c.Now())
	if err != nil {
		return err
	}
	return s.c.Store.Commit(ctx, func(tx *db.Tx) error {
		if err := tx.SetRepoTrust(ctx, repo, false, ""); err != nil {
			return err
		}
		return tx.AppendAudit(ctx, e)
	})
}

// legacyInterrupt marks the Phase 2 rows that claim to be queued or running
// (they have no turn) as interrupted, with one audit entry each, in one
// Commit. It runs once at startup, before anything is loaded.
func (s *Service) legacyInterrupt(ctx context.Context) (int, error) {
	n := 0
	err := s.c.Store.Commit(ctx, func(tx *db.Tx) error {
		n = 0
		legacy, err := tx.ListLegacyLiveTasks(ctx)
		if err != nil {
			return err
		}
		now := s.c.Now().UTC()
		for _, l := range legacy {
			if err := tx.MarkLegacyInterrupted(ctx, l, now); err != nil {
				return err
			}
			e, err := s.auditEntry("system", "task.interrupted", l.ID, map[string]any{"outcome": "failed_interrupted", "reason": "restart"})
			if err != nil {
				return err
			}
			if err := tx.AppendAudit(ctx, e); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}
