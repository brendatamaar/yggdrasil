// Package dbtest has helpers that insert rows for other packages' tests.
// They write without an audit entry and fill in IDs and times, which
// production code must not do (invariant 8): import this package from _test.go
// files only. The archtest rule that bans un-audited writes outside
// internal/db allows it because it lives here.
package dbtest

import (
	"context"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// Project inserts p and fails the test on error.
func Project(t testing.TB, s *db.Store, p core.Project) core.Project {
	t.Helper()
	var out core.Project
	if p.ID == "" {
		p.ID = core.ProjectID(db.NewID("prj"))
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	commit(t, s, func(ctx context.Context, tx *db.Tx) (err error) { out, err = tx.CreateProject(ctx, p); return })
	return out
}

// Repo inserts r and fails the test on error.
func Repo(t testing.TB, s *db.Store, r core.Repo) core.Repo {
	t.Helper()
	var out core.Repo
	if r.ID == "" {
		r.ID = core.RepoID(db.NewID("rep"))
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	commit(t, s, func(ctx context.Context, tx *db.Tx) (err error) { out, err = tx.CreateRepo(ctx, r); return })
	return out
}

func commit(t testing.TB, s *db.Store, fn func(context.Context, *db.Tx) error) {
	t.Helper()
	ctx := context.Background()
	if err := s.Commit(ctx, func(tx *db.Tx) error { return fn(ctx, tx) }); err != nil {
		t.Fatal(err)
	}
}
