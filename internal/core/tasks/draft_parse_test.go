package tasks

import (
	"errors"
	"fmt"
	"math"
	"math/rand"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// This file tests the import boundary (IssueRef, ParseDraftSpec, the dedup key)
// against an INDEPENDENT oracle. The oracle is written from the documented
// rules (docs/design/phase-6-actions.md, phase-6-newrepos.md, the T test map),
// byte by byte, and shares no code with ParseDraftSpec, IssueRef.Validate,
// ValidModelName or core.ID validators.

// ---- independent oracle ----

var oracleName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:\-]{0,63}$`)

func oracleUTF8(s string) bool {
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && n == 1 {
			return false
		}
		i += n
	}
	return true
}

// oracleID: a non-empty identifier with no space, tab, CR, LF, slash,
// backslash or control character (C0, DEL, C1).
func oracleID(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return false
		}
	}
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ' ', '\t', '\r', '\n', '/', '\\':
			return false
		}
	}
	return true
}

func oracleNoBlankOrControl(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] <= 0x20 || s[i] == 0x7f {
			return false
		}
	}
	return true
}

func oracleNone(s, set string) bool { return !strings.ContainsAny(s, set) }

// oracleIssue: host (<=255 bytes, no / @ ? # \), project path (<=1024 bytes, no
// @ ? # \ :), both UTF-8 without blank or control bytes, positive number.
func oracleIssue(instance, project string, number int) bool {
	return number > 0 &&
		len(instance) >= 1 && len(instance) <= 255 && oracleUTF8(instance) && oracleNone(instance, "/@?#\\") &&
		len(project) >= 1 && len(project) <= 1024 && oracleUTF8(project) && oracleNone(project, "@?#\\:") &&
		oracleNoBlankOrControl(instance) && oracleNoBlankOrControl(project)
}

// oracleTrim removes leading and trailing Unicode white space.
func oracleTrim(s string) string {
	runes := []rune(s)
	// An invalid byte decodes to U+FFFD, which is not white space, so trimming
	// on runes and on bytes agree on where the text begins and ends.
	a, b := 0, len(runes)
	for a < b && unicode.IsSpace(runes[a]) {
		a++
	}
	for b > a && unicode.IsSpace(runes[b-1]) {
		b--
	}
	if !oracleUTF8(s) {
		return strings.TrimSpace(s) // invalid UTF-8 is refused anyway; keep it byte-exact
	}
	return string(runes[a:b])
}

func oracleTitle(trimmed string) bool {
	if trimmed == "" || !oracleUTF8(trimmed) || utf8.RuneCountInString(trimmed) > 200 {
		return false
	}
	for _, r := range trimmed {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func oracleBrief(s string) bool {
	return len(s) <= 64*1024 && oracleUTF8(s) && !strings.ContainsRune(s, 0)
}

// oracleDraft reports whether a raw spec is valid.
func oracleDraft(s DraftSpec) bool {
	return oracleID(string(s.RequestID)) && len(s.RequestID) <= 100 &&
		oracleID(string(s.Project)) && oracleID(string(s.Repo)) &&
		oracleIssue(s.Issue.Instance, s.Issue.Project, s.Issue.Number) &&
		oracleTitle(oracleTrim(s.Title)) && oracleBrief(s.Brief) &&
		oracleName.MatchString(s.Agent) && (s.Model == "" || oracleName.MatchString(s.Model))
}

// oracleKey formats the documented dedup key independently.
func oracleKey(instance, project string, number int) string {
	return "issue:" + instance + "/" + project + "#" + strconv.Itoa(number)
}

// oracleParseKey is the independent inverse: instance has no '/', project has
// no '#', so the first '/' and the last '#' delimit the parts unambiguously.
func oracleParseKey(key string) (instance, project string, number int, ok bool) {
	rest, found := strings.CutPrefix(key, "issue:")
	if !found {
		return
	}
	slash := strings.IndexByte(rest, '/')
	hash := strings.LastIndexByte(rest, '#')
	if slash < 0 || hash < slash {
		return
	}
	n, err := strconv.Atoi(rest[hash+1:])
	if err != nil || n <= 0 {
		return
	}
	return rest[:slash], rest[slash+1 : hash], n, true
}

// ---- generators ----

var oddRunes = []string{"a", "Z", "7", "-", ".", "_", ":", " ", "\t", "\n", "\r", "\x00", "\x7f", "/", "@", "?", "#", "\\",
	"é", "日", "\xff", "\xc3", "\xa9", "\u0085", "\u2028", "\u00a0", "\ufeff", "😀"}

func randText(r *rand.Rand, max int) string {
	var b strings.Builder
	for n := r.Intn(max + 1); n > 0; n-- {
		b.WriteString(oddRunes[r.Intn(len(oddRunes))])
	}
	return b.String()
}

func validSpec() DraftSpec {
	return DraftSpec{RequestID: "req_1", Project: "prj_1", Repo: "rep_1", Title: "Fix login",
		Issue: IssueRef{Instance: "git.acme.dev", Project: "acme/shop-api", Number: 12}, Brief: "Users cannot log in.", Agent: "mock"}
}

// randSpec starts from a valid spec and corrupts a few fields with odd text, so
// both accepted and rejected inputs are common.
func randSpec(r *rand.Rand) DraftSpec {
	s := validSpec()
	for n := r.Intn(4); n > 0; n-- {
		switch r.Intn(12) {
		case 0:
			s.RequestID = core.RequestID(randText(r, 6))
		case 1:
			s.Project = core.ProjectID(randText(r, 4))
		case 2:
			s.Repo = core.RepoID(randText(r, 4))
		case 3:
			s.Issue.Instance = "git" + randText(r, 3) + ".acme.dev"
		case 4:
			s.Issue.Project = "acme" + randText(r, 3) + "/shop-api"
		case 5:
			s.Issue.Number = r.Intn(5) - 2
		case 6:
			s.Title = randText(r, 8)
		case 7:
			s.Title = " \t" + s.Title + randText(r, 2)
		case 8:
			s.Brief = randText(r, 12)
		case 9:
			s.Agent = []string{"mock", "", "-x", "a b", "claude-code", strings.Repeat("a", 64), strings.Repeat("a", 65)}[r.Intn(7)]
		case 10:
			s.Model = []string{"", "m1", "--m", "m\n", strings.Repeat("m", 65), "claude-3.5:x"}[r.Intn(6)]
		case 11:
			s.Title = strings.Repeat("é", 195+r.Intn(12)) // rune bound, not byte bound
		}
	}
	return s
}

// ---- properties ----

// Accepted implies valid, and valid implies accepted, against the independent
// oracle; an accepted spec is canonical (parse is idempotent) and differs from
// the input only by the trimmed title.
func TestPropParseDraftSpecMatchesIndependentOracle(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		for i := 0; i < 60; i++ {
			raw := randSpec(r)
			got, err := ParseDraftSpec(raw)
			want := oracleDraft(raw)
			if (err == nil) != want {
				t.Fatalf("accepted=%v oracle=%v for %#v (err %v)", err == nil, want, raw, err)
			}
			if err != nil {
				if !errors.Is(err, ErrInvalidDraft) {
					t.Fatalf("rejection is not ErrInvalidDraft: %v", err)
				}
				continue
			}
			if !oracleDraft(got) {
				t.Fatalf("accepted spec fails the oracle: %#v", got)
			}
			if again, err := ParseDraftSpec(got); err != nil || again != got {
				t.Fatalf("not idempotent: %#v -> %#v (%v)", got, again, err)
			}
			wantOut := raw
			wantOut.Title = oracleTrim(raw.Title)
			if got != wantOut {
				t.Fatalf("parse changed more than the title trim:\n got %#v\nwant %#v", got, wantOut)
			}
		}
	})
}

// Generated provenance reconstructs the same issue, and distinct valid issues
// never share a key (the key is an injective encoding).
func TestPropIssueProvenanceRoundTripsAndKeysAreInjective(t *testing.T) {
	instances := []string{"git.acme.dev", "git.acme.dev:8443", "a", "a:1", "[::1]:80", "gitlab.acme.test"}
	projects := []string{"acme/shop-api", "acme", "acme/shop/api", "a/b/c", "b", "acme/shop-api/x", "日本/店", "a-b/c_d.e"}
	seen := map[string]IssueRef{}
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		for i := 0; i < 40; i++ {
			ref := IssueRef{Instance: instances[r.Intn(len(instances))], Project: projects[r.Intn(len(projects))], Number: 1 + r.Intn(1000)}
			if r.Intn(10) == 0 {
				ref.Number = 1 + r.Intn(math.MaxInt32)
			}
			if !oracleIssue(ref.Instance, ref.Project, ref.Number) {
				t.Fatalf("generator produced an invalid ref %+v", ref)
			}
			if err := ref.Validate(); err != nil {
				t.Fatalf("valid ref refused: %+v %v", ref, err)
			}
			src := ref.Source()
			if err := src.Validate(); err != nil || src.Kind != core.SourceIssue {
				t.Fatalf("source %+v: %v", src, err)
			}
			if src.DedupKey != oracleKey(ref.Instance, ref.Project, ref.Number) || ref.DedupKey() != src.DedupKey {
				t.Fatalf("key %q, independent format %q", src.DedupKey, oracleKey(ref.Instance, ref.Project, ref.Number))
			}
			inst, proj, n, ok := oracleParseKey(src.DedupKey)
			if !ok || (IssueRef{Instance: inst, Project: proj, Number: n}) != ref {
				t.Fatalf("key %q does not reconstruct %+v (got %q %q %d)", src.DedupKey, ref, inst, proj, n)
			}
			if src.Instance != ref.Instance || src.Project != ref.Project || src.Number != ref.Number {
				t.Fatalf("source lost identity: %+v vs %+v", src, ref)
			}
			if prior, dup := seen[src.DedupKey]; dup && prior != ref {
				t.Fatalf("key collision %q: %+v and %+v", src.DedupKey, prior, ref)
			}
			seen[src.DedupKey] = ref
		}
	})
}

// ---- one test per rejection rule, with boundaries ----

func mustReject(t *testing.T, name string, s DraftSpec) {
	t.Helper()
	if _, err := ParseDraftSpec(s); !errors.Is(err, ErrInvalidDraft) {
		t.Errorf("%s: err=%v, want ErrInvalidDraft", name, err)
	}
	if oracleDraft(s) {
		t.Errorf("%s: test case is not invalid by the oracle", name)
	}
}

func mustAccept(t *testing.T, name string, s DraftSpec) DraftSpec {
	t.Helper()
	got, err := ParseDraftSpec(s)
	if err != nil {
		t.Errorf("%s: refused: %v", name, err)
	}
	if !oracleDraft(s) {
		t.Errorf("%s: test case is not valid by the oracle", name)
	}
	return got
}

func TestParseDraftSpecRequestIDRules(t *testing.T) {
	for name, id := range map[string]string{"empty": "", "space": "a b", "tab": "a\tb", "newline": "a\nb", "cr": "a\rb", "slash": "a/b",
		"backslash": `a\b`, "101 bytes": strings.Repeat("r", 101)} {
		s := validSpec()
		s.RequestID = core.RequestID(id)
		mustReject(t, "request "+name, s)
	}
	for name, id := range map[string]string{"100 bytes": strings.Repeat("r", 100), "dash": "req-1", "unicode": "req_é"} {
		s := validSpec()
		s.RequestID = core.RequestID(id)
		mustAccept(t, "request "+name, s)
	}
}

func TestParseDraftSpecProjectAndRepoIDRules(t *testing.T) {
	for _, bad := range []string{"", "a b", "a/b", `a\b`, "a\nb"} {
		s := validSpec()
		s.Project = core.ProjectID(bad)
		mustReject(t, fmt.Sprintf("project %q", bad), s)
		s = validSpec()
		s.Repo = core.RepoID(bad)
		mustReject(t, fmt.Sprintf("repo %q", bad), s)
	}
}

func TestParseDraftSpecIssueIdentityRules(t *testing.T) {
	type tc struct {
		name   string
		mutate func(*IssueRef)
	}
	rejected := []tc{
		{"number zero", func(r *IssueRef) { r.Number = 0 }},
		{"number negative", func(r *IssueRef) { r.Number = -1 }},
		{"number min int", func(r *IssueRef) { r.Number = math.MinInt }},
		{"empty instance", func(r *IssueRef) { r.Instance = "" }},
		{"empty project", func(r *IssueRef) { r.Project = "" }},
		{"instance 256 bytes", func(r *IssueRef) { r.Instance = strings.Repeat("h", 256) }},
		{"project 1025 bytes", func(r *IssueRef) { r.Project = strings.Repeat("p", 1025) }},
		{"instance slash", func(r *IssueRef) { r.Instance = "git.acme.dev/x" }},
		{"instance scheme", func(r *IssueRef) { r.Instance = "https://git.acme.dev" }},
		{"instance at", func(r *IssueRef) { r.Instance = "tok@git.acme.dev" }},
		{"instance question", func(r *IssueRef) { r.Instance = "git.acme.dev?x" }},
		{"instance hash", func(r *IssueRef) { r.Instance = "git.acme.dev#x" }},
		{"instance backslash", func(r *IssueRef) { r.Instance = `git\acme` }},
		{"project at", func(r *IssueRef) { r.Project = "tok@acme/shop-api" }},
		{"project question", func(r *IssueRef) { r.Project = "acme/shop-api?x" }},
		{"project hash", func(r *IssueRef) { r.Project = "acme/shop-api#12" }},
		{"project backslash", func(r *IssueRef) { r.Project = `acme\shop-api` }},
		{"project colon", func(r *IssueRef) { r.Project = "acme:shop" }},
		{"instance space", func(r *IssueRef) { r.Instance = "git acme" }},
		{"project space", func(r *IssueRef) { r.Project = "acme /shop" }},
		{"instance tab", func(r *IssueRef) { r.Instance = "git\tacme" }},
		{"project newline", func(r *IssueRef) { r.Project = "acme\n/shop" }},
		{"instance NUL", func(r *IssueRef) { r.Instance = "git\x00acme" }},
		{"project DEL", func(r *IssueRef) { r.Project = "acme\x7f/shop" }},
		{"instance invalid UTF-8", func(r *IssueRef) { r.Instance = "git\xffacme" }},
		{"project invalid UTF-8", func(r *IssueRef) { r.Project = "acme/\xc3" }},
		// "\xc3" and "\xa9" are each invalid alone but would be "é" if joined:
		// the fields are checked one by one and never glued.
		{"invalid UTF-8 split across fields", func(r *IssueRef) { r.Instance = "git\xc3"; r.Project = "\xa9acme" }},
	}
	for _, c := range rejected {
		s := validSpec()
		c.mutate(&s.Issue)
		mustReject(t, c.name, s)
		if err := s.Issue.Validate(); !errors.Is(err, ErrInvalidDraft) {
			t.Errorf("%s: IssueRef.Validate = %v", c.name, err)
		}
	}
	accepted := []tc{
		{"number 1", func(r *IssueRef) { r.Number = 1 }},
		{"number max int", func(r *IssueRef) { r.Number = math.MaxInt }},
		{"instance 255 bytes", func(r *IssueRef) { r.Instance = strings.Repeat("h", 255) }},
		{"project 1024 bytes", func(r *IssueRef) { r.Project = strings.Repeat("p", 1024) }},
		{"instance with port", func(r *IssueRef) { r.Instance = "git.acme.dev:8443" }},
		{"project with slashes", func(r *IssueRef) { r.Project = "acme/shop/api" }},
		{"unicode project", func(r *IssueRef) { r.Project = "日本/店" }},
		{"valid joined rune", func(r *IssueRef) { r.Instance = "gité"; r.Project = "acme/é" }},
	}
	for _, c := range accepted {
		s := validSpec()
		c.mutate(&s.Issue)
		got := mustAccept(t, c.name, s)
		if got.Issue != s.Issue {
			t.Errorf("%s: parse changed the issue identity: %+v", c.name, got.Issue)
		}
	}
}

func TestParseDraftSpecTitleRules(t *testing.T) {
	for name, title := range map[string]string{
		"empty": "", "spaces only": " \t\n ", "201 ASCII runes": strings.Repeat("a", 201),
		"201 multibyte runes": strings.Repeat("é", 201), "interior newline": "a\nb", "interior tab": "a\tb", "interior CR": "a\rb",
		"NUL": "a\x00b", "DEL": "a\x7fb", "invalid UTF-8": "a\xffb", "escape": "a\x1bb",
	} {
		s := validSpec()
		s.Title = title
		mustReject(t, "title "+name, s)
	}
	for name, title := range map[string]string{
		"200 ASCII runes":       strings.Repeat("a", 200),
		"200 multibyte runes":   strings.Repeat("é", 200), // 400 bytes: the bound is runes, not bytes
		"200 emoji":             strings.Repeat("😀", 200),
		"surrounded by space":   " \t\r\n  Fix login \r\n",
		"interior double space": "Fix  login",
	} {
		s := validSpec()
		s.Title = title
		got := mustAccept(t, "title "+name, s)
		if got.Title != oracleTrim(title) {
			t.Errorf("title %s: %q", name, got.Title)
		}
	}
}

func TestParseDraftSpecBriefRules(t *testing.T) {
	for name, brief := range map[string]string{
		"64 KiB + 1 byte": strings.Repeat("a", 64*1024+1), "NUL": "a\x00b", "invalid UTF-8": "a\xffb",
		"split invalid UTF-8": "a\xc3", "multibyte over by one byte": strings.Repeat("é", 32*1024) + "a",
	} {
		s := validSpec()
		s.Brief = brief
		mustReject(t, "brief "+name, s)
	}
	for name, brief := range map[string]string{
		"empty": "", "exactly 64 KiB": strings.Repeat("a", 64*1024), "multibyte exactly 64 KiB": strings.Repeat("é", 32*1024),
		"CRLF kept": "Line one\r\nLine two\r\n", "tabs and controls kept": "a\tb\x1bc", "markdown": "# Heading\n\n- [ ] item\n",
	} {
		s := validSpec()
		s.Brief = brief
		got := mustAccept(t, "brief "+name, s)
		if got.Brief != brief {
			t.Errorf("brief %s was changed", name)
		}
	}
}

func TestParseDraftSpecAgentAndModelRules(t *testing.T) {
	for name, v := range map[string]string{"empty": "", "leading dash": "-x", "double dash": "--agent", "space": "a b", "newline": "a\nb",
		"slash": "a/b", "semicolon": "a;b", "dollar": "$(x)", "65 bytes": strings.Repeat("a", 65), "leading dot": ".a", "unicode": "agént"} {
		s := validSpec()
		s.Agent = v
		mustReject(t, "agent "+name, s)
		s = validSpec()
		s.Model = v
		if v == "" {
			if _, err := ParseDraftSpec(s); err != nil {
				t.Errorf("an empty model must be allowed: %v", err)
			}
			continue
		}
		mustReject(t, "model "+name, s)
	}
	for name, v := range map[string]string{"64 bytes": strings.Repeat("a", 64), "dots": "claude-3.5", "colon": "m:latest", "underscore": "m_1", "digit first": "4o"} {
		s := validSpec()
		s.Agent, s.Model = v, v
		mustAccept(t, name, s)
	}
}

// Whitespace normalization is intentional: only the title is trimmed; every
// other field passes through byte for byte (CRLF included).
func TestParseDraftSpecOnlyTrimsTheTitle(t *testing.T) {
	s := validSpec()
	s.Title = "\r\n  Fix login\t"
	s.Brief = "line1\r\nline2\r\n"
	got, err := ParseDraftSpec(s)
	if err != nil {
		t.Fatal(err)
	}
	want := s
	want.Title = "Fix login"
	if got != want {
		t.Fatalf("%#v", got)
	}
}

// ---- fuzz ----

// FuzzDraftSpecIdentity fuzzes every field of the boundary, so identity
// rejection (not only prompt and title) is exercised. The oracle decides in
// both directions; an accepted spec is canonical.
func FuzzDraftSpecIdentity(f *testing.F) {
	f.Add("req_1", "prj_1", "rep_1", "git.acme.dev", "acme/shop-api", 12, "Fix login", "Users cannot log in.", "mock", "")
	f.Add("", "a b", "a/b", "https://x", "a#1", 0, " ", "\x00", "--x", "--y")
	f.Add("r", "p", "q", "git\xc3", "\xa9acme", 1, "t\n", "b\r\n", "mock", "m:1")
	f.Fuzz(func(t *testing.T, req, prj, rep, inst, ip string, n int, title, brief, agent, model string) {
		raw := DraftSpec{RequestID: core.RequestID(req), Project: core.ProjectID(prj), Repo: core.RepoID(rep),
			Issue: IssueRef{Instance: inst, Project: ip, Number: n}, Title: title, Brief: brief, Agent: agent, Model: model}
		got, err := ParseDraftSpec(raw)
		if (err == nil) != oracleDraft(raw) {
			t.Fatalf("accepted=%v oracle=%v for %#v (err %v)", err == nil, oracleDraft(raw), raw, err)
		}
		if err != nil {
			if !errors.Is(err, ErrInvalidDraft) {
				t.Fatalf("not ErrInvalidDraft: %v", err)
			}
			return
		}
		if again, err := ParseDraftSpec(got); err != nil || again != got {
			t.Fatalf("not idempotent: %#v -> %#v (%v)", got, again, err)
		}
		src := got.Issue.Source()
		if src.Validate() != nil || src.DedupKey != oracleKey(inst, ip, n) {
			t.Fatalf("provenance %+v", src)
		}
		if i, p, num, ok := oracleParseKey(src.DedupKey); !ok || i != inst || p != ip || num != n {
			t.Fatalf("key %q does not reconstruct the issue", src.DedupKey)
		}
	})
}
