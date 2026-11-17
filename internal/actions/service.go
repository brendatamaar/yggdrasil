package actions

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/core"
	coreactions "github.com/brendatamaar/yggdrasil/internal/core/actions"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// ErrClosed is returned once the service is shut down.
var ErrClosed = errors.New("the action service is shutting down")

// Scope names what a catalogue is for: exactly one of a project or a repo.
type Scope struct {
	Project core.ProjectID
	Repo    core.RepoID
}

// RepoOfferings are the offerings of one tracked, ready repo.
type RepoOfferings struct {
	Repo      core.RepoID
	Offerings []coreactions.Offering
}

// Catalogue is what Offerings returns.
type Catalogue struct {
	Repos []RepoOfferings
}

// Input is one explicit submission. ExpectedHead and Digest are copied from the
// preview when the caller has one; both are optional.
type Input struct {
	Request      coreactions.Request
	Digest       string
	ExpectedHead string
}

// Preview is what an action would attach and do, before anything starts. It
// writes nothing.
type Preview struct {
	Request      coreactions.Request
	Digest       string
	Mode         core.TaskMode
	Effect       coreactions.Effect
	Context      coreactions.PreparedContext
	ExpectedHead string
	// Refusal and Message are set when the action cannot be offered now.
	Refusal coreactions.RefuseReason
	Message string
}

// Outcome is the result of a dispatch or a lookup. Result holds IDs only.
type Outcome struct {
	RequestID core.RequestID
	// State is submitting, completed, refused or cancelled.
	State   db.ActionState
	Result  coreactions.Result
	Refusal coreactions.RefuseReason
	// Message says plainly what happened or why nothing did.
	Message string
	// Replayed is true when an earlier submission of the same request answered.
	Replayed bool
}

// Service is the imperative shell of workspace actions. It parses requests at
// the boundary, loads state, calls the core Step, persists state and audit in
// one Store.Commit and then interprets the commands.
type Service struct {
	d      Deps
	ctx    context.Context
	cancel context.CancelFunc
	g      *errgroup.Group

	// retryEvery is how often an unresolved request is reconciled again.
	retryEvery time.Duration

	mu       sync.Mutex
	closed   bool
	inflight map[core.RequestID]chan struct{}
	retrying map[core.RequestID]bool
	once     sync.Once
}

// New builds the service. Missing required ports are a programming error and
// are reported, not defaulted.
func New(d Deps) (*Service, error) {
	switch {
	case d.Scope == nil, d.Context == nil, d.Tasks == nil, d.Proposals == nil, d.Requests == nil:
		return nil, errors.New("actions: the scope, context, task, proposal and request ports are required")
	}
	if d.Clock == nil {
		d.Clock = time.Now
	}
	if d.IDs == nil {
		d.IDs = db.NewID
	}
	if d.Redactor == nil {
		d.Redactor = safety.NewRedactor()
	}
	ctx, cancel := context.WithCancel(context.Background())
	g, gctx := errgroup.WithContext(ctx)
	return &Service{
		d: d, ctx: gctx, cancel: cancel, g: g, retryEvery: 5 * time.Second,
		inflight: map[core.RequestID]chan struct{}{}, retrying: map[core.RequestID]bool{},
	}, nil
}

// Close stops every worker and waits for them. It is idempotent.
func (s *Service) Close() error {
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		s.cancel()
	})
	_ = s.g.Wait()
	return nil
}

func (s *Service) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// acquire serialises work on one RequestID. The returned release must be called
// exactly once, by whoever finishes the work last.
func (s *Service) acquire(ctx context.Context, id core.RequestID) (func(), error) {
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return nil, ErrClosed
		}
		ch, busy := s.inflight[id]
		if !busy {
			ch = make(chan struct{})
			s.inflight[id] = ch
			s.mu.Unlock()
			return func() {
				s.mu.Lock()
				delete(s.inflight, id)
				s.mu.Unlock()
				close(ch)
			}, nil
		}
		s.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// Offerings returns the catalogue over the current tracked READY repos of a
// scope. A repo that is untracked, reserved or unknown is db.ErrNotFound; a
// project lists only its ready repos. No path or host URL is ever an input.
func (s *Service) Offerings(ctx context.Context, scope Scope) (Catalogue, error) {
	if s.isClosed() {
		return Catalogue{}, ErrClosed
	}
	switch {
	case (scope.Project == "") == (scope.Repo == ""):
		return Catalogue{}, fmt.Errorf("%w: name one project or one repo", coreactions.ErrInvalidRequest)
	case scope.Repo != "":
		if err := scope.Repo.Validate(); err != nil {
			return Catalogue{}, fmt.Errorf("%w: the repo id is not valid", coreactions.ErrInvalidRequest)
		}
		f, err := s.d.Scope.Facts(ctx, scope.Repo)
		if err != nil {
			return Catalogue{}, fmt.Errorf("offerings of repo %s: %w", scope.Repo, err)
		}
		return Catalogue{Repos: []RepoOfferings{{Repo: scope.Repo, Offerings: coreactions.Catalogue(f)}}}, nil
	}
	if err := scope.Project.Validate(); err != nil {
		return Catalogue{}, fmt.Errorf("%w: the project id is not valid", coreactions.ErrInvalidRequest)
	}
	repos, err := s.d.Scope.ReposOf(ctx, scope.Project)
	if err != nil {
		return Catalogue{}, fmt.Errorf("offerings of project %s: %w", scope.Project, err)
	}
	var out Catalogue
	for _, id := range repos {
		f, err := s.d.Scope.Facts(ctx, id)
		if errors.Is(err, db.ErrNotFound) {
			continue // untracked between the two reads
		}
		if err != nil {
			return Catalogue{}, fmt.Errorf("offerings of repo %s: %w", id, err)
		}
		out.Repos = append(out.Repos, RepoOfferings{Repo: id, Offerings: coreactions.Catalogue(f)})
	}
	return out, nil
}

func (s *Service) checkInput(in Input) error {
	if err := in.Request.Validate(); err != nil {
		return err
	}
	if in.Digest != "" && in.Digest != in.Request.Digest() {
		return db.ErrDigestMismatch
	}
	return nil
}

// Preview shows what an action would attach and do. It reads the tracked scope
// and the bounded context and persists nothing.
func (s *Service) Preview(ctx context.Context, req coreactions.Request) (Preview, error) {
	if s.isClosed() {
		return Preview{}, ErrClosed
	}
	if err := req.Validate(); err != nil {
		return Preview{}, err
	}
	r := &run{s: s, preview: true}
	r.sub = coreactions.Submission{}
	r.feed(ctx, coreactions.Requested{Request: req, IDs: s.newIDs(req.Kind), Now: s.d.Clock()})
	if r.err != nil {
		return Preview{}, r.err
	}
	p := Preview{Request: req, Digest: req.Digest(), Mode: req.Kind.Mode(), Effect: req.Kind.Effect()}
	switch st := r.sub.State.(type) {
	case coreactions.Ready:
		p.Context, p.ExpectedHead = st.Context, st.ExpectedHead
		if r.l != nil {
			p.Effect = effectOf(req.Kind, st.Context)
		}
	case coreactions.Refused:
		p.Refusal, p.Message = st.Reason, message(st.Reason)
	}
	return p, nil
}

func effectOf(k coreactions.Kind, c coreactions.PreparedContext) coreactions.Effect {
	return coreactions.Dispatch{Request: coreactions.Request{Kind: k}, Context: c}.Effect()
}

// newIDs makes the identifiers the core carries for the request's effects. The
// task service names a task itself, so IDs.Task only has to be well formed.
func (s *Service) newIDs(k coreactions.Kind) coreactions.IDs {
	switch k.Effect() {
	case coreactions.EffectStartPull:
		return coreactions.IDs{Proposals: []core.ProposalID{core.ProposalID(s.d.IDs("prp"))}}
	}
	return coreactions.IDs{Task: core.TaskID(s.d.IDs("tsk"))}
}

// Get returns the stored outcome of a request without changing anything.
func (s *Service) Get(ctx context.Context, id core.RequestID) (Outcome, error) {
	if s.isClosed() {
		return Outcome{}, ErrClosed
	}
	if err := id.Validate(); err != nil {
		return Outcome{}, fmt.Errorf("%w: the request id is not valid", coreactions.ErrInvalidRequest)
	}
	rec, err := s.d.Requests.GetActionRequest(ctx, id)
	if err != nil {
		return Outcome{}, fmt.Errorf("get action request %s: %w", id, err)
	}
	return outcomeOfRecord(rec), nil
}

func outcomeOfRecord(rec db.ActionRecord) Outcome {
	o := Outcome{
		RequestID: rec.Request.RequestID, State: rec.State, Refusal: rec.Refusal,
		Result: coreactions.Result{TaskID: rec.Task, ProposalIDs: rec.Proposals, BatchID: rec.Batch},
	}
	switch rec.State {
	case db.ActionCompleted:
		o.Message = completedMessage(rec.Request.Kind)
	case db.ActionRefused:
		o.Message = message(rec.Refusal)
	case db.ActionSubmitting:
		o.Message = "This request is being finished. It will not be started twice."
	case db.ActionCancelled:
		o.Message = "This request was cancelled."
	}
	return o
}

func completedMessage(k coreactions.Kind) string {
	switch k.Effect() {
	case coreactions.EffectStartPull:
		return "A pull proposal was created. Confirm it in Review to run it."
	case coreactions.EffectDraftTask:
		return "Saved as a draft. Nothing runs until you start it."
	}
	return "The task was started."
}

func apiState(s db.ActionState) api.ActionRequestState { return api.ActionRequestState(s) }

// Dispatch performs one explicit submission exactly once per RequestID. A
// repeat returns the first result; a repeat with a changed request is
// db.ErrDigestMismatch. Nothing is confirmed here: proposals stay pending.
func (s *Service) Dispatch(ctx context.Context, in Input) (Outcome, error) {
	if s.isClosed() {
		return Outcome{}, ErrClosed
	}
	if err := s.checkInput(in); err != nil {
		return Outcome{}, err
	}
	id := in.Request.RequestID
	release, err := s.acquire(ctx, id)
	if err != nil {
		return Outcome{}, err
	}
	handed := false
	defer func() {
		if !handed {
			release()
		}
	}()

	rec, err := s.d.Requests.GetActionRequest(ctx, id)
	switch {
	case err == nil:
		if rec.Digest != in.Request.Digest() {
			return Outcome{}, db.ErrDigestMismatch
		}
		out, rerr := s.replay(ctx, rec)
		out.Replayed = true
		return out, rerr
	case !errors.Is(err, db.ErrNotFound):
		return Outcome{}, fmt.Errorf("look up action request %s: %w", id, err)
	}

	r := &run{s: s, expected: in.ExpectedHead, release: release}
	r.feed(ctx, coreactions.Requested{Request: in.Request, IDs: s.newIDs(in.Request.Kind), Now: s.d.Clock()})
	if r.err == nil {
		if ready, ok := r.sub.State.(coreactions.Ready); ok {
			head := coreactions.Pin(r.l.facts, in.Request.Kind)
			if in.ExpectedHead != "" && in.ExpectedHead != ready.ExpectedHead {
				head = in.ExpectedHead // the preview the user saw is not the state now
			}
			r.expectedPin = ready.ExpectedHead
			r.feed(ctx, coreactions.SubmitRequested{ScopeHead: head, Now: s.d.Clock()})
		}
	}
	if r.handed {
		handed = true
		return r.wait(ctx)
	}
	if r.err != nil {
		return Outcome{}, r.err
	}
	return r.outcome(), nil
}

// replay answers a request that already has a row. A row left submitting is
// reconciled read-only (never submitted again).
func (s *Service) replay(ctx context.Context, rec db.ActionRecord) (Outcome, error) {
	if rec.State != db.ActionSubmitting {
		return outcomeOfRecord(rec), nil
	}
	return s.reconcileRecord(ctx, rec)
}

// reconcileRecord rebuilds the Submitting state from its row and feeds
// Recovered; the core answers with a read-only lookup of the durable link.
func (s *Service) reconcileRecord(ctx context.Context, rec db.ActionRecord) (Outcome, error) {
	r := &run{s: s, rec: &rec}
	r.sub = coreactions.Submission{
		Request: rec.Request, Digest: rec.Digest, Seq: rec.Attempt, Version: rec.Version,
		State: coreactions.Submitting{Attempt: rec.Attempt, Key: rec.Key},
	}
	r.feed(ctx, coreactions.Recovered{Now: s.d.Clock()})
	if r.err != nil {
		return outcomeOfRecord(rec), r.err
	}
	if _, still := r.sub.State.(coreactions.Submitting); still {
		s.retryLater(rec.Request.RequestID)
	}
	return r.outcome(), nil
}

// Recover reconciles every request left submitting by a crash. It is read-only
// toward the effects: a request whose task or proposals exist completes, one
// with nothing behind it is refused so the user can retry, and one that cannot
// be answered yet is retried in the background. Nothing is submitted twice.
func (s *Service) Recover(ctx context.Context) error {
	if s.isClosed() {
		return ErrClosed
	}
	recs, err := s.d.Requests.ListUnsettledActionRequests(ctx)
	if err != nil {
		return fmt.Errorf("list unsettled action requests: %w", err)
	}
	var first error
	for _, rec := range recs {
		release, aerr := s.acquire(ctx, rec.Request.RequestID)
		if aerr != nil {
			return aerr
		}
		cur, gerr := s.d.Requests.GetActionRequest(ctx, rec.Request.RequestID)
		if gerr == nil && cur.State == db.ActionSubmitting {
			_, gerr = s.reconcileRecord(ctx, cur)
		}
		release()
		if gerr != nil && first == nil {
			first = fmt.Errorf("recover action request %s: %w", rec.Request.RequestID, gerr)
		}
	}
	return first
}

// retryLater keeps reconciling one unresolved request until it is answered or
// the service stops. No timer drops an unresolved intent.
func (s *Service) retryLater(id core.RequestID) {
	s.mu.Lock()
	if s.closed || s.retrying[id] {
		s.mu.Unlock()
		return
	}
	s.retrying[id] = true
	s.mu.Unlock()
	s.g.Go(func() error {
		defer func() {
			s.mu.Lock()
			delete(s.retrying, id)
			s.mu.Unlock()
		}()
		t := time.NewTicker(s.retryEvery)
		defer t.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return nil
			case <-t.C:
			}
			release, err := s.acquire(s.ctx, id)
			if err != nil {
				return nil
			}
			rec, gerr := s.d.Requests.GetActionRequest(s.ctx, id)
			done := gerr != nil || rec.State != db.ActionSubmitting
			if !done {
				r := &run{s: s, rec: &rec, quiet: true}
				r.sub = coreactions.Submission{
					Request: rec.Request, Digest: rec.Digest, Seq: rec.Attempt, Version: rec.Version,
					State: coreactions.Submitting{Attempt: rec.Attempt, Key: rec.Key},
				}
				r.feed(s.ctx, coreactions.Recovered{Now: s.d.Clock()})
				_, still := r.sub.State.(coreactions.Submitting)
				done = !still || r.err != nil && errors.Is(r.err, context.Canceled)
			}
			release()
			if done {
				return nil
			}
		}
	})
}
