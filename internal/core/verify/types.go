package verify

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Limits.
const (
	// MaxAutoAttempts is the first run plus one automatic resume after a
	// restart; the user's Retry is the only way past it.
	MaxAutoAttempts = 2
	// MaxSteps and MaxNameRunes bound one chain, like core/builds.
	MaxSteps     = 64
	MaxNameRunes = 200
	// MaxEnv bounds the extra environment of one step.
	MaxEnv = 32
	// MaxFailureLogBytes caps the log tail ComposeFailure accepts.
	MaxFailureLogBytes = 16 * 1024
	// MaxStartFailureRunes caps the text of a StartFailure.
	MaxStartFailureRunes = 300
)

// TurnID identifies the task turn a verification is for. It mirrors
// core/tasks.TurnID; the shell converts.
type TurnID string

// Validate reports whether the ID is well formed.
func (id TurnID) Validate() error {
	if id == "" || strings.ContainsAny(string(id), " \t\r\n/\\") {
		return fmt.Errorf("turn id %q: %w", string(id), core.ErrInvalidID)
	}
	return nil
}

// Attempt counts the runs of one turn, from 1. It never decreases.
type Attempt int

// Validate reports whether a is 1 or more.
func (a Attempt) Validate() error {
	if a < 1 {
		return fmt.Errorf("attempt %d: must be 1 or more", int(a))
	}
	return nil
}

// SkipReason says why a repo is not verified, by rule.
type SkipReason string

const (
	SkipNoChanges SkipReason = "no_changes"
	SkipDisabled  SkipReason = "disabled"
)

// Validate reports whether r is known.
func (r SkipReason) Validate() error {
	switch r {
	case SkipNoChanges, SkipDisabled:
		return nil
	}
	return fmt.Errorf("unknown skip reason %q", string(r))
}

// UnverifiableReason says why a repo that should be verified could not be.
type UnverifiableReason string

const (
	UnverifiableNoCommand       UnverifiableReason = "no_command"
	UnverifiableNotTrusted      UnverifiableReason = "not_trusted"
	UnverifiableWorktreeMissing UnverifiableReason = "worktree_missing"
	UnverifiableCouldNotQueue   UnverifiableReason = "could_not_queue"
)

// Validate reports whether r is known.
func (r UnverifiableReason) Validate() error {
	switch r {
	case UnverifiableNoCommand, UnverifiableNotTrusted, UnverifiableWorktreeMissing, UnverifiableCouldNotQueue:
		return nil
	}
	return fmt.Errorf("unknown unverifiable reason %q", string(r))
}

// Stage is the part of a verification a build belongs to.
type Stage string

const (
	StageBootstrap Stage = "bootstrap"
	StageVerify    Stage = "verify"
)

// Validate reports whether s is known.
func (s Stage) Validate() error {
	switch s {
	case StageBootstrap, StageVerify:
		return nil
	}
	return fmt.Errorf("unknown stage %q", string(s))
}

// CancelCause says who cancelled.
type CancelCause string

const (
	CancelUser     CancelCause = "user"
	CancelExternal CancelCause = "external"
	// CancelNotTrusted: the repo stopped being trusted while the chain ran.
	CancelNotTrusted CancelCause = "not_trusted"
)

// Validate reports whether c is known.
func (c CancelCause) Validate() error {
	switch c {
	case CancelUser, CancelExternal, CancelNotTrusted:
		return nil
	}
	return fmt.Errorf("unknown cancel cause %q", string(c))
}

// StartFailure is the plain-language reason a step could not start (for
// example "npm was not found"). It is shown to the user and never holds a
// token (the shell redacts before it builds one).
type StartFailure string

// Validate reports whether f is non-empty, short and free of control
// characters.
func (f StartFailure) Validate() error {
	if f == "" || utf8.RuneCountInString(string(f)) > MaxStartFailureRunes ||
		strings.ContainsFunc(string(f), func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return errors.New("a start failure must be 1 to 300 characters without control characters")
	}
	return nil
}

// FailReason is how a step failed (sealed): ExitCode or StartFailed.
type FailReason interface {
	Validate() error
	isFailReason()
}

// ExitCode: the step ran and exited with a non-zero code.
type ExitCode struct{ Code int }

// StartFailed: the step could not be started.
type StartFailed struct{ Failure StartFailure }

func (ExitCode) isFailReason()    {}
func (StartFailed) isFailReason() {}

// Validate reports whether the code is non-zero.
func (r ExitCode) Validate() error {
	if r.Code == 0 {
		return errors.New("a failed step cannot have exit code 0")
	}
	return nil
}

// Validate reports whether the failure text is valid.
func (r StartFailed) Validate() error { return r.Failure.Validate() }

// StepSpec is one command of a chain: argv only, with extra environment on
// top of the clean one (never os.Environ). It mirrors core/builds.StepSpec.
type StepSpec struct {
	Name string
	Argv []string
	Env  []string
}

// Validate checks the shape rules the core can check; the shell parser adds
// the platform rules.
func (s StepSpec) Validate() error {
	if n := utf8.RuneCountInString(s.Name); strings.TrimSpace(s.Name) == "" || n > MaxNameRunes {
		return fmt.Errorf("step name must have 1 to %d characters", MaxNameRunes)
	}
	if len(s.Argv) == 0 || s.Argv[0] == "" {
		return fmt.Errorf("step %q has no command", s.Name)
	}
	for _, a := range s.Argv {
		if strings.ContainsRune(a, 0) {
			return fmt.Errorf("step %q has a NUL character in its command", s.Name)
		}
	}
	if len(s.Env) > MaxEnv {
		return fmt.Errorf("step %q has more than %d environment entries", s.Name, MaxEnv)
	}
	for _, e := range s.Env {
		if k, _, ok := strings.Cut(e, "="); !ok || k == "" || strings.ContainsRune(e, 0) {
			return fmt.Errorf("step %q has an invalid environment entry", s.Name)
		}
	}
	return nil
}

// Plan is what the shell resolved from the toolchain defaults, the repo's
// override and its full-tests opt-in (BL-08). The core never sees toolchains
// or settings.
type Plan struct {
	// Bootstrap steps run before the first verification and again when
	// DepsHash changes (WE-05). May be empty.
	Bootstrap []StepSpec
	// Verify is the chain, with full tests already appended when opted in.
	Verify []StepSpec
	// DepsHash is the hash of the dependency manifests the bootstrap is for.
	DepsHash string
}

// Validate checks both chains.
func (p Plan) Validate() error {
	for _, c := range [][]StepSpec{p.Bootstrap, p.Verify} {
		if len(c) > MaxSteps {
			return fmt.Errorf("a chain can have at most %d steps", MaxSteps)
		}
		for i, s := range c {
			if err := s.Validate(); err != nil {
				return fmt.Errorf("step %d: %w", i+1, err)
			}
		}
	}
	return nil
}

// Facts are read by the shell and parsed at the boundary before Enter.
type Facts struct {
	// Enabled is the project switch "run verification automatically".
	Enabled bool
	// Trusted is the repo's trust (SF-09).
	Trusted bool
	// WorktreeActive: the task's worktree for the repo exists and is active.
	WorktreeActive bool
	// HasChanges: non-empty diff against the worktree base commit, untracked
	// files included.
	HasChanges bool
}

// BuildSpec is what an EnqueueBuild asks the builds service to run: the
// stage's steps in the task's worktree.
type BuildSpec struct {
	Worktree core.WorktreeID
	Steps    []StepSpec
}

// Validate checks the worktree and the 1 to MaxSteps steps.
func (s BuildSpec) Validate() error {
	if err := s.Worktree.Validate(); err != nil {
		return err
	}
	if len(s.Steps) == 0 || len(s.Steps) > MaxSteps {
		return fmt.Errorf("a build needs 1 to %d steps", MaxSteps)
	}
	for i, st := range s.Steps {
		if err := st.Validate(); err != nil {
			return fmt.Errorf("step %d: %w", i+1, err)
		}
	}
	return nil
}
