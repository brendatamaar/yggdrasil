package builds

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// Command is one user-defined step in a repo's build settings.
type Command struct {
	Name string   `json:"name"`
	Argv []string `json:"argv"`
}

// RepoBuildSettings are the per-repo build settings (BL-08, WE-05). The zero
// value is not the default; use DefaultBuildSettings.
type RepoBuildSettings struct {
	// VerifyOverride replaces the toolchain's default verify chain when it has
	// steps. Empty means "use the default".
	VerifyOverride []Command
	// FullTests appends the toolchain's full-test steps to the verify chain.
	FullTests bool
	// Bootstrap lets a task worktree restore its dependencies automatically
	// before the first verification (WE-05). On by default.
	Bootstrap bool
}

// DefaultBuildSettings is what a repo without stored settings gets.
func DefaultBuildSettings() RepoBuildSettings { return RepoBuildSettings{Bootstrap: true} }

// Validate applies the same rules as build steps: 1 to MaxSteps commands, a
// name of 1 to MaxNameRunes characters, a program in argv[0], no NUL, and
// nothing a .cmd or .bat file could misread (platform.BatchArgsOK).
func (s RepoBuildSettings) Validate() error {
	if len(s.VerifyOverride) > corebuilds.MaxSteps {
		return fmt.Errorf("a verify chain can have at most %d steps", corebuilds.MaxSteps)
	}
	for i, c := range s.VerifyOverride {
		if n := utf8.RuneCountInString(c.Name); strings.TrimSpace(c.Name) == "" || n > corebuilds.MaxNameRunes {
			return fmt.Errorf("verify step %d: the name must have 1 to %d characters", i+1, corebuilds.MaxNameRunes)
		}
		if len(c.Argv) == 0 || c.Argv[0] == "" {
			return fmt.Errorf("verify step %q has no command. Give the program and its arguments as a list", c.Name)
		}
		for _, a := range c.Argv {
			if strings.ContainsRune(a, 0) {
				return fmt.Errorf("verify step %q has a NUL character in its command", c.Name)
			}
		}
		if err := platform.BatchArgsOK(c.Argv); err != nil {
			return fmt.Errorf("verify step %q: %w", c.Name, err)
		}
	}
	return nil
}

// settingsJSON is the stored shape. Bootstrap is a pointer so that a missing
// field keeps the default (true).
type settingsJSON struct {
	Verify    []Command `json:"verify,omitempty"`
	FullTests bool      `json:"fullTests"`
	Bootstrap *bool     `json:"bootstrap,omitempty"`
}

// ParseBuildSettings parses and validates settings JSON. Unknown fields and
// trailing data are refused. An omitted "bootstrap" means true.
func ParseBuildSettings(data []byte) (RepoBuildSettings, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var raw settingsJSON
	if err := dec.Decode(&raw); err != nil {
		if errors.Is(err, io.EOF) {
			return RepoBuildSettings{}, errors.New("the build settings are empty")
		}
		return RepoBuildSettings{}, fmt.Errorf("the build settings are not valid: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return RepoBuildSettings{}, errors.New("the build settings have extra data after the settings object")
	}
	s := RepoBuildSettings{FullTests: raw.FullTests, Bootstrap: raw.Bootstrap == nil || *raw.Bootstrap}
	for _, c := range raw.Verify {
		s.VerifyOverride = append(s.VerifyOverride, Command{Name: c.Name, Argv: append([]string(nil), c.Argv...)})
	}
	if err := s.Validate(); err != nil {
		return RepoBuildSettings{}, err
	}
	return s, nil
}

// FormatBuildSettings is the inverse of ParseBuildSettings. It refuses invalid
// settings.
func FormatBuildSettings(s RepoBuildSettings) ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	b := s.Bootstrap
	out, err := json.Marshal(settingsJSON{Verify: s.VerifyOverride, FullTests: s.FullTests, Bootstrap: &b})
	if err != nil {
		return nil, fmt.Errorf("format build settings: %w", err)
	}
	return out, nil
}
