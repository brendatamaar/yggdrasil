package github

import (
	"context"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	"github.com/brendatamaar/yggdrasil/internal/hosts"
)

var _ hosts.ContextClient = (*Client)(nil)

func (c *Client) contextAPI(inst corehosts.Instance) hosts.ContextAPI {
	return hosts.ContextAPI{Getter: c.g, Root: api(inst), GitLab: false}
}
func (c *Client) FailedJobs(ctx context.Context, inst corehosts.Instance, token string, p corehosts.ProjectPath, run, head string) (hosts.ContextResult, error) {
	return c.contextAPI(inst).FailedJobs(ctx, inst, token, p, run, head)
}
func (c *Client) Issues(ctx context.Context, inst corehosts.Instance, token string, p corehosts.ProjectPath) (hosts.ContextResult, error) {
	return c.contextAPI(inst).Issues(ctx, inst, token, p)
}
func (c *Client) Issue(ctx context.Context, inst corehosts.Instance, token string, p corehosts.ProjectPath, n int) (hosts.ContextResult, error) {
	return c.contextAPI(inst).Issue(ctx, inst, token, p, n)
}
func (c *Client) MRDiff(ctx context.Context, inst corehosts.Instance, token string, p corehosts.ProjectPath, n int) (hosts.ContextResult, error) {
	return c.contextAPI(inst).MRDiff(ctx, inst, token, p, n)
}
