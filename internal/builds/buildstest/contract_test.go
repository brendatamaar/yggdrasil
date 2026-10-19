package buildstest_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/builds/buildstest"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
)

func TestFakeStoreContract(t *testing.T) {
	buildstest.StoreContract(t, func(t *testing.T) buildstest.Harness {
		f := buildstest.NewFake(map[core.RepoID]core.ProjectID{"r1": "p1", "r2": "p1", "r3": "p2"})
		return buildstest.Harness{
			Store: f, Project: "p1", Repos: [2]core.RepoID{"r1", "r2"}, Other: "r3", Worktree: "wt1",
			AuditFor: func(_ context.Context, subject string) []core.AuditEntry {
				var out []core.AuditEntry
				for _, e := range f.Audit() {
					if e.Subject == subject {
						out = append(out, e)
					}
				}
				return out
			},
		}
	})
}

func TestSQLiteStoreContract(t *testing.T) {
	buildstest.StoreContract(t, func(t *testing.T) buildstest.Harness {
		ctx := context.Background()
		d, err := db.Open(ctx, filepath.Join(t.TempDir(), "ygg.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = d.Close() })
		s := db.NewStore(d, nil)
		p1 := dbtest.Project(t, s, core.Project{Name: "acme-shop", Path: t.TempDir()})
		p2 := dbtest.Project(t, s, core.Project{Name: "other", Path: t.TempDir()})
		r1 := dbtest.Repo(t, s, core.Repo{ProjectID: p1.ID, Name: "shop-api", Path: filepath.Join(p1.Path, "shop-api")})
		r2 := dbtest.Repo(t, s, core.Repo{ProjectID: p1.ID, Name: "shop-web", Path: filepath.Join(p1.Path, "shop-web")})
		r3 := dbtest.Repo(t, s, core.Repo{ProjectID: p2.ID, Name: "x", Path: filepath.Join(p2.Path, "x")})
		w := dbtest.Worktree(t, s, r1)
		return buildstest.Harness{
			Store: builds.NewSQLiteStore(s), Project: p1.ID, Repos: [2]core.RepoID{r1.ID, r2.ID}, Other: r3.ID, Worktree: w.ID,
			AuditFor: func(ctx context.Context, subject string) []core.AuditEntry {
				es, err := s.ListAudit(ctx, subject, 100)
				if err != nil {
					t.Fatal(err)
				}
				return es
			},
		}
	})
}
