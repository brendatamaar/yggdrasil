package gitlab_test

import (
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	"github.com/brendatamaar/yggdrasil/internal/hosts"
	"github.com/brendatamaar/yggdrasil/internal/hosts/gitlab"
	"github.com/brendatamaar/yggdrasil/internal/hosts/hoststest"
)

func route(r *http.Request) string {
	p, q := r.URL.Path, r.URL.Query()
	switch {
	case strings.HasSuffix(p, "/user"):
		return "user"
	case strings.HasSuffix(p, "/pipelines"):
		return "pipelines"
	case strings.Contains(p, "/projects/") && strings.HasSuffix(p, "/merge_requests"):
		return "mrs"
	case q.Get("reviewer_username") != "":
		return "inbox_review"
	case q.Get("scope") == "assigned_to_me":
		return "inbox_assigned"
	case q.Get("scope") == "created_by_me":
		return "inbox_authored"
	}
	return "unknown"
}

func TestClientContract(t *testing.T) {
	hoststest.HostClientContract(t, func(t *testing.T) hoststest.Harness {
		rp := hoststest.NewReplay(t, os.DirFS("testdata"), route)
		g := hosts.NewGetter(nil, func() time.Time { return hoststest.ContractNow })
		return hoststest.Harness{
			Client:   gitlab.New(g),
			Instance: corehosts.Instance{ID: "git.acme.dev", Kind: corehosts.KindGitLab, Base: rp.Server.URL, Token: true},
			Token:    "glpat-SENTINEL-0123456789",
			Use:      func(_ *testing.T, s hoststest.Scenario) { rp.Use(s) },
			Requests: rp.Requests,
		}
	})
}
