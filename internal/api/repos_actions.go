package api

import "net/http"

// This file is owned by the git actions task (WS-06 to WS-08): pull, fetch,
// checkout, branch switch and open in editor. Its GitActionsService interface
// and its Deps field are added by that task; until then nothing is wired and
// nothing is registered. The file is excluded from tygo (tygo.yaml).

// routesReposActions registers the git action endpoints.
func (s *Server) routesReposActions(mux *http.ServeMux) {
	_ = mux // the git actions task adds its routes here
}
