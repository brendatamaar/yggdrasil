package api

import (
	"context"
	"net/http"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/verify"
)

// The verification endpoints (VF-01 to VF-03). Phase 4 foundations: the
// routes and the service interface exist, every handler answers 501 until AP1
// replaces it. The DTOs are in types_verify.go. The file is excluded from tygo
// (tygo.yaml): it holds Go-only wiring.

// VerificationService is implemented by an adapter in cmd/ygg over
// internal/verify (docs/design/verify.md). A nil service answers 501.
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
	// (VF-03). A busy task is a conflict, not an error to retry silently.
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

// TODO(AP1): handlers.

func (s *Server) handleListVerifications(w http.ResponseWriter, _ *http.Request) {
	notReady(w, "Verification results")
}

func (s *Server) handleRetryVerification(w http.ResponseWriter, _ *http.Request) {
	notReady(w, "Verifying again")
}

func (s *Server) handleCancelVerification(w http.ResponseWriter, _ *http.Request) {
	notReady(w, "Cancelling a verification")
}

func (s *Server) handleSendFailure(w http.ResponseWriter, _ *http.Request) {
	notReady(w, "Sending a failure to the agent")
}
