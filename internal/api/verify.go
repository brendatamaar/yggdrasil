package api

import (
	"context"
	"net/http"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/verify"
)

// The verification endpoints (VF-01 to VF-03). The DTOs are in
// types_verify.go; verify_adapter.go adapts internal/verify to the service
// interface. The file is excluded from tygo (tygo.yaml): it holds Go-only
// wiring.

// VerificationService is implemented over internal/verify (see
// NewVerificationService). A nil service answers 501.
//
// Errors the handlers translate: apperr classes (NotFound, Conflict,
// Unavailable); anything else is a generic 500 with the details only in the log.
type VerificationService interface {
	// List returns a task's verifications, one per repo with a worktree.
	List(ctx context.Context, task core.TaskID) (VerificationsResponse, error)
	// Retry verifies the task's repos again ("Verify again"). A live run is not
	// replaced: the user cancels first.
	Retry(ctx context.Context, task core.TaskID) (VerificationsResponse, error)
	// Cancel stops a live verification.
	Cancel(ctx context.Context, id verify.VerificationID) (VerificationDTO, error)
	// SendFailure sends a failed verification to the agent as a new message
	// (VF-03). A busy task is a conflict, not an error to retry silently. An
	// unknown outcome is a conflict too: the failure is never sent twice.
	SendFailure(ctx context.Context, id verify.VerificationID) (SendFailureResponse, error)
}

// routesVerify registers the verification endpoints. They are registered
// whether or not the service is wired.
func (s *Server) routesVerify(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/tasks/{id}/verifications", s.handleListVerifications)
	mux.HandleFunc("POST /api/tasks/{id}/verify/retry", s.handleRetryVerification)
	mux.HandleFunc("POST /api/verifications/{id}/cancel", s.handleCancelVerification)
	mux.HandleFunc("POST /api/verifications/{id}/send-failure", s.handleSendFailure)
}

func (s *Server) handleListVerifications(w http.ResponseWriter, r *http.Request) {
	svc := s.phase4().Verification
	if svc == nil {
		notReady(w, "Verification results")
		return
	}
	task, ok := pathTaskID(w, r)
	if !ok {
		return
	}
	out, err := svc.List(r.Context(), task)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, normalizeVerifications(out))
}

func (s *Server) handleRetryVerification(w http.ResponseWriter, r *http.Request) {
	svc := s.phase4().Verification
	if svc == nil {
		notReady(w, "Verifying again")
		return
	}
	task, ok := pathTaskID(w, r)
	if !ok {
		return
	}
	out, err := svc.Retry(r.Context(), task)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, normalizeVerifications(out))
}

// verificationID reads the {id} path value; it answers 404 itself.
func verificationID(w http.ResponseWriter, r *http.Request) (verify.VerificationID, bool) {
	id, ok := pathOtherID(w, r, "verification", func(s string) error { return verify.VerificationID(s).Validate() })
	return verify.VerificationID(id), ok
}

func (s *Server) handleCancelVerification(w http.ResponseWriter, r *http.Request) {
	svc := s.phase4().Verification
	if svc == nil {
		notReady(w, "Cancelling a verification")
		return
	}
	id, ok := verificationID(w, r)
	if !ok {
		return
	}
	out, err := svc.Cancel(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleSendFailure(w http.ResponseWriter, r *http.Request) {
	svc := s.phase4().Verification
	if svc == nil {
		notReady(w, "Sending a failure to the agent")
		return
	}
	id, ok := verificationID(w, r)
	if !ok {
		return
	}
	out, err := svc.SendFailure(r.Context(), id)
	if err != nil {
		// A conflict (agent busy, already sent, result unknown) carries the
		// service's own plain-language message to the UI.
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// normalizeVerifications makes the list a JSON array, never null.
func normalizeVerifications(v VerificationsResponse) VerificationsResponse {
	if v.Verifications == nil {
		v.Verifications = []VerificationDTO{}
	}
	return v
}
