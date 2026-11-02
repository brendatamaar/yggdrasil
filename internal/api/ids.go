package api

import (
	"net/http"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// pathTaskID reads and validates the {id} path value as a task ID; it answers
// 404 itself (an ID that can't exist is a missing item).
func pathTaskID(w http.ResponseWriter, r *http.Request) (core.TaskID, bool) {
	id := core.TaskID(r.PathValue("id"))
	if id.Validate() != nil {
		writeError(w, http.StatusNotFound, ErrNotFound, "That task doesn't exist (any more).", "Refresh the page and try again.")
		return "", false
	}
	return id, true
}

// pathOtherID validates the {id} path value with check and answers 404 with
// what ("verification", "note", ...) when it is not well formed.
func pathOtherID(w http.ResponseWriter, r *http.Request, what string, check func(string) error) (string, bool) {
	id := r.PathValue("id")
	if check(id) != nil {
		writeError(w, http.StatusNotFound, ErrNotFound, "That "+what+" doesn't exist (any more).", "Refresh the page and try again.")
		return "", false
	}
	return id, true
}
