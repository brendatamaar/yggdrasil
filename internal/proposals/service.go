package proposals

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/mainlock"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// RepoInfo is a repo as the proposals shell needs it.
type RepoInfo struct {
	ID   core.RepoID
	Path string
	// Tracked is false for a repo the user untracked: nothing may run in it
	// (invariant 6).
	Tracked bool
	// Name is the repo's display name.
	Name string
	// DefaultBranch is the default branch stored for the repo ("" if unknown).
	DefaultBranch string
}

// WorktreeInfo is a task worktree.
type WorktreeInfo struct {
	ID     core.WorktreeID
	Repo   core.RepoID
	Task   core.TaskID
	Path   string
	Branch string
	// Name is the worktree's folder name; Status its row status.
	Name   string
	Status core.WorktreeStatus
}

// TaskInfo is the scope of a task.
type TaskInfo struct {
	ID    core.TaskID
	Repos []core.RepoID
}

// Resolver reads repos, worktrees and tasks. Each method returns an error
// wrapping db.ErrNotFound when the thing does not exist.
type Resolver interface {
	Repo(ctx context.Context, id core.RepoID) (RepoInfo, error)
	Worktree(ctx context.Context, id core.WorktreeID) (WorktreeInfo, error)
	Task(ctx context.Context, id core.TaskID) (TaskInfo, error)
}

// BuildOutcome is how a build ended.
type BuildOutcome struct {
	Succeeded bool
	// Reason is plain text for a failure or cancellation.
	Reason string
}

// Builds runs the install step of a proposal through the builds service.
type Builds interface {
	// Enqueue queues the single install step of the repo's toolchain (step is
	// the toolchain step ID) in the main checkout and returns the build ID.
	// owner names the proposal ("proposal:<id>") for the builds audit trail.
	// The builds service takes the main-checkout lock itself.
	Enqueue(ctx context.Context, repo core.RepoID, step, owner string) (core.BuildID, error)
	// Wait blocks until the build ends.
	Wait(ctx context.Context, id core.BuildID) (BuildOutcome, error)
}

// HostKind says which git host a remote belongs to.
type HostKind string

const (
	HostGitLab  HostKind = "gitlab"
	HostGitHub  HostKind = "github"
	HostGitea   HostKind = "gitea"
	HostUnknown HostKind = "unknown"
)

// Hosts classifies a remote URL. The URL may carry credentials: implementations
// must not log it.
type Hosts interface {
	Kind(remoteURL string) HostKind
}

// Event tells subscribers (the hub) that a proposal changed.
type Event struct {
	ID     core.ProposalID
	Task   core.TaskID
	Repo   core.RepoID
	Status core.ProposalStatus
}

// Publisher receives Events; it must not block.
type Publisher interface{ Publish(Event) }

// Config wires the service. Every field except Publisher, Logger, Clock, NewID,
// Redactor and ExecTimeout is required.
type Config struct {
	Store    *db.Store
	Git      git.Writer
	Resolver Resolver
	Lock     mainlock.MainLock
	Builds   Builds
	Hosts    Hosts

	Publisher Publisher
	Logger    *slog.Logger
	Redactor  safety.Redactor
	Clock     func() time.Time
	// NewID makes an ID with a prefix ("prp", "cs", "aud"); default db.NewID.
	NewID func(prefix string) string
	// ExecTimeout bounds one executor run; default 10 minutes (installs may
	// take long, so it only applies to git).
	ExecTimeout time.Duration
	// InstallTimeout bounds the wait for an install build; default 20 minutes.
	InstallTimeout time.Duration

	// Landing (merge_local and remove_worktree, RV-05, RV-06). Landing and
	// Reader default to Git when it implements them; Platform defaults to the
	// real one. Marks, Live and WorktreeRoot are needed to run these two kinds;
	// without them the executors refuse and change nothing.
	Landing      git.LandingGit
	Reader       LandingReader
	Platform     platform.Landing
	Marks        WorktreeMarks
	Live         LiveProbe
	WorktreeRoot string
	// StageID makes the ID of a removal's staging folder (1-64 of
	// [A-Za-z0-9_-]); default derives one from NewID.
	StageID func() string
}

// Service creates, decides and executes proposals (RV-02, GH-03).
type Service struct {
	cfg Config

	ctx    context.Context
	cancel context.CancelFunc
	group  errgroup.Group

	qmu     sync.Mutex
	queues  map[string][]func()
	running map[string]bool
	closed  bool
}

// New checks cfg and returns a service. Call Start to run recovery, and Close
// on shutdown.
func New(cfg Config) (*Service, error) {
	switch {
	case cfg.Store == nil:
		return nil, errors.New("proposals: no store")
	case cfg.Git == nil:
		return nil, errors.New("proposals: no git writer")
	case cfg.Resolver == nil:
		return nil, errors.New("proposals: no resolver")
	case cfg.Lock == nil:
		return nil, errors.New("proposals: no main-checkout lock")
	case cfg.Builds == nil:
		return nil, errors.New("proposals: no builds service")
	case cfg.Hosts == nil:
		return nil, errors.New("proposals: no hosts")
	}
	if _, ok := cfg.Git.(BranchReader); !ok {
		return nil, errors.New("proposals: the git writer cannot read the current branch")
	}
	if _, ok := cfg.Git.(StagedReader); !ok {
		return nil, errors.New("proposals: the git writer cannot list staged files")
	}
	if cfg.Landing == nil {
		cfg.Landing, _ = cfg.Git.(git.LandingGit)
	}
	if cfg.Reader == nil {
		cfg.Reader, _ = cfg.Git.(LandingReader)
	}
	if cfg.Platform == nil {
		cfg.Platform = platform.RealLanding{}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.Redactor == nil {
		cfg.Redactor = safety.NewRedactor()
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.NewID == nil {
		cfg.NewID = db.NewID
	}
	if cfg.StageID == nil {
		newID := cfg.NewID
		cfg.StageID = func() string { return newID("rm") }
	}
	if cfg.InstallTimeout <= 0 {
		cfg.InstallTimeout = 20 * time.Minute
	}
	if cfg.ExecTimeout <= 0 {
		cfg.ExecTimeout = 10 * time.Minute
	}
	return &Service{cfg: cfg, queues: map[string][]func(){}, running: map[string]bool{}}, nil
}

// Start sets the context that owns every executor goroutine and reconciles the
// proposals that were Confirmed when the last run ended. It never executes
// anything on its own (docs/design/proposals.md decision 4).
func (s *Service) Start(ctx context.Context) error {
	s.qmu.Lock()
	if s.ctx != nil {
		s.qmu.Unlock()
		return errors.New("proposals: already started")
	}
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.qmu.Unlock()
	return s.recover(s.ctx)
}

// Close stops accepting work, cancels running executors and waits for them.
// A proposal whose executor was cut short stays Confirmed and is reconciled
// at the next start.
func (s *Service) Close() {
	s.qmu.Lock()
	s.closed = true
	cancel := s.cancel
	s.qmu.Unlock()
	if cancel != nil {
		cancel()
	}
	_ = s.group.Wait()
}

func (s *Service) now() time.Time { return s.cfg.Clock().UTC() }

func (s *Service) redact(text string) string { return s.cfg.Redactor.Redact(text) }

// Errors the API maps to responses.
var (
	// ErrRefused is wrapped by *Refusal.
	ErrRefused = errors.New("proposal refused")
	// ErrAlreadyDecided: the proposal is not in a state this input applies to.
	ErrAlreadyDecided = errors.New("proposal already decided")
	// ErrNotStarted: Start was not called, or the service is closed.
	ErrNotStarted = errors.New("proposals service is not running")
)

// Refusal is a plain-language "no" for a request. No state changed.
type Refusal struct {
	Reason  coreprop.RefuseReason
	Message string
}

func (r *Refusal) Error() string        { return "proposal refused: " + string(r.Reason) + ": " + r.Message }
func (r *Refusal) Is(target error) bool { return target == ErrRefused }

// UserMessage is the text to show.
func (r *Refusal) UserMessage() string { return r.Message }

func refusal(reason coreprop.RefuseReason, detail string) *Refusal {
	msg := detail
	if msg == "" {
		switch reason {
		case coreprop.RefuseHashMismatch:
			msg = "What you confirmed is not what this proposal now says. Reload the page and review it again."
		case coreprop.RefuseRepoUntracked:
			msg = "This repo is no longer tracked, so nothing can run in it. Track the repo again first."
		case coreprop.RefuseTooMany:
			msg = "This task already has the maximum number of pending proposals. Confirm or reject some first."
		case coreprop.RefuseUnsupported:
			msg = "This kind of proposal is not supported yet."
		default:
			msg = "The proposal was refused because something it names is not allowed. Check the files, branch and remote."
		}
	}
	return &Refusal{Reason: reason, Message: msg}
}

// View is a proposal for the API: the stored header, the exact lines the user
// confirms, and the outcome so far.
type View struct {
	ID core.ProposalID
	// RepoName is the repo's display name; Remote is the host a push or merge
	// request goes to. Neither is part of the action hash.
	RepoName   string
	Remote     string
	ChangeSet  core.ChangeSetID
	Task       core.TaskID
	Repo       core.RepoID
	Kind       core.ProposalKind
	Status     core.ProposalStatus
	FromAgent  bool
	Summary    string
	ActionHash string
	// Lines is Describe(action): shown to the user instead of only Summary.
	Lines     []string
	CreatedAt time.Time
	// DecidedAt is the time of the last status change after creation.
	DecidedAt *time.Time
	// Attempt is the execution attempt (0 while pending or rejected).
	Attempt   int
	FailKind  string
	Message   string
	Retryable bool
	Result    *coreprop.Result
}

func viewOf(p coreprop.Proposal) View {
	v := View{
		ID: p.ID, ChangeSet: p.ChangeSet, Task: p.Task, Repo: p.Repo, Kind: p.Action.Kind(), Status: p.State.Status(),
		Summary: p.Summary, ActionHash: p.ActionHash, CreatedAt: p.Created,
	}
	_, v.FromAgent = p.Source.(coreprop.AgentSource)
	for _, l := range coreprop.Describe(p.Action) {
		v.Lines = append(v.Lines, l.Text)
	}
	switch st := p.State.(type) {
	case coreprop.Confirmed:
		v.Attempt = st.Attempt
		v.DecidedAt = &st.At
	case coreprop.Rejected:
		v.DecidedAt = &st.At
	case coreprop.Executed:
		v.DecidedAt = &st.At
		r := st.Result
		v.Result = &r
	case coreprop.Failed:
		v.Attempt, v.DecidedAt = st.Attempt, &st.At
		v.FailKind, v.Message, v.Retryable = string(st.Kind), st.Message, st.Retryable
	}
	return v
}

// Get returns one proposal.
func (s *Service) Get(ctx context.Context, id core.ProposalID) (View, error) {
	if err := id.Validate(); err != nil {
		return View{}, fmt.Errorf("get proposal: %w", db.ErrNotFound)
	}
	rec, err := s.cfg.Store.GetProposalRecord(ctx, id)
	if err != nil {
		return View{}, fmt.Errorf("get proposal %s: %w", id, err)
	}
	return s.view(ctx, rec.Proposal), nil
}

// ListFilter selects proposals; set Task, or Status, or both.
type ListFilter struct {
	Task   core.TaskID
	Status core.ProposalStatus
}

// List returns proposals oldest first.
func (s *Service) List(ctx context.Context, f ListFilter) ([]View, error) {
	var recs []db.ProposalRecord
	var err error
	switch {
	case f.Task != "":
		recs, err = s.cfg.Store.ListTaskProposalRecords(ctx, f.Task)
	case f.Status != "":
		recs, err = s.cfg.Store.ListProposalRecords(ctx, f.Status)
	default:
		return nil, errors.New("list proposals: give a task or a status")
	}
	if err != nil {
		return nil, fmt.Errorf("list proposals: %w", err)
	}
	out := make([]View, 0, len(recs))
	for _, r := range recs {
		if f.Status != "" && r.Proposal.State.Status() != f.Status {
			continue
		}
		out = append(out, s.view(ctx, r.Proposal))
	}
	return out, nil
}

// PendingCount is the number of proposals of a task waiting for the user.
func (s *Service) PendingCount(ctx context.Context, task core.TaskID) (int, error) {
	return s.cfg.Store.GetPendingProposalCount(ctx, task)
}

// CreateRequest is a proposal to create. Action comes from
// coreprop.ParseAction (the MCP or API boundary).
type CreateRequest struct {
	Task    core.TaskID
	Action  coreprop.Action
	Summary string
	// Source is AgentSource{Task} for an agent, UserSource{} for the user;
	// nil means the user.
	Source coreprop.Source
}

const maxSummary = 2000

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// errRefused carries a Refusal out of a Commit function so the transaction
// rolls back (including the change set made for it).
type errRefused struct{ r *Refusal }

func (e errRefused) Error() string { return e.r.Error() }

// Create checks the facts, stores a pending proposal and its change set and
// audit entry in one transaction, and publishes it. It runs nothing.
func (s *Service) Create(ctx context.Context, req CreateRequest) (View, error) {
	if req.Action == nil {
		return View{}, refusal(coreprop.RefuseUnsupported, "")
	}
	if err := req.Task.Validate(); err != nil {
		return View{}, refusal(coreprop.RefuseBadFacts, "The task for this proposal is not valid.")
	}
	facts, repo, detail, err := s.gatherFacts(ctx, req)
	if err != nil {
		return View{}, err
	}
	if facts.RepoTracked && facts.PathsOK && facts.RemoteSupported {
		if req.Action, err = s.fillExpected(ctx, req.Action); err != nil {
			return View{}, err
		}
	}
	src := req.Source
	if src == nil {
		src = coreprop.UserSource{}
	}
	id := core.ProposalID(s.cfg.NewID("prp"))
	now := s.now()
	var created coreprop.Proposal
	var cmds []coreprop.Command
	err = s.cfg.Store.Commit(ctx, func(tx *db.Tx) error {
		cs, err := tx.EnsureChangeSet(ctx, req.Task, core.ChangeSetID(s.cfg.NewID("cs")), now)
		if err != nil {
			return err
		}
		pending, err := tx.CountPendingProposals(ctx, req.Task)
		if err != nil {
			return err
		}
		f := facts
		f.PendingForTask, f.TaskChangeSet = pending, cs
		var p coreprop.Proposal
		p, cmds = coreprop.Step(coreprop.Proposal{}, coreprop.Created{
			Proposal: coreprop.Proposal{
				ID: id, ChangeSet: cs, Task: req.Task, Repo: repo, Source: src, Action: req.Action,
				Summary: clip(s.redact(req.Summary), maxSummary),
			},
			Facts: f, Now: now,
		})
		for _, c := range cmds {
			if rf, ok := c.(coreprop.Refuse); ok {
				return errRefused{refusal(rf.Reason, detailFor(rf.Reason, detail))}
			}
		}
		if p.State == nil {
			return errRefused{refusal(coreprop.RefuseBadFacts, detail)}
		}
		created = p
		if err := tx.CreateProposalRecord(ctx, p); err != nil {
			return err
		}
		return s.writeAudit(ctx, tx, p, cmds, "", now)
	})
	if err != nil {
		var er errRefused
		if errors.As(err, &er) {
			return View{}, er.r
		}
		return View{}, fmt.Errorf("create proposal: %w", err)
	}
	s.publishAll(created, cmds)
	return s.view(ctx, created), nil
}

// detailFor keeps our own explanation only for the generic reason it explains.
func detailFor(r coreprop.RefuseReason, detail string) string {
	if r == coreprop.RefuseBadFacts {
		return detail
	}
	return ""
}

// Confirm records the user's confirmation of the exact action hash they saw and
// starts the execution. A wrong hash, an untracked repo or a decided proposal
// changes nothing.
func (s *Service) Confirm(ctx context.Context, id core.ProposalID, hash, by string) (View, error) {
	return s.decide(ctx, id, func(rec db.ProposalRecord, tracked bool, now time.Time) coreprop.Input {
		return coreprop.Confirm{Hash: hash, By: by, RepoTracked: tracked, Now: now}
	}, by)
}

// Reject records that the user rejected a pending proposal.
func (s *Service) Reject(ctx context.Context, id core.ProposalID, by string) (View, error) {
	return s.decide(ctx, id, func(rec db.ProposalRecord, tracked bool, now time.Time) coreprop.Input {
		return coreprop.Reject{By: by, Now: now}
	}, by)
}

// Retry starts another attempt of a retryable failure. It is only ever a user
// click.
func (s *Service) Retry(ctx context.Context, id core.ProposalID, by string) (View, error) {
	return s.decide(ctx, id, func(rec db.ProposalRecord, tracked bool, now time.Time) coreprop.Input {
		return coreprop.Retry{By: by, RepoTracked: tracked, Now: now}
	}, by)
}

func containsRefuse(cmds []coreprop.Command) (coreprop.Refuse, bool) {
	for _, c := range cmds {
		if r, ok := c.(coreprop.Refuse); ok {
			return r, true
		}
	}
	return coreprop.Refuse{}, false
}

// decide is the shared path of Confirm, Reject and Retry: load, Step, one
// Commit with the audit entry, then the commands. It is split from run so a
// test can stop between the commit and the execution (a crash point).
func (s *Service) decide(ctx context.Context, id core.ProposalID, mk func(db.ProposalRecord, bool, time.Time) coreprop.Input, by string) (View, error) {
	view, run, err := s.persistDecision(ctx, id, mk, by)
	if err != nil {
		return view, err
	}
	run()
	return view, nil
}

func (s *Service) persistDecision(ctx context.Context, id core.ProposalID, mk func(db.ProposalRecord, bool, time.Time) coreprop.Input, by string) (View, func(), error) {
	if err := id.Validate(); err != nil {
		return View{}, nil, fmt.Errorf("proposal %s: %w", id, db.ErrNotFound)
	}
	s.qmu.Lock()
	ready := s.ctx != nil && !s.closed
	s.qmu.Unlock()
	if !ready {
		return View{}, nil, ErrNotStarted
	}
	for attempt := 0; attempt < 4; attempt++ {
		rec, err := s.cfg.Store.GetProposalRecord(ctx, id)
		if err != nil {
			return View{}, nil, fmt.Errorf("load proposal %s: %w", id, err)
		}
		tracked := s.repoTracked(ctx, rec.Proposal.Repo)
		now := s.now()
		next, cmds := coreprop.Step(rec.Proposal, mk(rec, tracked, now))
		if r, ok := containsRefuse(cmds); ok {
			return s.view(ctx, rec.Proposal), nil, refusal(r.Reason, "")
		}
		if len(cmds) == 0 {
			return s.view(ctx, rec.Proposal), nil, ErrAlreadyDecided
		}
		err = s.cfg.Store.Commit(ctx, func(tx *db.Tx) error {
			if _, err := tx.SaveProposalRecord(ctx, next, rec.Version); err != nil {
				return err
			}
			return s.writeAudit(ctx, tx, next, cmds, by, now)
		})
		if errors.Is(err, db.ErrConflict) {
			continue // someone else decided first: reload and let Step judge
		}
		if err != nil {
			return View{}, nil, fmt.Errorf("save proposal %s: %w", id, err)
		}
		return s.view(ctx, next), func() { s.publishAll(next, cmds); s.dispatch(next, cmds) }, nil
	}
	return View{}, nil, fmt.Errorf("proposal %s changed too often while it was being decided; try again", id)
}

func (s *Service) repoTracked(ctx context.Context, repo core.RepoID) bool {
	info, err := s.cfg.Resolver.Repo(ctx, repo)
	return err == nil && info.Tracked
}
