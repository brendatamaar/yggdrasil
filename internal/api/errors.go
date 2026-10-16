package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/brendatamaar/yggdrasil/internal/db"
)

// writeJSON writes v as the response body.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes an ErrorBody (SF-01). message and hint are plain language;
// they never carry internal error text, paths or tokens.
func writeError(w http.ResponseWriter, status int, code ErrorCode, message, hint string) {
	writeJSON(w, status, ErrorBody{Code: code, Message: message, Hint: hint})
}

// fail maps an internal error to a response. Internal details are logged
// (redacted by the log handler) and never sent to the client.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeError(w, http.StatusNotFound, ErrNotFound,
			"That item doesn't exist (any more).", "Refresh the page and try again.")
	case errors.Is(err, db.ErrSubTaskDepth):
		writeError(w, http.StatusConflict, ErrConflict,
			"Sub-tasks can only be one level deep.", "Create the task without a parent.")
	default:
		s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, ErrInternal,
			"Something went wrong inside Yggdrasil.", "Try again. If it keeps happening, check the Yggdrasil log.")
	}
}

// badRequest writes a 400.
func badRequest(w http.ResponseWriter, message, hint string) {
	writeError(w, http.StatusBadRequest, ErrBadRequest, message, hint)
}

// decodeBody reads a JSON body of at most maxBody bytes into v, refusing
// unknown fields.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		badRequest(w, "The request body isn't valid.", "Send a JSON object with the documented fields.")
		return false
	}
	return true
}

const maxBody = 1 << 20
