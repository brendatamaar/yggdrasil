// Package github reads pull requests and CI status from GitHub (GH-01, GH-02,
// GH-04). It sends GET requests only. The "pipeline" is the aggregate of the
// branch head's check runs and combined commit status. A fine-grained token
// needs Pull requests (read), Commit statuses (read) and Actions or Checks
// (read).
package github

import (
	"context"
	"net/url"
	"strings"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	"github.com/brendatamaar/yggdrasil/internal/hosts"
)

// Client implements hosts.HostClient for GitHub and GitHub Enterprise Server.
type Client struct{ g *hosts.Getter }

// New returns a client that sends its requests through g.
func New(g *hosts.Getter) *Client { return &Client{g: g} }

var _ hosts.HostClient = (*Client)(nil)

// api returns the REST root: api.github.com for github.com, /api/v3 on an
// Enterprise Server.
func api(inst corehosts.Instance) string {
	if inst.ID == "github.com" {
		return "https://api.github.com"
	}
	return inst.Base + "/api/v3"
}

// RepoState implements hosts.HostClient.
func (c *Client) RepoState(ctx context.Context, inst corehosts.Instance, token string, project corehosts.ProjectPath, branch, etag string) (corehosts.RepoState, string, error) {
	owner, _, _ := strings.Cut(string(project), "/")
	repo := api(inst) + "/repos/" + hosts.PathSegments(project)
	ref := url.PathEscape(branch)
	resps, next, err := c.g.GetAll(ctx, token, []hosts.Request{
		{URL: repo + "/pulls?state=all&sort=updated&direction=desc&per_page=20&head=" + url.QueryEscape(owner+":"+branch)},
		{URL: repo + "/commits/" + ref + "/check-runs?per_page=100", Optional: true},
		{URL: repo + "/commits/" + ref + "/status", Optional: true},
	}, etag)
	if err != nil {
		return corehosts.RepoState{}, "", err
	}
	mrs, err := parsePulls(resps[0].Body)
	if err != nil {
		return corehosts.RepoState{}, "", err
	}
	pl, err := parsePipeline(resps[1].Body, resps[2].Body, inst.Base+"/"+string(project)+"/actions")
	if err != nil {
		return corehosts.RepoState{}, "", err
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
		query string
		role  corehosts.Role
	}{
		{"is:pr is:open review-requested:@me", corehosts.RoleReviewRequested},
		{"is:pr is:open assignee:@me", corehosts.RoleAssigned},
		{"is:pr is:open author:@me", corehosts.RoleAuthored},
	} {
		r, err := c.g.Get(ctx, api(inst)+"/search/issues?per_page=50&sort=updated&q="+url.QueryEscape(q.query), token, hosts.AuthBearer, "")
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
