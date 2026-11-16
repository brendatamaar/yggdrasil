package platform

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// maxIgnoreSize is the largest .gitignore Yggdrasil reads or edits.
const maxIgnoreSize = 1 << 20

// maxInitFileSize bounds what FileHash reads back of a file Yggdrasil created.
const maxInitFileSize = 4 << 20

// HashHex is the lower-case sha256 hex of b, the hash format of every
// receipt and expectation in safe_fs.go.
func HashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// PlanIgnoreAppend is the pure part of IgnoreWriter.AppendIgnore. It returns
// the exact bytes to add after existing so that the result keeps every
// existing byte, the file's line-ending style (CRLF when CRLF lines
// outnumber bare LF lines, else LF) and its final-newline convention (a file
// that did not end in a newline still does not, but its last line is first
// terminated). Only entries with no exact line match in existing are added,
// once each, in order. A nil result means every entry is already present.
func PlanIgnoreAppend(existing []byte, entries []string) ([]byte, error) {
	for _, e := range entries {
		switch {
		case e == "":
			return nil, fmt.Errorf("an ignore entry is empty: %w", ErrInvalidArgument)
		case len(e) > 4096:
			return nil, fmt.Errorf("an ignore entry is longer than 4096 bytes: %w", ErrInvalidArgument)
		case strings.ContainsAny(e, "\r\n\x00"):
			return nil, fmt.Errorf("an ignore entry contains a line break or NUL: %w", ErrInvalidArgument)
		}
	}
	have := map[string]bool{}
	crlf, bare := 0, 0
	for _, line := range bytes.Split(existing, []byte{'\n'}) {
		have[strings.TrimSuffix(string(line), "\r")] = true
	}
	for i, c := range existing {
		if c != '\n' {
			continue
		}
		if i > 0 && existing[i-1] == '\r' {
			crlf++
		} else {
			bare++
		}
	}
	var missing []string
	for _, e := range entries {
		if !have[e] {
			have[e] = true
			missing = append(missing, e)
		}
	}
	if len(missing) == 0 {
		return nil, nil
	}
	eol := "\n"
	if crlf > bare {
		eol = "\r\n"
	}
	finalNL := len(existing) == 0 || existing[len(existing)-1] == '\n'
	var b strings.Builder
	if !finalNL {
		b.WriteString(eol)
	}
	for i, e := range missing {
		b.WriteString(e)
		if finalNL || i < len(missing)-1 {
			b.WriteString(eol)
		}
	}
	return []byte(b.String()), nil
}
