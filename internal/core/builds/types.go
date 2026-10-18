package builds

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Limits on a Spec, enforced by Validate and, with friendlier messages, by the
// request parser in the shell.
const (
	MaxSteps     = 64
	MaxNameRunes = 200
)

// Actor is who asked for a change: "user", "agent:<name>" or "system". It
// ends up in the audit log.
type Actor string

// Validate reports whether a is non-empty and has no control characters.
func (a Actor) Validate() error {
	if a == "" || strings.ContainsFunc(string(a), isControl) {
		return fmt.Errorf("invalid actor %q", string(a))
	}
	return nil
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

// PresetID identifies a saved build preset (BL-05).
type PresetID string

// Validate reports whether the ID is well formed.
func (id PresetID) Validate() error {
	if id == "" || strings.ContainsAny(string(id), " \t\r\n/\\") {
		return fmt.Errorf("preset id %q: %w", string(id), core.ErrInvalidID)
	}
	return nil
}

// Target says where a build runs. It is sealed: MainCheckout or InWorktree.
// A worktree build never falls back to the main checkout.
type Target interface{ isTarget() }

// MainCheckout runs the build in each repo's main checkout.
type MainCheckout struct{}

// InWorktree runs the build in one task worktree (BL-09).
type InWorktree struct{ WorktreeID core.WorktreeID }

func (MainCheckout) isTarget() {}
func (InWorktree) isTarget()   {}

// StepSpec is one command of a build: argv only, with no shell entry point
// (BL-15). The shell resolves Argv[0] in the repo's directory.
type StepSpec struct {
	RepoID core.RepoID
	Name   string
	Argv   []string
}

// Validate checks the shape rules the core can check: the shell parser adds
// the platform rules (shell metacharacters) and the repo lookups.
func (s StepSpec) Validate() error {
	if err := s.RepoID.Validate(); err != nil {
		return err
	}
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
	return nil
}

// Spec is what to build: a target and an ordered, non-empty list of steps. A
// Spec with no step is not a state; it is rejected at the boundary.
type Spec struct {
	Target Target
	Steps  []StepSpec
	// PresetID is the preset the build was started from, "" when none.
	PresetID PresetID
}

// Validate checks the target and the 1 to MaxSteps steps.
func (s Spec) Validate() error {
	switch t := s.Target.(type) {
	case MainCheckout:
	case InWorktree:
		if err := t.WorktreeID.Validate(); err != nil {
			return err
		}
	default:
		return errors.New("build has no target")
	}
	if len(s.Steps) == 0 {
		return errors.New("a build needs at least one step")
	}
	if len(s.Steps) > MaxSteps {
		return fmt.Errorf("a build can have at most %d steps", MaxSteps)
	}
	for i, st := range s.Steps {
		if err := st.Validate(); err != nil {
			return fmt.Errorf("step %d: %w", i+1, err)
		}
	}
	if s.PresetID != "" {
		return s.PresetID.Validate()
	}
	return nil
}

// Preset is a saved list of steps for a project (BL-05).
type Preset struct {
	ID        PresetID
	ProjectID core.ProjectID
	Name      string
	Steps     []StepSpec
}

// Validate checks the ID, project, name and steps.
func (p Preset) Validate() error {
	if err := p.ID.Validate(); err != nil {
		return err
	}
	if err := p.ProjectID.Validate(); err != nil {
		return err
	}
	if n := utf8.RuneCountInString(p.Name); strings.TrimSpace(p.Name) == "" || n > MaxNameRunes {
		return fmt.Errorf("preset name must have 1 to %d characters", MaxNameRunes)
	}
	return Spec{Target: MainCheckout{}, Steps: p.Steps}.Validate()
}

// Artifact is a file a successful build produced (BL-07).
type Artifact struct {
	RepoID core.RepoID
	// Path is relative to the repo, with forward slashes.
	Path string
	Size int64
}

// Coord is a Maven-style coordinate (BL-04).
type Coord struct{ Group, Artifact, Version string }

// Module is one repo's place in the cross-repo dependency graph (BL-04).
type Module struct {
	RepoID core.RepoID
	Coord  Coord
	Deps   []Coord
}
