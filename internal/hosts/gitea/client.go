// Package gitea reads pull requests and commit status from Gitea (GH-01,
// GH-02). It is community-maintained and sends GET requests only.
package gitea

import (
	"context"
	"net/url"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	"github.com/brendatamaar/yggdrasil/internal/hosts"
)

// Client implements hosts.HostClient for Gitea.
type Client struct{ g *hosts.Getter }

// New returns a client that sends its requests through g.
func New(g *hosts.Getter) *Client { return &Client{g: g} }

var _ hosts.HostClient = (*Client)(nil)

func api(inst corehosts.Instance) string { return inst.Base + "/api/v1" }

// RepoState implements hosts.HostClient.
func (c *Client) RepoState(ctx context.Context, inst corehosts.Instance, token string, project corehosts.ProjectPath, branch, etag string) (corehosts.RepoState, string, error) {
	repo := api(inst) + "/repos/" + hosts.PathSegments(project)
	resps, next, err := c.g.GetAll(ctx, token, []hosts.Request{
		{URL: repo + "/pulls?state=all&sort=recentupdate&limit=50"},
		{URL: repo + "/commits/" + url.PathEscape(branch) + "/status", Optional: true},
	}, etag)
	if err != nil {
		return corehosts.RepoState{}, "", err
	}
	mrs, err := parsePulls(resps[0].Body, branch)
	if err != nil {
		return corehosts.RepoState{}, "", err
	}
	pl, err := parseStatus(resps[1].Body)
	if err != nil {
		return corehosts.RepoState{}, "", err
	}
	if pl != nil && pl.URL == "" {
		pl.URL = inst.Base + "/" + string(project) + "/actions"
	}
	return corehosts.RepoState{MR: hosts.PickMR(mrs), Pipeline: pl}, next, nil
}

// Inbox implements hosts.HostClient.
func (c *Client) Inbox(ctx context.Context, inst corehosts.Instance, token string) ([]corehosts.InboxItem, error) {
	if token == "" {
		return nil, hosts.NeedToken()
	}
	var all []corehosts.InboxItem
	for _, q := range []struct {
		flag string
		role corehosts.Role
	}{
		{"review_requested=true", corehosts.RoleReviewRequested},
		{"assigned=true", corehosts.RoleAssigned},
		{"created=true", corehosts.RoleAuthored},
	} {
		r, err := c.g.Get(ctx, api(inst)+"/repos/issues/search?type=pulls&state=open&limit=50&"+q.flag, token, hosts.AuthBearer, "")
		if err != nil {
			return nil, err
		}
		items, err := parseSearch(r.Body, q.role)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
	}
	return hosts.DedupeInbox(all), nil
}
