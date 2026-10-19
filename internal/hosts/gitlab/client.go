// Package gitlab reads merge requests and pipelines from a GitLab instance
// (GH-01, GH-02). It sends GET requests only.
package gitlab

import (
	"context"
	"net/url"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	"github.com/brendatamaar/yggdrasil/internal/hosts"
)

// Client implements hosts.HostClient for GitLab (API v4).
type Client struct{ g *hosts.Getter }

// New returns a client that sends its requests through g.
func New(g *hosts.Getter) *Client { return &Client{g: g} }

var _ hosts.HostClient = (*Client)(nil)

func api(inst corehosts.Instance) string { return inst.Base + "/api/v4" }

// RepoState implements hosts.HostClient: the branch's MR and latest pipeline.
func (c *Client) RepoState(ctx context.Context, inst corehosts.Instance, token string, project corehosts.ProjectPath, branch, etag string) (corehosts.RepoState, string, error) {
	pid := url.PathEscape(string(project))
	q := url.QueryEscape(branch)
	base := api(inst) + "/projects/" + pid
	resps, next, err := c.g.GetAll(ctx, token, []hosts.Request{
		{URL: base + "/merge_requests?state=all&order_by=updated_at&sort=desc&per_page=20&source_branch=" + q, Auth: hosts.AuthPrivateToken},
		{URL: base + "/pipelines?order_by=id&sort=desc&per_page=1&ref=" + q, Auth: hosts.AuthPrivateToken, Optional: true},
	}, etag)
	if err != nil {
		return corehosts.RepoState{}, "", err
	}
	mrs, err := parseMRs(resps[0].Body)
	if err != nil {
		return corehosts.RepoState{}, "", err
	}
	pl, err := parsePipelines(resps[1].Body)
	if err != nil {
		return corehosts.RepoState{}, "", err
	}
	return corehosts.RepoState{MR: hosts.PickMR(mrs), Pipeline: pl}, next, nil
}

// Inbox implements hosts.HostClient: review requests, assigned and authored
// open MRs.
func (c *Client) Inbox(ctx context.Context, inst corehosts.Instance, token string) ([]corehosts.InboxItem, error) {
	if token == "" {
		return nil, hosts.NeedToken()
	}
	get := func(path string) ([]byte, error) {
		r, err := c.g.Get(ctx, api(inst)+path, token, hosts.AuthPrivateToken, "")
		return r.Body, err
	}
	ub, err := get("/user")
	if err != nil {
		return nil, err
	}
	user, err := parseUser(ub)
	if err != nil {
		return nil, err
	}
	var all []corehosts.InboxItem
	for _, q := range []struct {
		path string
		role corehosts.Role
	}{
		{"/merge_requests?scope=all&state=opened&per_page=50&reviewer_username=" + url.QueryEscape(user), corehosts.RoleReviewRequested},
		{"/merge_requests?scope=assigned_to_me&state=opened&per_page=50", corehosts.RoleAssigned},
		{"/merge_requests?scope=created_by_me&state=opened&per_page=50", corehosts.RoleAuthored},
	} {
		body, err := get(q.path)
		if err != nil {
			return nil, err
		}
		items, err := parseInbox(body, q.role)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
	}
	return hosts.DedupeInbox(all), nil
}
