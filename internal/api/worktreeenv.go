package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	wenv "github.com/brendatamaar/yggdrasil/internal/core/worktreeenv"
	"github.com/brendatamaar/yggdrasil/internal/worktreeenv"
)

// The worktree environment endpoints (WE-01 to WE-03,
// docs/design/worktree-env.md). The DTOs are in types_worktreeenv.go. This
// file is Go-only wiring and is excluded from tygo (tygo.yaml).

// WorktreeEnvService is implemented by an adapter in cmd/ygg over
// *worktreeenv.Service (SetProfile, BlockFor, CopyProfile with
// env.TriggerUserConfirmed). The handler has already parsed the paths with
// wenv.ParseProfile; SetRepoEnv receives the normalised list. Errors are
// apperr-classified, or the worktreeenv sentinels / *worktreeenv.Error, which
// this package maps.
type WorktreeEnvService interface {
	// RepoEnv returns the repo's settings (defaults for a repo without any).
	RepoEnv(ctx context.Context, repo core.RepoID) (RepoEnvDTO, error)
	// SetRepoEnv stores the settings and returns what is stored.
	SetRepoEnv(ctx context.Context, repo core.RepoID, in RepoEnvDTO) (RepoEnvDTO, error)
	// WorktreeEnv returns a worktree's port block, last profile copy and
	// compose state.
	WorktreeEnv(ctx context.Context, w core.WorktreeID) (WorktreeEnvDTO, error)
	// CopyProfile copies the repo's profile into the worktree, as a
	// user-confirmed action, and returns the new state.
	CopyProfile(ctx context.Context, w core.WorktreeID) (WorktreeEnvDTO, error)
}

// ComposeAction is the closed set of compose actions a user can ask for.
type ComposeAction string

// The closed set of ComposeAction values.
const (
	ComposeActionConfirm ComposeAction = "confirm"
	ComposeActionUp      ComposeAction = "up"
	ComposeActionStop    ComposeAction = "stop"
	ComposeActionDown    ComposeAction = "down"
)

// ComposeAPI is implemented by the same adapter as WorktreeEnvService when
// docker compose is built in (WE-03). Without it the compose endpoints answer
// 501. Each action is a user-confirmed request; for ComposeActionConfirm, hash
// is the compose file hash the user was shown ("" confirms the current file).
// Errors wrap the worktreeenv.ErrCompose* sentinels in *worktreeenv.Error.
type ComposeAPI interface {
	ComposeAction(ctx context.Context, w core.WorktreeID, a ComposeAction, hash string) (WorktreeEnvDTO, error)
}

func (s *Server) routesWorktreeEnv(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/repos/{id}/env", s.handleGetRepoEnv)
	mux.HandleFunc("PUT /api/repos/{id}/env", s.handlePutRepoEnv)
	mux.HandleFunc("GET /api/worktrees/{id}/env", s.handleGetWorktreeEnv)
	mux.HandleFunc("POST /api/worktrees/{id}/profile/copy", s.handleCopyProfile)
	mux.HandleFunc("GET /api/worktrees/{id}/compose", s.handleGetWorktreeEnv)
	for _, a := range []ComposeAction{ComposeActionConfirm, ComposeActionUp, ComposeActionStop, ComposeActionDown} {
		mux.HandleFunc("POST /api/worktrees/{id}/compose/"+string(a), s.handleCompose(a))
	}
}

// handleCompose is one compose action (WE-03). The action comes from the
// route, never from the body, so the set of things that can run is closed.
func (s *Server) handleCompose(a ComposeAction) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		svc, ok := s.phase5().WorktreeEnv.(ComposeAPI)
		if !ok || svc == nil {
			notReady(w, "Docker compose for a worktree")
			return
		}
		id, ok := envWorktreeID(w, r)
		if !ok {
			return
		}
		var hash string
		if a == ComposeActionConfirm {
			// A confirmation says which version of the file it confirms.
			var req ComposeConfirmRequest
			if r.ContentLength == 0 || !decodeBody(w, r, &req) {
				if r.ContentLength == 0 {
					badRequest(w, "Say which version of the compose file you are confirming.", "Send the composeHash that was shown with the worktree.")
				}
				return
			}
			if req.Hash == "" {
				badRequest(w, "Say which version of the compose file you are confirming.", "Send the composeHash that was shown with the worktree.")
				return
			}
			hash = req.Hash
		}
		out, err := svc.ComposeAction(r.Context(), id, a, hash)
		if err != nil {
			s.fail(w, r, envErr(err))
			return
		}
		if out.Profile == nil {
			out.Profile = []ProfileEntryResult{}
		}
		writeJSON(w, http.StatusOK, out)
		s.PublishWorktreeEnv(id)
	}
}

// envErr gives the worktreeenv package's own errors an apperr class, so fail
// can map them. Errors that already carry a class pass through.
func envErr(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := apperr.ClassOf(err); ok {
		return err
	}
	var ee *worktreeenv.Error
	switch {
	case errors.Is(err, worktreeenv.ErrNotFound):
		return apperr.New(apperr.NotFound, "That repo or worktree isn't tracked (any more).", err)
	case errors.Is(err, worktreeenv.ErrComposeBusy), errors.Is(err, worktreeenv.ErrComposeConfirm), errors.Is(err, worktreeenv.ErrComposeStale), errors.Is(err, worktreeenv.ErrComposeNoop):
		msg := "That can't be done right now."
		if errors.As(err, &ee) {
			msg = ee.UserMessage()
		}
		return apperr.New(apperr.Conflict, msg, err)
	case errors.Is(err, worktreeenv.ErrConflict):
		return apperr.New(apperr.Conflict, "This worktree's ports changed while you were working. Reload and try again.", err)
	case errors.As(err, &ee):
		return apperr.New(apperr.Invalid, ee.UserMessage(), err)
	}
	return err
}

func envRepoID(w http.ResponseWriter, r *http.Request) (core.RepoID, bool) {
	id := core.RepoID(r.PathValue("id"))
	if id.Validate() != nil {
		writeError(w, http.StatusNotFound, ErrNotFound, "That repo doesn't exist (any more).", "Refresh the page and try again.")
		return "", false
	}
	return id, true
}

func envWorktreeID(w http.ResponseWriter, r *http.Request) (core.WorktreeID, bool) {
	id := core.WorktreeID(r.PathValue("id"))
	if id.Validate() != nil {
		writeError(w, http.StatusNotFound, ErrNotFound, "That worktree doesn't exist (any more).", "Refresh the page and try again.")
		return "", false
	}
	return id, true
}

func (s *Server) handleGetRepoEnv(w http.ResponseWriter, r *http.Request) {
	svc := s.phase5().WorktreeEnv
	if svc == nil {
		notReady(w, "Environment settings")
		return
	}
	id, ok := envRepoID(w, r)
	if !ok {
		return
	}
	out, err := svc.RepoEnv(r.Context(), id)
	if err != nil {
		s.fail(w, r, envErr(err))
		return
	}
	if out.ProfilePaths == nil {
		out.ProfilePaths = []string{}
	}
	writeJSON(w, http.StatusOK, out)
}

// parseRepoEnv is the boundary parser of the settings body: profile paths and
// the compose file go through the core rules (relative, no "..", no ".git", no
// globs, no control characters, at most 50 entries).
func parseRepoEnv(id core.RepoID, in RepoEnvDTO) (RepoEnvDTO, error) {
	bad := func(err error) error {
		return apperr.New(apperr.Invalid, "Profile files must be paths inside the repo, one file each: "+err.Error()+".", err)
	}
	paths, err := wenv.ParseProfile(in.ProfilePaths)
	if err != nil {
		return RepoEnvDTO{}, bad(err)
	}
	out := RepoEnvDTO{RepoID: id, ProfilePaths: make([]string, 0, len(paths)), AgentCopy: in.AgentCopy, ComposeEnabled: in.ComposeEnabled}
	for _, p := range paths {
		out.ProfilePaths = append(out.ProfilePaths, string(p))
	}
	if in.ComposeFile != "" {
		cf, err := wenv.ParseComposeFile(in.ComposeFile)
		if err != nil {
			return RepoEnvDTO{}, apperr.New(apperr.Invalid, "The compose file must be a .yml or .yaml file inside the repo: "+err.Error()+".", err)
		}
		out.ComposeFile = string(cf)
	}
	if out.ComposeEnabled && out.ComposeFile == "" {
		return RepoEnvDTO{}, apperr.New(apperr.Invalid, "Choose the compose file before turning compose on.", errors.New("compose enabled without a file"))
	}
	return out, nil
}

func (s *Server) handlePutRepoEnv(w http.ResponseWriter, r *http.Request) {
	svc := s.phase5().WorktreeEnv
	if svc == nil {
		notReady(w, "Environment settings")
		return
	}
	id, ok := envRepoID(w, r)
	if !ok {
		return
	}
	var req RepoEnvDTO
	if !decodeBody(w, r, &req) {
		return
	}
	if req.RepoID != "" && req.RepoID != id {
		badRequest(w, "The repo in the request doesn't match its address.", "Reload the page and try again.")
		return
	}
	parsed, err := parseRepoEnv(id, req)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out, err := svc.SetRepoEnv(r.Context(), id, parsed)
	if err != nil {
		s.fail(w, r, envErr(err))
		return
	}
	if out.ProfilePaths == nil {
		out.ProfilePaths = []string{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetWorktreeEnv(w http.ResponseWriter, r *http.Request) {
	svc := s.phase5().WorktreeEnv
	if svc == nil {
		notReady(w, "Worktree environment")
		return
	}
	id, ok := envWorktreeID(w, r)
	if !ok {
		return
	}
	out, err := svc.WorktreeEnv(r.Context(), id)
	if err != nil {
		s.fail(w, r, envErr(err))
		return
	}
	if out.Profile == nil {
		out.Profile = []ProfileEntryResult{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCopyProfile(w http.ResponseWriter, r *http.Request) {
	svc := s.phase5().WorktreeEnv
	if svc == nil {
		notReady(w, "Copying the environment files")
		return
	}
	id, ok := envWorktreeID(w, r)
	if !ok {
		return
	}
	out, err := svc.CopyProfile(r.Context(), id)
	if err != nil {
		s.fail(w, r, envErr(err))
		return
	}
	if out.Profile == nil {
		out.Profile = []ProfileEntryResult{}
	}
	writeJSON(w, http.StatusOK, out)
}

// PublishWorktreeEnv tells clients on the "worktreeenv" topic that a
// worktree's environment changed. The adapter in cmd/ygg passes it as
// worktreeenv.Config.OnProfileDone. The state is read through the service, so
// no path is sent.
func (s *Server) PublishWorktreeEnv(w core.WorktreeID) {
	svc := s.phase5().WorktreeEnv
	if svc == nil {
		return
	}
	dto, err := svc.WorktreeEnv(s.ctx, w)
	if err != nil {
		s.log.Warn("read worktree environment for publishing", "worktree", w, "err", err)
		return
	}
	if dto.Profile == nil {
		dto.Profile = []ProfileEntryResult{}
	}
	s.hub.publishNext(worktreeEnvTopic{}, WorktreeEnvChangedMessage{Env: dto})
}
