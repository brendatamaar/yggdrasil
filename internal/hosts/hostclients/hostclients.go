// Package hostclients builds the set of live HostClients for the hosts
// service. It exists so that internal/hosts does not import its own client
// packages.
package hostclients

import (
	"net/http"
	"time"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	"github.com/brendatamaar/yggdrasil/internal/hosts"
	"github.com/brendatamaar/yggdrasil/internal/hosts/gitea"
	"github.com/brendatamaar/yggdrasil/internal/hosts/github"
	"github.com/brendatamaar/yggdrasil/internal/hosts/gitlab"
)

// New returns one client per host kind. Every request goes through a
// hosts.Getter, which can only send GET. base may be nil.
func New(base *http.Client, now func() time.Time) map[corehosts.Kind]hosts.HostClient {
	g := hosts.NewGetter(base, now)
	return map[corehosts.Kind]hosts.HostClient{
		corehosts.KindGitLab: gitlab.New(g),
		corehosts.KindGitHub: github.New(g),
		corehosts.KindGitea:  gitea.New(g),
	}
}
