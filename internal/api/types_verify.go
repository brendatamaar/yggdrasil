package api

import (
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// DTOs of automatic verification (VF-01 to VF-03, WE-05; docs/design/
// verify.md). The handlers are in verify.go.

// TopicVerifications carries VerificationChangedMessage: a verification of any
// task changed state. The client refetches the task's verifications.
const TopicVerifications = "verifications"

// VerificationChangedMessage is the data of a message on TopicVerifications.
// It holds no log, path or command (docs/AGENTS.md 5.9).
type VerificationChangedMessage struct {
	VerificationID string      `json:"verificationId"`
	TaskID         core.TaskID `json:"taskId"`
	RepoID         core.RepoID `json:"repoId"`
	State          string      `json:"state"`
}

// The values of VerificationDTO.State and VerificationSummaryDTO.State.
const (
	VerifyNone          = "none"
	VerifySkipped       = "skipped"
	VerifyUnverifiable  = "unverifiable"
	VerifyBootstrapping = "bootstrapping"
	VerifyVerifying     = "verifying"
	VerifyPassed        = "passed"
	VerifyFailed        = "failed"
	VerifyInterrupted   = "interrupted"
	VerifyCancelled     = "cancelled"
)

// VerificationDTO is the verification of one repo of a task. State is one of
// the Verify* values. Reason is the skip reason, unverifiable reason or cancel
// cause in plain words, by state. Stage ("bootstrap" or "verify") and
// FailedStep (the step name) are set on a failed or interrupted verification;
// ExitCode only when a step ran and exited non-zero. BuildID joins the log in
// the Builds page. CanSendFailure is true when "Send failure to agent" is
// offered.
type VerificationDTO struct {
	ID             string       `json:"id"`
	TaskID         core.TaskID  `json:"taskId"`
	RepoID         core.RepoID  `json:"repoId"`
	RepoName       string       `json:"repoName,omitempty"`
	WorktreeID     string       `json:"worktreeId"`
	State          string       `json:"state"`
	Turn           string       `json:"turn,omitempty"`
	Attempt        int          `json:"attempt,omitempty"`
	Reason         string       `json:"reason,omitempty"`
	Stage          string       `json:"stage,omitempty"`
	FailedStep     string       `json:"failedStep,omitempty"`
	ExitCode       *int         `json:"exitCode,omitempty"`
	Message        string       `json:"message,omitempty"`
	BuildID        core.BuildID `json:"buildId,omitempty"`
	Since          *time.Time   `json:"since,omitempty"`
	At             *time.Time   `json:"at,omitempty"`
	CanSendFailure bool         `json:"canSendFailure,omitempty"`
}

// VerificationsResponse is the body of GET /api/tasks/{id}/verifications and
// POST /api/tasks/{id}/verify/retry.
type VerificationsResponse struct {
	Verifications []VerificationDTO       `json:"verifications"`
	Summary       VerificationSummaryDTO `json:"summary"`
}

// VerificationSummaryDTO is the worst state over a task's repos (Failed >
// Interrupted > Unverifiable > Cancelled > running > Passed > Skipped > None),
// with counts. It is derived, never stored. The board card and the task view
// carry it as an optional field that stays absent until verification is wired.
type VerificationSummaryDTO struct {
	State   string `json:"state"`
	Total   int    `json:"total"`
	Passed  int    `json:"passed"`
	Failed  int    `json:"failed"`
	Running int    `json:"running"`
}

// SendFailureResponse is the body of POST /api/verifications/{id}/send-failure:
// the failure was sent to the agent as a new message and TurnID is its turn.
type SendFailureResponse struct {
	TaskID core.TaskID `json:"taskId"`
	TurnID string      `json:"turnId"`
}
