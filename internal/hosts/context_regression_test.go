package hosts

// Regression tests for two defects found by TM1-H in
// internal/hosts/context_redact.go. Both are fixed; the tests stay to keep them so.

import (
	"encoding/json"
	"strings"
	"testing"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

// Regression (invariant 4): redactContext once removed invalid UTF-8 AFTER
// redacting, so a host that put an invalid byte inside the credential defeated
// the redactor and the stripping reassembled the credential in the kept text.
// text() now strips invalid bytes first.
func TestRegression_RedactContextCredentialSplitByInvalidByte(t *testing.T) {
	const token = "sentinel-credential-7f3a91"
	split := "sentinel-cr\xeaedential-7f3a91" // the token with one invalid byte inside
	in := ContextResult{Value: corehosts.IssueDetail{Summary: corehosts.IssueSummary{Number: 5, Title: "t " + split}, Body: "body " + split}}
	out := redactContext(in, token)
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), token) {
		t.Fatalf("the credential was reassembled after redaction: %.200s", raw)
	}
}

// Regression (labels): boundContext once halved the Source of every unavailable
// label on each pass, even when the oversize came from body text; it now cuts
// only labels over maxUnavailableSource. A GitHub failed-job read with a log over the 64 KiB budget
// returns the gap "external checks" as "externa", so the UI cannot say which
// part is missing.
func TestRegression_BoundContextKeepsUnavailableLabels(t *testing.T) {
	f := jobsFixture{lab: false, head: contractHeadA, jobs: 1, log: func(int) (int, int) { return 200, 100 << 10 }}
	jl, gaps, _, err := f.read(t)
	if err != nil || len(jl.Jobs) != 1 {
		t.Fatal(err)
	}
	if len(gaps) != 1 || gaps[0].Source != "external checks" || gaps[0].Reason != corehosts.ContextUnsupported {
		t.Fatalf("the label of the missing part was damaged: %+v", gaps)
	}
}
