package gitea_test

import (
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	"github.com/brendatamaar/yggdrasil/internal/hosts"
	"github.com/brendatamaar/yggdrasil/internal/hosts/gitea"
	"github.com/brendatamaar/yggdrasil/internal/hosts/hoststest"
)

func route(r *http.Request) string {
	p, q := r.URL.Path, r.URL.Query()
	switch {
	case strings.HasSuffix(p, "/repos/issues/search"):
		switch {
		case q.Get("review_requested") != "":
			return "inbox_review"
		case q.Get("assigned") != "":
			return "inbox_assigned"
		case q.Get("created") != "":
			return "inbox_authored"
		}
	case strings.HasSuffix(p, "/pulls"):
		return "pulls"
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
			Client:   gitea.New(g),
			Instance: corehosts.Instance{ID: "code.example.org", Kind: corehosts.KindGitea, Base: rp.Server.URL, Token: true},
			Token:    "gitea-SENTINEL-0123456789",
			Use:      func(_ *testing.T, s hoststest.Scenario) { rp.Use(s) },
			Requests: rp.Requests,
		}
	})
}
