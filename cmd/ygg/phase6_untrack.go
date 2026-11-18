package main

import (
	"context"
	"errors"
	"log/slog"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/newrepos"
)

// untrackingNewRepos is the api.TrackingService that also tells the new-repo
// service when a project or repo is untracked (NR-02): the operation stops, its
// worker is cancelled and an effect that was running is left Unknown (never
// failed, never rolled back); folders and files already created stay on disk.
type untrackingNewRepos struct {
	api.TrackingService
	nr interface {
		Untracked(context.Context, core.OperationID) error
	}
	store interface {
		ListReservations(ctx context.Context, project core.ProjectID) ([]db.ReservationRecord, error)
		GetReservationByRepo(ctx context.Context, repo core.RepoID) (db.ReservationRecord, error)
	}
	log *slog.Logger
}

var _ api.TrackingService = untrackingNewRepos{}

func (u untrackingNewRepos) stop(ctx context.Context, recs []db.ReservationRecord) {
	for _, r := range recs {
		if err := u.nr.Untracked(ctx, r.Operation.ID); err != nil && !errors.Is(err, newrepos.ErrNotApplicable) {
			u.log.Warn("a new repo was untracked but its work was not stopped", "operation", r.Operation.ID, "err", err)
		}
	}
}

func (u untrackingNewRepos) UntrackProject(ctx context.Context, project core.ProjectID, actor corerepos.Actor) (corerepos.Outcome, error) {
	out, err := u.TrackingService.UntrackProject(ctx, project, actor)
	if err != nil {
		return out, err
	}
	recs, lerr := u.store.ListReservations(ctx, project)
	if lerr != nil {
		u.log.Warn("could not look for the new repos of an untracked project", "project", project, "err", lerr)
		return out, nil
	}
	u.stop(ctx, recs)
	return out, nil
}

func (u untrackingNewRepos) UntrackRepo(ctx context.Context, repo core.RepoID, actor corerepos.Actor) (corerepos.Outcome, error) {
	out, err := u.TrackingService.UntrackRepo(ctx, repo, actor)
	if err != nil {
		return out, err
	}
	if rec, gerr := u.store.GetReservationByRepo(ctx, repo); gerr == nil {
		u.stop(ctx, []db.ReservationRecord{rec})
	}
	return out, nil
}
