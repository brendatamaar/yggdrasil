package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/newrepos"
	"github.com/brendatamaar/yggdrasil/internal/core/notify"
	"github.com/brendatamaar/yggdrasil/internal/core/standup"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
)

// Boundary parser tests of the Phase 6 endpoints (docs/AGENTS.md 5.11): for each
// parser a round trip where a formatter exists, "accepted implies valid", one
// case per rejection rule, and a fuzz target.

func isRefusal(err error) bool {
	var r *refusal6
	return errors.As(err, &r)
}

func query(raw string) *http.Request {
	return httptest.NewRequest(http.MethodGet, "/x?"+raw, nil)
}

// ---- parseNumber / parseRunID ----

func TestParseNumberRoundTrip(t *testing.T) {
	for _, n := range []int{1, 2, 9, 10, 4242, 99999999, 999999999} {
		got, err := parseNumber(strconv.Itoa(n))
		if err != nil || got != n {
			t.Errorf("parseNumber(%d) = %d, %v", n, got, err)
		}
	}
}

func TestParseNumberRejects(t *testing.T) {
	for name, v := range map[string]string{
		"empty":            "",
		"zero":             "0",
		"leading zero":     "007",
		"plus sign":        "+1",
		"minus sign":       "-1",
		"space":            "1 ",
		"leading space":    " 1",
		"decimal":          "1.5",
		"hex":              "0x10",
		"letters":          "12a",
		"too many digits":  "1234567890",
		"non-ascii digits": "١٢",
		"fullwidth digits": "１",
		"newline":          "1\n",
		"url":              "https://git.example.test/acme/shop-api/pull/3",
	} {
		if n, err := parseNumber(v); err == nil || !isRefusal(err) || n != 0 {
			t.Errorf("%s: parseNumber(%q) = %d, %v; want a refusal", name, v, n, err)
		}
	}
}

func TestParseRunIDRoundTripAndRejects(t *testing.T) {
	for _, v := range []string{"1", "9001", "123456789012345678"} {
		got, err := parseRunID(v)
		if err != nil || got != v {
			t.Errorf("parseRunID(%q) = %q, %v", v, got, err)
		}
	}
	for name, v := range map[string]string{
		"empty": "", "zero": "0", "leading zero": "01", "sign": "-5", "url": "https://x.test/runs/1",
		"path": "../1", "too long": "1234567890123456789", "letters": "abc", "space": "1 2",
	} {
		if _, err := parseRunID(v); err == nil || !isRefusal(err) {
			t.Errorf("%s: parseRunID(%q) accepted or not a refusal: %v", name, v, err)
		}
	}
}

func FuzzParseNumber(f *testing.F) {
	for _, s := range []string{"1", "0", "007", "999999999", "1234567890", "-1", "", "١", "1\x00"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, v string) {
		n, err := parseNumber(v)
		if err != nil {
			if !isRefusal(err) || n != 0 {
				t.Fatalf("a rejection must be a refusal with no value: %d, %v", n, err)
			}
			return
		}
		// Accepted implies valid, and the text is the canonical form.
		if n < 1 || n > 999999999 || strconv.Itoa(n) != v {
			t.Fatalf("accepted %q as %d", v, n)
		}
	})
}

func FuzzParseRunID(f *testing.F) {
	for _, s := range []string{"1", "0", "9001", "123456789012345678", "1234567890123456789", "-1", "", "a"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, v string) {
		got, err := parseRunID(v)
		if err != nil {
			if !isRefusal(err) || got != "" {
				t.Fatalf("a rejection must be a refusal with no value: %q, %v", got, err)
			}
			return
		}
		n, perr := strconv.ParseInt(got, 10, 64)
		if got != v || perr != nil || n < 1 || strconv.FormatInt(n, 10) != got {
			t.Fatalf("accepted %q as %q", v, got)
		}
	})
}

// ---- parseNoticeQuery ----

func TestParseNoticeQueryRoundTrip(t *testing.T) {
	for _, c := range []struct {
		after int64
		limit int
	}{{0, 1}, {5, 50}, {123456789, notify.MaxPage}, {999999999999999999, 7}} {
		q := url.Values{"after": {strconv.FormatInt(c.after, 10)}, "limit": {strconv.Itoa(c.limit)}}
		after, limit, err := parseNoticeQuery(query(q.Encode()))
		if err != nil || after != c.after || limit != c.limit {
			t.Errorf("%v: got %d, %d, %v", c, after, limit, err)
		}
	}
	after, limit, err := parseNoticeQuery(query(""))
	if err != nil || after != 0 || limit != defaultNoticePage {
		t.Errorf("defaults: %d, %d, %v", after, limit, err)
	}
}

func TestParseNoticeQueryRejects(t *testing.T) {
	for name, raw := range map[string]string{
		"negative after":     "after=-1",
		"non-digit after":    "after=abc",
		"signed after":       "after=%2B5",
		"after too long":     "after=1234567890123456789",
		"after float":        "after=1.5",
		"after space":        "after=%205",
		"limit zero":         "limit=0",
		"limit over max":     "limit=" + strconv.Itoa(notify.MaxPage+1),
		"limit five digits":  "limit=10000",
		"limit leading zero": "limit=050",
		"limit negative":     "limit=-5",
		"limit letters":      "limit=ten",
	} {
		if _, _, err := parseNoticeQuery(query(raw)); err == nil || !isRefusal(err) {
			t.Errorf("%s: %q accepted or not a refusal: %v", name, raw, err)
		}
	}
}

func FuzzParseNoticeQuery(f *testing.F) {
	for _, s := range []string{"", "after=1&limit=2", "after=-1", "limit=0", "limit=201", "after=99999999999999999999", "after=%00"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if _, err := url.ParseQuery(raw); err != nil {
			return
		}
		r := &http.Request{URL: &url.URL{RawQuery: raw}}
		after, limit, err := parseNoticeQuery(r)
		if err != nil {
			if !isRefusal(err) {
				t.Fatalf("not a refusal: %v", err)
			}
			return
		}
		if after < 0 || limit < 1 || limit > notify.MaxPage {
			t.Fatalf("accepted %q as after=%d limit=%d", raw, after, limit)
		}
	})
}

// ---- parseRangeQuery ----

func TestParseRangeQueryRoundTrip(t *testing.T) {
	from := time.Date(2026, 3, 1, 9, 30, 0, 0, time.FixedZone("x", 3600))
	to := from.Add(48 * time.Hour)
	q := url.Values{"from": {from.Format(time.RFC3339)}, "to": {to.Format(time.RFC3339)}}
	rg, err := parseRangeQuery("prj_1", query(q.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	if !rg.From.Equal(from) || !rg.To.Equal(to) || rg.From.Location() != time.UTC || rg.Project != "prj_1" {
		t.Fatalf("round trip lost the instant or the project: %+v", rg)
	}
}

func TestParseRangeQueryRejects(t *testing.T) {
	ok1, ok2 := "2026-03-01T00:00:00Z", "2026-03-02T00:00:00Z"
	long := "2027-03-01T00:00:00Z"
	for name, c := range map[string]struct {
		project core.ProjectID
		raw     string
	}{
		"missing from":   {"prj_1", "to=" + ok2},
		"missing to":     {"prj_1", "from=" + ok1},
		"both missing":   {"prj_1", ""},
		"not rfc3339":    {"prj_1", "from=2026-03-01&to=" + ok2},
		"unix seconds":   {"prj_1", "from=1772323200&to=" + ok2},
		"reversed":       {"prj_1", "from=" + ok2 + "&to=" + ok1},
		"empty range":    {"prj_1", "from=" + ok1 + "&to=" + ok1},
		"too long range": {"prj_1", "from=" + ok1 + "&to=" + long},
		"value too long": {"prj_1", "from=" + ok1 + strings.Repeat("0", 70) + "&to=" + ok2},
		"bad project":    {"prj/1", "from=" + ok1 + "&to=" + ok2},
	} {
		if _, err := parseRangeQuery(c.project, query(c.raw)); err == nil || !isRefusal(err) {
			t.Errorf("%s: accepted or not a refusal: %v", name, err)
		}
	}
}

func FuzzParseRangeQuery(f *testing.F) {
	f.Add("from=2026-03-01T00:00:00Z&to=2026-03-02T00:00:00Z")
	f.Add("from=x&to=y")
	f.Add("")
	f.Fuzz(func(t *testing.T, raw string) {
		if _, err := url.ParseQuery(raw); err != nil {
			return
		}
		r := &http.Request{URL: &url.URL{RawQuery: raw}}
		rg, err := parseRangeQuery("prj_fuzz", r)
		if err != nil {
			if !isRefusal(err) {
				t.Fatalf("not a refusal: %v", err)
			}
			return
		}
		again, err := standup.ParseRange(rg.Project, rg.From, rg.To)
		if err != nil || again != rg || !rg.From.Before(rg.To) || rg.From.Location() != time.UTC || rg.To.Location() != time.UTC {
			t.Fatalf("accepted %q as an invalid range %+v: %v", raw, rg, err)
		}
	})
}

// ---- plainName ----

func TestPlainNameRules(t *testing.T) {
	for _, v := range []string{"mock", "claude-code", "a.b_c-1", "A", strings.Repeat("a", maxPlainName)} {
		if err := plainName("agent", v, true); err != nil {
			t.Errorf("%q refused: %v", v, err)
		}
	}
	if err := plainName("agent", "", false); err != nil {
		t.Errorf("an optional empty name refused: %v", err)
	}
	for name, c := range map[string]struct {
		v        string
		required bool
	}{
		"required empty": {"", true},
		"too long":       {strings.Repeat("a", maxPlainName+1), false},
		"space":          {"my agent", false},
		"slash":          {"a/b", false},
		"backslash":      {`a\b`, false},
		"colon":          {"a:b", false},
		"unicode":        {"agént", false},
		"nul":            {"a\x00", false},
		"newline":        {"a\n", false},
		"shell chars":    {"a;rm", false},
	} {
		if err := plainName("agent", c.v, c.required); err == nil || !isRefusal(err) {
			t.Errorf("%s: %q accepted or not a refusal: %v", name, c.v, err)
		}
	}
}

func FuzzPlainName(f *testing.F) {
	for _, s := range []string{"", "mock", "a b", "agént", "a\x00", strings.Repeat("x", 80)} {
		f.Add(s, true)
	}
	f.Fuzz(func(t *testing.T, v string, required bool) {
		err := plainName("agent", v, required)
		if err != nil {
			if !isRefusal(err) {
				t.Fatalf("not a refusal: %v", err)
			}
			return
		}
		if v == "" {
			if required {
				t.Fatal("a required empty name was accepted")
			}
			return
		}
		if len(v) > maxPlainName {
			t.Fatalf("accepted an over-long name %q", v)
		}
		for _, r := range v {
			if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '.' && r != '_' && r != '-' {
				t.Fatalf("accepted %q with %q", v, r)
			}
		}
	})
}

// ---- checkSpecText ----

func TestCheckSpecTextRules(t *testing.T) {
	good := NewRepoPreviewRequest{Leaf: "shop-new", README: "# shop-new\r\n", Ignore: "/bin/\n", CommitMessage: "Initial commit"}
	if err := checkSpecText(good); err != nil {
		t.Fatalf("a good spec refused: %v", err)
	}
	if err := checkSpecText(NewRepoPreviewRequest{Leaf: "shop-new"}); err != nil {
		t.Fatalf("the optional files are optional: %v", err)
	}
	big := strings.Repeat("a", newrepos.MaxContent+1)
	for name, mut := range map[string]func(*NewRepoPreviewRequest){
		"empty leaf":        func(r *NewRepoPreviewRequest) { r.Leaf = "" },
		"dot leaf":          func(r *NewRepoPreviewRequest) { r.Leaf = ".." },
		"separator in leaf": func(r *NewRepoPreviewRequest) { r.Leaf = "a/b" },
		"drive in leaf":     func(r *NewRepoPreviewRequest) { r.Leaf = "C:evil" },
		"backslash in leaf": func(r *NewRepoPreviewRequest) { r.Leaf = `a\b` },
		"device name leaf":  func(r *NewRepoPreviewRequest) { r.Leaf = "NUL" },
		"trailing dot leaf": func(r *NewRepoPreviewRequest) { r.Leaf = "shop." },
		"short alias leaf":  func(r *NewRepoPreviewRequest) { r.Leaf = "SHOP~1" },
		"git leaf":          func(r *NewRepoPreviewRequest) { r.Leaf = ".GIT" },
		"long leaf":         func(r *NewRepoPreviewRequest) { r.Leaf = strings.Repeat("a", newrepos.MaxLeaf+1) },
		"readme too long":   func(r *NewRepoPreviewRequest) { r.README = big },
		"readme not utf8":   func(r *NewRepoPreviewRequest) { r.README = "a\xffb" },
		"readme nul":        func(r *NewRepoPreviewRequest) { r.README = "a\x00b" },
		"ignore too long":   func(r *NewRepoPreviewRequest) { r.Ignore = big },
		"ignore not utf8":   func(r *NewRepoPreviewRequest) { r.Ignore = "\xc3\x28" },
		"ignore nul":        func(r *NewRepoPreviewRequest) { r.Ignore = "\x00" },
		"message too long":  func(r *NewRepoPreviewRequest) { r.CommitMessage = strings.Repeat("m", newrepos.MaxMessage+1) },
		"message not utf8":  func(r *NewRepoPreviewRequest) { r.CommitMessage = "\xff" },
		"message nul":       func(r *NewRepoPreviewRequest) { r.CommitMessage = "a\x00" },
	} {
		req := good
		mut(&req)
		if err := checkSpecText(req); err == nil || !isRefusal(err) {
			t.Errorf("%s: accepted or not a refusal: %v", name, err)
		}
	}
}

func FuzzCheckSpecText(f *testing.F) {
	f.Add("shop-new", "# x\n", "/bin/\n", "Initial commit")
	f.Add("..", "", "", "")
	f.Add("a\x00", "\xff", "\x00", "\xff")
	f.Fuzz(func(t *testing.T, leaf, readme, ignore, msg string) {
		req := NewRepoPreviewRequest{Leaf: leaf, README: readme, Ignore: ignore, CommitMessage: msg}
		err := checkSpecText(req)
		if err != nil {
			if !isRefusal(err) {
				t.Fatalf("not a refusal: %v", err)
			}
			return
		}
		if newrepos.CheckLeaf(leaf) != nil {
			t.Fatalf("accepted leaf %q that the core refuses", leaf)
		}
		for _, c := range []string{readme, ignore, msg} {
			if strings.ContainsRune(c, 0) || len(c) > newrepos.MaxContent || strings.ToValidUTF8(c, "") != c {
				t.Fatalf("accepted text %q", c)
			}
		}
	})
}

// ---- request IDs, including the reserved prefix ----

func TestParseRequestIDRules(t *testing.T) {
	for _, v := range []string{"req_1", "req_polish_1", "r", strings.Repeat("a", maxRequestIDLen)} {
		id, err := parseRequestID(v)
		if err != nil || string(id) != v {
			t.Errorf("%q: %q, %v", v, id, err)
		}
	}
	for name, v := range map[string]string{
		"empty":          "",
		"too long":       strings.Repeat("a", maxRequestIDLen+1),
		"reserved":       "anchor-rep_1",
		"reserved upper": "ANCHOR-rep_1",
		"reserved mixed": "AnChOr-x",
		"reserved bare":  "anchor-",
		"made by anchor": string(coretasks.AnchorRequest("rep_0123456789abcdef")),
		"space":          "req 1",
		"slash":          "req/1",
		"newline":        "req_1\n",
	} {
		if _, err := parseRequestID(v); err == nil || !isRefusal(err) {
			t.Errorf("%s: %q accepted or not a refusal: %v", name, v, err)
		}
	}
}

// A client request ID with a NUL or another control character is refused.
func TestParseRequestIDRefusesControlCharacters(t *testing.T) {
	for _, v := range []string{"req\x00", "req\x1b[31m", "req\x7f"} {
		if _, err := parseRequestID(v); err == nil {
			t.Errorf("request ID %q is accepted (control characters must be refused)", v)
		}
	}
}

func FuzzParseRequestID(f *testing.F) {
	for _, s := range []string{"req_1", "anchor-x", "ANCHOR-x", "", "a b", strings.Repeat("a", 101), "\xff"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, v string) {
		id, err := parseRequestID(v)
		if err != nil {
			if !isRefusal(err) || id != "" {
				t.Fatalf("rejection must be a refusal with no value: %q, %v", id, err)
			}
			return
		}
		if string(id) != v || id.Validate() != nil || len(v) > maxRequestIDLen || strings.HasPrefix(strings.ToLower(v), coretasks.ReservedRequestPrefix) {
			t.Fatalf("accepted %q", v)
		}
	})
}
