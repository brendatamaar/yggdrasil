package worktreeenv

import "time"

// Service is the compose state of one worktree. It is a closed set: Off,
// NeedsConfirm, Down, ComposeStarting, Up, ComposeStopping, ComposeFailed.
type Service interface{ isService() }

// Off means compose is not enabled for the repo.
type Off struct{}

// NeedsConfirm means the compose file changed (or was never confirmed).
type NeedsConfirm struct{ FileHash string }

// Down has no running project; Confirmed is the hash the user confirmed.
type Down struct{ Confirmed string }

// ComposeStarting runs `up -d`.
type ComposeStarting struct {
	Confirmed string
	Attempt   int
	Since     time.Time
}

// Up has a running project.
type Up struct {
	Confirmed string
	Attempt   int
	Since     time.Time
}

// ComposeStopping runs `stop` or `down`.
type ComposeStopping struct {
	Confirmed string
	Op        ComposeOp
	Attempt   int
}

// FailKind says why compose failed.
type FailKind string

// The closed set of FailKind values.
const (
	FailDockerMissing FailKind = "docker_missing"
	FailDaemonDown    FailKind = "daemon_down"
	FailComposeError  FailKind = "compose_error"
)

// ComposeFailed is at rest; it may retry.
type ComposeFailed struct {
	Confirmed string
	Kind      FailKind
	Summary   string // redacted, no paths
	At        time.Time
}

func (Off) isService()             {}
func (NeedsConfirm) isService()    {}
func (Down) isService()            {}
func (ComposeStarting) isService() {}
func (Up) isService()              {}
func (ComposeStopping) isService() {}
func (ComposeFailed) isService()   {}

// PsResult is what `compose ps` found at startup.
type PsResult string

// The closed set of PsResult values.
const (
	PsRunning     PsResult = "running"
	PsNotRunning  PsResult = "not_running"
	PsUnavailable PsResult = "unavailable"
)

// ComposeInput is a closed set.
type ComposeInput interface{ isComposeInput() }

// Enabled turns compose on for the repo with the current file hash.
type Enabled struct{ Hash string }

// Disabled turns compose off; containers are left as they are.
type Disabled struct{}

// Confirmed is the user's confirmation of a file hash.
type Confirmed struct{ Hash string }

// FileChanged reports a new hash of the compose file.
type FileChanged struct{ Hash string }

// UpRequested, StopRequested and DownRequested are the user's actions.
type (
	UpRequested struct {
		Attempt int
		Now     Now
	}
	StopRequested struct {
		Attempt int
		Now     Now
	}
	DownRequested struct {
		Attempt int
		Now     Now
	}
)

// OpFinished reports the end of an operation. Failure is empty on success.
type OpFinished struct {
	Attempt int
	Failure FailKind
	Summary string
	Now     Now
}

// RecoveredPs is fed once at startup.
type RecoveredPs struct {
	Result PsResult
	Hash   string
	Now    Now
}

func (Enabled) isComposeInput()       {}
func (Disabled) isComposeInput()      {}
func (Confirmed) isComposeInput()     {}
func (FileChanged) isComposeInput()   {}
func (UpRequested) isComposeInput()   {}
func (StopRequested) isComposeInput() {}
func (DownRequested) isComposeInput() {}
func (OpFinished) isComposeInput()    {}
func (RecoveredPs) isComposeInput()   {}

// ComposeCommand is a closed set: RunCompose, ComposeAudit, ComposePublish, ComposeReject.
type ComposeCommand interface{ isComposeCommand() }

// RunCompose runs one operation in the worktree. Op is a closed set and the
// command has no field for extra arguments (invariant C2).
type RunCompose struct {
	Op      ComposeOp
	Attempt int
}

// ComposeAudit is committed with the transition.
type ComposeAudit struct {
	Action string // compose.confirmed, compose.up, compose.stop, compose.down, compose.fail
}

// ComposePublish tells subscribers the state changed.
type ComposePublish struct{}

// ComposeRejectReason is the closed set of reasons a compose request is refused.
type ComposeRejectReason string

// The closed set of ComposeRejectReason values.
const (
	RejectConfirmFirst ComposeRejectReason = "confirm_first"
	RejectBusy         ComposeRejectReason = "busy"
)

// ComposeReject answers a refused request.
type ComposeReject struct{ Reason ComposeRejectReason }

func (RunCompose) isComposeCommand()     {}
func (ComposeAudit) isComposeCommand()   {}
func (ComposePublish) isComposeCommand() {}
func (ComposeReject) isComposeCommand()  {}

// Compose is the compose-lifecycle decision. Foundation stub; task E2 implements it.
func Compose(s Service, in ComposeInput) (Service, []ComposeCommand) {
	return s, nil
}
