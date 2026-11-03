package proposals

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

const maxStageIDLen = 64

// StageIDFor derives the staging ID of a removal from its proposal ID, so a
// retry plans the same staged folder as the run that crashed. The result is
// 1-64 of [A-Za-z0-9_-]. An ID that is already that is kept as it is;
// otherwise the bad characters become '_' and a hash of the original ID is
// appended, so two different proposals never share a folder. Never empty.
func StageIDFor(id core.ProposalID) string {
	s := string(id)
	var b strings.Builder
	clean := true
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
			clean = false
		}
	}
	if clean && s != "" && len(s) <= maxStageIDLen {
		return s
	}
	sum := sha256.Sum256([]byte(s))
	h := hex.EncodeToString(sum[:8])
	base := b.String()
	if len(base) > maxStageIDLen-len(h)-1 {
		base = base[:maxStageIDLen-len(h)-1]
	}
	if base == "" {
		return "rm-" + h
	}
	return base + "-" + h
}
