package worktreeenv

import (
	"encoding/json"
	"errors"
	"strings"
)

// Errors of the compose parsers, one per rule.
var (
	ErrComposeExt    = errors.New("the compose file must end in .yml or .yaml")
	ErrProjectEmpty  = errors.New("the compose project name is empty")
	ErrProjectChars  = errors.New("the compose project name may only hold a-z, 0-9 and '-'")
	ErrProjectLong   = errors.New("the compose project name is longer than 40 characters")
	ErrPsUndecodable = errors.New("the output of docker compose ps could not be read")
	ErrComposeOp     = errors.New("not a compose operation")
)

// MaxProjectName is the longest <name> in ygg-<name>.
const MaxProjectName = 40

// ParseComposeFile validates the compose file setting: a repo-relative path
// (same rules as profile paths) ending in .yml or .yaml.
func ParseComposeFile(s string) (RelPath, error) {
	p, err := parseRelPath(s)
	if err != nil {
		return "", err
	}
	l := strings.ToLower(string(p))
	if !strings.HasSuffix(l, ".yml") && !strings.HasSuffix(l, ".yaml") {
		return "", ErrComposeExt
	}
	return p, nil
}

// ProjectName is a validated compose project name, "ygg-<name>".
type ProjectName string

// ParseProjectName builds the project name from the worktree name part. The
// name must be 1..40 characters of [a-z0-9-].
func ParseProjectName(name string) (ProjectName, error) {
	if name == "" {
		return "", ErrProjectEmpty
	}
	if len(name) > MaxProjectName {
		return "", ErrProjectLong
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return "", ErrProjectChars
		}
	}
	return ProjectName("ygg-" + name), nil
}

// Name is the <name> part; ParseProjectName(p.Name()) == p.
func (p ProjectName) Name() string { return strings.TrimPrefix(string(p), "ygg-") }

// Validate reports whether p has the form ParseProjectName produces.
func (p ProjectName) Validate() error {
	if !strings.HasPrefix(string(p), "ygg-") {
		return ErrProjectChars
	}
	_, err := ParseProjectName(p.Name())
	return err
}

// Validate reports whether the op is in the closed set.
func (o ComposeOp) Validate() error {
	switch o {
	case OpUp, OpStop, OpDown, OpPs:
		return nil
	}
	return ErrComposeOp
}

// Validate reports whether r is in the closed set.
func (r PsResult) Validate() error {
	switch r {
	case PsRunning, PsNotRunning, PsUnavailable:
		return nil
	}
	return ErrPsUndecodable
}

// Argv is the command line of an operation. It is fixed per op and holds
// nothing from the user but the project name and the file (invariant C2):
// never -v, --volumes, --rmi or --remove-orphans. The file is relative to the
// worktree, which is the working directory.
func Argv(op ComposeOp, project ProjectName, file RelPath) ([]string, error) {
	base := []string{"docker", "compose", "-p", string(project), "--file", string(file)}
	switch op {
	case OpUp:
		return append(base, "up", "-d"), nil
	case OpStop:
		return append(base, "stop"), nil
	case OpDown:
		return append(base, "down"), nil
	case OpPs:
		return append(base, "ps", "--format", "json"), nil
	}
	return nil, ErrComposeOp
}

type psRow struct {
	State string `json:"State"`
}

// ParsePs reads the output of `docker compose ps --format json`: one JSON
// object per line (newer Docker) or one array (older). Any container in state
// "running" or "restarting" gives PsRunning; no containers or none running
// gives PsNotRunning. Output that is not JSON is an error; the shell then
// treats the project as PsUnavailable.
func ParsePs(out []byte) (PsResult, error) {
	text := strings.TrimSpace(string(out))
	if text == "" {
		return PsNotRunning, nil
	}
	var rows []psRow
	if text[0] == '[' {
		if err := json.Unmarshal([]byte(text), &rows); err != nil {
			return "", ErrPsUndecodable
		}
	} else {
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var r psRow
			if err := json.Unmarshal([]byte(line), &r); err != nil {
				return "", ErrPsUndecodable
			}
			rows = append(rows, r)
		}
	}
	for _, r := range rows {
		if r.State == "running" || r.State == "restarting" {
			return PsRunning, nil
		}
	}
	return PsNotRunning, nil
}

// FormatPs writes the canonical output ParsePs reads back to r.
func FormatPs(r PsResult) []byte {
	if r == PsRunning {
		return []byte(`{"State":"running"}` + "\n")
	}
	return nil
}
