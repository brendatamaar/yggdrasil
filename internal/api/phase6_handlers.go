package api

import (
	"net/http"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/notify"
)

// The handlers of the Phase 6 routes (docs/design/phase-6-*.md). Every route
// sits behind the same middleware as the rest of /api: the Host/Origin guard
// and the UI session (a POST without the session cookie or with a foreign
// Origin never reaches a handler). A handler decodes a bounded body, validates
// it at the boundary, calls the bound service and answers with a redacted,
// bounded DTO. A nil service answers 501 before anything is read. None of them
// confirms a proposal: confirmation is only ever the proposals API.
//
// The file is Go-only wiring (no exported names), so tygo has nothing to emit.

func (s *Server) routesPhase6(mux *http.ServeMux) {
	mux.HandleFunc(RouteRepoActions, s.handleRepoActions)
	mux.HandleFunc(RouteActionPreview, s.handleActionPreview)
	mux.HandleFunc(RouteActionExecute, s.handleActionExecute)
	mux.HandleFunc(RouteActionGet, s.handleActionGet)
	mux.HandleFunc(RouteFailedJobs, s.handleFailedJobs)
	mux.HandleFunc(RouteIssues, s.handleIssues)
	mux.HandleFunc(RouteIssue, s.handleIssue)
	mux.HandleFunc(RouteMRContext, s.handleMRContext)
	mux.HandleFunc(RouteImportIssueDraft, s.handleImportIssueDraft)
	mux.HandleFunc(RouteStartDraft, s.handleStartDraft)
	mux.HandleFunc(RouteNoise, s.handleNoise)
	mux.HandleFunc(RouteNoiseProposal, s.handleNoiseProposal)
	mux.HandleFunc(RouteNotices, s.handleNotices)
	mux.HandleFunc(RouteStandup, s.handleStandup)
	mux.HandleFunc(RouteStandupPolish, s.handleStandupPolish)
	mux.HandleFunc(RouteNewRepoPreview, s.handleNewRepoPreview)
	mux.HandleFunc(RouteNewRepoReserve, s.handleNewRepoReserve)
	mux.HandleFunc(RouteNewRepoGet, s.handleNewRepoGet)
	mux.HandleFunc(RouteNewRepoList, s.handleNewRepoList)
	mux.HandleFunc(RoutePublishPreview, s.handlePublishPreview)
	mux.HandleFunc(RoutePublishNewRepo, s.handlePublishNewRepo)
	mux.HandleFunc(RouteNewRepoRetry, s.handleNewRepoRetry)
}

// ---- workspace actions ----

func (s *Server) handleRepoActions(w http.ResponseWriter, r *http.Request) {
	svc := s.phase6().Actions
	if svc == nil {
		notReady(w, "Workspace actions")
		return
	}
	repo, err := parseRepoID(r.PathValue("id"))
	if err != nil {
		s.bad6(w, err)
		return
	}
	resp, err := svc.Catalogue(r.Context(), repo)
	if err != nil {
		s.fail6(w, r, err)
		return
	}
	boundCatalogue(&resp)
	s.ok6(w, r, resp)
}

func (s *Server) handleActionPreview(w http.ResponseWriter, r *http.Request) {
	svc := s.phase6().Actions
	if svc == nil {
		notReady(w, "Workspace actions")
		return
	}
	var req ActionRequestDTO
	if !decode6(w, r, &req) {
		return
	}
	if err := parseActionRequest(req); err != nil {
		s.bad6(w, err)
		return
	}
	resp, err := svc.Preview(r.Context(), req)
	if err != nil {
		s.fail6(w, r, err)
		return
	}
	boundPreview(&resp)
	s.ok6(w, r, resp)
}

func (s *Server) handleActionExecute(w http.ResponseWriter, r *http.Request) {
	svc := s.phase6().Actions
	if svc == nil {
		notReady(w, "Workspace actions")
		return
	}
	var req ActionExecuteRequest
	if !decode6(w, r, &req) {
		return
	}
	if err := parseActionExecute(req); err != nil {
		s.bad6(w, err)
		return
	}
	resp, err := svc.Execute(r.Context(), req)
	if err != nil {
		s.fail6(w, r, err)
		return
	}
	s.ok6(w, r, resp)
}

func (s *Server) handleActionGet(w http.ResponseWriter, r *http.Request) {
	svc := s.phase6().Actions
	if svc == nil {
		notReady(w, "Workspace actions")
		return
	}
	id, err := parseRequestID(r.PathValue("requestId"))
	if err != nil {
		s.bad6(w, err)
		return
	}
	resp, err := svc.Get(r.Context(), id)
	if err != nil {
		s.fail6(w, r, err)
		return
	}
	s.ok6(w, r, resp)
}

// ---- host context ----

func (s *Server) handleFailedJobs(w http.ResponseWriter, r *http.Request) {
	svc := s.phase6().HostContext
	if svc == nil {
		notReady(w, "Host context")
		return
	}
	repo, err := parseRepoID(r.PathValue("id"))
	if err != nil {
		s.bad6(w, err)
		return
	}
	run, err := parseRunID(r.PathValue("runId"))
	if err != nil {
		s.bad6(w, err)
		return
	}
	resp, err := svc.FailedJobs(r.Context(), repo, run)
	if err != nil {
		s.fail6(w, r, err)
		return
	}
	boundFailedJobs(&resp)
	s.ok6(w, r, resp)
}

func (s *Server) handleIssues(w http.ResponseWriter, r *http.Request) {
	svc := s.phase6().HostContext
	if svc == nil {
		notReady(w, "Host context")
		return
	}
	repo, err := parseRepoID(r.PathValue("id"))
	if err != nil {
		s.bad6(w, err)
		return
	}
	resp, err := svc.Issues(r.Context(), repo)
	if err != nil {
		s.fail6(w, r, err)
		return
	}
	boundIssues(&resp)
	s.ok6(w, r, resp)
}

// repoAndNumber reads {id} and {number}; false means a 400 was written.
func (s *Server) repoAndNumber(w http.ResponseWriter, r *http.Request) (core.RepoID, int, bool) {
	repo, err := parseRepoID(r.PathValue("id"))
	if err != nil {
		s.bad6(w, err)
		return "", 0, false
	}
	n, err := parseNumber(r.PathValue("number"))
	if err != nil {
		s.bad6(w, err)
		return "", 0, false
	}
	return repo, n, true
}

func (s *Server) handleIssue(w http.ResponseWriter, r *http.Request) {
	svc := s.phase6().HostContext
	if svc == nil {
		notReady(w, "Host context")
		return
	}
	repo, n, ok := s.repoAndNumber(w, r)
	if !ok {
		return
	}
	resp, err := svc.Issue(r.Context(), repo, n)
	if err != nil {
		s.fail6(w, r, err)
		return
	}
	boundIssue(resp.Issue)
	s.ok6(w, r, resp)
}

func (s *Server) handleMRContext(w http.ResponseWriter, r *http.Request) {
	svc := s.phase6().HostContext
	if svc == nil {
		notReady(w, "Host context")
		return
	}
	repo, n, ok := s.repoAndNumber(w, r)
	if !ok {
		return
	}
	resp, err := svc.MRContext(r.Context(), repo, n)
	if err != nil {
		s.fail6(w, r, err)
		return
	}
	boundMRContext(&resp)
	s.ok6(w, r, resp)
}

// ---- draft tasks ----

func (s *Server) handleImportIssueDraft(w http.ResponseWriter, r *http.Request) {
	svc := s.phase6().Drafts
	if svc == nil {
		notReady(w, "Importing an issue")
		return
	}
	repo, n, ok := s.repoAndNumber(w, r)
	if !ok {
		return
	}
	var req ImportIssueDraftRequest
	if !decode6(w, r, &req) {
		return
	}
	if err := parseImportDraft(req); err != nil {
		s.bad6(w, err)
		return
	}
	resp, err := svc.ImportIssueDraft(r.Context(), repo, n, req)
	if err != nil {
		s.fail6(w, r, err)
		return
	}
	s.ok6(w, r, resp)
}

func (s *Server) handleStartDraft(w http.ResponseWriter, r *http.Request) {
	svc := s.phase6().Drafts
	if svc == nil {
		notReady(w, "Starting a draft task")
		return
	}
	id, err := parseTaskIDValue(r.PathValue("id"))
	if err != nil {
		s.bad6(w, err)
		return
	}
	var req StartDraftRequest
	if !decode6(w, r, &req) {
		return
	}
	if err := parseStartDraft(req); err != nil {
		s.bad6(w, err)
		return
	}
	// The same switch as POST /api/tasks: a write mode needs write modes on.
	if !s.writeModeAllowed(string(req.Mode)) {
		badRequest(w, errWriteModes, writeModesHint)
		return
	}
	resp, err := svc.StartDraft(r.Context(), id, req)
	if err != nil {
		s.fail6(w, r, err)
		return
	}
	s.ok6(w, r, resp)
}

// ---- noise ----

func (s *Server) handleNoise(w http.ResponseWriter, r *http.Request) {
	svc := s.phase6().Noise
	if svc == nil {
		notReady(w, "Ignore suggestions")
		return
	}
	repo, err := parseRepoID(r.PathValue("id"))
	if err != nil {
		s.bad6(w, err)
		return
	}
	resp, err := svc.Noise(r.Context(), repo)
	if err != nil {
		s.fail6(w, r, err)
		return
	}
	boundNoise(&resp)
	s.ok6(w, r, resp)
}

// handleNoiseProposal creates a pending ignore proposal. It never confirms it.
func (s *Server) handleNoiseProposal(w http.ResponseWriter, r *http.Request) {
	svc := s.phase6().Noise
	if svc == nil {
		notReady(w, "Ignore suggestions")
		return
	}
	repo, err := parseRepoID(r.PathValue("id"))
	if err != nil {
		s.bad6(w, err)
		return
	}
	var req CreateIgnoreProposalRequest
	if !decode6(w, r, &req) {
		return
	}
	if err := parseIgnoreProposal(req); err != nil {
		s.bad6(w, err)
		return
	}
	resp, err := svc.CreateIgnoreProposal(r.Context(), repo, req)
	if err != nil {
		s.fail6(w, r, err)
		return
	}
	s.ok6(w, r, resp)
}

// ---- notifications ----

func (s *Server) handleNotices(w http.ResponseWriter, r *http.Request) {
	svc := s.phase6().Notices
	if svc == nil {
		notReady(w, "Notification history")
		return
	}
	after, limit, err := parseNoticeQuery(r)
	if err != nil {
		s.bad6(w, err)
		return
	}
	if limit > notify.MaxPage {
		limit = notify.MaxPage
	}
	resp, err := svc.Notices(r.Context(), after, limit)
	if err != nil {
		s.fail6(w, r, err)
		return
	}
	boundNotices(&resp, limit)
	s.ok6(w, r, resp)
}

// ---- standup ----

func (s *Server) handleStandup(w http.ResponseWriter, r *http.Request) {
	svc := s.phase6().Standup
	if svc == nil {
		notReady(w, "The standup summary")
		return
	}
	project, err := parseProjectID(r.PathValue("id"))
	if err != nil {
		s.bad6(w, err)
		return
	}
	rg, err := parseRangeQuery(project, r)
	if err != nil {
		s.bad6(w, err)
		return
	}
	resp, err := svc.Get(r.Context(), rg.Project, rg.From, rg.To)
	if err != nil {
		s.fail6(w, r, err)
		return
	}
	boundStandup(&resp)
	s.ok6(w, r, resp)
}

// handleStandupPolish is an explicit user submission; a refresh (GET) never
// reaches it.
func (s *Server) handleStandupPolish(w http.ResponseWriter, r *http.Request) {
	svc := s.phase6().Standup
	if svc == nil {
		notReady(w, "The standup summary")
		return
	}
	project, err := parseProjectID(r.PathValue("id"))
	if err != nil {
		s.bad6(w, err)
		return
	}
	var req StandupPolishRequest
	if !decode6(w, r, &req) {
		return
	}
	if err := parsePolish(project, req); err != nil {
		s.bad6(w, err)
		return
	}
	resp, err := svc.Polish(r.Context(), project, req)
	if err != nil {
		s.fail6(w, r, err)
		return
	}
	s.ok6(w, r, resp)
}

// ---- new repos ----

func (s *Server) handleNewRepoPreview(w http.ResponseWriter, r *http.Request) {
	svc := s.phase6().NewRepos
	if svc == nil {
		notReady(w, "New repos")
		return
	}
	project, err := parseProjectID(r.PathValue("id"))
	if err != nil {
		s.bad6(w, err)
		return
	}
	var req NewRepoPreviewRequest
	if !decode6(w, r, &req) {
		return
	}
	if err := parseNewRepoPreview(req); err != nil {
		s.bad6(w, err)
		return
	}
	resp, err := svc.Preview(r.Context(), project, req)
	if err != nil {
		s.fail6(w, r, err)
		return
	}
	resp.Lines, resp.Problems = boundLines(resp.Lines, resp.Problems)
	s.ok6(w, r, resp)
}

// handleNewRepoReserve reserves the name and makes a pending proposal. It
// creates no folder and confirms nothing.
func (s *Server) handleNewRepoReserve(w http.ResponseWriter, r *http.Request) {
	svc := s.phase6().NewRepos
	if svc == nil {
		notReady(w, "New repos")
		return
	}
	project, err := parseProjectID(r.PathValue("id"))
	if err != nil {
		s.bad6(w, err)
		return
	}
	var req ReserveNewRepoRequest
	if !decode6(w, r, &req) {
		return
	}
	if err := parseReserve(req); err != nil {
		s.bad6(w, err)
		return
	}
	resp, err := svc.Reserve(r.Context(), project, req)
	if err != nil {
		s.fail6(w, r, err)
		return
	}
	boundOperation(&resp)
	s.ok6(w, r, resp)
}

func (s *Server) handleNewRepoGet(w http.ResponseWriter, r *http.Request) {
	svc := s.phase6().NewRepos
	if svc == nil {
		notReady(w, "New repos")
		return
	}
	op, err := parseOperationID(r.PathValue("id"))
	if err != nil {
		s.bad6(w, err)
		return
	}
	resp, err := svc.Get(r.Context(), op)
	if err != nil {
		s.fail6(w, r, err)
		return
	}
	boundOperation(&resp)
	s.ok6(w, r, resp)
}

func (s *Server) handleNewRepoList(w http.ResponseWriter, r *http.Request) {
	svc := s.phase6().NewRepos
	if svc == nil {
		notReady(w, "New repos")
		return
	}
	project, err := parseProjectID(r.PathValue("id"))
	if err != nil {
		s.bad6(w, err)
		return
	}
	ops, err := svc.List(r.Context(), project)
	if err != nil {
		s.fail6(w, r, err)
		return
	}
	ops, _ = capSlice(nonNil6(ops), maxP6Records)
	for i := range ops {
		boundOperation(&ops[i])
	}
	s.ok6(w, r, ops)
}

func (s *Server) handlePublishPreview(w http.ResponseWriter, r *http.Request) {
	svc := s.phase6().NewRepos
	if svc == nil {
		notReady(w, "New repos")
		return
	}
	op, err := parseOperationID(r.PathValue("id"))
	if err != nil {
		s.bad6(w, err)
		return
	}
	var req PublishPreviewRequest
	if !decode6(w, r, &req) {
		return
	}
	if err := parsePublishPreview(req); err != nil {
		s.bad6(w, err)
		return
	}
	resp, err := svc.PublishPreview(r.Context(), op, req)
	if err != nil {
		s.fail6(w, r, err)
		return
	}
	resp.Lines, resp.Problems = boundLines(resp.Lines, resp.Problems)
	s.ok6(w, r, resp)
}

// handlePublishNewRepo creates a pending publish proposal. The remote is made
// only after the user confirms that proposal through the proposals API.
func (s *Server) handlePublishNewRepo(w http.ResponseWriter, r *http.Request) {
	svc := s.phase6().NewRepos
	if svc == nil {
		notReady(w, "New repos")
		return
	}
	op, err := parseOperationID(r.PathValue("id"))
	if err != nil {
		s.bad6(w, err)
		return
	}
	var req PublishNewRepoRequest
	if !decode6(w, r, &req) {
		return
	}
	if err := parsePublish(req); err != nil {
		s.bad6(w, err)
		return
	}
	resp, err := svc.Publish(r.Context(), op, req)
	if err != nil {
		s.fail6(w, r, err)
		return
	}
	s.ok6(w, r, resp)
}

// handleNewRepoRetry is a fresh, explicit confirmation of the spec hash; it
// never retries by itself.
func (s *Server) handleNewRepoRetry(w http.ResponseWriter, r *http.Request) {
	svc := s.phase6().NewRepos
	if svc == nil {
		notReady(w, "New repos")
		return
	}
	op, err := parseOperationID(r.PathValue("id"))
	if err != nil {
		s.bad6(w, err)
		return
	}
	var req RetryNewRepoRequest
	if !decode6(w, r, &req) {
		return
	}
	if err := parseRetry(req); err != nil {
		s.bad6(w, err)
		return
	}
	resp, err := svc.Retry(r.Context(), op, req.Hash)
	if err != nil {
		s.fail6(w, r, err)
		return
	}
	boundOperation(&resp)
	s.ok6(w, r, resp)
}
