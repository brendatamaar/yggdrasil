package db

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
)

func commentReceiptFixture() coreprop.CommentReceipt {
	return coreprop.CommentReceipt{CommentID: "C_shop-42", Marker: strings.Repeat("a", 16), BodyHash: strings.Repeat("b", 64), Head: strings.Repeat("c", 40)}
}

// Independent of Validate and both codecs: an accepted receipt must contain
// plain host identity and bounded lowercase hex evidence, with no foreign fields.
func commentEvidenceValid(r coreprop.CommentReceipt) bool {
	if len(r.CommentID) == 0 || len(r.CommentID) > 200 || strings.ContainsAny(r.CommentID, " /\\?:@#") {
		return false
	}
	for _, c := range r.CommentID {
		if c < 32 || c == 127 {
			return false
		}
	}
	return regexp.MustCompile(`^[0-9a-f]{16,64}$`).MatchString(r.Marker) && regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(r.BodyHash) && regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`).MatchString(r.Head)
}

func assertCommentAcceptedValid(t *testing.T, raw string) {
	t.Helper()
	r, err := DecodeCommentReceipt(raw)
	if err != nil {
		return
	}
	if !commentEvidenceValid(r) {
		t.Fatalf("accepted invalid comment evidence: %#v", r)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		t.Fatal(err)
	}
	var kindField string
	if err := json.Unmarshal(fields["kind"], &kindField); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 5 || kindField != "comment" {
		t.Fatalf("accepted wrong wire fields: %s", raw)
	}
	for field := range fields {
		switch field {
		case "kind", "comment_id", "marker", "body_hash", "head":
		default:
			t.Fatalf("accepted foreign %s", field)
		}
	}
	kind, encoded, err := EncodeCommentReceipt(r)
	if err != nil || kind != "comment" {
		t.Fatalf("accepted receipt cannot encode: %v", err)
	}
	back, err := DecodeCommentReceipt(encoded)
	if err != nil || back != r {
		t.Fatalf("accepted roundtrip: %#v %v", back, err)
	}
}

func commentBoundarySeeds(t testing.TB) []string {
	t.Helper()
	_, raw, err := EncodeCommentReceipt(commentReceiptFixture())
	if err != nil {
		t.Fatal(err)
	}
	seeds := []string{raw, `null`, `{}`, `{`, `[]`, raw + ` {}`, raw + ` null`, raw + ` garbage`}
	var original map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &original); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{7, 39, 41, 63} {
		seeds = append(seeds, strings.Replace(raw, strings.Repeat("c", 40), strings.Repeat("c", n), 1))
	}
	for field := range original {
		for _, bad := range []string{"", `null`, `0`, `[]`, `""`, `"bad/value"`} {
			m := map[string]json.RawMessage{}
			for k, v := range original {
				if k != field {
					m[k] = v
				}
			}
			if bad != "" {
				m[field] = json.RawMessage(bad)
			}
			b, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			seeds = append(seeds, string(b))
		}
	}
	for _, field := range []string{"identity", "git_dir_identity", "name", "content_hash", "commit", "tree", "message_hash", "author", "remote_id", "url", "extra"} {
		for _, bad := range []string{`""`, `null`, `0`, `"foreign"`} {
			seeds = append(seeds, strings.TrimSuffix(raw, "}")+`,"`+field+`":`+bad+`}`)
		}
	}
	return seeds
}

func TestCommentReceiptBoundary(t *testing.T) {
	r := commentReceiptFixture()
	kind, raw, err := EncodeCommentReceipt(r)
	if err != nil || kind != "comment" {
		t.Fatalf("encode: %s %v", kind, err)
	}
	back, err := DecodeCommentReceipt(raw)
	if err != nil || back != r {
		t.Fatalf("roundtrip: %#v %v", back, err)
	}
	for i, seed := range commentBoundarySeeds(t) {
		assertCommentAcceptedValid(t, seed)
		if i > 0 {
			if _, err := DecodeCommentReceipt(seed); err == nil {
				t.Errorf("accepted invalid seed %d: %s", i, seed)
			}
		}
	}
	// Domain-invalid values must fail at both encode and decode boundaries.
	fields := []string{"comment_id", "marker", "body_hash", "head"}
	for _, field := range fields {
		for _, bad := range []string{"", strings.Repeat("x", 201), "bad\x00value", strings.Repeat("A", 64), strings.Repeat("a", 6), strings.Repeat("a", 7), strings.Repeat("a", 39), strings.Repeat("a", 41), strings.Repeat("a", 63), strings.Repeat("a", 65), "bad/value", "bad value"} {
			changed := r
			switch field {
			case "comment_id":
				changed.CommentID = bad
			case "marker":
				changed.Marker = bad
			case "body_hash":
				changed.BodyHash = bad
			case "head":
				changed.Head = bad
			}
			if commentEvidenceValid(changed) {
				continue
			}
			if _, _, err := EncodeCommentReceipt(changed); err == nil {
				t.Errorf("encoded invalid %s=%q", field, bad)
			}
			m := map[string]string{"kind": "comment", "comment_id": changed.CommentID, "marker": changed.Marker, "body_hash": changed.BodyHash, "head": changed.Head}
			b, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeCommentReceipt(string(b)); err == nil {
				t.Errorf("decoded invalid %s=%q", field, bad)
			}
		}
	}
}

func FuzzCommentReceiptAcceptedImpliesValid(f *testing.F) {
	for _, seed := range commentBoundarySeeds(f) {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) { assertCommentAcceptedValid(t, raw) })
}
