package worktreeenv_test

import (
	"context"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	env "github.com/brendatamaar/yggdrasil/internal/core/worktreeenv"
	"github.com/brendatamaar/yggdrasil/internal/worktreeenv"
	"github.com/brendatamaar/yggdrasil/internal/worktreeenv/worktreeenvtest"
)

func TestOSComposeFilesContract(t *testing.T) {
	worktreeenvtest.ComposeFilesContract(t, func(t *testing.T) worktreeenvtest.ComposeFilesEnv {
		return worktreeenvtest.OSComposeFilesEnv(t, worktreeenv.OSComposeFiles{})
	})
}

func TestFakeComposeFilesContract(t *testing.T) {
	worktreeenvtest.ComposeFilesContract(t, func(t *testing.T) worktreeenvtest.ComposeFilesEnv {
		f := &worktreeenvtest.ComposeFiles{}
		return worktreeenvtest.ComposeFilesEnv{
			Files: f, Root: "/wt",
			Put: func(rel env.RelPath, data []byte) { f.Put("/wt", rel, string(data)) },
		}
	})
}

func TestFakeComposeStoreContract(t *testing.T) {
	worktreeenvtest.ComposeStoreContract(t, func(t *testing.T) worktreeenvtest.ComposeStoreEnv {
		s := worktreeenvtest.NewStore("rep_1")
		return worktreeenvtest.ComposeStoreEnv{Store: s, Repo: "rep_1"}
	})
}

func TestSQLiteComposeStoreContract(t *testing.T) {
	worktreeenvtest.ComposeStoreContract(t, func(t *testing.T) worktreeenvtest.ComposeStoreEnv {
		s := newDBStore(t)
		repo, _ := seedRows(t, s)
		st := worktreeenv.DBStore{S: s}
		return worktreeenvtest.ComposeStoreEnv{
			Store: st, Repo: repo,
			PutProfile: func() {
				e := core.AuditEntry{ID: "aud_p1", At: nowUTC(), Actor: "user", Action: "worktreeenv.profile_settings", Subject: "repo:" + string(repo)}
				if err := st.PutRepoEnv(context.Background(), repo, worktreeenv.RepoEnv{Profile: []env.RelPath{".env"}}, nowUTC(), e); err != nil {
					t.Fatal(err)
				}
			},
			Audits: func(subject string) int {
				got, err := s.ListAudit(context.Background(), subject, 100)
				if err != nil {
					t.Fatal(err)
				}
				return len(got)
			},
		}
	})
}

func nowUTC() time.Time { return time.Now().UTC() }
