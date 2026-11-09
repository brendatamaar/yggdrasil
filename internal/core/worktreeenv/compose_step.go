package worktreeenv

// Audit actions of the compose lifecycle (invariant 8). Every status change
// carries exactly one.
const (
	ActionComposeEnabled     = "compose.enabled"
	ActionComposeDisabled    = "compose.disabled"
	ActionComposeConfirmed   = "compose.confirmed"
	ActionComposeFileChanged = "compose.file_changed"
	ActionComposeUp          = "compose.up"
	ActionComposeStarted     = "compose.started"
	ActionComposeStop        = "compose.stop"
	ActionComposeDown        = "compose.down"
	ActionComposeStopped     = "compose.stopped"
	ActionComposeFail        = "compose.fail"
	ActionComposeRecovered   = "compose.recovered"
)

// Compose is the compose-lifecycle decision for one worktree:
// (state, input) -> (state, commands). It is pure: hashes, attempt numbers and
// times arrive in the input. An input the state does not handle returns the
// state unchanged and no commands.
//
// Shutdown has no input here on purpose: nothing is executed when ygg exits,
// the containers outlive it (invariant C7).
func Compose(s Service, in ComposeInput) (Service, []ComposeCommand) {
	switch st := s.(type) {
	case Off:
		return composeOff(st, in)
	case NeedsConfirm:
		return composeNeedsConfirm(st, in)
	case Down:
		return composeDown(st, in)
	case ComposeStarting:
		return composeStarting(st, in)
	case Up:
		return composeUp(st, in)
	case ComposeStopping:
		return composeStopping(st, in)
	case ComposeFailed:
		return composeFailed(st, in)
	}
	return s, nil
}

// changed is a status change: audited and published (C6).
func composeChanged(s Service, action string) (Service, []ComposeCommand) {
	return s, []ComposeCommand{ComposeAudit{Action: action}, ComposePublish{}}
}

func composeBusy(s Service) (Service, []ComposeCommand) {
	return s, []ComposeCommand{ComposeReject{Reason: RejectBusy}}
}

// run starts an operation: the command, then the audit entry and the publish.
func runOp(s Service, op ComposeOp, attempt int, action string) (Service, []ComposeCommand) {
	return s, []ComposeCommand{RunCompose{Op: op, Attempt: attempt}, ComposeAudit{Action: action}, ComposePublish{}}
}

// startUp leaves Down or Failed for Starting, but only when the file the user
// confirmed is the file as it is now (C1, C5).
func startUp(s Service, confirmed string, in UpRequested) (Service, []ComposeCommand) {
	if in.Hash == "" {
		// The file could not be read: nothing to confirm, nothing to run.
		return s, []ComposeCommand{ComposeReject{Reason: RejectConfirmFirst}}
	}
	if in.Hash != confirmed {
		return NeedsConfirm{FileHash: in.Hash}, []ComposeCommand{
			ComposeReject{Reason: RejectConfirmFirst}, ComposeAudit{Action: ActionComposeFileChanged}, ComposePublish{},
		}
	}
	return runOp(ComposeStarting{Confirmed: confirmed, Attempt: in.Attempt, Since: in.Now}, OpUp, in.Attempt, ActionComposeUp)
}

func stopComposeOp(confirmed string, op ComposeOp, attempt int) (Service, []ComposeCommand) {
	action := ActionComposeStop
	if op == OpDown {
		action = ActionComposeDown
	}
	return runOp(ComposeStopping{Confirmed: confirmed, Op: op, Attempt: attempt}, op, attempt, action)
}

func composeOff(s Off, in ComposeInput) (Service, []ComposeCommand) {
	e, ok := in.(Enabled)
	if !ok || e.Hash == "" {
		return s, nil
	}
	if e.Confirmed == e.Hash {
		return composeChanged(Down{Confirmed: e.Hash}, ActionComposeEnabled)
	}
	return composeChanged(NeedsConfirm{FileHash: e.Hash}, ActionComposeEnabled)
}

// disable turns compose off from a state at rest. Nothing is run; containers
// stay as they are.
func composeDisable() (Service, []ComposeCommand) {
	return composeChanged(Off{}, ActionComposeDisabled)
}

func composeNeedsConfirm(s NeedsConfirm, in ComposeInput) (Service, []ComposeCommand) {
	switch v := in.(type) {
	case Confirmed:
		if v.Hash != "" && v.Hash == s.FileHash {
			return composeChanged(Down{Confirmed: v.Hash}, ActionComposeConfirmed)
		}
	case FileChanged:
		if v.Hash != "" && v.Hash != s.FileHash {
			return NeedsConfirm{FileHash: v.Hash}, []ComposeCommand{ComposePublish{}}
		}
	case Disabled:
		return composeDisable()
	case UpRequested, StopRequested, DownRequested:
		return s, []ComposeCommand{ComposeReject{Reason: RejectConfirmFirst}}
	case RecoveredPs:
		if v.Result == PsRunning {
			return composeChanged(Up{Since: v.Now}, ActionComposeRecovered)
		}
	}
	return s, nil
}

func composeDown(s Down, in ComposeInput) (Service, []ComposeCommand) {
	switch v := in.(type) {
	case UpRequested:
		return startUp(s, s.Confirmed, v)
	case DownRequested:
		return stopComposeOp(s.Confirmed, OpDown, v.Attempt)
	case FileChanged:
		if v.Hash != "" && v.Hash != s.Confirmed {
			return composeChanged(NeedsConfirm{FileHash: v.Hash}, ActionComposeFileChanged)
		}
	case Disabled:
		return composeDisable()
	case RecoveredPs:
		switch v.Result {
		case PsRunning:
			return composeChanged(Up{Confirmed: s.Confirmed, Since: v.Now}, ActionComposeRecovered)
		case PsUnavailable:
			return composeChanged(ComposeFailed{Confirmed: s.Confirmed, Kind: FailDockerMissing, At: v.Now}, ActionComposeRecovered)
		}
	}
	return s, nil
}

func composeStarting(s ComposeStarting, in ComposeInput) (Service, []ComposeCommand) {
	switch v := in.(type) {
	case OpFinished:
		if v.Attempt != s.Attempt {
			return s, nil
		}
		if v.Failure == "" {
			return composeChanged(Up{Confirmed: s.Confirmed, Attempt: s.Attempt, Since: v.Now}, ActionComposeStarted)
		}
		return composeChanged(ComposeFailed{Confirmed: s.Confirmed, Kind: v.Failure, Summary: v.Summary, At: v.Now}, ActionComposeFail)
	case UpRequested, StopRequested, DownRequested, Disabled:
		return composeBusy(s)
	}
	return s, nil
}

func composeUp(s Up, in ComposeInput) (Service, []ComposeCommand) {
	switch v := in.(type) {
	case StopRequested:
		return stopComposeOp(s.Confirmed, OpStop, v.Attempt)
	case DownRequested:
		return stopComposeOp(s.Confirmed, OpDown, v.Attempt)
	case FileChanged:
		// The project stays up; the UI warns that the file is no longer the
		// one that was confirmed.
		if v.Hash != "" && v.Hash != s.Confirmed {
			return s, []ComposeCommand{ComposePublish{}}
		}
	case Disabled:
		return composeDisable()
	}
	return s, nil
}

func composeStopping(s ComposeStopping, in ComposeInput) (Service, []ComposeCommand) {
	switch v := in.(type) {
	case OpFinished:
		if v.Attempt != s.Attempt {
			return s, nil
		}
		if v.Failure == "" {
			return composeChanged(Down{Confirmed: s.Confirmed}, ActionComposeStopped)
		}
		return composeChanged(ComposeFailed{Confirmed: s.Confirmed, Kind: v.Failure, Summary: v.Summary, At: v.Now}, ActionComposeFail)
	case UpRequested, StopRequested, DownRequested, Disabled:
		return composeBusy(s)
	}
	return s, nil
}

func composeFailed(s ComposeFailed, in ComposeInput) (Service, []ComposeCommand) {
	switch v := in.(type) {
	case UpRequested:
		return startUp(s, s.Confirmed, v)
	case StopRequested:
		return stopComposeOp(s.Confirmed, OpStop, v.Attempt)
	case DownRequested:
		return stopComposeOp(s.Confirmed, OpDown, v.Attempt)
	case FileChanged:
		if v.Hash != "" && v.Hash != s.Confirmed {
			return composeChanged(NeedsConfirm{FileHash: v.Hash}, ActionComposeFileChanged)
		}
	case Disabled:
		return composeDisable()
	}
	return s, nil
}
