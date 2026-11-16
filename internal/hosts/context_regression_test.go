package hosts

// FAILING REGRESSION TESTS (DO NOT MERGE UNTIL FIXED). Each test names a
// production defect found by TM1-H; it fails on purpose until the defect is
// fixed in internal/hosts/context_redact.go.

import (
	"encoding/json"
	"strings"
	"testing"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

// DEFECT 1 (invariant 4): redactContext removes invalid UTF-8 AFTER redacting
// (context_redact.go, text(): strings.ToValidUTF8(red.Redact(s), "")). A host
// that puts an invalid byte inside the credential defeats the redactor, and the
// stripping then reassembles the credential in the kept text.
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

// DEFECT 2 (labels): boundContext halves the Source of every unavailable label
// on each pass (context_redact.go, boundContext), even when the oversize comes
// from body text. A GitHub failed-job read with a log over the 64 KiB budget
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
