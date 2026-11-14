package git

import (
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Independent primitive oracle: never uses a production validator or parser.
func rangeOIDValid(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && (len(b) == 20 || len(b) == 32) && s == strings.ToLower(s)
}

func rangeCommitValid(c Commit) bool {
	if !rangeOIDValid(c.Hash) || len(c.Short) == 0 || len(c.Short) > len(c.Hash) || c.Short != c.Hash[:len(c.Short)] {
		return false
	}
	for _, p := range c.Parents {
		if !rangeOIDValid(p) {
			return false
		}
	}
	return !strings.ContainsAny(c.Author+c.Email+c.Subject, "\x00\x1f")
}

func rangeFramingValid(s string, cut bool) bool {
	records := strings.Split(s, "\x00")
	if !cut && records[len(records)-1] != "" {
		return false
	}
	for _, rec := range records[:len(records)-1] {
		f := strings.Split(rec, "\x1f")
		if len(f) != 7 {
			return false
		}
		d, err := time.Parse(time.RFC3339Nano, f[4])
		if err != nil || !rangeCommitValid(Commit{Hash: f[0], Short: f[1], Author: f[2], Email: f[3], Date: d, Parents: strings.Fields(f[5]), Subject: f[6]}) {
			return false
		}
	}
	return true
}

func encodeRangeCommit(c Commit) string {
	return strings.Join([]string{c.Hash, c.Short, c.Author, c.Email, c.Date.Format(time.RFC3339Nano), strings.Join(c.Parents, " "), c.Subject}, "\x1f") + "\x00"
}

func TestLogRangeFullFieldRoundTrip(t *testing.T) {
	from := rangeTime("2025-03-02T00:00:00Z")
	for _, size := range []int{40, 64} {
		for _, parents := range [][]string{{}, {strings.Repeat("b", size), strings.Repeat("c", size)}} {
			want := Commit{Hash: strings.Repeat("a", size), Short: "aaaaaaa", Author: "Fictional Éva", Email: "eva@example.test", Date: from.Add(123456789), Parents: parents, Subject: "shop-api: café ✓"}
			wire := encodeRangeCommit(want)
			if !rangeFramingValid(wire, false) {
				t.Fatal("invalid round-trip generator")
			}
			got, cut, err := parseLogRange(wire, false, from, from.Add(time.Hour), 1)
			if err != nil || cut || !reflect.DeepEqual(got, []Commit{want}) {
				t.Fatalf("full-field round trip: got %+v, %v, %v; want %+v", got, cut, err, want)
			}
		}
	}
}

func TestLogRangeRejectEachRule(t *testing.T) {
	from := rangeTime("2025-03-02T00:00:00Z")
	base := []string{strings.Repeat("a", 40), "aaaaaaa", "Fictional", "f@example.test", from.Format(time.RFC3339), "", "subject"}
	for _, tc := range []struct {
		name  string
		field int
		value string
	}{
		{"empty hash", 0, ""}, {"short hash", 0, strings.Repeat("a", 39)}, {"uppercase hash", 0, strings.Repeat("A", 40)}, {"nonhex hash", 0, strings.Repeat("g", 40)},
		{"empty short", 1, ""}, {"mismatched short", 1, "bbbbbbb"}, {"overlong short", 1, strings.Repeat("a", 41)},
		{"bad date", 4, "yesterday"}, {"bad parent length", 5, "abc"}, {"bad parent hex", 5, strings.Repeat("g", 40)},
		{"second parent invalid", 5, strings.Repeat("b", 40) + " bad"}, {"extra field", 6, "subject\x1fextra"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fields := append([]string{}, base...)
			fields[tc.field] = tc.value
			wire := strings.Join(fields, "\x1f") + "\x00"
			if rangeFramingValid(wire, false) {
				t.Fatal("invalid negative control")
			}
			for _, cut := range []bool{false, true} {
				if _, _, err := parseLogRange(wire, cut, from, from.Add(time.Hour), 1); err == nil {
					t.Fatalf("accepted broken rule with cut=%v", cut)
				}
			}
		})
	}
}
