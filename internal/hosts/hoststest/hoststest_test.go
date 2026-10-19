package hoststest_test

import (
	"testing"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	"github.com/brendatamaar/yggdrasil/internal/hosts/hoststest"
)

// The fake passes the same contract as the live clients.
func TestFakeContract(t *testing.T) {
	hoststest.HostClientContract(t, func(t *testing.T) hoststest.Harness {
		f := &hoststest.Fake{}
		return hoststest.Harness{
			Client:   f,
			Instance: corehosts.Instance{ID: "git.acme.dev", Kind: corehosts.KindGitLab, Base: "https://git.acme.dev", Token: true},
			Token:    "tok-SENTINEL",
			Use:      func(_ *testing.T, s hoststest.Scenario) { f.Script(s) },
		}
	})
}
