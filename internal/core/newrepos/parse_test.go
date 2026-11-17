package newrepos

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Boundary parser tests (docs/AGENTS.md 5.11): ParseInitSpec, ParsePublishSpec,
// CheckLeaf, ParseStage, ValidateReceipt and ValidateRemoteURL each get a
// round trip, an accepted-implies-valid check, one test per rejection rule and
// a fuzz target. NR-01, NR-02, NR-03, NR-04.

// leafInvariants is what every accepted leaf must satisfy, written out
// independently of CheckLeaf: one child name, nothing a path or a Windows
// name rule could turn into something else.
func leafInvariants(t testing.TB, leaf string) {
	t.Helper()
	switch {
	case leaf == "" || leaf == "." || leaf == "..":
		t.Fatalf("accepted %q", leaf)
	case len(leaf) > MaxLeaf || !utf8.ValidString(leaf):
		t.Fatalf("accepted over-long or invalid text %q", leaf)
	case strings.ContainsAny(leaf, "/\\:<>\"|?*"):
		t.Fatalf("accepted a separator, drive or stream marker or a Windows-forbidden character: %q", leaf)
	case strings.HasSuffix(leaf, ".") || strings.HasSuffix(leaf, " ") || strings.HasPrefix(leaf, " "):
		t.Fatalf("accepted a name Windows rewrites: %q", leaf)
	case strings.EqualFold(leaf, ".git"):
		t.Fatalf("accepted .git: %q", leaf)
	}
	for _, r := range leaf {
		if r < 0x20 || r == 0x7f {
			t.Fatalf("accepted a control character: %q", leaf)
		}
	}
	// One child of the parent, whichever way the OS reads it.
	parent := filepath.Join(string(filepath.Separator), "work", "acme-shop")
	joined := filepath.Join(parent, leaf)
	if filepath.Dir(joined) != parent || filepath.Base(joined) != leaf {
		t.Fatalf("leaf %q is not a single child of its parent: %q", leaf, joined)
	}
	if path.Base(path.Join("/p", leaf)) != leaf || path.Dir(path.Join("/p", leaf)) != "/p" {
		t.Fatalf("leaf %q escapes with slash semantics", leaf)
	}
}

// ---- CheckLeaf ----

// One rejection rule per test, so a weakened rule names itself.
func TestCheckLeafRejectionRules(t *testing.T) {
	rules := []struct {
		rule string
		bad  []string
	}{
		{"empty", []string{""}},
		{"dot names", []string{".", ".."}},
		{"too long", []string{strings.Repeat("a", MaxLeaf+1), strings.Repeat("é", MaxLeaf)}},
		{"invalid utf-8", []string{"\xff", "a\xc0\xafb", "\xed\xa0\x80"}},
		{"forward slash is a separator", []string{"a/b", "/", "/abs", "../up", "a/"}},
		{"backslash is a separator", []string{`a\b`, `\`, `..\up`, `\\wsl$\Ubuntu`, `\\.\COM1`, `\\?\C:\x`}},
		{"colon is a drive or stream marker", []string{"C:", "C:x", "a:b", "a::$DATA", "a:stream", ":"}},
		{"windows forbidden characters", []string{"a<b", "a>b", `a"b`, "a|b", "a?b", "a*b"}},
		{"control characters", []string{"a\x00b", "a\nb", "a\rb", "a\tb", "a\x1fb", "a\x7fb", "\x01"}},
		{"leading space", []string{" x", "  x"}},
		{"trailing space", []string{"x ", "x  "}},
		{"trailing dot", []string{"x.", "x..", "...", "a.b."}},
		{"dot git in any case", []string{".git", ".GIT", ".Git", ".gIt"}},
		{"8.3 short name", []string{"PROGRA~1", "progra~1", "a~1", "LONGDI~1.TXT", "abcdef~12", "x~9.git"}},
		{"classic device names", []string{"CON", "con", "Prn", "AUX", "NUL", "nul", "CONIN$", "CONOUT$", "CLOCK$"}},
		{"device names with an extension", []string{"con.txt", "NUL.md", "aux.tar.gz", "COM1.txt", "LPT9.log"}},
		{"numbered devices", []string{"COM0", "COM1", "com9", "LPT0", "lpt1", "LPT9"}},
	}
	for _, r := range rules {
		t.Run(r.rule, func(t *testing.T) {
			for _, bad := range r.bad {
				if err := CheckLeaf(bad); err == nil {
					t.Errorf("%q was accepted", bad)
				} else if !strings.Contains(err.Error(), ErrInvalidSpec.Error()) {
					t.Errorf("%q: error %v does not wrap ErrInvalidSpec", bad, err)
				}
			}
		})
	}
}

func TestCheckLeafAcceptsOrdinaryNames(t *testing.T) {
	good := []string{
		"shop-new", "shop_new", "Shop.New", "héllo", "a b", "x~y", "shop-api", "a", strings.Repeat("a", MaxLeaf),
		"my project", "v1.2.3", ".github", ".hidden", "COM10", "CONSOLE", "AUXILIARY", "NULL", "a.git.b", "x~", "x~a",
		"日本語", "ünï côdé", "a-b_c.d",
	}
	for _, g := range good {
		if err := CheckLeaf(g); err != nil {
			t.Errorf("%q refused: %v", g, err)
			continue
		}
		leafInvariants(t, g)
	}
}

// Accepted implies valid: random names built from a hostile alphabet. Whatever
// CheckLeaf accepts satisfies every leaf invariant.
func TestCheckLeafAcceptedImpliesValidProperty(t *testing.T) {
	alphabet := []string{"a", "b", "Z", "0", "9", ".", " ", "-", "_", "~", "/", `\`, ":", "*", "?", "\x00", "\n", "é", "日", "\x7f", "<", "$",
		"con", "NUL", "COM1", "aux", ".git", "~1", "..", "\xff"}
	rng := rand.New(rand.NewSource(7))
	accepted := 0
	for i := 0; i < 20000; i++ {
		var b strings.Builder
		for n := rng.Intn(6); n >= 0; n-- {
			b.WriteString(alphabet[rng.Intn(len(alphabet))])
		}
		leaf := b.String()
		if CheckLeaf(leaf) != nil {
			continue
		}
		accepted++
		leafInvariants(t, leaf)
	}
	if accepted < 200 {
		t.Fatalf("the generator only produced %d accepted names; the property is not exercised", accepted)
	}
}

// Round trip: an accepted leaf, taken through a spec, the JSON a store would
// keep and back, is accepted again, unchanged, with the same hash.
func TestCheckLeafRoundTripThroughSpec(t *testing.T) {
	for _, leaf := range []string{"shop-new", "a b", "héllo", "Shop.New", strings.Repeat("a", MaxLeaf)} {
		s := validInit()
		s.Leaf = leaf
		parsed, err := ParseInitSpec(s)
		if err != nil {
			t.Fatalf("%q: %v", leaf, err)
		}
		raw, err := json.Marshal(parsed)
		if err != nil {
			t.Fatal(err)
		}
		var back InitSpec
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatal(err)
		}
		again, err := ParseInitSpec(back)
		if err != nil || again != parsed || again.Hash() != parsed.Hash() {
			t.Fatalf("%q did not round-trip: %+v vs %+v (%v)", leaf, again, parsed, err)
		}
	}
}

func FuzzCheckLeaf(f *testing.F) {
	for _, s := range []string{"shop-new", "", ".", "..", "a/b", `a\b`, "a:b", "CON", "con.txt", "PROGRA~1", ".git", "x.", "x ", "a\x00", "\xff",
		strings.Repeat("a", 65), "COM1", "日本語", "a b"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, leaf string) {
		if err := CheckLeaf(leaf); err != nil {
			return
		}
		leafInvariants(t, leaf)
	})
}

// ---- ParseInitSpec ----

func TestParseInitSpecRoundTrip(t *testing.T) {
	cases := map[string]InitSpec{
		"plain":         validInit(),
		"crlf content":  func() InitSpec { s := validInit(); s.README = "# x\r\nline\r\n"; s.Ignore = "*.log\r\n"; return s }(),
		"empty content": func() InitSpec { s := validInit(); s.README = ""; s.Ignore = ""; return s }(),
		"unicode": func() InitSpec {
			s := validInit()
			s.README = "# café 日本語\n"
			s.CommitMessage = "Début ✓"
			return s
		}(),
		"max content": func() InitSpec {
			s := validInit()
			s.README = strings.Repeat("a", MaxContent)
			s.Ignore = strings.Repeat("b", MaxContent)
			return s
		}(),
		"max message":      func() InitSpec { s := validInit(); s.CommitMessage = strings.Repeat("m", MaxMessage); return s }(),
		"multi-line title": func() InitSpec { s := validInit(); s.CommitMessage = "Initial commit\n\nBody line.\n"; return s }(),
	}
	seen := map[string]string{}
	for name, s := range cases {
		parsed, err := ParseInitSpec(s)
		if err != nil || parsed != s {
			t.Fatalf("%s: parse changed or refused the spec: %+v, %v", name, parsed, err)
		}
		raw, _ := json.Marshal(parsed)
		var back InitSpec
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatal(err)
		}
		again, err := ParseInitSpec(back)
		if err != nil || again != parsed || again.Hash() != parsed.Hash() {
			t.Fatalf("%s: round trip changed the spec", name)
		}
		if other, dup := seen[parsed.Hash()]; dup {
			t.Fatalf("%s and %s share a hash", name, other)
		}
		seen[parsed.Hash()] = name
	}
}

// One rejection per rule of InitSpec.Validate, one test each.
func TestParseInitSpecRejectionRules(t *testing.T) {
	rules := map[string]func(*InitSpec){
		"project id empty":          func(s *InitSpec) { s.Project = "" },
		"project id malformed":      func(s *InitSpec) { s.Project = "not an id" },
		"repo id empty":             func(s *InitSpec) { s.Repo = "" },
		"repo id malformed":         func(s *InitSpec) { s.Repo = "../rep" },
		"leaf invalid":              func(s *InitSpec) { s.Leaf = "../x" },
		"leaf absolute":             func(s *InitSpec) { s.Leaf = "/etc" },
		"leaf drive letter":         func(s *InitSpec) { s.Leaf = `C:\x` },
		"branch empty":              func(s *InitSpec) { s.Branch = "" },
		"branch not main":           func(s *InitSpec) { s.Branch = "master" },
		"branch looks like option":  func(s *InitSpec) { s.Branch = "-main" },
		"readme over the limit":     func(s *InitSpec) { s.README = strings.Repeat("a", MaxContent+1) },
		"ignore over the limit":     func(s *InitSpec) { s.Ignore = strings.Repeat("a", MaxContent+1) },
		"readme nul":                func(s *InitSpec) { s.README = "a\x00b" },
		"ignore nul":                func(s *InitSpec) { s.Ignore = "\x00" },
		"readme invalid utf-8":      func(s *InitSpec) { s.README = "a\xffb" },
		"ignore invalid utf-8":      func(s *InitSpec) { s.Ignore = "\xc0\xaf" },
		"message empty":             func(s *InitSpec) { s.CommitMessage = "" },
		"message only whitespace":   func(s *InitSpec) { s.CommitMessage = " \n\t " },
		"message over the limit":    func(s *InitSpec) { s.CommitMessage = strings.Repeat("m", MaxMessage+1) },
		"message nul":               func(s *InitSpec) { s.CommitMessage = "a\x00" },
		"message invalid utf-8":     func(s *InitSpec) { s.CommitMessage = "a\xff" },
		"whole spec zero value":     func(s *InitSpec) { *s = InitSpec{} },
		"leaf is a windows device":  func(s *InitSpec) { s.Leaf = "NUL" },
		"leaf is a short name":      func(s *InitSpec) { s.Leaf = "PROGRA~1" },
		"leaf is an alternate data": func(s *InitSpec) { s.Leaf = "a::$DATA" },
	}
	for name, mutate := range rules {
		t.Run(name, func(t *testing.T) {
			s := validInit()
			mutate(&s)
			if _, err := ParseInitSpec(s); err == nil {
				t.Fatalf("accepted %+v", s)
			}
			if s.Validate() == nil {
				t.Fatal("Validate accepts what ParseInitSpec refused")
			}
		})
	}
}

// Accepted implies valid: random specs. An accepted spec satisfies every
// stated bound, and re-parsing it returns it unchanged.
func TestParseInitSpecAcceptedImpliesValidProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	pickStr := func(opts ...string) string {
		if rng.Intn(10) < 8 {
			return opts[0]
		}
		return opts[rng.Intn(len(opts))]
	}
	accepted := 0
	for i := 0; i < 5000; i++ {
		s := InitSpec{
			Project:       core.ProjectID("prj_" + pickStr("1", "a", "", "x y")),
			Repo:          core.RepoID("rep_" + pickStr("1", "b", "", "../")),
			Leaf:          pickStr("shop-new", "a/b", "NUL", "x.", "ok name", "", "é", `a\b`),
			Branch:        pickStr("main", "main", "dev", ""),
			README:        pickStr("# x\n", "", "a\x00", "a\xff", strings.Repeat("a", MaxContent), strings.Repeat("a", MaxContent+1), "a\r\nb\r\n"),
			Ignore:        pickStr("*.log\n", "", "\x00", strings.Repeat("b", MaxContent+1)),
			CommitMessage: pickStr("Initial commit", " ", "", "a\x00", strings.Repeat("m", MaxMessage+1), "ok"),
		}
		got, err := ParseInitSpec(s)
		if err != nil {
			continue
		}
		accepted++
		checkAcceptedInit(t, got)
		if got != s {
			t.Fatalf("parse changed an accepted spec: %+v -> %+v", s, got)
		}
	}
	if accepted < 100 {
		t.Fatalf("only %d accepted specs; the property is not exercised", accepted)
	}
}

func checkAcceptedInit(t testing.TB, s InitSpec) {
	t.Helper()
	if s.Validate() != nil {
		t.Fatalf("accepted but not valid: %+v", s)
	}
	leafInvariants(t, s.Leaf)
	if s.Branch != "main" {
		t.Fatalf("accepted branch %q", s.Branch)
	}
	for _, c := range []string{s.README, s.Ignore} {
		if len(c) > MaxContent || !utf8.ValidString(c) || strings.ContainsRune(c, 0) {
			t.Fatalf("accepted file content over the bounds or not text")
		}
	}
	if strings.TrimSpace(s.CommitMessage) == "" || len(s.CommitMessage) > MaxMessage || !utf8.ValidString(s.CommitMessage) || strings.ContainsRune(s.CommitMessage, 0) {
		t.Fatalf("accepted commit message %q", s.CommitMessage)
	}
	if len(s.Hash()) != 64 {
		t.Fatalf("hash %q", s.Hash())
	}
}

func FuzzParseInitSpec(f *testing.F) {
	f.Add("shop-new", "# x\n", "*.log\n", "Initial commit", "main")
	f.Add("../x", "a\x00", "", " ", "dev")
	f.Add("NUL", strings.Repeat("a", MaxContent+1), "\xff", "m\x00", "")
	f.Fuzz(func(t *testing.T, leaf, readme, ignore, msg, branch string) {
		s := InitSpec{Project: "prj_1", Repo: "rep_1", Leaf: leaf, Branch: branch, README: readme, Ignore: ignore, CommitMessage: msg}
		got, err := ParseInitSpec(s)
		if err != nil {
			if s.Validate() == nil {
				t.Fatalf("Validate accepts what ParseInitSpec refused: %+v", s)
			}
			return
		}
		checkAcceptedInit(t, got)
		if got != s {
			t.Fatalf("parse changed an accepted spec")
		}
		// The hash binds every field: change one byte and it must move.
		s2 := s
		s2.Leaf = s.Leaf + "x"
		if s2.Hash() == s.Hash() {
			t.Fatal("hash ignores the leaf")
		}
	})
}

// ---- ParsePublishSpec ----

func TestParsePublishSpecRoundTrip(t *testing.T) {
	cases := map[string]PublishSpec{
		"private org": validPublish(),
		"public user": func() PublishSpec {
			s := validPublish()
			s.Visibility = VisibilityPublic
			s.OwnerKind = OwnerUser
			return s
		}(),
		"sha-256 head": func() PublishSpec { s := validPublish(); s.ExpectedHead = strings.Repeat("c", 64); return s }(),
		"max names": func() PublishSpec {
			s := validPublish()
			s.Owner = strings.Repeat("o", MaxOwner)
			s.Name = strings.Repeat("n", MaxRepoName)
			return s
		}(),
		"host and port": func() PublishSpec {
			s := validPublish()
			s.HostInstance = "git.acme-shop.test:8443"
			return s
		}(),
		"dotted names": func() PublishSpec { s := validPublish(); s.Owner = "acme.shop"; s.Name = "shop_api-v2.1"; return s }(),
	}
	seen := map[string]string{}
	for name, s := range cases {
		parsed, err := ParsePublishSpec(s)
		if err != nil || parsed != s {
			t.Fatalf("%s: %+v, %v", name, parsed, err)
		}
		raw, _ := json.Marshal(parsed)
		var back PublishSpec
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatal(err)
		}
		again, err := ParsePublishSpec(back)
		if err != nil || again != parsed || again.Hash() != parsed.Hash() {
			t.Fatalf("%s: round trip changed the spec", name)
		}
		if other, dup := seen[parsed.Hash()]; dup {
			t.Fatalf("%s and %s share a hash", name, other)
		}
		seen[parsed.Hash()] = name
	}
}

// One rejection per rule of PublishSpec.Validate, one test each.
func TestParsePublishSpecRejectionRules(t *testing.T) {
	rules := map[string]func(*PublishSpec){
		"repo id empty":             func(s *PublishSpec) { s.Repo = "" },
		"repo id malformed":         func(s *PublishSpec) { s.Repo = "../rep" },
		"host empty":                func(s *PublishSpec) { s.HostInstance = "" },
		"host upper case":           func(s *PublishSpec) { s.HostInstance = "GitHub.com" },
		"host with a space":         func(s *PublishSpec) { s.HostInstance = "git hub.com" },
		"host with a tab":           func(s *PublishSpec) { s.HostInstance = "github.com\t" },
		"host with a slash":         func(s *PublishSpec) { s.HostInstance = "github.com/x" },
		"host with a scheme":        func(s *PublishSpec) { s.HostInstance = "https://github.com" },
		"host with a backslash":     func(s *PublishSpec) { s.HostInstance = `github.com\x` },
		"host with userinfo":        func(s *PublishSpec) { s.HostInstance = "tok@github.com" },
		"host with a token":         func(s *PublishSpec) { s.HostInstance = "ghp_abc:x@github.com" },
		"host with DEL":             func(s *PublishSpec) { s.HostInstance = "github.com\x7f" },
		"owner empty":               func(s *PublishSpec) { s.Owner = "" },
		"owner too long":            func(s *PublishSpec) { s.Owner = strings.Repeat("o", MaxOwner+1) },
		"owner starts with a dot":   func(s *PublishSpec) { s.Owner = ".acme" },
		"owner starts with a dash":  func(s *PublishSpec) { s.Owner = "-acme" },
		"owner ends with .git":      func(s *PublishSpec) { s.Owner = "acme.git" },
		"owner with a slash":        func(s *PublishSpec) { s.Owner = "acme/shop" },
		"owner with a space":        func(s *PublishSpec) { s.Owner = "acme shop" },
		"owner with an at sign":     func(s *PublishSpec) { s.Owner = "acme@x" },
		"owner not ascii":           func(s *PublishSpec) { s.Owner = "ácme" },
		"owner dot dot":             func(s *PublishSpec) { s.Owner = ".." },
		"name empty":                func(s *PublishSpec) { s.Name = "" },
		"name too long":             func(s *PublishSpec) { s.Name = strings.Repeat("n", MaxRepoName+1) },
		"name starts with a dot":    func(s *PublishSpec) { s.Name = ".shop" },
		"name starts with a dash":   func(s *PublishSpec) { s.Name = "-shop" },
		"name ends with .git":       func(s *PublishSpec) { s.Name = "shop.git" },
		"name with a slash":         func(s *PublishSpec) { s.Name = "a/b" },
		"name with a colon":         func(s *PublishSpec) { s.Name = "a:b" },
		"name with a newline":       func(s *PublishSpec) { s.Name = "a\nb" },
		"owner kind empty":          func(s *PublishSpec) { s.OwnerKind = "" },
		"owner kind unknown":        func(s *PublishSpec) { s.OwnerKind = "team" },
		"visibility empty":          func(s *PublishSpec) { s.Visibility = "" },
		"visibility unknown":        func(s *PublishSpec) { s.Visibility = "internal" },
		"visibility upper case":     func(s *PublishSpec) { s.Visibility = "Private" },
		"branch empty":              func(s *PublishSpec) { s.Branch = "" },
		"branch not main":           func(s *PublishSpec) { s.Branch = "dev" },
		"branch is a full ref":      func(s *PublishSpec) { s.Branch = "refs/heads/main" },
		"head empty":                func(s *PublishSpec) { s.ExpectedHead = "" },
		"head too short":            func(s *PublishSpec) { s.ExpectedHead = strings.Repeat("a", 39) },
		"head between sizes":        func(s *PublishSpec) { s.ExpectedHead = strings.Repeat("a", 41) },
		"head abbreviated":          func(s *PublishSpec) { s.ExpectedHead = "abc1234" },
		"head upper case":           func(s *PublishSpec) { s.ExpectedHead = strings.Repeat("A", 40) },
		"head not hex":              func(s *PublishSpec) { s.ExpectedHead = strings.Repeat("g", 40) },
		"head is a ref name":        func(s *PublishSpec) { s.ExpectedHead = "HEAD" },
		"marker empty":              func(s *PublishSpec) { s.Marker = "" },
		"marker too short":          func(s *PublishSpec) { s.Marker = strings.Repeat("b", 15) },
		"marker too long":           func(s *PublishSpec) { s.Marker = strings.Repeat("b", 65) },
		"marker not hex":            func(s *PublishSpec) { s.Marker = strings.Repeat("z", 16) },
		"marker upper case":         func(s *PublishSpec) { s.Marker = strings.Repeat("B", 16) },
		"marker with closing brace": func(s *PublishSpec) { s.Marker = strings.Repeat("b", 15) + "]" },
		"whole spec zero value":     func(s *PublishSpec) { *s = PublishSpec{} },
	}
	for name, mutate := range rules {
		t.Run(name, func(t *testing.T) {
			s := validPublish()
			mutate(&s)
			if _, err := ParsePublishSpec(s); err == nil {
				t.Fatalf("accepted %+v", s)
			}
			if s.Validate() == nil {
				t.Fatal("Validate accepts what ParsePublishSpec refused")
			}
		})
	}
}

func checkAcceptedPublish(t testing.TB, s PublishSpec) {
	t.Helper()
	if s.Validate() != nil {
		t.Fatalf("accepted but not valid: %+v", s)
	}
	if s.Visibility != VisibilityPrivate && s.Visibility != VisibilityPublic {
		t.Fatalf("accepted visibility %q", s.Visibility)
	}
	if s.Branch != "main" {
		t.Fatalf("accepted branch %q", s.Branch)
	}
	if strings.ContainsAny(s.HostInstance, "/\\@ \t\n\x7f") || s.HostInstance != strings.ToLower(s.HostInstance) || s.HostInstance == "" {
		t.Fatalf("accepted host %q", s.HostInstance)
	}
	for _, n := range []string{s.Owner, s.Name} {
		if n == "" || n[0] == '.' || n[0] == '-' || strings.HasSuffix(n, ".git") || strings.ContainsAny(n, "/\\:@ \x00\n") {
			t.Fatalf("accepted account or repository name %q", n)
		}
		for _, r := range n {
			if r > 0x7f {
				t.Fatalf("accepted non-ascii name %q", n)
			}
		}
	}
	if (len(s.ExpectedHead) != 40 && len(s.ExpectedHead) != 64) || strings.Trim(s.ExpectedHead, "0123456789abcdef") != "" {
		t.Fatalf("accepted head %q", s.ExpectedHead)
	}
	if len(s.Marker) < 16 || len(s.Marker) > 64 || strings.Trim(s.Marker, "0123456789abcdef") != "" {
		t.Fatalf("accepted marker %q", s.Marker)
	}
	// The remote URL the shell builds from these fields is credential-free.
	if err := s.ValidateDestination(fmt.Sprintf("https://%s/%s/%s.git", s.HostInstance, s.Owner, s.Name)); err != nil {
		t.Fatalf("an accepted spec names a destination that is not valid: %v", err)
	}
	if d := s.Description(); strings.ContainsAny(d, "@\n") || !strings.Contains(d, s.Marker) {
		t.Fatalf("description %q", d)
	}
}

func TestParsePublishSpecAcceptedImpliesValidProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(13))
	pick := func(opts ...string) string {
		if rng.Intn(10) < 8 {
			return opts[0]
		}
		return opts[rng.Intn(len(opts))]
	}
	accepted := 0
	for i := 0; i < 8000; i++ {
		s := PublishSpec{
			Repo:         core.RepoID("rep_" + pick("1", "2", "", "/")),
			HostInstance: pick("github.com", "git.acme-shop.test", "GitHub.com", "a@b", "", "h/x", "git.test:8443"),
			Owner:        pick("acme", "acme-shop", "", ".x", "a/b", "ácme", "acme.git", "a_b"),
			OwnerKind:    OwnerKind(pick("org", "user", "", "team")),
			Name:         pick("shop-new", "shop-api", "", "-x", "x.git", "a b", "n.1"),
			Visibility:   Visibility(pick("private", "public", "", "internal")),
			Branch:       pick("main", "main", "dev", ""),
			ExpectedHead: pick(strings.Repeat("a", 40), strings.Repeat("b", 64), strings.Repeat("A", 40), "abc", ""),
			Marker:       pick(strings.Repeat("c", 16), strings.Repeat("d", 32), "short", strings.Repeat("E", 16), ""),
		}
		got, err := ParsePublishSpec(s)
		if err != nil {
			continue
		}
		accepted++
		checkAcceptedPublish(t, got)
		if got != s {
			t.Fatalf("parse changed an accepted spec")
		}
	}
	if accepted < 100 {
		t.Fatalf("only %d accepted specs; the property is not exercised", accepted)
	}
}

func FuzzParsePublishSpec(f *testing.F) {
	v := validPublish()
	f.Add(string(v.Repo), v.HostInstance, v.Owner, string(v.OwnerKind), v.Name, string(v.Visibility), v.Branch, v.ExpectedHead, v.Marker)
	f.Add("rep_1", "u@GitHub.com/x", "..", "team", "x.git", "", "dev", "abc", "ZZ")
	f.Fuzz(func(t *testing.T, repo, host, owner, kind, name, vis, branch, head, marker string) {
		s := PublishSpec{Repo: core.RepoID(repo), HostInstance: host, Owner: owner, OwnerKind: OwnerKind(kind), Name: name,
			Visibility: Visibility(vis), Branch: branch, ExpectedHead: head, Marker: marker}
		got, err := ParsePublishSpec(s)
		if err != nil {
			if s.Validate() == nil {
				t.Fatalf("Validate accepts what ParsePublishSpec refused")
			}
			return
		}
		checkAcceptedPublish(t, got)
		if got != s {
			t.Fatalf("parse changed an accepted spec")
		}
	})
}

// ---- ParseStage ----

func TestParseStageRejectionRules(t *testing.T) {
	for _, bad := range []string{"", "rm_rf", "CREATE_DIRECTORY", "create_directory ", " init_git", "create-directory", "push_head\x00",
		"create_remote;", "../init_git", "delete_directory", "force_push", "finalize"} {
		if s, err := ParseStage(bad); err == nil {
			t.Errorf("stage %q was accepted as %v", bad, s)
		}
	}
}

func FuzzParseStage(f *testing.F) {
	for _, s := range []string{"create_directory", "push_head", "", "x", "force_push", "create_remote\n"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		st, err := ParseStage(name)
		if err != nil {
			return
		}
		// Round trip: an accepted name is exactly the stage's own name, and the
		// stage is one of the closed sets.
		if st.Name() != name {
			t.Fatalf("ParseStage(%q) = %v", name, st)
		}
		found := false
		for _, s := range InitStages() {
			found = found || Stage(s) == st
		}
		for _, s := range PublishStages() {
			found = found || Stage(s) == st
		}
		if !found {
			t.Fatalf("stage %q is outside the closed sets", name)
		}
	})
}

// ---- ValidateRemoteURL and ValidateReceipt ----

func TestValidateRemoteURLRejectionRules(t *testing.T) {
	rules := map[string][]string{
		"not https":            {"http://git.acme-shop.test/acme/shop-api.git", "ssh://git.acme-shop.test/acme/shop-api.git", "git@git.acme-shop.test:acme/shop-api.git", "/srv/git/shop-api", "", "file:///srv/git"},
		"no path":              {"https://git.acme-shop.test", "https://git.acme-shop.test/"},
		"no host":              {"https:///acme/shop-api.git"},
		"userinfo":             {"https://user:secret@git.acme-shop.test/acme/shop-api.git", "https://tok@git.acme-shop.test/acme/shop-api.git", "https://@git.acme-shop.test/a/b.git"},
		"query":                {"https://git.acme-shop.test/acme/shop-api.git?token=x"},
		"fragment":             {"https://git.acme-shop.test/acme/shop-api.git#x"},
		"percent escapes":      {"https://git.acme-shop.test/acme/shop%2Dapi.git", "https://git.acme-shop.test/%2e%2e/x"},
		"backslash":            {`https://git.acme-shop.test\acme\shop-api.git`},
		"whitespace":           {"https://git.acme-shop.test/acme/shop api.git", "https://git.acme-shop.test/acme/shop-api.git\n", "https://git.acme-shop.test/a\tb"},
		"control character":    {"https://git.acme-shop.test/a\x00b", "https://git.acme-shop.test/a\x7fb"},
		"empty host with port": {"https://:8443/acme/shop-api.git"},
		"bad port":             {"https://git.acme-shop.test:0/a/b.git", "https://git.acme-shop.test:65536/a/b.git", "https://git.acme-shop.test:http/a/b.git", "https://git.acme-shop.test:/a/b.git", "https://git.acme-shop.test:-1/a/b.git"},
	}
	for rule, bad := range rules {
		t.Run(rule, func(t *testing.T) {
			for _, u := range bad {
				if ValidateRemoteURL(u) == nil {
					t.Errorf("%q accepted", u)
				}
			}
		})
	}
	for _, good := range []string{"https://git.acme-shop.test/acme/shop-api.git", "https://github.com/acme/shop-api.git", "https://git.acme-shop.test:8443/acme/shop-api.git"} {
		if err := ValidateRemoteURL(good); err != nil {
			t.Errorf("%q refused: %v", good, err)
		}
	}
}

func FuzzValidateRemoteURL(f *testing.F) {
	for _, s := range []string{"https://git.acme-shop.test/acme/shop-api.git", "https://u:p@h/x", "http://h/x", "https://h:99999/x", "", "https://h/a?b#c"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if ValidateRemoteURL(raw) != nil {
			return
		}
		// Accepted implies a credential-free https URL: nothing in it can carry a token.
		if !strings.HasPrefix(raw, "https://") || strings.ContainsAny(raw, "@?#%\\") {
			t.Fatalf("accepted %q", raw)
		}
		for _, r := range raw {
			if r <= ' ' || r == 0x7f {
				t.Fatalf("accepted %q with a control or space", raw)
			}
		}
		host, rest, ok := strings.Cut(strings.TrimPrefix(raw, "https://"), "/")
		if !ok || host == "" || rest == "" {
			t.Fatalf("accepted %q without a host and path", raw)
		}
	})
}

func TestValidateReceiptRejectionRules(t *testing.T) {
	hex40, hex64 := strings.Repeat("a", 40), strings.Repeat("b", 64)
	good := []Receipt{
		DirReceipt{Identity: "dir:1:2"}, GitReceipt{GitDirIdentity: "git@dir:1:2"},
		FileReceipt{Name: "README.md", ContentHash: hex64}, FileReceipt{Name: ".gitignore", ContentHash: hex64},
		CommitReceipt{Commit: hex40, Tree: hex40, MessageHash: hex64, Author: "A <a@acme-shop.test>"},
		TrackedReceipt{}, RemoteReceipt{RemoteID: "1001", URL: "https://github.com/acme/shop-new.git"},
		OriginReceipt{URL: "https://github.com/acme/shop-new.git"}, PushReceipt{Head: hex40},
	}
	for _, r := range good {
		if err := ValidateReceipt(r); err != nil {
			t.Errorf("%T refused: %v", r, err)
		}
	}
	bad := map[string]Receipt{
		"dir identity empty":     DirReceipt{},
		"dir identity newline":   DirReceipt{Identity: "a\nb"},
		"dir identity too long":  DirReceipt{Identity: strings.Repeat("a", 4097)},
		"git identity empty":     GitReceipt{},
		"file name unexpected":   FileReceipt{Name: "../README.md", ContentHash: hex64},
		"file hash short":        FileReceipt{Name: "README.md", ContentHash: hex40},
		"file hash upper":        FileReceipt{Name: "README.md", ContentHash: strings.Repeat("B", 64)},
		"commit id short":        CommitReceipt{Commit: "abc", Tree: hex40, MessageHash: hex64, Author: "A"},
		"commit tree missing":    CommitReceipt{Commit: hex40, MessageHash: hex64, Author: "A"},
		"commit message hash 40": CommitReceipt{Commit: hex40, Tree: hex40, MessageHash: hex40, Author: "A"},
		"commit author empty":    CommitReceipt{Commit: hex40, Tree: hex40, MessageHash: hex64},
		"commit author newline":  CommitReceipt{Commit: hex40, Tree: hex40, MessageHash: hex64, Author: "A\nB"},
		"remote id empty":        RemoteReceipt{URL: "https://github.com/acme/x.git"},
		"remote url token":       RemoteReceipt{RemoteID: "1", URL: "https://tok@github.com/acme/x.git"},
		"origin url http":        OriginReceipt{URL: "http://github.com/acme/x.git"},
		"origin url empty":       OriginReceipt{},
		"push head empty":        PushReceipt{},
		"push head 41":           PushReceipt{Head: strings.Repeat("a", 41)},
		"nil receipt":            nil,
	}
	for name, r := range bad {
		if err := ValidateReceipt(r); err == nil {
			t.Errorf("%s: %T accepted", name, r)
		}
	}
}

func FuzzValidateReceipt(f *testing.F) {
	f.Add("x", "README.md", strings.Repeat("a", 64), "https://github.com/acme/x.git")
	f.Add("", "../x", "zz", "https://t@h/x")
	f.Fuzz(func(t *testing.T, id, name, hash, url string) {
		for _, r := range []Receipt{
			DirReceipt{Identity: id}, GitReceipt{GitDirIdentity: id}, FileReceipt{Name: name, ContentHash: hash},
			CommitReceipt{Commit: hash, Tree: hash, MessageHash: hash, Author: id}, RemoteReceipt{RemoteID: id, URL: url},
			OriginReceipt{URL: url}, PushReceipt{Head: hash},
		} {
			if ValidateReceipt(r) != nil {
				continue
			}
			switch v := r.(type) {
			case RemoteReceipt:
				if strings.ContainsAny(v.URL, "@?#") || !strings.HasPrefix(v.URL, "https://") {
					t.Fatalf("accepted receipt URL %q", v.URL)
				}
			case OriginReceipt:
				if strings.ContainsAny(v.URL, "@?#") || !strings.HasPrefix(v.URL, "https://") {
					t.Fatalf("accepted receipt URL %q", v.URL)
				}
			case FileReceipt:
				if v.Name != "README.md" && v.Name != ".gitignore" {
					t.Fatalf("accepted file receipt name %q", v.Name)
				}
			case DirReceipt:
				if v.Identity == "" || strings.ContainsAny(v.Identity, "\x00\r\n") {
					t.Fatalf("accepted identity %q", v.Identity)
				}
			}
		}
	})
}
