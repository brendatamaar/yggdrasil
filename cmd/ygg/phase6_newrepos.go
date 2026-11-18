package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corenewrepos "github.com/brendatamaar/yggdrasil/internal/core/newrepos"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/hosts"
	"github.com/brendatamaar/yggdrasil/internal/mainlock"
	"github.com/brendatamaar/yggdrasil/internal/newrepos"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/proposals"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// Wiring of new repos and publish (NR-01 to NR-04, MCP-05;
// docs/design/phase-6-newrepos.md).
//
// The new-repo service is built BEFORE the proposals service (the proposals
// service takes it as its NewRepoLifecycle) and before the server (its hub is
// bound afterwards, see newRepoHub). Nothing in this file writes a main
// checkout or a remote: the service does that only after a confirmation, and
// every confirmation reaches it as the immutable spec hash, never as the
// proposal's action hash (the proposals executor computes it).
//
// Shutdown order (see app.run): the server stops, then stopActions, then
// stopNewRepos (the worker of every operation stops and its last state is
// committed, then the follow-ups still queued for the proposals service are
// handed over), then proposals, tasks, notices, hosts and finally the store.
// The new-repo worker is stopped before the store closes so a stage that was
// running is recorded Uncertain, and before proposals so the last outcomes
// still reach the proposals service.

// newRepoFakes replaces the host-facing capabilities in tests only (never set
// by loadConfig): a fake git host, a fake publish transport and a fake comment
// host, so no test reaches a real host.
type newRepoFakes struct {
	Remote    hosts.RepoHost
	Transport git.PublishTransport
	Comments  hosts.CommentHost
}

// newRepoParts is what newApp builds before the proposals service.
type newRepoParts struct {
	svc      *newrepos.Service
	hub      *newRepoHub
	notifier *opNotifier
	// confirmed is the confirmed-write host capability (comments and repos). It
	// is never given to HTTP, MCP or an agent.
	confirmed *hosts.ConfirmedHosts
	comments  hosts.CommentHost
}

// newNewRepos builds the new-repo service over the real capabilities (or the
// test fakes): the safe filesystem, the git wrapper, the host's repo creation
// and the in-process publish transport.
func newNewRepos(store *db.Store, g *git.Git, lock *mainlock.Registry, hostSvc *hosts.Service, redactor safety.Redactor, log *slog.Logger, fakes *newRepoFakes) (*newRepoParts, error) {
	confirmed := hosts.NewConfirmedHosts(hostSvc, contextResolver{store: store, hosts: hostSvc}, nil)
	var remote hosts.RepoHost = confirmed
	var comments hosts.CommentHost = confirmed
	var transport git.PublishTransport
	if fakes != nil && fakes.Transport != nil {
		transport = fakes.Transport
	} else {
		t, err := git.NewGoGitTransport(pushAuthorizer{confirmed: confirmed, instances: hostSvc}, nil)
		if err != nil {
			return nil, fmt.Errorf("start publishing: %w", err)
		}
		transport = t
	}
	if fakes != nil && fakes.Remote != nil {
		remote = fakes.Remote
	}
	if fakes != nil && fakes.Comments != nil {
		comments = fakes.Comments
	}
	notifier := &opNotifier{log: log, wake: make(chan struct{}, 1), seen: map[core.OperationID]bool{}}
	hub := &newRepoHub{notifier: notifier}
	svc, err := newrepos.NewService(newrepos.Deps{
		Store: store, FS: platform.NewSafeInitFS(), Git: g, Origin: g, Remote: remote, Transport: transport,
		// The registry is non-blocking; TryLock polls it, holding it only while
		// a stage mutates the checkout.
		Lock: newrepos.TryLock(lock, 0),
		Hub:  hub, Clock: func() time.Time { return time.Now().UTC() }, IDs: db.NewID,
		Projects: store, Paths: platform.RealPaths{}, Logger: log, Redact: redactor.Redact,
	})
	if err != nil {
		return nil, fmt.Errorf("start new repos: %w", err)
	}
	return &newRepoParts{svc: svc, hub: hub, notifier: notifier, confirmed: confirmed, comments: commentHost{inner: comments}}, nil
}

// newRepoHub is the api.Phase6Hub of the new-repo service. It publishes to the
// server once the server exists and tells the proposals service about every
// state change of an operation (proposals.Service.OperationChanged), which is
// how a Confirmed init or publish proposal learns its outcome.
type newRepoHub struct {
	srv      atomic.Pointer[api.Server]
	notifier *opNotifier
}

var _ api.Phase6Hub = (*newRepoHub)(nil)

func (h *newRepoHub) PublishNotice(m api.NoticeChangedMessage) {
	if s := h.srv.Load(); s != nil {
		s.PublishNotice(m)
	}
}

func (h *newRepoHub) PublishAction(m api.ActionChangedMessage) {
	if s := h.srv.Load(); s != nil {
		s.PublishAction(m)
	}
}

// PublishNewRepo is called after the state it announces is committed. The
// proposals follow-up is queued, never run here: the caller may hold the
// service's locks.
func (h *newRepoHub) PublishNewRepo(m api.NewRepoChangedMessage) {
	if s := h.srv.Load(); s != nil {
		s.PublishNewRepo(m)
	}
	h.notifier.notify(m.OperationID)
}

// opNotifier hands the operations that changed to the proposals service, one
// goroutine, in order, never blocking the caller. Operations queued while it
// is busy are merged. close drains what is queued, so the last outcome of a
// stopping worker still reaches the proposals service.
type opNotifier struct {
	log  *slog.Logger
	wake chan struct{}
	// follow is proposals.Service.OperationChanged; set before start.
	follow func(context.Context, core.OperationID) error

	mu      sync.Mutex
	queue   []core.OperationID
	seen    map[core.OperationID]bool
	started bool
	closed  bool
	done    chan struct{}
}

func (n *opNotifier) notify(op core.OperationID) {
	n.mu.Lock()
	if n.closed || n.seen[op] {
		n.mu.Unlock()
		return
	}
	n.seen[op] = true
	n.queue = append(n.queue, op)
	n.mu.Unlock()
	select {
	case n.wake <- struct{}{}:
	default:
	}
}

func (n *opNotifier) take() (core.OperationID, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.queue) == 0 {
		return "", false
	}
	op := n.queue[0]
	n.queue = n.queue[1:]
	delete(n.seen, op)
	return op, true
}

// start runs the follow-up loop. ctx only bounds one call; the loop itself ends
// in close, after the queue is empty.
func (n *opNotifier) start(ctx context.Context) {
	n.mu.Lock()
	if n.started || n.follow == nil {
		n.mu.Unlock()
		return
	}
	n.started, n.done = true, make(chan struct{})
	n.mu.Unlock()
	go n.run(context.WithoutCancel(ctx))
}

func (n *opNotifier) run(ctx context.Context) {
	defer close(n.done)
	for {
		for {
			op, ok := n.take()
			if !ok {
				break
			}
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if err := n.follow(cctx, op); err != nil && !errors.Is(err, db.ErrNotFound) {
				n.log.Warn("a new repo changed but its proposal could not follow it; it is reconciled at the next start", "operation", op, "err", err)
			}
			cancel()
		}
		n.mu.Lock()
		stop := n.closed && len(n.queue) == 0
		n.mu.Unlock()
		if stop {
			return
		}
		<-n.wake
	}
}

// close stops the loop after it handed over everything queued.
func (n *opNotifier) close() {
	n.mu.Lock()
	n.closed = true
	started, done := n.started, n.done
	n.mu.Unlock()
	if !started {
		return
	}
	select {
	case n.wake <- struct{}{}:
	default:
	}
	<-done
}

// ---- the API adapter ----

// newRepoProposals is the part of the proposals service the new-repo API uses.
type newRepoProposals interface {
	Create(ctx context.Context, req proposals.CreateRequest) (proposals.View, error)
	Get(ctx context.Context, id core.ProposalID) (proposals.View, error)
	Reject(ctx context.Context, id core.ProposalID, by string) (proposals.View, error)
}

// newRepoStore is the part of the store the adapter reads.
type newRepoStore interface {
	ListTaskProposalRecords(ctx context.Context, task core.TaskID) ([]db.ProposalRecord, error)
}

// newRepoAPI implements api.NewRepoService over the new-repo service and the
// proposals service. Reserve and Publish create PENDING proposals only; the
// operation moves on when the user confirms the proposal through the proposals
// API, never here.
type newRepoAPI struct {
	svc   *newrepos.Service
	props newRepoProposals
	store newRepoStore
	hub   api.Phase6Hub
}

var _ api.NewRepoService = newRepoAPI{}

// wrap classifies an error of the new-repo service for the API. The refusals
// that say nothing was written (RefusedError, ErrHashMismatch, ErrNotApplicable)
// are passed through unwrapped: the API maps them to 409.
func (newRepoAPI) wrap(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, newrepos.ErrClosed):
		return api.ShuttingDown(err)
	case errors.Is(err, corenewrepos.ErrInvalidSpec):
		msg := strings.TrimPrefix(err.Error(), corenewrepos.ErrInvalidSpec.Error()+": ")
		return apperr.New(apperr.Invalid, "That is not valid: "+msg+".", err)
	}
	return err
}

func (a newRepoAPI) Preview(ctx context.Context, project core.ProjectID, req api.NewRepoPreviewRequest) (api.NewRepoPreviewResponse, error) {
	p, err := a.svc.PreviewReserve(ctx, project, newrepos.ReserveRequest{
		Leaf: req.Leaf, README: req.README, Ignore: req.Ignore, CommitMessage: req.CommitMessage,
	})
	if err != nil {
		return api.NewRepoPreviewResponse{}, a.wrap(err)
	}
	return api.NewRepoPreviewResponse{
		Spec: api.NewRepoSpecDTO{Leaf: p.Spec.Leaf, Branch: p.Spec.Branch, README: p.Spec.README, Ignore: p.Spec.Ignore, CommitMessage: p.Spec.CommitMessage},
		Hash: p.Hash, Lines: p.Lines, Problems: p.Problems,
	}, nil
}

func (a newRepoAPI) Reserve(ctx context.Context, project core.ProjectID, req api.ReserveNewRepoRequest) (api.NewRepoOperationDTO, error) {
	rec, err := a.svc.Reserve(ctx, project, newrepos.ReserveRequest{
		Leaf: req.Leaf, README: req.README, Ignore: req.Ignore, CommitMessage: req.CommitMessage, Agent: req.Agent,
	}, req.Hash)
	if err != nil {
		return api.NewRepoOperationDTO{}, a.wrap(err)
	}
	// A repeat of the same reservation returns the existing one; a reservation
	// that a crash left without its proposal gets it now.
	if st, ok := rec.Operation.State.(corenewrepos.Reserved); ok && rec.Proposal == "" {
		spec := st.Spec
		_, err := a.props.Create(ctx, proposals.CreateRequest{
			Task: rec.SetupTask, Summary: "Create the new repo " + spec.Leaf,
			Action: coreprop.InitRepo{
				Operation: rec.Operation.ID, Project: spec.Project, Repo: spec.Repo, Leaf: spec.Leaf, Branch: spec.Branch,
				README: spec.README, Ignore: spec.Ignore, CommitMessage: spec.CommitMessage,
			},
		})
		if err != nil {
			return api.NewRepoOperationDTO{}, a.wrap(err)
		}
		if rec, err = a.svc.Get(ctx, rec.Operation.ID); err != nil {
			return api.NewRepoOperationDTO{}, a.wrap(err)
		}
		// The proposal's own transaction moved the operation; tell the UI.
		if code, cerr := stateCode(rec.Operation.State); cerr == nil {
			a.hub.PublishNewRepo(api.NewRepoChangedMessage{OperationID: rec.Operation.ID, State: code})
		}
	}
	return newRepoDTO(rec)
}

func (a newRepoAPI) Get(ctx context.Context, op core.OperationID) (api.NewRepoOperationDTO, error) {
	rec, err := a.svc.Get(ctx, op)
	if err != nil {
		return api.NewRepoOperationDTO{}, a.wrap(err)
	}
	return newRepoDTO(rec)
}

func (a newRepoAPI) List(ctx context.Context, project core.ProjectID) ([]api.NewRepoOperationDTO, error) {
	recs, err := a.svc.List(ctx, project)
	if err != nil {
		return nil, a.wrap(err)
	}
	out := make([]api.NewRepoOperationDTO, 0, len(recs))
	for _, r := range recs {
		d, err := newRepoDTO(r)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

func publishRequestOf(req api.PublishPreviewRequest) newrepos.PublishRequest {
	return newrepos.PublishRequest{
		HostInstance: req.HostInstance, Owner: req.Owner, OwnerKind: corenewrepos.OwnerKind(req.OwnerKind),
		Name: req.Name, Visibility: corenewrepos.Visibility(req.Visibility),
	}
}

func (a newRepoAPI) PublishPreview(ctx context.Context, op core.OperationID, req api.PublishPreviewRequest) (api.PublishPreviewResponse, error) {
	p, err := a.svc.PreparePublish(ctx, op, publishRequestOf(req), "")
	if err != nil {
		return api.PublishPreviewResponse{}, a.wrap(err)
	}
	return api.PublishPreviewResponse{
		Hash: p.Hash, Lines: p.Lines, Visibility: api.NewRepoVisibility(p.Spec.Visibility),
		ExpectedHead: p.Spec.ExpectedHead, Problems: p.Problems,
	}, nil
}

// Publish makes the pending publish proposal. The remote is created only after
// the user confirms that proposal.
func (a newRepoAPI) Publish(ctx context.Context, op core.OperationID, req api.PublishNewRepoRequest) (api.ProposalDTO, error) {
	rec, err := a.svc.Get(ctx, op)
	if err != nil {
		return api.ProposalDTO{}, a.wrap(err)
	}
	pubReq := publishRequestOf(req.PublishPreviewRequest)
	pv, err := a.svc.PreparePublish(ctx, op, pubReq, "")
	if err != nil {
		return api.ProposalDTO{}, a.wrap(err)
	}
	if len(pv.Problems) > 0 {
		return api.ProposalDTO{}, apperr.New(apperr.Conflict, "This cannot be published yet: "+strings.Join(pv.Problems, " "), nil)
	}
	if pv.Hash != req.Hash {
		return api.ProposalDTO{}, newrepos.ErrHashMismatch
	}
	action := publishActionOf(op, pv.Spec)
	view, found, err := a.pendingPublish(ctx, rec.SetupTask, action)
	if err != nil {
		return api.ProposalDTO{}, err
	}
	if !found {
		if view, err = a.props.Create(ctx, proposals.CreateRequest{
			Task: rec.SetupTask, Action: action,
			Summary: fmt.Sprintf("Publish %s to %s/%s on %s", rec.Leaf, pv.Spec.Owner, pv.Spec.Name, pv.Spec.HostInstance),
		}); err != nil {
			return api.ProposalDTO{}, a.wrap(err)
		}
	}
	// Remember the confirmed destination under the proposal, so the user's
	// confirmation of the proposal finds it by its hash.
	bound, err := a.svc.PreparePublish(ctx, op, pubReq, view.ID)
	if err != nil || len(bound.Problems) > 0 || bound.Hash != pv.Hash {
		// The proposal would fail on confirm; take it back instead of leaving it.
		if _, rerr := a.props.Reject(ctx, view.ID, "system"); rerr != nil {
			return api.ProposalDTO{}, fmt.Errorf("publish %s: the destination changed and the proposal could not be withdrawn: %w", op, errors.Join(err, rerr))
		}
		if err != nil {
			return api.ProposalDTO{}, a.wrap(err)
		}
		return api.ProposalDTO{}, apperr.New(apperr.Conflict, "The destination changed while the proposal was made, so nothing was proposed. Preview again.", nil)
	}
	return api.NewProposalDTO(view), nil
}

// pendingPublish finds a pending proposal that already holds exactly this
// action, so a repeated request does not make a second one.
func (a newRepoAPI) pendingPublish(ctx context.Context, task core.TaskID, action coreprop.PublishRepo) (proposals.View, bool, error) {
	recs, err := a.store.ListTaskProposalRecords(ctx, task)
	if err != nil {
		return proposals.View{}, false, fmt.Errorf("look for an existing publish proposal: %w", err)
	}
	for _, r := range recs {
		if pa, ok := r.Proposal.Action.(coreprop.PublishRepo); ok && pa == action && r.Proposal.State.Status() == core.ProposalPending {
			v, err := a.props.Get(ctx, r.Proposal.ID)
			return v, err == nil, err
		}
	}
	return proposals.View{}, false, nil
}

func (a newRepoAPI) Retry(ctx context.Context, op core.OperationID, hash string) (api.NewRepoOperationDTO, error) {
	if err := a.svc.Retry(ctx, op, hash); err != nil {
		return api.NewRepoOperationDTO{}, a.wrap(err)
	}
	return a.Get(ctx, op)
}

// publishActionOf converts the publish spec the user previewed into the
// proposal's action. The proposals executor checks that it equals the spec the
// lifecycle remembered.
func publishActionOf(op core.OperationID, s corenewrepos.PublishSpec) coreprop.PublishRepo {
	return coreprop.PublishRepo{
		Operation: op, Repo: s.Repo, HostInstance: s.HostInstance, Owner: s.Owner, OwnerKind: string(s.OwnerKind),
		Name: s.Name, Visibility: string(s.Visibility), Branch: s.Branch, ExpectedHead: s.ExpectedHead, Marker: s.Marker,
	}
}

// ---- DTO mapping ----

// stateCode maps a lifecycle state to the API's state value.
func stateCode(st corenewrepos.State) (api.NewRepoState, error) {
	switch st.(type) {
	case corenewrepos.Reserved:
		return api.NewRepoReserved, nil
	case corenewrepos.AwaitingConfirm:
		return api.NewRepoAwaitingConfirm, nil
	case corenewrepos.Initializing:
		return api.NewRepoInitializing, nil
	case corenewrepos.Ready:
		return api.NewRepoReady, nil
	case corenewrepos.Publishing:
		return api.NewRepoPublishing, nil
	case corenewrepos.Published:
		return api.NewRepoPublished, nil
	case corenewrepos.Uncertain:
		return api.NewRepoUncertain, nil
	case corenewrepos.Stopped:
		return api.NewRepoStopped, nil
	}
	return "", errors.New("unknown new repo state")
}

// newRepoDTO is the backend lifecycle variant for the UI: the state, the
// stages with their progress and, for a parked operation, the immutable spec
// hash the user confirms again on Retry.
func newRepoDTO(rec db.ReservationRecord) (api.NewRepoOperationDTO, error) {
	code, err := stateCode(rec.Operation.State)
	if err != nil {
		return api.NewRepoOperationDTO{}, fmt.Errorf("new repo %s: %w", rec.Operation.ID, err)
	}
	d := api.NewRepoOperationDTO{
		OperationID: rec.Operation.ID, ProjectID: rec.Operation.Project, RepoID: rec.Operation.Repo,
		SetupTaskID: rec.SetupTask, Leaf: rec.Leaf, State: code, ProposalID: rec.Proposal, SpecHash: rec.SpecHash,
	}
	var pos position
	switch st := rec.Operation.State.(type) {
	case corenewrepos.Reserved, corenewrepos.AwaitingConfirm:
		pos = position{flow: flowInit, at: 0, kind: atNone}
	case corenewrepos.Initializing:
		d.Attempt = st.Attempt
		pos = position{flow: flowInit, at: indexOfStage(st.Stage), kind: atCurrent}
	case corenewrepos.Ready:
		pos = position{flow: flowInit, at: len(corenewrepos.InitStages()), kind: atNone}
	case corenewrepos.Publishing:
		d.Attempt = st.Attempt
		pos = position{flow: flowPublish, at: indexOfStage(st.Stage), kind: atCurrent}
	case corenewrepos.Published:
		d.RemoteURL = st.URL
		pos = position{flow: flowPublish, at: len(corenewrepos.PublishStages()), kind: atNone}
	case corenewrepos.Uncertain:
		d.Attempt, d.CanRetry = st.Attempt, true
		d.Reason = "Yggdrasil could not tell whether the step \"" + stageLabel(st.Stage) + "\" finished. It checks this without repeating the step, and a retry needs your confirmation."
		d.SpecHash = resumeHash(st.Resume, rec.SpecHash)
		pos = resumePosition(st.Resume, st.Stage, atUnknown)
	case corenewrepos.Stopped:
		d.Attempt = st.Attempt
		d.Reason = newrepos.Explain(st.Reason)
		_, none := st.Resume.(corenewrepos.ResumeNone)
		d.CanRetry = st.Reason.Retryable() && !none
		d.SpecHash = resumeHash(st.Resume, rec.SpecHash)
		pos = resumePosition(st.Resume, st.Stage, atFailed)
	}
	d.Stages = pos.rows()
	return d, nil
}

func stageLabel(s corenewrepos.Stage) string {
	if s == nil {
		return "unknown"
	}
	return strings.ReplaceAll(s.Name(), "_", " ")
}

func indexOfStage(s corenewrepos.Stage) int {
	switch v := s.(type) {
	case corenewrepos.InitStage:
		return slices.Index(corenewrepos.InitStages(), v)
	case corenewrepos.PublishStage:
		return slices.Index(corenewrepos.PublishStages(), v)
	}
	return 0
}

// resumeHash is the spec hash a retry of the parked stage needs: the init spec
// for initialization, the publish spec for publishing.
func resumeHash(r corenewrepos.Resumable, fallback string) string {
	switch v := r.(type) {
	case corenewrepos.ResumeInit:
		return v.Spec.Hash()
	case corenewrepos.ResumePublish:
		return v.Spec.Hash()
	case corenewrepos.ResumeNone:
	}
	return fallback
}

type flow int

const (
	flowInit flow = iota
	flowPublish
)

type marker int

const (
	atNone marker = iota
	atCurrent
	atUnknown
	atFailed
)

// position is where progress stands: everything before at is done, the stage at
// at carries kind, the rest is pending. A flow of publishing also lists the
// initialization stages, all done.
type position struct {
	flow flow
	at   int
	kind marker
}

func resumePosition(r corenewrepos.Resumable, stage corenewrepos.Stage, kind marker) position {
	switch r.(type) {
	case corenewrepos.ResumePublish:
		return position{flow: flowPublish, at: indexOfStage(stage), kind: kind}
	case corenewrepos.ResumeInit:
		return position{flow: flowInit, at: indexOfStage(stage), kind: kind}
	case corenewrepos.ResumeNone:
	}
	return position{flow: flowInit, at: 0, kind: atNone}
}

func (p position) rows() []api.NewRepoStageDTO {
	var names []string
	for _, s := range corenewrepos.InitStages() {
		names = append(names, s.Name())
	}
	at, nInit := p.at, len(names)
	if p.flow == flowPublish {
		for _, s := range corenewrepos.PublishStages() {
			names = append(names, s.Name())
		}
		at += nInit
	}
	rows := make([]api.NewRepoStageDTO, 0, len(names))
	for i, n := range names {
		st := api.StagePending
		switch {
		case i < at:
			st = api.StageDone
		case i == at && p.kind == atCurrent:
			st = api.StageCurrent
		case i == at && p.kind == atUnknown:
			st = api.StageUnknown
		case i == at && p.kind == atFailed:
			st = api.StageFailed
		}
		rows = append(rows, api.NewRepoStageDTO{Name: n, Status: st})
	}
	return rows
}
