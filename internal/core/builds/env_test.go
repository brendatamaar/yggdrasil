package builds

import (
	"fmt"
	"strings"
	"testing"
)

func TestStepSpecEnvValidate(t *testing.T) {
	ok := StepSpec{RepoID: "r", Name: "n", Argv: []string{"x"}, Env: []string{"CI=true", "A_1=", "_B=a=b"}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	many := make([]string, MaxEnv+1)
	for i := range many {
		many[i] = fmt.Sprintf("K%d=v", i)
	}
	bad := map[string][]string{
		"no equals":        {"ABC"},
		"empty key":        {"=v"},
		"key with dash":    {"A-B=v"},
		"key with digit 1": {"1A=v"},
		"key with space":   {"A B=v"},
		"NUL in value":     {"A=b\x00c"},
		"newline in value": {"A=b\nc"},
		"long value":       {"A=" + strings.Repeat("x", MaxEnvValueLen+1)},
		"long key":         {strings.Repeat("A", MaxEnvKeyLen+1) + "=v"},
		"duplicate key":    {"A=1", "A=2"},
		"too many":         many,
	}
	for name, env := range bad {
		s := ok
		s.Env = env
		if s.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// envEntryOK is an independent validity predicate for one KEY=VALUE entry.
func envEntryOK(e string) bool {
	i := strings.IndexByte(e, '=')
	if i <= 0 {
		return false
	}
	k, v := e[:i], e[i+1:]
	if len(k) > MaxEnvKeyLen || len(v) > MaxEnvValueLen {
		return false
	}
	for j, c := range []byte(k) {
		isLetter := c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		isDigit := c >= '0' && c <= '9'
		if !isLetter && (!isDigit || j <= 0) {
			return false
		}
	}
	return !strings.ContainsAny(v, "\x00\r\n")
}

func TestParseEnvEntryRoundTrip(t *testing.T) {
	for _, c := range [][2]string{{"CI", "true"}, {"_A1", ""}, {"K", "a=b=c"}, {"Z", " spaced value\t"}, {"P", `C:\Program Files\x`}} {
		k, v, err := ParseEnvEntry(c[0] + "=" + c[1])
		if err != nil || k != c[0] || v != c[1] {
			t.Errorf("%v: got %q %q %v", c, k, v, err)
		}
	}
}

func TestParseEnvEntryRejects(t *testing.T) {
	bad := map[string]string{
		"no equals":     "ABC",
		"empty key":     "=v",
		"digit first":   "1A=v",
		"long key":      strings.Repeat("A", MaxEnvKeyLen+1) + "=v",
		"long value":    "A=" + strings.Repeat("x", MaxEnvValueLen+1),
		"NUL":           "A=b\x00c",
		"CR":            "A=b\rc",
		"LF":            "A=b\nc",
		"dash in key":   "A-B=v",
		"non-ASCII key": "\u00c4=v",
		"empty string":  "",
	}
	for name, e := range bad {
		if _, _, err := ParseEnvEntry(e); err == nil {
			t.Errorf("%s: accepted %q", name, e)
		}
	}
	// The caps themselves are allowed.
	if _, _, err := ParseEnvEntry(strings.Repeat("A", MaxEnvKeyLen) + "=" + strings.Repeat("x", MaxEnvValueLen)); err != nil {
		t.Errorf("entry at the caps rejected: %v", err)
	}
}

func FuzzParseEnvEntry(f *testing.F) {
	for _, s := range []string{"CI=true", "A=", "=v", "1A=v", "A=b\x00", "A=b\r", "A=b\n", "x", "A=b=c", "\u00c4=1", "_=_"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, e string) {
		k, v, err := ParseEnvEntry(e)
		if want := envEntryOK(e); (err == nil) != want {
			t.Fatalf("%q: err=%v, independent predicate says %v", e, err, want)
		}
		if err == nil && k+"="+v != e {
			t.Fatalf("round trip: %q -> %q %q", e, k, v)
		}
	})
}
