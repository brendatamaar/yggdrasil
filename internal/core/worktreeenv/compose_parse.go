package worktreeenv

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
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
	// ErrComposeIndirect means the compose file pulls in other files (include,
	// extends, env_file), which the confirmation could not cover.
	ErrComposeIndirect = errors.New("the compose file uses include:, extends: or env_file:")
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

// ComposeDir is the folder of a compose file relative to the worktree, "" for
// the worktree root.
func ComposeDir(file RelPath) RelPath {
	i := strings.LastIndex(string(file), "/")
	if i < 0 {
		return ""
	}
	return file[:i]
}

// EnvFilePath is the `.env` next to the compose file, the only environment
// file docker is told to read.
func EnvFilePath(file RelPath) RelPath {
	if d := ComposeDir(file); d != "" {
		return d + "/.env"
	}
	return ".env"
}

// ComposeHash is what the user confirms: the compose file and the sibling
// `.env` (nil when absent, which hashes like an empty file). Each part is
// length-prefixed so bytes cannot move from one to the other.
func ComposeHash(compose, dotenv []byte) string {
	h := sha256.New()
	for _, part := range [][]byte{compose, dotenv} {
		h.Write([]byte(strconv.Itoa(len(part)) + ":"))
		h.Write(part)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// composeIndirectKeys are the keys that make compose read other files.
var composeIndirectKeys = []string{"include", "extends", "env_file"}

// ScanComposeFile refuses a compose file that reads other files through
// `include:`, `extends:` or `env_file:`: those files are outside what the user
// confirmed. It is a line-level scan, deliberately over-cautious: a key of one
// of these names anywhere (also in flow style or quoted) is refused.
func ScanComposeFile(data []byte) error {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if i := strings.Index(line, " #"); i >= 0 {
			line = line[:i]
		}
		for _, k := range composeIndirectKeys {
			if hasYAMLKey(line, k) {
				return fmt.Errorf("%w (%s:)", ErrComposeIndirect, k)
			}
		}
	}
	return nil
}

// hasYAMLKey reports whether line holds key as a mapping key: at the start, or
// after whitespace, '{', ',' or a quote, and followed by ':' and a space or
// the end of the line.
func hasYAMLKey(line, key string) bool {
	for from := 0; from < len(line); {
		j := strings.Index(line[from:], key)
		if j < 0 {
			return false
		}
		pos := from + j
		end := pos + len(key)
		from = end
		if pos > 0 && !strings.ContainsRune(" \t{,\"'", rune(line[pos-1])) {
			continue
		}
		rest := strings.TrimLeft(strings.TrimLeft(line[end:], "\"'"), " \t")
		if strings.HasPrefix(rest, ":") && (len(rest) == 1 || rest[1] == ' ' || rest[1] == '\t') {
			return true
		}
	}
	return false
}

// Argv is the command line of an operation. It is fixed per op and holds
// nothing from the user but the project name and two paths derived from the
// validated compose file (invariant C2): never -v, --volumes, --rmi or
// --remove-orphans. The compose file is read from standard input (`--file -`),
// so the bytes docker runs are the bytes that were hashed. The working
// directory is the worktree. A compose file in a folder gets
// --project-directory for that folder, and the `.env` next to it is passed with
// --env-file only when it exists (hasEnv), so docker reads no other
// environment file.
func Argv(op ComposeOp, project ProjectName, file RelPath, hasEnv bool) ([]string, error) {
	base := []string{"docker", "compose", "-p", string(project), "--file", "-"}
	if d := ComposeDir(file); d != "" {
		base = append(base, "--project-directory", string(d))
	}
	if hasEnv {
		base = append(base, "--env-file", string(EnvFilePath(file)))
	}
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
