package actions

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreactions "github.com/brendatamaar/yggdrasil/internal/core/actions"
	coreproposals "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/tasks"
)

func definite(reason coreactions.RefuseReason, msg string) *effectFailure {
	if msg == "" {
		msg = message(reason)
	}
	return &effectFailure{outcome: coreactions.SubmitDefiniteRefusal, reason: reason, message: msg}
}

// classify says whether an error of a task or proposal port is a definite
// refusal (nothing exists) or an unknown outcome (the effect may exist, so the
// request is reconciled and never blindly repeated).
func (r *run) classify(err error) *effectFailure {
	var rej *tasks.RejectedError
	var ue *coretasks.UserError
	switch {
	case errors.As(err, &rej):
		return definite(coreactions.RefuseAdmission, r.s.d.Redactor.Redact(rej.Message))
	case errors.As(err, &ue):
		return definite(coreactions.RefuseAdmission, r.s.d.Redactor.Redact(ue.Message))
	case errors.Is(err, db.ErrDigestMismatch):
		return definite(coreactions.RefuseDigestMismatch, "")
	case errors.Is(err, coretasks.ErrInvalidDraft):
		return definite(coreactions.RefuseAdmission, "")
	case errors.Is(err, db.ErrNotFound):
		return definite(coreactions.RefuseUntracked, "")
	}
	return &effectFailure{outcome: coreactions.SubmitUnknown, message: "The result of the request is not known yet. It will be checked and never started twice."}
}

// perform runs the one effect the core chose for the dispatch.
func (r *run) perform(ctx context.Context, c coreactions.Dispatch) (coreactions.Result, *effectFailure) {
	req := c.Request
	if r.l == nil {
		return coreactions.Result{}, definite(coreactions.RefusePrepareFailed, "")
	}
	switch c.Effect() {
	case coreactions.EffectSubmitTask:
		return r.submitTask(ctx, c)
	case coreactions.EffectDraftTask:
		if req.Kind == coreactions.ImportIssue {
			return r.importIssue(ctx, c)
		}
		return r.waitingDraft(ctx, c)
	case coreactions.EffectStartPull:
		return r.startPull(ctx, c)
	}
	return coreactions.Result{}, definite(coreactions.RefuseNoOffering, "")
}

// submitTask submits one Ask task, or for FixPipeline one Code task in its own
// worktree, through the ordinary admission path. A diagnosis never asks for
// Code: the mode comes from the kind, never from text.
func (r *run) submitTask(ctx context.Context, c coreactions.Dispatch) (coreactions.Result, *effectFailure) {
	req := c.Request
	mode := req.Kind.Mode()
	if !coreactions.ModeAllowed(req.Kind, mode) {
		return coreactions.Result{}, definite(coreactions.RefuseNoOffering, "")
	}
	res, err := r.s.d.Tasks.SubmitWithRequest(ctx, req.RequestID, tasks.MessageRequest{
		ProjectID: r.l.target.Project, Prompt: r.s.d.Redactor.Redact(compose(req.Kind, c.Context)), Mode: string(mode),
		Scope: string(core.ScopeRepo), Repos: []string{string(req.RepoID)}, Agent: req.Agent, Model: req.Model, Title: titleOf(req),
	})
	if err != nil {
		return coreactions.Result{}, r.classify(err)
	}
	return coreactions.Result{TaskID: res.Task.ID}, nil
}

// importIssue stores a draft task only: no turn, process or worktree.
func (r *run) importIssue(ctx context.Context, c coreactions.Dispatch) (coreactions.Result, *effectFailure) {
	req, t := c.Request, r.l.target
	if !t.HasHost || r.l.issueNum <= 0 {
		return coreactions.Result{}, definite(coreactions.RefuseCapability, "")
	}
	title, _ := clamp(strings.TrimSpace(clean(r.l.issueTitle)), 200)
	title = strings.Join(strings.Fields(title), " ")
	res, err := r.s.d.Tasks.ImportIssueDraft(ctx, coretasks.DraftSpec{
		RequestID: req.RequestID, Project: t.Project, Repo: req.RepoID,
		Issue: coretasks.IssueRef{Instance: t.Instance, Project: t.HostProject, Number: r.l.issueNum},
		Title: r.s.d.Redactor.Redact(title), Brief: r.s.d.Redactor.Redact(compose(coreactions.ImportIssue, c.Context)),
		Agent: req.Agent, Model: req.Model,
	})
	if err != nil {
		return coreactions.Result{}, r.classify(err)
	}
	return coreactions.Result{TaskID: res.Task.ID}, nil
}

// waitingDraft keeps the brief of a diagnosis for a repo whose folder cannot be
// read. No run is faked: without a draft store the request is refused.
func (r *run) waitingDraft(ctx context.Context, c coreactions.Dispatch) (coreactions.Result, *effectFailure) {
	req := c.Request
	if r.s.d.Drafts == nil {
		return coreactions.Result{}, definite(coreactions.RefuseAdmission,
			"The repo folder can't be read, so nothing can run on it. Restore the folder, then try again.")
	}
	id, err := r.s.d.Drafts.SaveWaitingDraft(ctx, req.RequestID, WaitingDraft{
		Project: r.l.target.Project, Repo: req.RepoID, Title: titleOf(req),
		Brief: r.s.d.Redactor.Redact(compose(req.Kind, c.Context)), Agent: req.Agent, Model: req.Model,
	})
	if err != nil {
		return coreactions.Result{}, r.classify(err)
	}
	return coreactions.Result{TaskID: id}, nil
}

// startPull creates one pending PullFF proposal; the pull itself runs only
// after the user confirms it, through the existing git service.
func (r *run) startPull(ctx context.Context, c coreactions.Dispatch) (coreactions.Result, *effectFailure) {
	req := c.Request
	if len(c.IDs.Proposals) != 1 {
		return coreactions.Result{}, definite(coreactions.RefuseInvalid, "")
	}
	pt := r.l.pull
	a := coreproposals.PullFF{Repo: req.RepoID, Branch: pt.Branch, Upstream: pt.Upstream, ExpectHead: pt.Head}
	if err := a.Validate(); err != nil {
		return coreactions.Result{}, definite(coreactions.RefuseScopeChanged,
			"This repo can't be pulled right now: "+r.s.d.Redactor.Redact(err.Error()))
	}
	if !coreactions.StillCurrent(r.l.facts.Head, pt.Head) {
		return coreactions.Result{}, definite(coreactions.RefuseStaleHead, "")
	}
	kind, payload := coreproposals.RowOf(a)
	ids, err := r.s.d.Proposals.CreateForRequest(ctx, req.RequestID, []ProposalSpec{{
		ID: c.IDs.Proposals[0], Repo: req.RepoID, Kind: kind, Payload: payload,
		Summary: fmt.Sprintf("Pull %s into %s (fast-forward only)", pt.Upstream, pt.Branch),
	}})
	if err != nil {
		return coreactions.Result{}, r.classify(err)
	}
	return coreactions.Result{ProposalIDs: ids}, nil
}
