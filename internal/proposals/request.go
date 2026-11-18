package proposals

import (
	"context"
	"errors"
	"fmt"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// Request-bound creation for the workspace-action service (AC-03, AC-04,
// WS-09). The method set below is what the composition root adapts to
// actions.ProposalCreator:
//
//	CreateForRequest(ctx, core.RequestID, []RequestProposal) ([]core.ProposalID, error)
//	FindForRequest(ctx, core.RequestID) ([]core.ProposalID, error)
//
// Creation makes PENDING proposals only: it asks for no confirmation and runs
// nothing (invariant 2). The decision (create, reuse or refuse a changed list)
// is coreprop.PlanRequest; this file reads what exists, runs the plan in one
// transaction with the audit entries, and returns the IDs.

// RequestProposal is one proposal to create for a request; the fields mean the
// same as actions.ProposalSpec. Kind and Payload are the stored form of the
// action (coreprop.RowOf).
type RequestProposal struct {
	Task    core.TaskID
	Repo    core.RepoID
	Summary string
	Kind    core.ProposalKind
	Payload []byte
}

// ErrRequestChanged: the request already created proposals and this call names
// different ones. Nothing was created.
var ErrRequestChanged = errors.New("this request already created different proposals")

// RequestChangedError is ErrRequestChanged for one request, with a plain
// message.
type RequestChangedError struct{ Request core.RequestID }

func (e *RequestChangedError) Error() string {
	return "request " + string(e.Request) + ": " + ErrRequestChanged.Error()
}
func (e *RequestChangedError) Is(target error) bool { return target == ErrRequestChanged }

// UserMessage is the text to show.
func (e *RequestChangedError) UserMessage() string {
	return "This request already created proposals, and the list now differs. Start the action again to get a new request."
}

type requestItems struct {
	items []coreprop.RequestItem
}

// parseRequest turns the boundary values into core items. A disabled or
// unknown kind, an invalid payload or an over-long list is refused here.
func (s *Service) parseRequest(items []RequestProposal) (requestItems, error) {
	if len(items) == 0 {
		return requestItems{}, refusal(coreprop.RefuseBadFacts, "There is nothing to propose.")
	}
	if len(items) > coreprop.MaxRequestProposals {
		return requestItems{}, refusal(coreprop.RefuseTooMany, "This request would create too many proposals at once. Split it into smaller steps.")
	}
	out := make([]coreprop.RequestItem, len(items))
	for i, it := range items {
		if !coreprop.Enabled(it.Kind) {
			return requestItems{}, refusal(coreprop.RefuseUnsupported, "")
		}
		a, err := coreprop.ActionFromRow(it.Kind, it.Payload)
		if err != nil {
			return requestItems{}, refusal(coreprop.RefuseBadFacts, "A proposal in this request is not valid.")
		}
		switch a.(type) {
		case coreprop.InitRepo, coreprop.PublishRepo:
			// These link a reservation and start from the new-repo dialog.
			return requestItems{}, refusal(coreprop.RefuseUnsupported, "Creating and publishing a repo are started from the new repo dialog, not from an action.")
		}
		out[i] = coreprop.RequestItem{Task: it.Task, Repo: it.Repo, Summary: clip(s.redact(it.Summary), maxSummary), Action: a}
	}
	return requestItems{items: out}, nil
}

// storedItems reads the proposals already created for the request: the IDs
// RequestProposalID(req, 0..) in order up to the first missing one, at most
// limit of them. It writes nothing.
func (s *Service) storedItems(ctx context.Context, req core.RequestID, limit int) ([]core.ProposalID, []coreprop.RequestItem, error) {
	var ids []core.ProposalID
	var items []coreprop.RequestItem
	for i := 0; i < limit; i++ {
		id := coreprop.RequestProposalID(req, i)
		rec, err := s.cfg.Store.GetProposalRecord(ctx, id)
		if errors.Is(err, db.ErrNotFound) {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("read proposal %s of request %s: %w", id, req, err)
		}
		ids = append(ids, id)
		items = append(items, coreprop.RequestItem{
			Task: rec.Proposal.Task, Repo: rec.Proposal.Repo, Summary: rec.Proposal.Summary, Action: rec.Proposal.Action,
		})
	}
	return ids, items, nil
}

// FindForRequest returns the proposals a request created, in item order, or
// none. It is read-only.
func (s *Service) FindForRequest(ctx context.Context, req core.RequestID) ([]core.ProposalID, error) {
	if err := req.Validate(); err != nil {
		return nil, refusal(coreprop.RefuseBadFacts, "The request is not valid.")
	}
	ids, _, err := s.storedItems(ctx, req, coreprop.MaxRequestProposals)
	return ids, err
}

// CreateForRequest creates the pending proposals of a request, once. A retry
// with the same items returns the same IDs and creates nothing; different
// items under the same request return ErrRequestChanged. All proposals and
// their audit entries are written in one transaction, so a crash leaves all of
// them or none, and a retry completes the request with the same IDs.
func (s *Service) CreateForRequest(ctx context.Context, req core.RequestID, items []RequestProposal) ([]core.ProposalID, error) {
	if err := req.Validate(); err != nil {
		return nil, refusal(coreprop.RefuseBadFacts, "The request is not valid.")
	}
	parsed, err := s.parseRequest(items)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		_, existing, err := s.storedItems(ctx, req, len(items)+1)
		if err != nil {
			return nil, err
		}
		switch plan := coreprop.PlanRequest(req, parsed.items, existing).(type) {
		case coreprop.ReuseAll:
			return plan.IDs, nil
		case coreprop.RefuseRequest:
			switch plan.Reason {
			case coreprop.RequestChanged:
				return nil, &RequestChangedError{Request: req}
			case coreprop.RequestTooMany:
				return nil, refusal(coreprop.RefuseTooMany, "This request would create too many proposals at once. Split it into smaller steps.")
			}
			return nil, refusal(coreprop.RefuseBadFacts, "The request is not valid.")
		case coreprop.CreateAll:
			ids, err := s.createAll(ctx, req, parsed.items, plan.IDs)
			if err == nil {
				return ids, nil
			}
			var ref *Refusal
			var er errRefused
			if errors.As(err, &er) || errors.As(err, &ref) || ctx.Err() != nil {
				return nil, err
			}
			// A concurrent call may have created the rows first: look again.
			lastErr = err
		}
	}
	return nil, fmt.Errorf("create proposals for request %s: %w", req, lastErr)
}

// createAll gathers the facts of every item (scope, paths, pins), then stores
// all proposals and audit entries in one transaction.
func (s *Service) createAll(ctx context.Context, req core.RequestID, items []coreprop.RequestItem, ids []core.ProposalID) ([]core.ProposalID, error) {
	type prepared struct{ n newProposal }
	prep := make([]prepared, len(items))
	for i, it := range items {
		n, err := s.prepare(ctx, CreateRequest{Task: it.Task, Action: it.Action, Summary: it.Summary, Source: coreprop.UserSource{}})
		if err != nil {
			return nil, err
		}
		if n.Repo != it.Repo {
			return nil, refusal(coreprop.RefuseBadFacts, "A proposal names a repo its action does not act on.")
		}
		n.ID = ids[i]
		prep[i] = prepared{n: n}
	}
	now := s.now()
	created := make([]coreprop.Proposal, len(prep))
	cmds := make([][]coreprop.Command, len(prep))
	err := s.cfg.Store.Commit(ctx, func(tx *db.Tx) error {
		for i := range prep {
			n := prep[i].n
			n.Now = now
			var err error
			if created[i], cmds[i], err = s.storePending(ctx, tx, n, req); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		var er errRefused
		if errors.As(err, &er) {
			return nil, er.r
		}
		return nil, err
	}
	for i := range created {
		s.publishAll(created[i], cmds[i])
	}
	return ids, nil
}
