package proposals

import "testing"

// The draft parse lets a caller leave out the checksums the service fills in;
// the strict parse and Validate never do.
func TestDraftParseAcceptsMissingChecksumsButStrictRulesRejectThem(t *testing.T) {
	for _, tc := range []struct {
		kind  string
		field string
	}{
		{"commit", "expect_head"}, {"commit", "expect_diff_hash"},
		{"push", "expect_head"}, {"merge_request", "expect_head"},
	} {
		raw := mod(t, tc.kind, func(m map[string]any) { delete(m, tc.field) })
		a, err := ParseDraftAction(tc.kind, raw)
		if err != nil {
			t.Fatalf("%s without %s: draft parse: %v", tc.kind, tc.field, err)
		}
		if a.Validate() == nil {
			t.Errorf("%s without %s: Validate accepts an action with no checksum", tc.kind, tc.field)
		}
		if _, err := ParseAction(tc.kind, raw); err == nil {
			t.Errorf("%s without %s: ParseAction accepts it", tc.kind, tc.field)
		}
		kind, row := RowOf(a)
		if _, err := ActionFromRow(kind, row); err == nil {
			t.Errorf("%s without %s: ActionFromRow accepts it", tc.kind, tc.field)
		}
	}
}

func TestDraftParseStillChecksAChecksumThatIsPresent(t *testing.T) {
	raw := mod(t, "push", set("expect_head", "NOT-HEX"))
	if _, err := ParseDraftAction("push", raw); err == nil {
		t.Fatal("draft parse accepted a malformed expect_head")
	}
}
