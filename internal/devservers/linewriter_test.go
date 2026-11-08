package devservers

import (
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// Safety: a secret in a long line without a line break must not leak when it
// straddles the point where the line is cut into pieces.
func TestLineWriterRedactsASecretAcrossAChunkBoundary(t *testing.T) {
	const tok = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	red := safety.NewRedactor()
	for offset := MaxLineBytes - len(tok) - 8; offset <= MaxLineBytes+8; offset++ {
		var pieces []string
		w := newLineWriter(red.Redact, func(l string) { pieces = append(pieces, l) })
		stream := strings.Repeat("a", offset) + " " + tok + " " + strings.Repeat("b", 3*MaxLineBytes)
		for i := 0; i < len(stream); i += 997 { // writes of an awkward size
			_, _ = w.Write([]byte(stream[i:min(i+997, len(stream))]))
		}
		w.Flush()
		joined := strings.Join(pieces, "\n")
		for k := 0; k+8 <= len(tok); k++ {
			if strings.Contains(joined, tok[k:k+8]) {
				t.Fatalf("offset %d: part of the token %q leaked", offset, tok[k:k+8])
			}
		}
		if !strings.Contains(joined, "[REDACTED]") {
			t.Fatalf("offset %d: nothing was redacted", offset)
		}
	}
}

func TestLineWriterKeepsLinesApart(t *testing.T) {
	var got []string
	w := newLineWriter(nil, func(l string) { got = append(got, l) })
	_, _ = w.Write([]byte("one\r\ntw"))
	_, _ = w.Write([]byte("o\nthree"))
	w.Flush()
	if strings.Join(got, "|") != "one|two|three" {
		t.Errorf("lines = %q", got)
	}
}
