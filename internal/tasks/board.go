package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/brendatamaar/yggdrasil/internal/audit"
	"github.com/brendatamaar/yggdrasil/internal/core"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// MaxAgents returns the maxAgents setting, or the default when it was never
// set. A stored value that does not parse is an error naming the setting.
func (s *Service) MaxAgents(ctx context.Context) (int, error) {
	set, err := s.c.Store.GetSetting(ctx, SettingMaxAgents)
	if errors.Is(err, db.ErrNotFound) {
		return s.c.DefaultMax, nil
	}
	if err != nil {
		return 0, err
	}
	n, err := coretasks.ParseMax(set.Value)
	if err != nil {
		return 0, fmt.Errorf("setting %s: %w", SettingMaxAgents, err)
	}
	return n, nil
}

// SetMaxAgents stores a new maxAgents (1..16) with its audit entry. Lowering it
// never stops a running task: it only limits new starts.
func (s *Service) SetMaxAgents(ctx context.Context, value string) error {
	n, err := coretasks.ParseMax(value)
	if err != nil {
		return &coretasks.UserError{Message: err.Error() + "."}
	}
	e, err := s.settingAudit(n)
	if err != nil {
		return err
	}
	ver := db.SettingNew
	if cur, err := s.c.Store.GetSetting(ctx, SettingMaxAgents); err == nil {
		ver = cur.Version
	}
	err = s.c.Store.Commit(ctx, func(tx *db.Tx) error {
		if _, err := tx.SetSetting(ctx, SettingMaxAgents, coretasks.FormatMax(n), ver); err != nil {
			return err
		}
		return tx.AppendAudit(ctx, e)
	})
	if err != nil {
		return fmt.Errorf("save maxAgents: %w", err)
	}
	s.boardInput(coretasks.MaxChanged{Max: n})
	s.wakeLoop()
	return nil
}

// settingAudit is the audit entry of a maxAgents change.
func (s *Service) settingAudit(n int) (core.AuditEntry, error) {
	raw, err := json.Marshal(map[string]any{"maxAgents": n})
	if err != nil {
		return core.AuditEntry{}, fmt.Errorf("encode audit detail: %w", err)
	}
	return audit.Prepare(core.AuditEntry{Actor: "user", Action: "settings.maxAgents", Subject: "settings:" + SettingMaxAgents, Detail: raw},
		s.c.Redactor, core.AuditID(s.c.NewID("aud")), s.c.Now())
}

// BoardCard is one card of the agent board.
type BoardCard struct {
	Task             core.Task
	Column           coretasks.BoardColumn
	PendingProposals int
	// BlockedBy is the plain-language reason a queued task waits, or "".
	BlockedBy string
	// Usage is the total over the task's turns (tokens, cost, duration).
	Usage coretasks.Usage
}

// BoardView is the read model of the agent board (AG-12): a card per task with
// its column, drafts left out, plus the numbers the settings show.
type BoardView struct {
	Cards  []BoardCard
	Max    int
	Active int
	// Hint suggests raising maxAgents when every slot is busy and the machine
	// has room (D7); RaiseTo is 0 for no suggestion.
	Hint coretasks.Hint
}

// Board builds the board for a project, or for every tracked project when
// project is empty. The column comes from the pure core.Column; drafts are not
// shown.
func (s *Service) Board(ctx context.Context, project core.ProjectID) (BoardView, error) {
	var projects []core.ProjectID
	if project != "" {
		if _, err := s.c.Store.GetProject(ctx, project); err != nil {
			return BoardView{}, err
		}
		projects = []core.ProjectID{project}
	} else {
		all, err := s.c.Store.ListProjects(ctx)
		if err != nil {
			return BoardView{}, err
		}
		for _, p := range all {
			projects = append(projects, p.ID)
		}
	}
	view := BoardView{Cards: []BoardCard{}}
	for _, p := range projects {
		list, err := s.c.Store.ListTasks(ctx, p)
		if err != nil {
			return BoardView{}, err
		}
		for _, t := range list {
			if t.Status == core.TaskDraft {
				continue
			}
			pending := 0
			if s.c.PendingProposals != nil {
				if pending, err = s.c.PendingProposals(ctx, t.ID); err != nil {
					return BoardView{}, fmt.Errorf("count proposals of %s: %w", t.ID, err)
				}
			}
			turns, err := s.c.Store.ListTaskTurns(ctx, t.ID)
			if err != nil {
				return BoardView{}, fmt.Errorf("read turns of %s: %w", t.ID, err)
			}
			var usage coretasks.Usage
			for _, tr := range turns {
				usage = addCoreUsage(usage, tr.Usage)
			}
			view.Cards = append(view.Cards, BoardCard{Task: t, Column: coretasks.Column(stateOfStatus(t.Status), pending), PendingProposals: pending, Usage: usage})
			if t.Status == core.TaskQueued {
				view.Cards[len(view.Cards)-1].BlockedBy = s.blockedReason(t.ID)
			}
		}
	}
	s.boardMu.Lock()
	view.Max, view.Active = s.board.Max, s.board.Active()
	s.boardMu.Unlock()
	view.Hint = coretasks.RoomHint(s.c.Machine.CPUs(), s.c.Machine.FreeMemMB(), view.Max, view.Active)
	return view, nil
}

// stateOfStatus is the coarse core state a stored status stands for; the
// column depends on nothing finer.
func stateOfStatus(st core.TaskStatus) coretasks.State {
	switch st {
	case core.TaskQueued:
		return coretasks.Waiting{}
	case core.TaskRunning:
		return coretasks.Running{}
	case core.TaskNeedsYou:
		return coretasks.Idle{Outcome: coretasks.NeedsYou{}}
	case core.TaskReview:
		return coretasks.Idle{Outcome: coretasks.ReadyForReview{}}
	case core.TaskFailed:
		return coretasks.Idle{Outcome: coretasks.Failed{}}
	case core.TaskCancelled:
		return coretasks.Idle{Outcome: coretasks.Cancelled{}}
	case core.TaskDone:
		return coretasks.Idle{Outcome: coretasks.Answered{}}
	}
	return coretasks.Draft{}
}
