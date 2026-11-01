package verify

import "github.com/brendatamaar/yggdrasil/internal/core"

// FailureReport is what ComposeFailure turns into a message for the agent
// (VF-03). LogTail is already redacted and cut by the shell (TailLog).
type FailureReport struct {
	Repo    core.RepoID
	Stage   Stage
	Step    string
	Reason  FailReason
	LogTail string
}

// ComposeFailure builds the "Send failure to agent" message, capped at
// MaxFailureLogBytes of log. A failed bootstrap is worded differently from a
// failed verify step. Pure; not part of Step.
//
// TODO(V1): composer. Foundations ship the signature only.
func ComposeFailure(r FailureReport) (string, error) {
	_ = r
	return "", nil
}
