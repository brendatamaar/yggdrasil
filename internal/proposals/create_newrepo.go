package proposals

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/newrepos"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// Creation of the InitRepo and PublishRepo proposals (NR-01, NR-04). Ordinary
// proposal creation needs a ready, tracked repo (gatherFacts). An InitRepo is
// the one proposal that acts on a reserved repo, so it goes through the narrow
// resolver below, which is scoped by operation, setup task and repo: it sees
// only the reservation the proposal names, and checks that the proposal's
// immutable action is exactly the reserved spec.

// initFacts reads the facts of an InitRepo creation. A thing that is missing or
// does not belong together is a refusal (nothing is stored).
func (s *Service) initFacts(ctx context.Context, req CreateRequest, a coreprop.InitRepo) (coreprop.Facts, core.RepoID, *db.ReservationRecord, error) {
	bad := func(msg string) (coreprop.Facts, core.RepoID, *db.ReservationRecord, error) {
		return coreprop.Facts{}, "", nil, refusal(coreprop.RefuseBadFacts, msg)
	}
	if err := a.Validate(); err != nil {
		return bad("The new repo details are not valid. Check the folder name and the file contents.")
	}
	setup, err := s.cfg.Store.GetSetupTask(ctx, a.Repo)
	if errors.Is(err, db.ErrNotFound) {
		return bad("There is no new repo waiting for this proposal.")
	}
	if err != nil {
		return coreprop.Facts{}, "", nil, fmt.Errorf("read setup task of %s: %w", a.Repo, err)
	}
	if setup.ID != req.Task {
		return bad("That task does not own this new repo.")
	}
	rec, err := s.cfg.Store.GetReservation(ctx, a.Operation)
	if errors.Is(err, db.ErrNotFound) {
		return bad("There is no new repo waiting for this proposal.")
	}
	if err != nil {
		return coreprop.Facts{}, "", nil, fmt.Errorf("read reservation %s: %w", a.Operation, err)
	}
	spec := initSpecOf(a)
	switch {
	case rec.Operation.Repo != a.Repo || rec.Operation.Project != a.Project || rec.Leaf != a.Leaf || rec.SetupTask != req.Task:
		return bad("That proposal does not match the reserved new repo.")
	case rec.SpecHash != spec.Hash():
		return bad("The proposal differs from what was reserved. Start the new repo again.")
	}
	if _, reserved := rec.Operation.State.(newrepos.Reserved); !reserved || rec.Proposal != "" {
		return bad("A proposal for this new repo already exists.")
	}
	f := coreprop.Facts{RepoTracked: s.initTracked(ctx, a), PathsOK: true, RemoteSupported: true}
	return f, a.Repo, &rec, nil
}

// checkPublishCreate refuses a publish proposal for an operation that is not
// ready to publish: the repo must have been initialized and not be published.
func (s *Service) checkPublishCreate(ctx context.Context, a coreprop.PublishRepo) error {
	rec, err := s.cfg.Store.GetReservation(ctx, a.Operation)
	if errors.Is(err, db.ErrNotFound) {
		return refusal(coreprop.RefuseBadFacts, "There is no new repo for this publish.")
	}
	if err != nil {
		return fmt.Errorf("read reservation %s: %w", a.Operation, err)
	}
	if rec.Operation.Repo != a.Repo {
		return refusal(coreprop.RefuseBadFacts, "That publish names another repo than the new repo it belongs to.")
	}
	if coreprop.PlanNewRepo(core.ProposalPublish, operationView(rec.Operation.State)) != coreprop.PlanNewRepoStart {
		return refusal(coreprop.RefuseBadFacts, "The repo is not ready to publish. Finish creating it first, and a repo that is published or being published cannot be published again.")
	}
	return nil
}

// linkReservation moves the reserved operation to AwaitingConfirm with the new
// proposal, in the transaction that creates the proposal, so a proposal never
// exists without its operation knowing it (and the reverse).
func (s *Service) linkReservation(ctx context.Context, tx *db.Tx, rec db.ReservationRecord, p coreprop.Proposal, now time.Time) error {
	next, cmds := newrepos.Step(rec.Operation, newrepos.ProposalCreated{ID: p.ID, Hash: rec.SpecHash})
	if len(cmds) == 0 {
		return errRefused{refusal(coreprop.RefuseBadFacts, "A proposal for this new repo already exists.")}
	}
	if _, err := tx.SaveReservation(ctx, rec.Operation.ID, next.State, p.ID, rec.Operation.Version, now); err != nil {
		if errors.Is(err, db.ErrConflict) {
			return errRefused{refusal(coreprop.RefuseBadFacts, "The new repo changed while this proposal was made. Try again.")}
		}
		return fmt.Errorf("link reservation %s: %w", rec.Operation.ID, err)
	}
	return s.stageEntry(ctx, tx, p, 0, stageNewRepoLinked, now)
}
