package api

// Go-only wiring and contracts (excluded from tygo) of the Phase 6 areas
// (docs/design/phase-6-*.md). The foundation freezes the service interfaces,
// the route table and the hub contract; the handlers come with the API wiring
// task (W). Until a service is bound, its endpoints answer 501 with a plain
// message, exactly as for the Phase 5 services:
//
//	srv, _ := api.New(deps)
//	srv.SetPhase6Services(api.Phase6Services{Actions: actionsAdapter, ...})
//
// Every method takes a context.Context. Request IDs, digests and expected
// heads are validated by the service; the handlers only decode and bound the
// body. Errors the handlers translate: *coretasks.UserError (400),
// *tasks.RejectedError (409), db.ErrNotFound (404), db.ErrConflict and
// db.ErrDigestMismatch (409); anything else is a generic 500.

import (
	"context"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Routes of the Phase 6 areas. Handlers are registered by task W with the same
// Host/Origin/session and body-size rules as every other mutation surface; an
// ID in a path is a tracked ID resolved on the server, never a host URL or a
// filesystem path.
const (
	RouteRepoActions      = "GET /api/repos/{id}/actions"
	RouteActionPreview    = "POST /api/actions/preview"
	RouteActionExecute    = "POST /api/actions/execute"
	RouteActionGet        = "GET /api/actions/{requestId}"
	RouteFailedJobs       = "GET /api/repos/{id}/ci/runs/{runId}/failed-jobs"
	RouteIssues           = "GET /api/repos/{id}/issues"
	RouteIssue            = "GET /api/repos/{id}/issues/{number}"
	RouteMRContext        = "GET /api/repos/{id}/mrs/{number}/context"
	RouteImportIssueDraft = "POST /api/repos/{id}/issues/{number}/draft"
	RouteStartDraft       = "POST /api/tasks/{id}/start"
	RouteNoise            = "GET /api/repos/{id}/noise"
	RouteNoiseProposal    = "POST /api/repos/{id}/noise/proposals"
	RouteNotices          = "GET /api/notifications"
	RouteStandup          = "GET /api/projects/{id}/standup"
	RouteStandupPolish    = "POST /api/projects/{id}/standup/polish"
	RouteNewRepoPreview   = "POST /api/projects/{id}/new-repos/preview"
	RouteNewRepoReserve   = "POST /api/projects/{id}/new-repos"
	RouteNewRepoGet       = "GET /api/new-repos/{id}"
	RouteNewRepoList      = "GET /api/projects/{id}/new-repos"
	RoutePublishPreview   = "POST /api/new-repos/{id}/publish/preview"
	RoutePublishNewRepo   = "POST /api/new-repos/{id}/publish"
	RouteNewRepoRetry     = "POST /api/new-repos/{id}/retry"
)

// ActionService is the workspace-action catalogue, preview and execution
// (AC-01 to AC-06), implemented by internal/actions. Execute is idempotent per
// RequestID: a repeat returns the first result, a repeat with another digest is
// db.ErrDigestMismatch. Preview and Execute re-resolve the tracked scope on the
// server and refuse a stale head. Execute never confirms a proposal.
type ActionService interface {
	Catalogue(ctx context.Context, repo core.RepoID) (ActionCatalogueResponse, error)
	Preview(ctx context.Context, req ActionRequestDTO) (ActionPreviewDTO, error)
	Execute(ctx context.Context, req ActionExecuteRequest) (ActionResultDTO, error)
	Get(ctx context.Context, id core.RequestID) (ActionResultDTO, error)
}

// HostContextService serves bounded, redacted host reads (GH-05, GH-06) for a
// tracked repo, implemented by internal/hosts. An untracked or reserved repo
// is db.ErrNotFound; a missing token or unsupported host is a normal response
// with a blocked status, not an error.
type HostContextService interface {
	FailedJobs(ctx context.Context, repo core.RepoID, runID string) (FailedJobsResponse, error)
	Issues(ctx context.Context, repo core.RepoID) (IssuesResponse, error)
	Issue(ctx context.Context, repo core.RepoID, number int) (IssueResponse, error)
	MRContext(ctx context.Context, repo core.RepoID, number int) (MRContextResponse, error)
}

// DraftService creates and starts draft tasks from issues (AC-05), implemented
// by internal/tasks. ImportIssueDraft starts no turn, process or worktree.
// StartDraft goes through the ordinary admission, trust scan and queue, and a
// repo-setup draft can never be started (a *coretasks.UserError).
type DraftService interface {
	ImportIssueDraft(ctx context.Context, repo core.RepoID, number int, req ImportIssueDraftRequest) (DraftTaskResponse, error)
	StartDraft(ctx context.Context, task core.TaskID, req StartDraftRequest) (TaskResponse, error)
}

// NoiseService serves noise suggestions and creates the ignore proposal (WS-09),
// implemented by internal/repos and internal/proposals. Nothing it does writes
// a file: the proposal is pending until the user confirms it.
type NoiseService interface {
	Noise(ctx context.Context, repo core.RepoID) (NoiseResponse, error)
	CreateIgnoreProposal(ctx context.Context, repo core.RepoID, req CreateIgnoreProposalRequest) (ProposalDTO, error)
}

// NoticeService serves notification history (UI-06), implemented by
// internal/notify. after is a cursor (0 for the newest), limit at most 200.
type NoticeService interface {
	Notices(ctx context.Context, after int64, limit int) (NoticesResponse, error)
}

// StandupService serves the standup summary (AU-02, UI-03), implemented by
// internal/standup. Get never starts an agent; Polish starts one visible Ask
// task through the action path, on an explicit user request.
type StandupService interface {
	Get(ctx context.Context, project core.ProjectID, from, to time.Time) (StandupResponse, error)
	Polish(ctx context.Context, project core.ProjectID, req StandupPolishRequest) (ActionResultDTO, error)
}

// NewRepoService previews, reserves, initializes and publishes new repos (NR-01
// to NR-04), implemented by internal/newrepos. Preview writes nothing. Reserve
// writes only the reservation, the setup draft and a pending proposal;
// initialization and publishing run only after the user confirms the matching
// proposal hash through the proposals API. Retry is a fresh explicit
// confirmation, never automatic.
type NewRepoService interface {
	Preview(ctx context.Context, project core.ProjectID, req NewRepoPreviewRequest) (NewRepoPreviewResponse, error)
	Reserve(ctx context.Context, project core.ProjectID, req ReserveNewRepoRequest) (NewRepoOperationDTO, error)
	Get(ctx context.Context, op core.OperationID) (NewRepoOperationDTO, error)
	List(ctx context.Context, project core.ProjectID) ([]NewRepoOperationDTO, error)
	PublishPreview(ctx context.Context, op core.OperationID, req PublishPreviewRequest) (PublishPreviewResponse, error)
	Publish(ctx context.Context, op core.OperationID, req PublishNewRepoRequest) (ProposalDTO, error)
	Retry(ctx context.Context, op core.OperationID, hash string) (NewRepoOperationDTO, error)
}

// Phase6Services are the services of the Phase 6 areas. A nil field answers 501
// for its endpoints.
type Phase6Services struct {
	Actions     ActionService
	HostContext HostContextService
	Drafts      DraftService
	Noise       NoiseService
	Notices     NoticeService
	Standup     StandupService
	NewRepos    NewRepoService
}

// SetPhase6Services binds (or replaces) the Phase 6 services. Safe to call
// while the server runs. They are bound after New because the notification,
// action and new-repo workers publish to the server's hub.
func (s *Server) SetPhase6Services(p Phase6Services) { s.p6.Store(&p) }

func (s *Server) phase6() *Phase6Services {
	if p := s.p6.Load(); p != nil {
		return p
	}
	return &Phase6Services{}
}

// Phase6Hub is what the Phase 6 services publish through (*Server implements
// it). Publishing never blocks: a slow client is dropped, as on every topic.
// Every method must be called AFTER the state it announces is committed: a
// crash between the commit and the publish is recovered by the client's history
// query, which is why the messages carry IDs and cursors and no content.
type Phase6Hub interface {
	PublishNotice(m NoticeChangedMessage)
	PublishAction(m ActionChangedMessage)
	PublishNewRepo(m NewRepoChangedMessage)
}

var _ Phase6Hub = (*Server)(nil)

// PublishNotice sends a notice invalidation on TopicNotices.
func (s *Server) PublishNotice(m NoticeChangedMessage) { s.hub.publishNext(noticesTopic{}, m) }

// PublishAction sends an action state change on TopicActions.
func (s *Server) PublishAction(m ActionChangedMessage) { s.hub.publishNext(actionsTopic{}, m) }

// PublishNewRepo sends a new-repo state change on TopicNewRepos. When the state
// is NewRepoReady the repo list changed too: the status service publishes the
// repos topic with its real counts after it tracks the repo (the hub carries no
// counts here, so this method never fakes them).
func (s *Server) PublishNewRepo(m NewRepoChangedMessage) { s.hub.publishNext(newReposTopic{}, m) }
