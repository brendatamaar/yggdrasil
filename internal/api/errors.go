package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
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

// fail maps an error to a response by its class (internal/apperr) and nothing
// else: an owning package translates its own errors into an apperr.Error, so
// this function never learns another package's sentinels. The message is the
// error's own plain-language text. An error without a class is a bug or an
// outage: its details are logged (redacted by the log handler) and the client
// gets a generic message.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	class, ok := apperr.ClassOf(err)
	msg, _ := apperr.MessageOf(err)
	if !ok || msg == "" {
		s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, ErrInternal,
			"Something went wrong inside Yggdrasil.", "Try again. If it keeps happening, check the Yggdrasil log.")
		return
	}
	switch class {
	case apperr.NotFound:
		writeError(w, http.StatusNotFound, ErrNotFound, msg, "Refresh the page and try again.")
	case apperr.Conflict:
		writeError(w, http.StatusConflict, ErrConflict, msg, "Reload the page to see the current state, then try again.")
	case apperr.Invalid:
		writeError(w, http.StatusBadRequest, ErrBadRequest, msg, "Check the values you sent and try again.")
	case apperr.Unavailable:
		writeError(w, http.StatusServiceUnavailable, ErrUnavailable, msg, "Try again in a moment.")
	default:
		s.log.Error("request failed with an unknown error class", "method", r.Method, "path", r.URL.Path, "class", class, "err", err)
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
	// Exactly one JSON value: anything after it (even a second object) is refused.
	if err := dec.Decode(v); err != nil || dec.Decode(&json.RawMessage{}) != io.EOF {
		badRequest(w, "The request body isn't valid.", "Send a JSON object with the documented fields.")
		return false
	}
	return true
}

const maxBody = 1 << 20
