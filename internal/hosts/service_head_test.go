package hosts_test

import (
	"context"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	"github.com/brendatamaar/yggdrasil/internal/hosts"
	"github.com/brendatamaar/yggdrasil/internal/hosts/hoststest"
)

func cachedHead(t *testing.T, r *rig, id core.RepoID) string {
	t.Helper()
	idle, ok := r.repoView(t, id).Entry.Phase.(corehosts.Idle)
	if !ok {
		return ""
	}
	rs, ok := idle.Data.(corehosts.RepoState)
	if !ok || rs.MR == nil {
		return ""
	}
	return rs.MR.Head
}

// The cached MR carries the head the host reported (GH-05, AC-02), and a
// different head after a refresh replaces it, so a pin made earlier can be seen
// to be stale.
func TestServiceSurfacesAChangedMRHeadAfterRefresh(t *testing.T) {
	rem := remotes{"/w/shop-api": "git@git.acme.dev:acme-shop/shop-api.git"}
	r := newRig(t, gitlabEnv, rem)
	r.fake.Script(hoststest.OpenSuccess)
	r.svc.SetRepos([]hosts.RepoRef{{ID: "rep_a", Path: "/w/shop-api", Branch: "feature/cart"}})
	eventually(t, "first head", func() bool { return cachedHead(t, r, "rep_a") == hoststest.ExpectedHead })

	const newHead = "0123456789abcdef0123456789abcdef01234567"
	r.fake.SetRepoFn(func(_ context.Context, _ corehosts.Instance, _ string, _ corehosts.ProjectPath, _ string, _ string) (corehosts.RepoState, string, error) {
		m := hoststest.ExpectedMR(corehosts.MROpen)
		m.URL = "https://fake.example/acme-shop/shop-api/mr/42"
		m.Head = newHead
		return corehosts.RepoState{MR: &m}, `"v2"`, nil
	})
	if err := r.svc.Refresh(context.Background(), []corehosts.Key{corehosts.RepoKey{Repo: "rep_a"}}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "new head", func() bool { return cachedHead(t, r, "rep_a") == newHead })
}
