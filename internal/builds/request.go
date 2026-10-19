package builds

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// maxRequestBytes bounds a request body or preset the parsers look at.
const maxRequestBytes = 1 << 20

// Known is what ParseRequest checks a request against: the tracked repos and
// the worktree each tracked worktree belongs to.
type Known struct {
	Repos     map[core.RepoID]struct{}
	Worktrees map[core.WorktreeID]core.RepoID
}

// requestJSON is the body of POST /api/builds.
type requestJSON struct {
	// Target is "main" or "worktree".
	Target     string     `json:"target"`
	WorktreeID string     `json:"worktreeId,omitempty"`
	PresetID   string     `json:"presetId,omitempty"`
	Steps      []stepJSON `json:"steps"`
}

type stepJSON struct {
	RepoID string   `json:"repoId"`
	Name   string   `json:"name"`
	Argv   []string `json:"argv"`
}

// presetJSON is the body of POST /api/build-presets. The handler sets ID
// (generated) before parsing.
type presetJSON struct {
	ID        string     `json:"id"`
	ProjectID string     `json:"projectId"`
	Name      string     `json:"name"`
	Steps     []stepJSON `json:"steps"`
}

func invalid(msg string, err error) error { return apperr.New(apperr.Invalid, msg, err) }

func decodeStrict(raw []byte, into any) error {
	if len(raw) > maxRequestBytes {
		return invalid("The request is too large. Send at most 1 MiB.", nil)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return invalid("The request is not valid JSON in the expected shape. Check the field names and types.", err)
	}
	if dec.More() {
		return invalid("The request has extra data after the JSON object.", nil)
	}
	return nil
}

// ParseRequest parses and checks a build request against the tracked repos
// and worktrees.
func ParseRequest(raw []byte, known Known) (corebuilds.Spec, error) {
	var req requestJSON
	if err := decodeStrict(raw, &req); err != nil {
		return corebuilds.Spec{}, err
	}
	spec := corebuilds.Spec{PresetID: corebuilds.PresetID(req.PresetID)}
	var wtRepo core.RepoID
	switch req.Target {
	case "main":
		if req.WorktreeID != "" {
			return spec, invalid("A build in the main checkout cannot name a worktree. Use target \"worktree\" or remove worktreeId.", nil)
		}
		spec.Target = corebuilds.MainCheckout{}
	case "worktree":
		wt := core.WorktreeID(req.WorktreeID)
		if err := wt.Validate(); err != nil {
			return spec, invalid("A build in a worktree needs a valid worktreeId.", err)
		}
		repo, ok := known.Worktrees[wt]
		if !ok {
			return spec, invalid("That worktree is not known. Pick a worktree of a tracked repo.", nil)
		}
		wtRepo = repo
		spec.Target = corebuilds.InWorktree{WorktreeID: wt}
	default:
		return spec, invalid(`The target must be "main" or "worktree".`, nil)
	}
	if req.PresetID != "" {
		if err := spec.PresetID.Validate(); err != nil {
			return spec, invalid("The preset ID is not valid.", err)
		}
	}
	steps, err := parseSteps(req.Steps, func(r core.RepoID) error {
		if _, ok := known.Repos[r]; !ok {
			return invalid("A step names a repo that is not tracked. Track the repo first or pick another.", nil)
		}
		if wtRepo != "" && r != wtRepo {
			return invalid("A step names a repo that the worktree does not belong to. A worktree build can only run steps of its own repo.", nil)
		}
		return nil
	})
	if err != nil {
		return spec, err
	}
	spec.Steps = steps
	if err := spec.Validate(); err != nil {
		return corebuilds.Spec{}, invalid("The build request is not valid.", err)
	}
	return spec, nil
}

// ParsePreset parses and checks a preset to save. It cannot check that the
// repos are tracked (the preset belongs to a project, not a request): the
// handler checks the project and ParseRequest checks the repos when a preset
// runs.
func ParsePreset(raw []byte) (corebuilds.Preset, error) {
	var req presetJSON
	if err := decodeStrict(raw, &req); err != nil {
		return corebuilds.Preset{}, err
	}
	p := corebuilds.Preset{ID: corebuilds.PresetID(req.ID), ProjectID: core.ProjectID(req.ProjectID), Name: req.Name}
	if err := p.ID.Validate(); err != nil {
		return p, invalid("The preset needs a valid ID.", err)
	}
	if err := p.ProjectID.Validate(); err != nil {
		return p, invalid("The preset needs a valid project.", err)
	}
	if n := utf8.RuneCountInString(p.Name); isBlank(p.Name) || n > corebuilds.MaxNameRunes {
		return p, invalid(fmt.Sprintf("The preset name must have 1 to %d characters.", corebuilds.MaxNameRunes), nil)
	}
	steps, err := parseSteps(req.Steps, func(core.RepoID) error { return nil })
	if err != nil {
		return p, err
	}
	p.Steps = steps
	if err := p.Validate(); err != nil {
		return corebuilds.Preset{}, invalid("The preset is not valid.", err)
	}
	return p, nil
}

func isBlank(s string) bool {
	for _, r := range s {
		if r != ' ' && r != '\t' && r != '\n' && r != '\r' {
			return false
		}
	}
	return true
}

func parseSteps(in []stepJSON, repoOK func(core.RepoID) error) ([]corebuilds.StepSpec, error) {
	switch {
	case len(in) == 0:
		return nil, invalid("A build needs at least one step.", nil)
	case len(in) > corebuilds.MaxSteps:
		return nil, invalid(fmt.Sprintf("A build can have at most %d steps.", corebuilds.MaxSteps), nil)
	}
	out := make([]corebuilds.StepSpec, len(in))
	for i, s := range in {
		n := i + 1
		repo := core.RepoID(s.RepoID)
		if err := repo.Validate(); err != nil {
			return nil, invalid(fmt.Sprintf("Step %d needs a valid repo.", n), err)
		}
		if err := repoOK(repo); err != nil {
			return nil, withStep(err, n)
		}
		if c := utf8.RuneCountInString(s.Name); isBlank(s.Name) || c > corebuilds.MaxNameRunes {
			return nil, invalid(fmt.Sprintf("Step %d needs a name of 1 to %d characters.", n, corebuilds.MaxNameRunes), nil)
		}
		if len(s.Argv) == 0 || s.Argv[0] == "" {
			return nil, invalid(fmt.Sprintf("Step %d has no command. Give the program to run as the first argument.", n), nil)
		}
		for _, a := range s.Argv {
			for _, r := range a {
				if r == 0 {
					return nil, invalid(fmt.Sprintf("Step %d has a NUL character in its command. Remove it.", n), nil)
				}
			}
		}
		if err := platform.BatchArgsOK(s.Argv); err != nil {
			return nil, invalid(fmt.Sprintf("Step %d: %s", n, userBatchMessage(err)), err)
		}
		out[i] = corebuilds.StepSpec{RepoID: repo, Name: s.Name, Argv: append([]string(nil), s.Argv...)}
	}
	return out, nil
}

// userBatchMessage keeps the platform message, which is already plain
// language and names the argument, without the package prefix.
func userBatchMessage(err error) string {
	msg := err.Error()
	const prefix = "platform: an argument can't be passed to a .cmd or .bat file safely: "
	if len(msg) > len(prefix) && msg[:len(prefix)] == prefix {
		msg = msg[len(prefix):]
	}
	return msg + "."
}

func withStep(err error, n int) error {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return apperr.New(ae.Class, fmt.Sprintf("Step %d: %s", n, ae.Message), ae.Err)
	}
	return err
}

// CheckPreset applies the rule ParsePreset applies to every step (no argument
// a batch file could misread) to a preset that did not come through
// ParsePreset, such as one read from the database. The db package cannot
// import platform, so the SQLite adapter calls this on every preset it stores
// or returns.
func CheckPreset(p corebuilds.Preset) error {
	for i, s := range p.Steps {
		if err := platform.BatchArgsOK(s.Argv); err != nil {
			return fmt.Errorf("preset %s step %d: %w", p.ID, i+1, err)
		}
	}
	return nil
}

// FormatRequest is the inverse of ParseRequest: it writes spec as a request
// body.
func FormatRequest(spec corebuilds.Spec) ([]byte, error) {
	req := requestJSON{Target: "main", PresetID: string(spec.PresetID), Steps: stepsJSON(spec.Steps)}
	if w, ok := spec.Target.(corebuilds.InWorktree); ok {
		req.Target, req.WorktreeID = "worktree", string(w.WorktreeID)
	}
	return json.Marshal(req)
}

// FormatPreset is the inverse of ParsePreset.
func FormatPreset(p corebuilds.Preset) ([]byte, error) {
	return json.Marshal(presetJSON{ID: string(p.ID), ProjectID: string(p.ProjectID), Name: p.Name, Steps: stepsJSON(p.Steps)})
}

func stepsJSON(in []corebuilds.StepSpec) []stepJSON {
	out := make([]stepJSON, len(in))
	for i, s := range in {
		out[i] = stepJSON{RepoID: string(s.RepoID), Name: s.Name, Argv: s.Argv}
	}
	return out
}
