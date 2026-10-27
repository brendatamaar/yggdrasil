package proposals

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/audit"
	"github.com/brendatamaar/yggdrasil/internal/core"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// writeAudit appends the audit entry of each Audit command in the transaction
// of the state it belongs to (invariant 8). by is the user who decided, "" for
// system steps. The detail holds the kind, task, repo, attempt and outcome:
// never a path, a message or a token.
func (s *Service) writeAudit(ctx context.Context, tx *db.Tx, p coreprop.Proposal, cmds []coreprop.Command, by string, now time.Time) error {
	for _, c := range cmds {
		a, ok := c.(coreprop.Audit)
		if !ok {
			continue
		}
		actor := "system"
		switch a.Entry.Action {
		case "proposal.create":
			actor = "user"
			if _, agent := p.Source.(coreprop.AgentSource); agent {
				actor = "agent"
			}
		case "proposal.confirm", "proposal.reject", "proposal.retry":
			actor = "user"
			if by != "" && by != "user" {
				actor = "user:" + by
			}
		}
		attempt := 0
		switch st := p.State.(type) {
		case coreprop.Confirmed:
			attempt = st.Attempt
		case coreprop.Failed:
			attempt = st.Attempt
		}
		detail, err := json.Marshal(map[string]any{
			"kind": a.Entry.Kind, "task": p.Task, "repo": p.Repo, "attempt": attempt, "outcome": a.Entry.Outcome,
		})
		if err != nil {
			return fmt.Errorf("audit detail: %w", err)
		}
		e, err := audit.Prepare(core.AuditEntry{
			Actor: actor, Action: a.Entry.Action, Subject: "proposal:" + string(a.Entry.Proposal), Detail: detail,
		}, s.cfg.Redactor, core.AuditID(s.cfg.NewID("aud")), now)
		if err != nil {
			return fmt.Errorf("prepare audit entry: %w", err)
		}
		if err := tx.AppendAudit(ctx, e); err != nil {
			return err
		}
	}
	return nil
}

// publishAll interprets the Publish commands.
func (s *Service) publishAll(p coreprop.Proposal, cmds []coreprop.Command) {
	if s.cfg.Publisher == nil {
		return
	}
	for _, c := range cmds {
		if pub, ok := c.(coreprop.Publish); ok {
			s.cfg.Publisher.Publish(Event{ID: p.ID, Task: p.Task, Repo: p.Repo, Status: pub.Status})
		}
	}
}
