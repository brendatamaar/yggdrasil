package proposals

import (
	"context"
	"errors"

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
	// template is the confirmed MR description; its link marker is filled in
	// at execution (fillLinks).
	template string
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

	if err := s.checkBranchBinding(ctx, plan.where, wd.dir, plan.branch); err != nil {
		if errors.Is(err, errWrongBranch) {
			return failed(coreprop.FailPrecondition, false, "%s The branch changed since this was proposed: reject it and ask for a new one.", wrongBranchMsg)
		}
		return readFailure("check the branch", err)
	}
	defBranch := ""
	if info, err := s.cfg.Resolver.Repo(ctx, wd.repo); err == nil {
		defBranch = info.DefaultBranch
	}
	if defBranch != "" && plan.branch == defBranch {
		return failed(coreprop.FailPrecondition, false, "Yggdrasil does not push to the default branch. Push a task branch and open a merge request instead.")
	}
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
	linksNote := ""
	if plan.mr != nil {
		mr := *plan.mr
		mr.Description, linksNote = s.fillLinks(ctx, p, plan.template, host)
		plan.mr = &mr
	}
	res, err := s.cfg.Git.Push(ctx, wd.dir, git.PushRequest{
		Remote: plan.remote, Branch: plan.branch, ExpectHead: plan.expectHead, DefaultBranch: defBranch, MR: plan.mr,
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
	r.Note = "Pushed and opened a merge request." + linksNote
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
