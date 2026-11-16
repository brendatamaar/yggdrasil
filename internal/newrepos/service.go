package newrepos

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corenewrepos "github.com/brendatamaar/yggdrasil/internal/core/newrepos"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// Service is the shell of the new-repo flow (NR-01 to NR-04): it parses at the
// boundary, loads the stored state, calls the core Step, commits state,
// receipts and audit in one Store.Commit, and only then runs the commands.
// Every effect of a confirmed operation runs in one durable worker per
// operation (worker.go). It starts no agent task and calls no agent.
type Service struct {
	d Deps

	ctx    context.Context
	cancel context.CancelFunc
	eg     errgroup.Group

	// applyMu serializes load -> Step -> commit, so two inputs of one
	// operation never interleave.
	applyMu sync.Mutex

	mu      sync.Mutex // guards the fields below
	closed  bool
	workers map[core.OperationID]*worker
	pending map[core.OperationID]pendingPublish
}

type pendingPublish struct {
	spec     corenewrepos.PublishSpec
	hash     string
	proposal core.ProposalID
}

// NewService builds the service. The caller owns the dependencies; Close stops
// the workers but closes nothing else.
func NewService(d Deps) (*Service, error) {
	switch {
	case d.Store == nil || d.FS == nil || d.Git == nil || d.Origin == nil || d.Remote == nil ||
		d.Transport == nil || d.Lock == nil || d.Projects == nil || d.Paths == nil:
		return nil, errors.New("new repos: a dependency is missing")
	case d.Clock == nil || d.IDs == nil:
		return nil, errors.New("new repos: the service needs a clock and an ID source")
	}
	if d.Redact == nil {
		d.Redact = func(s string) string { return s }
	}
	if d.Logger == nil {
		d.Logger = slog.New(slog.DiscardHandler)
	}
	if d.RetryDelay == 0 {
		d.RetryDelay = 250 * time.Millisecond
	}
	if d.StageTimeout == 0 {
		d.StageTimeout = 2 * time.Minute
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{d: d, ctx: ctx, cancel: cancel, workers: map[core.OperationID]*worker{},
		pending: map[core.OperationID]pendingPublish{}}, nil
}

// Close stops every worker and waits for them. A stage that was running
// becomes Uncertain (never failed, never rolled back) and is reconciled by
// Recover on the next start. Close is idempotent.
func (s *Service) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.cancel()
	_ = s.eg.Wait()
	return nil
}

func (s *Service) logErr(msg string, err error, args ...any) {
	s.d.Logger.Error(msg, append(args, "error", s.d.Redact(err.Error()))...)
}

// ---- reserve ----

// ReserveRequest is what the browser sends: names and content, never a path.
type ReserveRequest struct {
	Leaf          string
	README        string
	Ignore        string
	CommitMessage string
	// Agent names the agent of the internal setup draft; the draft never runs.
	Agent string
}

// Preview is what the user confirms. Nothing is reserved or written for it.
type Preview struct {
	Spec     corenewrepos.InitSpec
	Hash     string
	Lines    []string
	Problems []string
}

// RepoIDFor is the deterministic repo ID of a leaf in a project: a leaf is
// unique per project, so the preview hash and the reservation hash agree.
func RepoIDFor(project core.ProjectID, leaf string) core.RepoID {
	sum := sha256.Sum256([]byte("ygg-newrepo\x00" + string(project) + "\x00" + leaf))
	return core.RepoID("rep_" + hex.EncodeToString(sum[:8]))
}

func (s *Service) parseSpec(project core.ProjectID, req ReserveRequest) (corenewrepos.InitSpec, error) {
	if err := project.Validate(); err != nil {
		return corenewrepos.InitSpec{}, fmt.Errorf("%w: the project is not valid", corenewrepos.ErrInvalidSpec)
	}
	// One child name, never a path: checked before anything else is built from it.
	if err := corenewrepos.CheckLeaf(req.Leaf); err != nil {
		return corenewrepos.InitSpec{}, err
	}
	spec := corenewrepos.InitSpec{
		Project: project, Repo: RepoIDFor(project, req.Leaf), Leaf: req.Leaf, Branch: corenewrepos.DefaultBranch,
		README: req.README, Ignore: req.Ignore, CommitMessage: req.CommitMessage,
	}
	if spec.README == "" {
		spec.README = "# " + req.Leaf + "\n"
	}
	if spec.Ignore == "" {
		spec.Ignore = ".env\n"
	}
	if spec.CommitMessage == "" {
		spec.CommitMessage = "Initial commit"
	}
	return corenewrepos.ParseInitSpec(spec)
}

// facts gathers what Reserve needs: the project is tracked, the leaf is free
// and git knows an author. It reads; it writes nothing.
func (s *Service) facts(ctx context.Context, spec corenewrepos.InitSpec) (parent string, f corenewrepos.Reserve, err error) {
	f.Spec = spec
	proj, err := s.d.Projects.GetProject(ctx, spec.Project)
	switch {
	case errors.Is(err, db.ErrNotFound):
		return "", f, nil
	case err != nil:
		return "", f, fmt.Errorf("read project %s: %w", spec.Project, err)
	}
	canon, err := s.d.Paths.ResolveRoot(proj.Path)
	if err != nil {
		return "", f, &RefusedError{Reason: corenewrepos.StopPreconditions}
	}
	f.ProjectTracked = true
	if _, lerr := os.Lstat(filepath.Join(canon, spec.Leaf)); errors.Is(lerr, fs.ErrNotExist) {
		f.LeafFree = true
	} else if lerr != nil {
		return "", f, fmt.Errorf("check %q: %w", spec.Leaf, lerr)
	}
	ok, err := s.d.Git.HasAuthor(ctx, canon)
	if err != nil {
		return "", f, fmt.Errorf("check git author: %w", err)
	}
	f.HasAuthor = ok
	return canon, f, nil
}

func (s *Service) newIDs(spec corenewrepos.InitSpec) corenewrepos.IDs {
	return corenewrepos.IDs{
		Operation: core.OperationID(s.d.IDs("opr")), Repo: spec.Repo,
		SetupTask: core.TaskID(s.d.IDs("tsk")), Proposal: core.ProposalID(s.d.IDs("prp")),
	}
}

// PreviewReserve parses and checks a request and shows what would be created.
// It writes nothing.
func (s *Service) PreviewReserve(ctx context.Context, project core.ProjectID, req ReserveRequest) (Preview, error) {
	spec, err := s.parseSpec(project, req)
	if err != nil {
		return Preview{}, err
	}
	parent, f, err := s.facts(ctx, spec)
	if err != nil {
		return Preview{}, err
	}
	f.IDs, f.Now = s.newIDs(spec), s.d.Clock()
	_, cmds := corenewrepos.Step(corenewrepos.Operation{}, f)
	p := Preview{Spec: spec, Hash: spec.Hash()}
	for _, c := range cmds {
		if e, ok := c.(corenewrepos.Explain); ok {
			p.Problems = append(p.Problems, Explain(e.Reason))
		}
	}
	p.Lines = []string{
		fmt.Sprintf("Create the new folder %q inside the project folder.", filepath.Join(parent, spec.Leaf)),
		fmt.Sprintf("Run git init with the first branch %q.", spec.Branch),
		"Write README.md and .gitignore with exactly the text shown.",
		fmt.Sprintf("Make one first commit: %q.", spec.CommitMessage),
		"Nothing is published and no agent is started.",
	}
	return p, nil
}

// Reserve stores the reservation, the internal setup draft and the audit entry
// in one commit. No folder is created. wantHash, if set, is the hash the user
// previewed; another spec is refused. A repeat of the same reservation returns
// the existing one.
func (s *Service) Reserve(ctx context.Context, project core.ProjectID, req ReserveRequest, wantHash string) (db.ReservationRecord, error) {
	spec, err := s.parseSpec(project, req)
	if err != nil {
		return db.ReservationRecord{}, err
	}
	if wantHash != "" && wantHash != spec.Hash() {
		return db.ReservationRecord{}, ErrHashMismatch
	}
	if existing, err := s.d.Store.GetReservationByRepo(ctx, spec.Repo); err == nil && existing.SpecHash == spec.Hash() {
		return existing, nil
	}
	parent, f, err := s.facts(ctx, spec)
	if err != nil {
		return db.ReservationRecord{}, err
	}
	f.IDs, f.Now = s.newIDs(spec), s.d.Clock()
	op, cmds := corenewrepos.Step(corenewrepos.Operation{}, f)
	if op.State == nil {
		for _, c := range cmds {
			if e, ok := c.(corenewrepos.Explain); ok {
				return db.ReservationRecord{}, &RefusedError{Reason: e.Reason}
			}
		}
		return db.ReservationRecord{}, &RefusedError{Reason: corenewrepos.StopPreconditions}
	}
	agent := req.Agent
	if agent == "" {
		agent = "setup"
	}
	err = s.d.Store.Commit(ctx, func(tx *db.Tx) error {
		if err := tx.ReserveRepo(ctx, db.ReserveRepo{
			Operation: op.ID, Project: spec.Project, Repo: spec.Repo, SetupTask: f.IDs.SetupTask,
			Path: filepath.Join(parent, spec.Leaf), Spec: spec, Agent: agent, Title: "Set up " + spec.Leaf, Now: f.Now,
		}); err != nil {
			return err
		}
		return s.audit(ctx, tx, "user", cmds, f.Now)
	})
	if errors.Is(err, db.ErrConflict) {
		return db.ReservationRecord{}, &RefusedError{Reason: corenewrepos.StopPathExists}
	}
	if err != nil {
		return db.ReservationRecord{}, fmt.Errorf("reserve %q: %w", spec.Leaf, err)
	}
	s.announce(op.ID, op.State)
	rec, err := s.d.Store.GetReservation(ctx, op.ID)
	if err != nil {
		return db.ReservationRecord{}, err
	}
	return rec, nil
}

// Get returns one operation.
func (s *Service) Get(ctx context.Context, op core.OperationID) (db.ReservationRecord, error) {
	return s.d.Store.GetReservation(ctx, op)
}

// List returns the operations of a project.
func (s *Service) List(ctx context.Context, project core.ProjectID) ([]db.ReservationRecord, error) {
	return s.d.Store.ListReservations(ctx, project)
}

// ---- proposal and confirm ----

// AttachProposal records the initialization proposal of a reserved operation
// (ProposalCreated); the proposal's hash must be the spec's hash.
func (s *Service) AttachProposal(ctx context.Context, op core.OperationID, proposal core.ProposalID, hash string) error {
	res, err := s.apply(ctx, op, func(db.ReservationRecord) (corenewrepos.Input, extra) {
		return corenewrepos.ProposalCreated{ID: proposal, Hash: hash}, extra{actor: "system", proposal: proposal}
	})
	if err != nil {
		return err
	}
	if !res.changed {
		return ErrNotApplicable
	}
	return nil
}

// ConfirmInit starts initialization after the user confirmed hash. It returns
// once the durable worker owns the operation.
func (s *Service) ConfirmInit(ctx context.Context, op core.OperationID, hash string, attempt int) error {
	res, err := s.apply(ctx, op, func(db.ReservationRecord) (corenewrepos.Input, extra) {
		return corenewrepos.Confirmed{Hash: hash, Attempt: attempt, Now: s.d.Clock()}, extra{actor: "user"}
	})
	if err != nil {
		return err
	}
	if !res.changed {
		if st, ok := res.before.State.(corenewrepos.AwaitingConfirm); ok && st.Hash != hash {
			return ErrHashMismatch
		}
		return ErrNotApplicable
	}
	return s.startWorker(op, res.effects)
}

// PublishRequest is the destination the user chose.
type PublishRequest struct {
	HostInstance string
	Owner        string
	OwnerKind    corenewrepos.OwnerKind
	Name         string
	// Visibility defaults to private.
	Visibility corenewrepos.Visibility
}

// PublishPreview is what the user confirms to publish.
type PublishPreview struct {
	Spec     corenewrepos.PublishSpec
	Hash     string
	Lines    []string
	Problems []string
}

// PreparePublish builds the immutable publish spec of a ready repo, checks the
// destination account on the server and, when proposal is not empty and there
// is no problem, remembers the spec so ConfirmPublish can find it by its hash.
// It writes nothing and creates nothing on the host. The remembered spec lives
// in memory: after a restart the user previews again.
func (s *Service) PreparePublish(ctx context.Context, op core.OperationID, req PublishRequest, proposal core.ProposalID) (PublishPreview, error) {
	rec, err := s.d.Store.GetReservation(ctx, op)
	if err != nil {
		return PublishPreview{}, err
	}
	ready, ok := rec.Operation.State.(corenewrepos.Ready)
	if !ok {
		return PublishPreview{}, ErrNotApplicable
	}
	vis := req.Visibility
	if vis == "" {
		vis = corenewrepos.VisibilityPrivate
	}
	sum := sha256.Sum256([]byte("ygg-marker\x00" + string(op) + "\x00" + req.HostInstance + "\x00" + req.Owner + "\x00" + req.Name))
	spec, err := corenewrepos.ParsePublishSpec(corenewrepos.PublishSpec{
		Repo: ready.Repo, HostInstance: req.HostInstance, Owner: req.Owner, OwnerKind: req.OwnerKind, Name: req.Name,
		Visibility: vis, Branch: corenewrepos.DefaultBranch, ExpectedHead: ready.InitialHead,
		Marker: hex.EncodeToString(sum[:16]),
	})
	if err != nil {
		return PublishPreview{}, err
	}
	p := PublishPreview{Spec: spec, Hash: spec.Hash()}
	if _, dir, err := s.repoDir(ctx, rec); err != nil {
		return PublishPreview{}, err
	} else if _, exists, oerr := s.d.Origin.Origin(ctx, dir); oerr != nil || exists {
		p.Problems = append(p.Problems, Explain(corenewrepos.StopOriginExists))
	}
	info, err := s.d.Remote.ResolveOwner(ctx, hostInstance(spec), spec.Owner)
	switch {
	case err != nil:
		p.Problems = append(p.Problems, "Yggdrasil could not check that account on the host. Check the host settings and token.")
	case info.Kind != string(spec.OwnerKind) || !info.CanCreate:
		p.Problems = append(p.Problems, "That account is not a "+string(spec.OwnerKind)+" you can create repositories under.")
	}
	where := "private"
	if vis == corenewrepos.VisibilityPublic {
		where = "PUBLIC (anyone can see it)"
	}
	p.Lines = []string{
		fmt.Sprintf("Create a %s repository %s/%s on %s.", where, spec.Owner, spec.Name, spec.HostInstance),
		fmt.Sprintf("Add it as origin and push branch %q at commit %s, without force.", spec.Branch, spec.ExpectedHead[:12]),
		"No token is written into the repo or sent to any other program.",
	}
	if proposal != "" && len(p.Problems) == 0 {
		s.mu.Lock()
		s.pending[op] = pendingPublish{spec: spec, hash: p.Hash, proposal: proposal}
		s.mu.Unlock()
	}
	return p, nil
}

// ConfirmPublish starts publishing a ready repo after the user confirmed hash.
func (s *Service) ConfirmPublish(ctx context.Context, op core.OperationID, hash string, attempt int) error {
	s.mu.Lock()
	pend, ok := s.pending[op]
	s.mu.Unlock()
	if !ok || pend.hash != hash {
		return ErrHashMismatch
	}
	rec, err := s.d.Store.GetReservation(ctx, op)
	if err != nil {
		return err
	}
	_, dir, err := s.repoDir(ctx, rec)
	if err != nil {
		return err
	}
	_, exists, err := s.d.Origin.Origin(ctx, dir)
	if err != nil {
		exists = true // an origin we cannot even read is not "absent"
	}
	fc, found, err := s.d.Git.ReadFirstCommit(ctx, dir, pend.spec.Branch)
	headOK := err == nil && found && fc.Commit == pend.spec.ExpectedHead
	res, err := s.apply(ctx, op, func(db.ReservationRecord) (corenewrepos.Input, extra) {
		return corenewrepos.PublishConfirmed{Spec: pend.spec, Hash: hash, Attempt: attempt, OriginAbsent: !exists,
			HeadMatches: headOK, Now: s.d.Clock()}, extra{actor: "user", publishProposal: pend.proposal}
	})
	if err != nil {
		return err
	}
	if !res.changed {
		if len(res.explain) > 0 {
			return &RefusedError{Reason: res.explain[0]}
		}
		return ErrNotApplicable
	}
	s.mu.Lock()
	delete(s.pending, op)
	s.mu.Unlock()
	return s.startWorker(op, res.effects)
}

// ---- reconcile, reject, untracked, recover ----

// ReconcileOperation runs the read-only reconciliation of an operation a crash
// left Initializing, Publishing or Uncertain. An operation that a live worker
// owns is left alone.
func (s *Service) ReconcileOperation(ctx context.Context, op core.OperationID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reconcileLocked(ctx, op)
}

func (s *Service) reconcileLocked(ctx context.Context, op core.OperationID) error {
	if s.closed {
		return ErrClosed
	}
	if _, busy := s.workers[op]; busy {
		return nil
	}
	rec, err := s.d.Store.GetReservation(ctx, op)
	if err != nil {
		return err
	}
	switch st := rec.Operation.State.(type) {
	case corenewrepos.Initializing, corenewrepos.Publishing:
		res, err := s.apply(ctx, op, func(db.ReservationRecord) (corenewrepos.Input, extra) {
			return corenewrepos.Recovered{Now: s.d.Clock()}, extra{actor: "system"}
		})
		if err != nil {
			return err
		}
		return s.startLocked(op, res.effects)
	case corenewrepos.Uncertain:
		// Step answers Recovered only for running states; an operation loaded
		// as Uncertain gets its read-only check re-issued here.
		return s.startLocked(op, []corenewrepos.Command{corenewrepos.ReconcileStage{Operation: op, Attempt: st.Attempt, Stage: st.Stage}})
	case corenewrepos.Reserved, corenewrepos.AwaitingConfirm, corenewrepos.Ready, corenewrepos.Published, corenewrepos.Stopped:
	}
	return nil
}

// Recover is called once at startup: every operation left Initializing or
// Publishing becomes Uncertain and is reconciled read-only, and every one
// loaded as Uncertain gets its reconciliation re-issued. Nothing is repeated.
func (s *Service) Recover(ctx context.Context) error {
	recs, err := s.d.Store.ListUnsettledReservations(ctx)
	if err != nil {
		return fmt.Errorf("recover new repos: %w", err)
	}
	var errs []error
	for _, r := range recs {
		if err := s.ReconcileOperation(ctx, r.Operation.ID); err != nil {
			s.logErr("recover new repo", err, "operation", r.Operation.ID)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Reject records that the user rejected the pending proposal. Nothing on disk
// is touched.
func (s *Service) Reject(ctx context.Context, op core.OperationID) error {
	res, err := s.apply(ctx, op, func(db.ReservationRecord) (corenewrepos.Input, extra) {
		return corenewrepos.Reject{Now: s.d.Clock()}, extra{actor: "user"}
	})
	if err != nil {
		return err
	}
	if !res.changed {
		return ErrNotApplicable
	}
	return nil
}

// Untracked records that the project or repo was untracked: the operation
// stops and its worker is cancelled. Reserved folders and files stay on disk.
func (s *Service) Untracked(ctx context.Context, op core.OperationID) error {
	res, err := s.apply(ctx, op, func(db.ReservationRecord) (corenewrepos.Input, extra) {
		return corenewrepos.Untracked{Now: s.d.Clock()}, extra{actor: "user"}
	})
	if err != nil {
		return err
	}
	if res.cancel {
		s.cancelWorker(op)
	}
	if !res.changed {
		return ErrNotApplicable
	}
	return nil
}

// ---- paths ----

// parentOf is the canonical folder of the tracked project, resolved again at
// every stage: a parent swapped since the preview is caught by the platform
// filesystem's own checks as well.
func (s *Service) parentOf(ctx context.Context, project core.ProjectID) (string, error) {
	p, err := s.d.Projects.GetProject(ctx, project)
	switch {
	case errors.Is(err, db.ErrNotFound):
		return "", definiteErr(corenewrepos.StopUntracked, err)
	case err != nil:
		return "", definiteErr(corenewrepos.StopPreconditions, err)
	}
	canon, err := s.d.Paths.ResolveRoot(p.Path)
	if err != nil {
		return "", definiteErr(corenewrepos.StopPreconditions, err)
	}
	return canon, nil
}

// repoDir returns the project parent and the repo folder of an operation.
func (s *Service) repoDir(ctx context.Context, rec db.ReservationRecord) (parent, dir string, err error) {
	parent, err = s.parentOf(ctx, rec.Operation.Project)
	if err != nil {
		return "", "", err
	}
	return parent, filepath.Join(parent, rec.Leaf), nil
}
