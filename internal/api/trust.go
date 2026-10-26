package api

import (
	"net/http"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/guard"
)

// The trust endpoints (SF-09, D17) go through TaskService. The DTOs are in
// types_trust.go. The file is excluded from tygo (tygo.yaml): it holds Go-only
// wiring.

// routesTrust registers the trust endpoints.
func (s *Server) routesTrust(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/repos/{id}/trust", s.handleGetTrust)
	mux.HandleFunc("POST /api/repos/{id}/trust", s.handleSetTrust)
	mux.HandleFunc("DELETE /api/repos/{id}/trust", s.handleRevokeTrust)
}

// trustDTO maps a trust state. Unknown counts as untrusted (it fails closed).
func trustDTO(repo core.RepoID, t guard.Trust) TrustDTO {
	out := TrustDTO{RepoID: repo, State: "untrusted", Files: []TrustFileDTO{}}
	var scan guard.Scan
	switch st := t.(type) {
	case guard.NoConfig:
		out.State = "none"
	case guard.Trusted:
		out.State, out.Hash, scan = "trusted", st.Hash, st.Scan
	case guard.Untrusted:
		out.Why, scan = string(st.Why.Kind), st.Found
		if st.Why.Kind != guard.WhyScanIncomplete {
			out.Hash = st.Found.Hash
		}
	}
	for _, f := range scan.Files {
		w := f.WouldRun
		if w == nil {
			w = []string{}
		}
		out.Files = append(out.Files, TrustFileDTO{Path: f.Path, WouldRun: w, Opaque: f.Opaque})
	}
	return out
}

// repoParam reads and validates the {id} of the path.
func repoParam(w http.ResponseWriter, r *http.Request) (core.RepoID, bool) {
	id := core.RepoID(r.PathValue("id"))
	if id.Validate() != nil {
		badRequest(w, "That repo ID isn't valid.", "")
		return "", false
	}
	return id, true
}

func (s *Server) handleGetTrust(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.service(w, "Repo trust")
	if !ok {
		return
	}
	id, ok := repoParam(w, r)
	if !ok {
		return
	}
	t, err := svc.TrustStatus(r.Context(), id)
	if err != nil {
		s.serviceErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, trustDTO(id, t))
}

func (s *Server) handleSetTrust(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.service(w, "Trusting a repo")
	if !ok {
		return
	}
	id, ok := repoParam(w, r)
	if !ok {
		return
	}
	var req TrustRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if !isHash64(req.Hash) {
		badRequest(w, "That isn't the hash Yggdrasil showed you.", "Reload the page and review the repo's agent settings again.")
		return
	}
	t, err := svc.Trust(r.Context(), id, req.Hash)
	if err != nil {
		s.serviceErr(w, r, err)
		return
	}
	// The core ignores a hash that is no longer the repo's config and returns
	// the state unchanged: anything but Trusted means the user approved
	// something other than what is there now.
	if _, trusted := t.(guard.Trusted); !trusted {
		writeError(w, http.StatusConflict, ErrConflict,
			"The repo's agent settings are not what you were shown, so nothing was trusted.",
			"Reload the page, review what the repo would run, then trust it again.")
		return
	}
	writeJSON(w, http.StatusOK, trustDTO(id, t))
}

func (s *Server) handleRevokeTrust(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.service(w, "Revoking trust in a repo")
	if !ok {
		return
	}
	id, ok := repoParam(w, r)
	if !ok {
		return
	}
	t, err := svc.Revoke(r.Context(), id)
	if err != nil {
		s.serviceErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, trustDTO(id, t))
}

// isHash64 reports whether s is a lower-case SHA-256 hex digest.
func isHash64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
