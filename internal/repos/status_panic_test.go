package repos

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
)

type emptySource struct{}

func (emptySource) ListProjects(context.Context) ([]core.Project, error)           { return nil, nil }
func (emptySource) ListRepos(context.Context, core.ProjectID) ([]core.Repo, error) { return nil, nil }

// A panic in the core triggers a restart; a second panic inside the restart
// must not crash the process.
func TestStatusStepSurvivesPanicInRestart(t *testing.T) {
	old := refreshCore
	t.Cleanup(func() { refreshCore = old })
	refreshCore = func(corerepos.Refresher, corerepos.RefreshInput) (corerepos.Refresher, []corerepos.RefreshCommand) {
		panic("broken invariant")
	}
	s := NewStatusService(emptySource{}, nil, func() time.Time { return time.Unix(0, 0).UTC() }, nil,
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	state := corerepos.NewRefresher(2)
	state.Seq = 7
	var g errgroup.Group
	next := s.step(context.Background(), &g, state, corerepos.Tick{Now: time.Unix(0, 0).UTC()})
	if next.Seq != 7 || next.Max != 2 || next.Entries == nil || next.InFlight == nil || next.Orphans == nil {
		t.Errorf("state after two panics = %+v, want an empty state that keeps Seq and Max", next)
	}
	_ = g.Wait()
}
