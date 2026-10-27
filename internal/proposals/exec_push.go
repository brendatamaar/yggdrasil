package proposals

import (
	"context"

	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/git"
)

// pushPlan is what Push and MergeRequest share.
type pushPlan struct {
	where      coreprop.Target
	branch     string
	remote     string
	expectHead string
	mr         *git.MROptions
}

// pushNote is the result of a push that found the work already on the remote.
const alreadyPushed = "The branch is already on the remote at this commit, so nothing was sent."

// runPush is the executor of Push and MergeRequest. Before it sends anything
// it asks the remote (ls-remote): when the branch is already there at the
// expected commit the earlier attempt landed (a crash after the push, or a
// timeout), and the proposal is Executed without a second push. Nothing here
// can force: internal/git has no such parameter.
func (s *Service) runPush(ctx context.Context, p coreprop.Proposal, plan pushPlan) outcome {
	wd, stop := s.open(ctx, p, plan.where, true)
	if stop != nil {
		return *stop
	}
	defer wd.done()
	ctx, cancel := context.WithTimeout(ctx, s.cfg.ExecTimeout)
	defer cancel()

	url, err := s.cfg.Git.RemoteURL(ctx, wd.dir, plan.remote)
	if err != nil {
		return readFailure("read the remote", err)
	}
	host, err := checkRemoteURL(url)
	if err != nil {
		return errUnsafeRemote
	}
	if plan.mr != nil && s.cfg.Hosts.Kind(url) != HostGitLab {
		return failed(coreprop.FailPrecondition, false, "Merge requests are only supported on GitLab for now. Push the branch and open the merge request on the host.")
	}
	if tip, err := s.cfg.Git.LsRemote(ctx, wd.dir, plan.remote, plan.branch); err == nil && tip != "" && tip == plan.expectHead {
		return done(coreprop.Result{Note: alreadyPushed + mrHint(plan)})
	}
	res, err := s.cfg.Git.Push(ctx, wd.dir, git.PushRequest{
		Remote: plan.remote, Branch: plan.branch, ExpectHead: plan.expectHead, MR: plan.mr,
	})
	if err != nil {
		return gitFailure(err)
	}
	if plan.mr == nil {
		return done(coreprop.Result{Note: "Pushed."})
	}
	r, err := coreprop.ParsePushOutput(res.Output, host)
	if err != nil {
		// Pushed, but no link we trust: still a success (docs/design/proposals.md).
		return done(coreprop.Result{Note: "Pushed; open the merge request from the repo page."})
	}
	r.Note = "Pushed and opened a merge request."
	return done(r)
}

func mrHint(plan pushPlan) string {
	if plan.mr == nil {
		return ""
	}
	return " Check the repo page for the merge request."
}

func (s *Service) execPush(ctx context.Context, p coreprop.Proposal, a coreprop.Push) outcome {
	return s.runPush(ctx, p, pushPlan{where: a.Where, branch: a.Branch, remote: a.Remote, expectHead: a.ExpectHead})
}
