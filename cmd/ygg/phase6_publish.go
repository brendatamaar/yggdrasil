package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	corenewrepos "github.com/brendatamaar/yggdrasil/internal/core/newrepos"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/hosts"
	"github.com/brendatamaar/yggdrasil/internal/mcp"
	"github.com/brendatamaar/yggdrasil/internal/newrepos"
	"github.com/brendatamaar/yggdrasil/internal/proposals"
)

// ---- review comments (AC-02) ----

// commentHost is proposals.CommentHost over the confirmed host capability. It
// classifies the errors the executor needs: a lost reply wraps
// proposals.ErrOutcomeUnknown (the executor reconciles it read-only and never
// posts again), and an answer that proves nothing was posted (the host rejected
// the request or refused the token) wraps proposals.ErrWriteRefused. Any other
// error stays unclassified, which the executor also treats as unknown: an
// adapter that forgets to classify cannot cause a second POST.
type commentHost struct{ inner hosts.CommentHost }

var _ proposals.CommentHost = commentHost{}

func (c commentHost) MRHead(ctx context.Context, repo core.RepoID, mr int) (string, error) {
	return c.inner.MRHead(ctx, repo, mr)
}

func (c commentHost) PostReviewComment(ctx context.Context, repo core.RepoID, mr int, expectedHead, body, marker string) (string, error) {
	id, err := c.inner.PostReviewComment(ctx, repo, mr, expectedHead, body, marker)
	if err == nil {
		return id, nil
	}
	var fe *hosts.FetchError
	switch {
	case errors.Is(err, hosts.ErrOutcomeUnknown):
		return "", fmt.Errorf("%w: %w", proposals.ErrOutcomeUnknown, err)
	case errors.Is(err, hosts.ErrWriteRejected), errors.As(err, &fe) && fe.Kind == corehosts.FailUnauthorized:
		return "", fmt.Errorf("%w: %w", proposals.ErrWriteRefused, err)
	}
	return "", err
}

func (c commentHost) FindReviewComment(ctx context.Context, repo core.RepoID, mr int, marker, bodyHash, expectedHead string) (coreprop.CommentLookup, error) {
	return c.inner.FindReviewComment(ctx, repo, mr, marker, bodyHash, expectedHead)
}

// ---- the publish credential ----

// pushAuthorizer is the git.Authorizer of the in-process publish transport. It
// finds the configured host instance by the request's own address and asks the
// confirmed host capability for the Authorization header through
// hosts.NewPushTransport, which binds the credential to the one repository
// path and refuses every other path, redirect or encoded form before the
// authorizer runs. The credential is never handed to a process, a URL or a git
// config (invariants 3 and 4).
type pushAuthorizer struct {
	confirmed hosts.PushAuthorizer
	instances instanceLister
}

var _ git.Authorizer = pushAuthorizer{}

func (a pushAuthorizer) Authorize(ctx context.Context, req *http.Request) error {
	if req == nil || req.URL == nil {
		return hosts.ErrContextScope
	}
	insts, err := a.instances.Instances(ctx)
	if err != nil {
		return fmt.Errorf("read the git host settings: %w", err)
	}
	id, ok := instanceOf(insts, req.URL)
	if !ok {
		return hosts.ErrContextScope
	}
	repoPath, ok := repoPathOf(req.URL.Path)
	if !ok {
		return hosts.ErrContextScope
	}
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	rt := hosts.NewPushTransport(a.confirmed, id, repoPath, copyAuthorization{dst: req})
	resp, err := rt.RoundTrip(req)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// copyAuthorization is the end of the checked chain: it sends nothing, it only
// copies the Authorization header the chain produced onto the real request.
type copyAuthorization struct{ dst *http.Request }

func (c copyAuthorization) RoundTrip(out *http.Request) (*http.Response, error) {
	c.dst.Header.Set("Authorization", out.Header.Get("Authorization"))
	return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody, Request: out}, nil
}

// instanceOf finds the instance whose address names the request's host.
func instanceOf(insts []corehosts.Instance, u *url.URL) (corehosts.InstanceID, bool) {
	for _, in := range insts {
		b, err := url.Parse(in.Base)
		if err == nil && b.Host != "" && strings.EqualFold(b.Host, u.Host) {
			return in.ID, true
		}
	}
	return "", false
}

// repoPathOf is the "/owner/name.git" prefix of a clone path (a deployment
// prefix may come first): up to and including the first segment ending in
// ".git". A path without one is refused.
func repoPathOf(p string) (string, bool) {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if strings.HasSuffix(s, ".git") && len(s) > len(".git") {
			return strings.Join(segs[:i+1], "/"), true
		}
	}
	return "", false
}

// ---- mcp.Publishing (MCP-05) ----

// reservations is the part of the new-repo service the MCP adapter uses.
type reservations interface {
	GetReservationByRepo(ctx context.Context, repo core.RepoID) (db.ReservationRecord, error)
}

// mcpPublishing implements mcp.Publishing over the new-repo service: Plan
// resolves the repo's operation and the server's values without remembering
// anything; Bind remembers the destination under the proposal. Neither
// confirms or creates anything.
type mcpPublishing struct {
	svc   *newrepos.Service
	store reservations
}

var _ mcp.Publishing = mcpPublishing{}

func (p mcpPublishing) Plan(ctx context.Context, repo core.RepoID, req mcp.PublishRequest) (mcp.PublishPlan, error) {
	rec, err := p.store.GetReservationByRepo(ctx, repo)
	if errors.Is(err, db.ErrNotFound) {
		return mcp.PublishPlan{}, mcp.ErrNotPublishable
	}
	if err != nil {
		return mcp.PublishPlan{}, fmt.Errorf("read the new repo of %s: %w", repo, err)
	}
	pv, err := p.svc.PreparePublish(ctx, rec.Operation.ID, publishRequestFromMCP(req), "")
	if errors.Is(err, newrepos.ErrNotApplicable) {
		return mcp.PublishPlan{}, mcp.ErrNotPublishable
	}
	if err != nil {
		return mcp.PublishPlan{}, err
	}
	s := pv.Spec
	return mcp.PublishPlan{
		Operation: rec.Operation.ID, Repo: repo, Host: s.HostInstance, Owner: s.Owner, OwnerKind: string(s.OwnerKind),
		Name: s.Name, Visibility: string(s.Visibility), Branch: s.Branch, ExpectedHead: s.ExpectedHead, Marker: s.Marker,
		Problems: append([]string(nil), pv.Problems...),
	}, nil
}

func (p mcpPublishing) Bind(ctx context.Context, plan mcp.PublishPlan, proposal core.ProposalID) error {
	pv, err := p.svc.PreparePublish(ctx, plan.Operation, publishRequestFromMCP(mcp.PublishRequest{
		Host: plan.Host, Owner: plan.Owner, OwnerKind: plan.OwnerKind, Name: plan.Name, Visibility: plan.Visibility,
	}), proposal)
	if err != nil {
		return fmt.Errorf("remember the publish destination of %s: %w", proposal, err)
	}
	if len(pv.Problems) > 0 {
		return fmt.Errorf("remember the publish destination of %s: it cannot be published now", proposal)
	}
	// The remembered spec must be the one the proposal carries.
	want := publishActionOf(plan.Operation, pv.Spec)
	if want.Marker != plan.Marker || want.ExpectedHead != plan.ExpectedHead || want.Branch != plan.Branch {
		return fmt.Errorf("remember the publish destination of %s: the repo changed since it was planned", proposal)
	}
	return nil
}

func publishRequestFromMCP(r mcp.PublishRequest) newrepos.PublishRequest {
	return newrepos.PublishRequest{
		HostInstance: r.Host, Owner: r.Owner, OwnerKind: corenewrepos.OwnerKind(r.OwnerKind), Name: r.Name,
		Visibility: corenewrepos.Visibility(r.Visibility),
	}
}

// ---- rejecting a proposal settles its operation ----

// rejectingProposals is the api.ProposalService that also tells the new-repo
// service when the user rejects an init or publish proposal: the lifecycle port
// of the proposals service has no Reject, and a rejected init proposal must
// settle its operation (Stopped, nothing on disk) instead of leaving it
// awaiting a confirmation that will never come. Rejecting is the only decision
// that reaches the operation from here; confirming still goes through the
// proposals executor.
type rejectingProposals struct {
	api.ProposalService
	nr interface {
		Reject(context.Context, core.OperationID) error
	}
	store interface {
		GetProposalRecord(ctx context.Context, id core.ProposalID) (db.ProposalRecord, error)
	}
	log *slog.Logger
}

var _ api.ProposalService = rejectingProposals{}

func (r rejectingProposals) Reject(ctx context.Context, id core.ProposalID, by string) (proposals.View, error) {
	v, err := r.ProposalService.Reject(ctx, id, by)
	if err != nil || (v.Kind != core.ProposalInitRepo && v.Kind != core.ProposalPublish) {
		return v, err
	}
	rec, gerr := r.store.GetProposalRecord(ctx, id)
	if gerr != nil {
		r.log.Warn("a new repo proposal was rejected but its operation was not settled; it is settled at the next start", "proposal", id, "err", gerr)
		return v, nil
	}
	if op, ok := newRepoOperationOf(rec.Proposal.Action); ok {
		// Not applicable means the operation was not waiting (a publish
		// proposal's operation is Ready): nothing to settle.
		if nerr := r.nr.Reject(ctx, op); nerr != nil && !errors.Is(nerr, newrepos.ErrNotApplicable) {
			r.log.Warn("a new repo proposal was rejected but its operation was not settled; it is settled at the next start", "operation", op, "err", nerr)
		}
	}
	return v, nil
}

func newRepoOperationOf(a coreprop.Action) (core.OperationID, bool) {
	switch v := a.(type) {
	case coreprop.InitRepo:
		return v.Operation, true
	case coreprop.PublishRepo:
		return v.Operation, true
	}
	return "", false
}

// ---- start: settle what a crash left ----

// proposalLister is the part of the store the start-up settlement reads.
type proposalLister interface {
	ListProposalRecords(ctx context.Context, status core.ProposalStatus) ([]db.ProposalRecord, error)
}

// settleNewRepoProposals runs at start, after the operations were recovered:
//   - an init proposal the user rejected whose operation still awaits its
//     confirmation (the process stopped between the two commits) settles the
//     operation as rejected;
//   - a pending publish proposal gets its destination remembered again (the
//     lifecycle keeps it in memory only), so confirming it after a restart does
//     not fail with "what you confirmed is not what was previewed".
//
// It writes nothing on disk or on a host. Failures are logged; the next start
// tries again.
func settleNewRepoProposals(ctx context.Context, log *slog.Logger, svc *newrepos.Service, store proposalLister) {
	rejected, err := store.ListProposalRecords(ctx, core.ProposalRejected)
	if err != nil {
		log.Warn("could not look for rejected new repo proposals", "err", err)
	}
	for _, r := range rejected {
		a, ok := r.Proposal.Action.(coreprop.InitRepo)
		if !ok {
			continue
		}
		if err := svc.Reject(ctx, a.Operation); err != nil && !errors.Is(err, newrepos.ErrNotApplicable) && !errors.Is(err, db.ErrNotFound) {
			log.Warn("could not settle a rejected new repo", "operation", a.Operation, "err", err)
		}
	}
	pending, err := store.ListProposalRecords(ctx, core.ProposalPending)
	if err != nil {
		log.Warn("could not look for pending publish proposals", "err", err)
	}
	// Each re-check reads the host once (the account check); bound the start-up
	// cost when the host is slow or unreachable.
	const maxRebind = 10
	rebound := 0
	for _, r := range pending {
		a, ok := r.Proposal.Action.(coreprop.PublishRepo)
		if !ok {
			continue
		}
		if rebound++; rebound > maxRebind {
			log.Warn("too many pending publish proposals to re-check at start; reject the rest and publish again")
			break
		}
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := svc.PreparePublish(cctx, a.Operation, newrepos.PublishRequest{
			HostInstance: a.HostInstance, Owner: a.Owner, OwnerKind: corenewrepos.OwnerKind(a.OwnerKind), Name: a.Name,
			Visibility: corenewrepos.Visibility(a.Visibility),
		}, r.Proposal.ID)
		cancel()
		if err != nil && !errors.Is(err, newrepos.ErrNotApplicable) {
			log.Warn("could not remember the destination of a pending publish proposal; reject it and publish again", "proposal", r.Proposal.ID, "err", err)
		}
	}
}
