package api

import "net/http"

// notReady answers an endpoint whose service is not built yet. It is a 501
// with a plain-language message, so a client (or a person) sees that the
// feature is missing instead of a confusing 404. The Phase 3 tasks replace
// the stub handlers one area at a time. The file is excluded from tygo
// (tygo.yaml): it holds Go-only wiring.
func notReady(w http.ResponseWriter, what string) {
	writeError(w, http.StatusNotImplemented, ErrUnavailable,
		what+" isn't available in this version of Yggdrasil yet.", "Check back after the next update.")
}
