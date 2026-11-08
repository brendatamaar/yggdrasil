package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	ct "github.com/brendatamaar/yggdrasil/internal/core/terminal"
	"github.com/brendatamaar/yggdrasil/internal/terminal"
)

// The UI maps exactly these statuses (docs/design/terminal.md).
func TestTerminalRefusalsHaveTheStatusesTheUIExpects(t *testing.T) {
	e := newEnv(t)
	for reason, want := range map[ct.Refusal]int{
		ct.RefusedNotTracked:      http.StatusNotFound,
		ct.RefusedTooManySessions: http.StatusTooManyRequests,
		ct.RefusedTooManyPending:  http.StatusTooManyRequests,
		ct.RefusedExpired:         http.StatusGone,
		ct.RefusedReused:          http.StatusGone,
		ct.RefusedUnknown:         http.StatusGone,
		ct.RefusedShuttingDown:    http.StatusServiceUnavailable,
	} {
		w := httptest.NewRecorder()
		e.srv.terminalFailure(w, httptest.NewRequest("POST", "/api/terminal", nil), &terminal.RefusedError{Reason: reason})
		if w.Code != want {
			t.Errorf("%s = %d, want %d", reason, w.Code, want)
		}
	}
	w := httptest.NewRecorder()
	e.srv.terminalFailure(w, httptest.NewRequest("POST", "/api/terminal", nil), terminal.ErrNotTracked)
	if w.Code != http.StatusNotFound {
		t.Errorf("ErrNotTracked = %d, want 404", w.Code)
	}
}
