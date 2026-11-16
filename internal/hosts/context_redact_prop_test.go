package hosts

// Redaction and truncation of host context (GH-05, invariant 4; TM1-H): every
// output of redactContext is valid UTF-8, free of the credential, within the
// 64 KiB serialized budget (measured here with encoding/json, independent of the
// production bound), and any loss is labelled.

import (
	"encoding/json"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

const redactTok = "sentinel-credential-7f3a91"

func genText(r *rand.Rand, max int) string {
	pieces := []string{"plain ", "日本語", "Überweisung ", redactTok, "x", "\n", "🙂", "\xff", "a b c "}
	var b strings.Builder
	for b.Len() < max && r.Intn(40) != 0 {
		b.WriteString(pieces[r.Intn(len(pieces))])
	}
	if max >= 5000 && r.Intn(4) == 0 { // a big one, to cross the budget at arbitrary byte offsets
		b.WriteString(strings.Repeat(pieces[r.Intn(len(pieces)-4)], r.Intn(6000)))
	}
	return b.String()
}

func genPayload(r *rand.Rand) (corehosts.ContextPayload, int) {
	switch r.Intn(4) {
	case 0:
		jobs := make([]corehosts.JobLog, 1+r.Intn(30))
		for i := range jobs {
			jobs[i] = corehosts.JobLog{JobID: "1", Name: genText(r, 30), Stage: genText(r, 10), Log: genText(r, 5000), LogUnavailable: genText(r, 20)}
		}
		return corehosts.JobLogs{RunID: "12", Head: contractHeadA, Jobs: jobs}, 0
	case 1:
		items := make([]corehosts.IssueSummary, r.Intn(500))
		for i := range items {
			items[i] = corehosts.IssueSummary{Number: i + 1, Title: genText(r, 20), Author: genText(r, 8), Labels: make([]string, r.Intn(30))}
			for j := range items[i].Labels {
				items[i].Labels[j] = genText(r, 3)
			}
		}
		return corehosts.IssueList{Items: items}, 1
	case 2:
		labels := make([]string, r.Intn(3000))
		return corehosts.IssueDetail{Summary: corehosts.IssueSummary{Number: 5, Title: genText(r, 50), Labels: labels}, Body: genText(r, 70000), URL: "https://git.acme.test/x"}, 2
	}
	return corehosts.MRContext{MR: corehosts.MergeRequest{Number: 3, Title: genText(r, 40), Source: genText(r, 10), Target: "main", State: corehosts.MROpen}, Head: contractHeadA, Diff: genText(r, 70000)}, 3
}

func anyTruncatedFlag(v corehosts.ContextPayload) bool {
	switch p := v.(type) {
	case corehosts.JobLogs:
		if p.Truncated {
			return true
		}
		for _, j := range p.Jobs {
			if j.LogTruncated {
				return true
			}
		}
	case corehosts.IssueList:
		return p.Truncated
	case corehosts.IssueDetail:
		return p.BodyTruncated
	case corehosts.MRContext:
		return p.DiffTruncated
	}
	return false
}

func checkRedacted(in ContextResult, out ContextResult) string {
	raw, err := json.Marshal(out)
	if err != nil {
		return "unserializable: " + err.Error()
	}
	if len(raw) > 64*1024 {
		return "serialized size over 64 KiB"
	}
	if strings.Contains(string(raw), redactTok) {
		return "credential in the output"
	}
	var all []string
	collectStrings(reflect.ValueOf(out), &all)
	for _, s := range all {
		if !utf8.ValidString(s) {
			return "invalid UTF-8 in the output"
		}
	}
	if out.Value == nil {
		return "the payload vanished without a label" // boundContext's last resort is an empty result
	}
	// Loss is labelled: whatever the independent measure says did not fit has a flag.
	// Invalid bytes are dropped by the reader (json shows them as \ufffd).
	redacted := strings.ReplaceAll(strings.ReplaceAll(mustJSON(in), redactTok, "[REDACTED]"), `\ufffd`, "")
	if len(redacted) > 64*1024 && !anyTruncatedFlag(out.Value) {
		return "content was dropped without a truncation label"
	}
	if len(in.Unavailable) > 0 && len(out.Unavailable) == 0 {
		return "a labelled gap disappeared"
	}
	return ""
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestPropRedactContextBoundsAndLabels(t *testing.T) {
	kinds := map[int]int{}
	for seed := int64(1); seed <= 40; seed++ {
		r := rand.New(rand.NewSource(seed))
		p, kind := genPayload(r)
		kinds[kind]++
		var gaps []corehosts.ContextUnavailable
		for i, n := 0, r.Intn(4); i < n; i++ {
			gaps = append(gaps, corehosts.ContextUnavailable{Source: genText(r, 30), Reason: corehosts.ContextNotFound})
		}
		in := ContextResult{Value: p, Unavailable: gaps}
		out := redactContext(in, redactTok)
		if msg := checkRedacted(in, out); msg != "" {
			t.Fatalf("seed %d kind %d: %s", seed, kind, msg)
		}
	}
	if len(kinds) != 4 {
		t.Fatalf("the generator reached only kinds %v", kinds)
	}
}

// Very many tiny labels and identifiers: structure, not text, is what must be cut.
func TestRedactContextManySmallLabelsAndGaps(t *testing.T) {
	labels := make([]string, 20000)
	for i := range labels {
		labels[i] = "l"
	}
	gaps := make([]corehosts.ContextUnavailable, 500)
	for i := range gaps {
		gaps[i] = corehosts.ContextUnavailable{Source: "job log 1", Reason: corehosts.ContextNotFound}
	}
	in := ContextResult{Value: corehosts.IssueDetail{Summary: corehosts.IssueSummary{Number: 5, Title: "t", Labels: labels}}, Unavailable: gaps}
	out := redactContext(in, redactTok)
	if msg := checkRedacted(in, out); msg != "" {
		t.Fatal(msg)
	}
}

// Multibyte text cut at every offset near the budget stays valid UTF-8.
func TestRedactContextMultibyteBoundaryCuts(t *testing.T) {
	for _, unit := range []string{"日", "é", "🙂", "a"} {
		for extra := 0; extra < 8; extra++ {
			body := strings.Repeat(unit, corehosts.ContextMaxPromptBytes/len(unit)) + strings.Repeat("x", extra)
			in := ContextResult{Value: corehosts.IssueDetail{Summary: corehosts.IssueSummary{Number: 5, Title: "t"}, Body: body}}
			out := redactContext(in, redactTok)
			if msg := checkRedacted(in, out); msg != "" {
				t.Fatalf("unit %q extra %d: %s", unit, extra, msg)
			}
		}
	}
}

func FuzzRedactContext(f *testing.F) {
	f.Add("body "+redactTok, "label", uint8(3))
	f.Add(strings.Repeat("日", 30000), "é", uint8(200))
	f.Fuzz(func(t *testing.T, body, label string, n uint8) {
		labels := make([]string, int(n)*40)
		for i := range labels {
			labels[i] = label
		}
		in := ContextResult{Value: corehosts.IssueDetail{Summary: corehosts.IssueSummary{Number: 5, Title: label, Labels: labels}, Body: body}, Unavailable: []corehosts.ContextUnavailable{{Source: label, Reason: corehosts.ContextNotFound}}}
		out := redactContext(in, redactTok)
		if out.Value == nil {
			t.Fatal("the payload vanished")
		}
		raw, _ := json.Marshal(out)
		if len(raw) > 64*1024 || strings.Contains(string(raw), redactTok) {
			t.Fatalf("bound or credential: %d bytes", len(raw))
		}
		d := out.Value.(corehosts.IssueDetail)
		if !utf8.ValidString(d.Body) || !utf8.ValidString(d.Summary.Title) {
			t.Fatal("invalid UTF-8")
		}
	})
}
