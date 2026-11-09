package worktreeenv_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	env "github.com/brendatamaar/yggdrasil/internal/core/worktreeenv"
	"github.com/brendatamaar/yggdrasil/internal/worktreeenv"
	"github.com/brendatamaar/yggdrasil/internal/worktreeenv/worktreeenvtest"
)

func TestComposeStoreRoundTripKeepsProfile(t *testing.T) {
	check := func(t *testing.T, st worktreeenv.ComposeStore, repo core.RepoID, putProfile func()) {
		ctx := context.Background()
		if got, err := st.ComposeSettings(ctx, repo); err != nil || got != (worktreeenv.ComposeSettings{}) {
			t.Fatalf("defaults: %+v %v", got, err)
		}
		want := worktreeenv.ComposeSettings{Enabled: true, File: "deploy/compose.yml", ConfirmedHash: "abc"}
		e := core.AuditEntry{ID: "aud_c1", At: time.Now().UTC(), Actor: "user", Action: "worktreeenv.compose_settings", Subject: "repo:" + string(repo)}
		if err := st.PutComposeSettings(ctx, repo, want, time.Now().UTC(), e); err != nil {
			t.Fatal(err)
		}
		if got, err := st.ComposeSettings(ctx, repo); err != nil || got != want {
			t.Fatalf("got %+v %v", got, err)
		}
		putProfile()
		if got, _ := st.ComposeSettings(ctx, repo); got != want {
			t.Fatalf("a profile change lost the compose settings: %+v", got)
		}
		if _, err := st.ComposeSettings(ctx, "rep_unknown"); !errors.Is(err, worktreeenv.ErrNotFound) {
			t.Fatalf("unknown repo: %v", err)
		}
	}
	t.Run("fake", func(t *testing.T) {
		s := worktreeenvtest.NewStore("rep_1")
		check(t, s, "rep_1", func() {})
	})
	t.Run("sqlite", func(t *testing.T) {
		s := newDBStore(t)
		repo, _ := seedRows(t, s)
		st := worktreeenv.DBStore{S: s}
		check(t, st, repo, func() {
			e := core.AuditEntry{ID: "aud_p1", At: time.Now().UTC(), Actor: "user", Action: "worktreeenv.profile_settings", Subject: "repo:" + string(repo)}
			if err := st.PutRepoEnv(context.Background(), repo, worktreeenv.RepoEnv{Profile: []env.RelPath{".env"}}, time.Now().UTC(), e); err != nil {
				t.Fatal(err)
			}
		})
	})
}
