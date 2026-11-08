package terminal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// TokenLen is the length of a token: 32 random bytes as unpadded base64url.
const TokenLen = 43

// MaxFrameBytes is the largest client frame (data or control).
const MaxFrameBytes = 64 << 10

// Validate reports whether the hash is 64 lower-case hex characters.
func (h TokenHash) Validate() error {
	if len(h) != 64 {
		return errors.New("token hash must be 64 hex characters")
	}
	for i := 0; i < len(h); i++ {
		c := h[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return errors.New("token hash must be lower-case hex")
		}
	}
	return nil
}

// HashToken returns the SHA-256 of a raw token, as lower-case hex. The shell
// calls it once, right after generating the token; the raw token goes no
// further into the core.
func HashToken(raw string) TokenHash {
	sum := sha256.Sum256([]byte(raw))
	return TokenHash(hex.EncodeToString(sum[:]))
}

// ParseToken checks the shape of a token from the WebSocket query and returns
// its hash. It accepts exactly the canonical unpadded base64url encoding of
// 32 bytes: 43 characters from A-Z a-z 0-9 - _, whose last character carries
// no stray bits.
func ParseToken(raw string) (TokenHash, error) {
	if len(raw) != TokenLen {
		return "", fmt.Errorf("terminal token must be %d characters", TokenLen)
	}
	for i := 0; i < len(raw); i++ {
		if !base64URLChar(raw[i]) {
			return "", errors.New("terminal token has a character that is not base64url")
		}
	}
	// 32 bytes are 256 bits; 43 characters hold 258, so the last two bits of
	// the final character are zero: its index is a multiple of 4.
	if !strings.ContainsRune("AEIMQUYcgkosw048", rune(raw[TokenLen-1])) {
		return "", errors.New("terminal token is not a canonical 32-byte encoding")
	}
	return HashToken(raw), nil
}

func base64URLChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_'
}

// Target is what a terminal is requested for: a repo (its main checkout) or a
// worktree. Exactly one is set. Never a path.
type Target struct {
	Repo     core.RepoID
	Worktree core.WorktreeID
}

// Validate reports whether exactly one well-formed ID is set.
func (t Target) Validate() error {
	if (t.Repo == "") == (t.Worktree == "") {
		return errors.New("give either a repo or a worktree, not both and not neither")
	}
	if t.Repo != "" {
		return checkRef("repo id", string(t.Repo))
	}
	return checkRef("worktree id", string(t.Worktree))
}

// checkRef accepts the opaque IDs of the database layer and nothing that could
// be read as a path.
func checkRef(kind, id string) error {
	if len(id) > 128 {
		return fmt.Errorf("%s is too long", kind)
	}
	for i := 0; i < len(id); i++ {
		if c := id[i]; !base64URLChar(c) && c != '.' {
			return fmt.Errorf("%s has a character that is not allowed", kind)
		}
	}
	if strings.Contains(id, "..") || strings.HasPrefix(id, ".") {
		return fmt.Errorf("%s is not allowed", kind)
	}
	return nil
}

// ParseIssue reads the body of POST /api/terminal: {"repoId": ...} or
// {"worktreeId": ...}. Unknown fields (a path, say) are rejected.
func ParseIssue(body []byte) (Target, error) {
	var in struct {
		Repo     string `json:"repoId"`
		Worktree string `json:"worktreeId"`
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return Target{}, fmt.Errorf("terminal request is not valid: %w", err)
	}
	if strings.TrimSpace(string(body[dec.InputOffset():])) != "" {
		return Target{}, errors.New("terminal request has extra data after the object")
	}
	t := Target{Repo: core.RepoID(in.Repo), Worktree: core.WorktreeID(in.Worktree)}
	if err := t.Validate(); err != nil {
		return Target{}, err
	}
	return t, nil
}

// ParseResize validates a size.
func ParseResize(cols, rows int) (Size, error) {
	s := Size{Cols: cols, Rows: rows}
	if err := s.Validate(); err != nil {
		return Size{}, err
	}
	return s, nil
}

// FrameKind is the WebSocket frame type.
type FrameKind int

// The closed set of FrameKind values.
const (
	TextFrame FrameKind = iota + 1
	BinaryFrame
)

// Frame is a parsed client frame. It is a closed set: DataFrame, ResizeFrame.
type Frame interface {
	isFrame()
	// Validate reports whether the frame satisfies the limits.
	Validate() error
}

// DataFrame is keystrokes for the shell. Never logged or audited.
type DataFrame struct{ Data []byte }

// ResizeFrame is a new terminal size.
type ResizeFrame struct{ Size Size }

func (DataFrame) isFrame()   {}
func (ResizeFrame) isFrame() {}

// Validate implements Frame.
func (f DataFrame) Validate() error {
	if len(f.Data) == 0 || len(f.Data) > MaxFrameBytes {
		return fmt.Errorf("terminal data frame must be 1..%d bytes", MaxFrameBytes)
	}
	return nil
}

// Validate implements Frame.
func (f ResizeFrame) Validate() error { return f.Size.Validate() }

// ParseFrame turns a client frame into a Frame. Binary frames are data; text
// frames must be {"type":"resize","cols":n,"rows":m} and nothing else.
func ParseFrame(kind FrameKind, payload []byte) (Frame, error) {
	if len(payload) > MaxFrameBytes {
		return nil, fmt.Errorf("terminal frame is larger than %d bytes", MaxFrameBytes)
	}
	switch kind {
	case BinaryFrame:
		f := DataFrame{Data: slices.Clone(payload)}
		if err := f.Validate(); err != nil {
			return nil, err
		}
		return f, nil
	case TextFrame:
		var in struct {
			Type string `json:"type"`
			Cols int    `json:"cols"`
			Rows int    `json:"rows"`
		}
		dec := json.NewDecoder(strings.NewReader(string(payload)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			return nil, fmt.Errorf("terminal control frame is not valid: %w", err)
		}
		if strings.TrimSpace(string(payload[dec.InputOffset():])) != "" {
			return nil, errors.New("terminal control frame has extra data")
		}
		if in.Type != "resize" {
			return nil, errors.New("terminal control frame type must be resize")
		}
		size, err := ParseResize(in.Cols, in.Rows)
		if err != nil {
			return nil, err
		}
		return ResizeFrame{Size: size}, nil
	}
	return nil, errors.New("terminal frame kind is unknown")
}

// Shell is a terminal shell choice by name, never a command line.
type Shell string

// The closed set of Shell values: the platform's default shell ($SHELL, or
// PowerShell on Windows) and PowerShell by name.
const (
	ShellDefault    Shell = "default"
	ShellPowerShell Shell = "powershell"
)

// Validate reports whether the shell is in the closed set.
func (s Shell) Validate() error {
	switch s {
	case ShellDefault, ShellPowerShell:
		return nil
	}
	return errors.New("terminal shell must be default or powershell")
}

// ParseShell reads the shell setting. It is a name; paths and command lines
// are rejected.
func ParseShell(name string) (Shell, error) {
	s := Shell(name)
	if err := s.Validate(); err != nil {
		return "", err
	}
	return s, nil
}
