package proposals

import (
	"context"

	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/git"
)

// execMR pushes the branch with GitLab merge-request push options (GH-03). The
// description is one line because push options cannot carry a newline.
func (s *Service) execMR(ctx context.Context, p coreprop.Proposal, a coreprop.MergeRequest) outcome {
	return s.runPush(ctx, p, pushPlan{
		where: a.Where, branch: a.Branch, remote: a.Remote, expectHead: a.ExpectHead,
		mr: &git.MROptions{Target: a.TargetBranch, Title: a.Title, Description: a.Description},
	})
}
