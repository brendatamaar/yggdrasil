package github_test

import (
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	"github.com/brendatamaar/yggdrasil/internal/hosts"
	"github.com/brendatamaar/yggdrasil/internal/hosts/github"
	"github.com/brendatamaar/yggdrasil/internal/hosts/hoststest"
)

func route(r *http.Request) string {
	p, q := r.URL.Path, r.URL.Query().Get("q")
	switch {
	case strings.HasSuffix(p, "/search/issues"):
		switch {
		case strings.Contains(q, "review-requested"):
			return "inbox_review"
		case strings.Contains(q, "assignee"):
			return "inbox_assigned"
		case strings.Contains(q, "author"):
			return "inbox_authored"
		}
	case strings.HasSuffix(p, "/pulls"):
		return "pulls"
	case strings.HasSuffix(p, "/check-runs"):
		return "checkruns"
	case strings.HasSuffix(p, "/status"):
		return "status"
	}
	return "unknown"
}

func TestClientContract(t *testing.T) {
	hoststest.HostClientContract(t, func(t *testing.T) hoststest.Harness {
		rp := hoststest.NewReplay(t, os.DirFS("testdata"), route)
		g := hosts.NewGetter(nil, func() time.Time { return hoststest.ContractNow })
		return hoststest.Harness{
			Client:   github.New(g),
			Instance: corehosts.Instance{ID: "git.acme.dev", Kind: corehosts.KindGitHub, Base: rp.Server.URL, Token: true},
			Token:    "github_pat_SENTINEL_0123456789",
			Use:      func(_ *testing.T, s hoststest.Scenario) { rp.Use(s) },
			Requests: rp.Requests,
		}
	})
}
