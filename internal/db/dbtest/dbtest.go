// Package dbtest has helpers that insert rows for other packages' tests.
// They write without an audit entry and fill in IDs and times, which
// production code must not do (invariant 8): import this package from _test.go
// files only. The archtest rule that bans un-audited writes outside
// internal/db allows it because it lives here.
package dbtest

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// Project inserts p and fails the test on error.
func Project(t testing.TB, s *db.Store, p core.Project) core.Project {
	t.Helper()
	if p.ID == "" {
		p.ID = core.ProjectID(db.NewID("prj"))
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	commit(t, s, func(ctx context.Context, tx *db.Tx) error {
		return tx.Workspace().InsertProject(ctx, corerepos.InsertProject{Project: corerepos.Project{
			ID: p.ID, Name: p.Name, Path: corerepos.CleanPath(p.Path), Life: corerepos.Active{Since: p.CreatedAt},
		}})
	})
	out, err := s.GetProject(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Repo inserts r and fails the test on error. The repo's folder is its folder
// under the project when r.Path lies there, else a folder named after the repo
// under the project's folder; the stored repo says which.
func Repo(t testing.TB, s *db.Store, r core.Repo) core.Repo {
	t.Helper()
	ctx := context.Background()
	if r.ID == "" {
		r.ID = core.RepoID(db.NewID("rep"))
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	ws, err := s.GetWorkspace(ctx)
	if err != nil {
		t.Fatal(err)
	}
	proj, ok := ws.Projects[r.ProjectID]
	if !ok {
		t.Fatalf("dbtest.Repo: unknown project %s", r.ProjectID)
	}
	rel, err := filepath.Rel(string(proj.Path), r.Path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		rel = r.Name
	}
	rp, err := corerepos.NewRelPath(filepath.ToSlash(rel))
	if err != nil {
		t.Fatalf("dbtest.Repo: %v", err)
	}
	commit(t, s, func(ctx context.Context, tx *db.Tx) error {
		return tx.Workspace().InsertRepo(ctx, corerepos.InsertRepo{
			ProjectID: r.ProjectID, ExpectVersion: proj.Version,
			Repo: corerepos.Repo{ID: r.ID, Name: r.Name, RelPath: rp, Life: corerepos.Active{Since: r.CreatedAt}, Trusted: r.Trusted},
		})
	})
	out, err := s.GetRepo(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func commit(t testing.TB, s *db.Store, fn func(context.Context, *db.Tx) error) {
	t.Helper()
	ctx := context.Background()
	if err := s.Commit(ctx, func(tx *db.Tx) error { return fn(ctx, tx) }); err != nil {
		t.Fatal(err)
	}
}
