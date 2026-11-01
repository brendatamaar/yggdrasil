package verify

import (
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// entry is the input of the shared Enter rule: the fields TurnEnded, Retry
// and Resume have in common, plus the turn and attempt the caller decided.
type entry struct {
	turn        TurnID
	attempt     Attempt
	facts       Facts
	plan        Plan
	bootBuild   core.BuildID
	verifyBuild core.BuildID
	now         time.Time
}

// enter is the shared entry rule of docs/design/verify.md. It returns the
// state to move to and the build command to emit, checking the conditions in
// the order the table lists them. ok is false when the input carries data that
// cannot start a run (invalid plan, turn or build IDs): the caller then
// ignores the whole input.
func (v Verification) enter(e entry) (next State, cmds []Command, ok bool) {
	if e.turn.Validate() != nil || e.plan.Validate() != nil || e.attempt.Validate() != nil {
		return nil, nil, false
	}
	switch {
	case !e.facts.Enabled:
		return Skipped{Turn: e.turn, Reason: SkipDisabled}, nil, true
	case !e.facts.WorktreeActive:
		return Unverifiable{Turn: e.turn, Reason: UnverifiableWorktreeMissing}, nil, true
	case !e.facts.Trusted:
		return Unverifiable{Turn: e.turn, Reason: UnverifiableNotTrusted}, nil, true
	case !e.facts.HasChanges:
		return Skipped{Turn: e.turn, Reason: SkipNoChanges}, nil, true
	case len(e.plan.Verify) == 0:
		return Unverifiable{Turn: e.turn, Reason: UnverifiableNoCommand}, nil, true
	}
	if e.verifyBuild.Validate() != nil {
		return nil, nil, false
	}
	if len(e.plan.Bootstrap) > 0 && e.plan.DepsHash != v.BootHash {
		if e.bootBuild.Validate() != nil || e.bootBuild == e.verifyBuild {
			return nil, nil, false
		}
		return Bootstrapping{
				Turn: e.turn, Attempt: e.attempt, Build: e.bootBuild, Plan: e.plan,
				NextBuild: e.verifyBuild, DepsHash: e.plan.DepsHash, Since: e.now,
			},
			[]Command{EnqueueBuild{Build: e.bootBuild, Stage: StageBootstrap, Spec: BuildSpec{Worktree: v.Worktree, Steps: e.plan.Bootstrap}}},
			true
	}
	return Verifying{Turn: e.turn, Attempt: e.attempt, Build: e.verifyBuild, Plan: e.plan, Since: e.now},
		[]Command{EnqueueBuild{Build: e.verifyBuild, Stage: StageVerify, Spec: BuildSpec{Worktree: v.Worktree, Steps: e.plan.Verify}}},
		true
}
