package mcp_test

import (
	"strings"
	"testing"

	cmcp "github.com/brendatamaar/yggdrasil/internal/core/mcp"
	cprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
)

// The Phase 6 proposal tools copy more rules (ignore entries, comment bodies,
// commit IDs, host and account names). Same method as drift_test.go: feed both
// sides and fail if one accepts what the other refuses.

var driftIgnoreEntries = []string{
	"", "/", "//", "/a/", "/a", "a/", "/a b/", "/ a/", "/a /", "/a./", "/.a/", "/.git/", "/.GIT/", "/a/.git/", "/a/../b/",
	"/./a/", "/a//b/", "/*/", "/a*/", "/a?/", "/a[b]/", "/!a/", "/a!/", "/#a/", "/a#/", `/a\*/`, `/a\#/`, `/a\/`, `/a\\/`,
	`/\*/`, "/C:/", "/a:b/", "/a<b/", `/a"b/`, "/a|b/", "/a\x00b/", "/a\nb/", "/a\tb/", "/a\x7f/", "/\xff/", "/ünï/",
	"/web/node_modules/", "/dist/", "/build/out/", "/" + strings.Repeat("a", 509) + "/", "/" + strings.Repeat("a", 510) + "/",
	"/CON/", "/a~1/", `/\ x/`, "/a/b/c/d/", "/%/", "/@x/", "/-x/", "/x-/",
}

var driftHeads = []string{
	"", "abc", "main", strings.Repeat("a", 39), strings.Repeat("a", 40), strings.Repeat("a", 41), strings.Repeat("a", 63),
	strings.Repeat("a", 64), strings.Repeat("a", 65), strings.Repeat("A", 40), strings.Repeat("g", 40), strings.Repeat("0", 40),
	strings.Repeat("a", 39) + "\n", " " + strings.Repeat("a", 39), strings.Repeat("a", 39) + "\x00", strings.Repeat("é", 20),
}

var driftBodies = []string{
	"", " ", "\n", "\t\r\n", "x", "line1\nline2", "tab\there", "cr\r\nlf", "bell\a", "nul\x00", "esc\x1b[0m", "del\x7f",
	"\xff", "ünï çödé", "emoji \U0001F600", "\u2028", "\u0085", strings.Repeat("b", 16<<10), strings.Repeat("b", 16<<10+1),
	strings.Repeat("é", 8<<10), strings.Repeat("é", 8<<10+1),
}

var driftAccounts = []string{
	"", "a", "A", "a-b", "a_b", "a.b", ".a", "-a", "a.git", "a.GIT", ".git", "git", "a/b", `a\b`, "a b", "a@b", "a:b", "é", "a\x00",
	"a\n", strings.Repeat("a", 100), strings.Repeat("a", 101), "a..b", "a--", "_", "0", "A.git.x", "a%b", "a~b",
}

var driftHosts = []string{
	"", "git.acme.dev", "GIT.acme.dev", "github.com", "a", "a b", "a/b", `a\b`, "a@b", "u@h", "h:22", "h:", "é", "É", "a\x00", "a\n",
	"a\x7f", " a", "a ", "https://h", "h.", ".h", "a_b", "a-b", "[::1]", "1.2.3.4", strings.Repeat("a", 255),
}

const driftMarker = "0123456789abcdef0123456789abcdef"

func mcpIgnoreErr(e string) error {
	return cmcp.CheckCall(cmcp.ProposeIgnore{Repo: "shop-api", Entries: []string{e}})
}

func coreIgnoreErr(e string) error {
	return cprop.IgnoreEntries{Where: cprop.MainCheckout{Repo: "rep_1"}, Entries: []string{e}, ExpectedAbsent: true}.Validate()
}

func mcpHeadErr(h string) error {
	return cmcp.CheckCall(cmcp.ProposeReviewComment{Repo: "shop-api", MR: 1, ExpectedHead: h, Body: "x"})
}

func coreHeadErr(h string) error {
	return cprop.ReviewComment{Repo: "rep_1", MRNumber: 1, ExpectedHead: h, Body: "x", Marker: driftMarker}.Validate()
}

func mcpBodyErr(b string) error {
	return cmcp.CheckCall(cmcp.ProposeReviewComment{Repo: "shop-api", MR: 1, ExpectedHead: strings.Repeat("a", 40), Body: b})
}

func coreBodyErr(b string) error {
	return cprop.ReviewComment{Repo: "rep_1", MRNumber: 1, ExpectedHead: strings.Repeat("a", 40), Body: b, Marker: driftMarker}.Validate()
}

func publishCore(host, owner, name string) cprop.PublishRepo {
	return cprop.PublishRepo{Operation: "op_1", Repo: "rep_1", HostInstance: host, Owner: owner, OwnerKind: "org", Name: name,
		Visibility: "private", Branch: "main", ExpectedHead: strings.Repeat("a", 40), Marker: driftMarker}
}

func mcpPublish(host, owner, name string) cmcp.ProposePublish {
	return cmcp.ProposePublish{Repo: "shop-api", Host: host, Owner: owner, OwnerKind: "org", Name: name, Visibility: "private"}
}

func TestDriftIgnoreRulesAgree(t *testing.T) {
	for _, e := range driftIgnoreEntries {
		agree(t, "ignore entry", e, mcpIgnoreErr(e), cprop.CheckIgnoreEntry(e))
		agree(t, "ignore entry (validate)", e, mcpIgnoreErr(e), coreIgnoreErr(e))
	}
}

func TestDriftCommentRulesAgree(t *testing.T) {
	for _, h := range driftHeads {
		agree(t, "head", h, mcpHeadErr(h), coreHeadErr(h))
	}
	for _, b := range driftBodies {
		agree(t, "comment body", b, mcpBodyErr(b), coreBodyErr(b))
	}
}

func TestDriftPublishRulesAgree(t *testing.T) {
	for _, a := range driftAccounts {
		agree(t, "owner", a, cmcp.CheckCall(mcpPublish("git.acme.dev", a, "n")), publishCore("git.acme.dev", a, "n").Validate())
		agree(t, "repository name", a, cmcp.CheckCall(mcpPublish("git.acme.dev", "o", a)), publishCore("git.acme.dev", "o", a).Validate())
	}
	for _, h := range driftHosts {
		agree(t, "host", h, cmcp.CheckCall(mcpPublish(h, "o", "n")), publishCore(h, "o", "n").Validate())
	}
}

// TestDriftPhase6LimitsAgree pins the shared numeric limits.
func TestDriftPhase6LimitsAgree(t *testing.T) {
	if cmcp.MaxIgnoreEntries != cprop.MaxIgnoreEntries || cmcp.MaxIgnoreBytes != cprop.MaxIgnoreBytes || cmcp.MaxCommentBytes != cprop.MaxCommentSize {
		t.Fatalf("limits drifted: mcp %d/%d/%d proposals %d/%d/%d", cmcp.MaxIgnoreEntries, cmcp.MaxIgnoreBytes, cmcp.MaxCommentBytes,
			cprop.MaxIgnoreEntries, cprop.MaxIgnoreBytes, cprop.MaxCommentSize)
	}
}

func FuzzDriftIgnoreEntry(f *testing.F) {
	for _, e := range driftIgnoreEntries {
		f.Add(e)
	}
	f.Fuzz(func(t *testing.T, e string) { agree(t, "ignore entry", e, mcpIgnoreErr(e), cprop.CheckIgnoreEntry(e)) })
}

func FuzzDriftAccount(f *testing.F) {
	for _, a := range driftAccounts {
		f.Add(a)
	}
	f.Fuzz(func(t *testing.T, a string) {
		agree(t, "owner", a, cmcp.CheckCall(mcpPublish("git.acme.dev", a, "n")), publishCore("git.acme.dev", a, "n").Validate())
	})
}

func FuzzDriftHost(f *testing.F) {
	for _, h := range driftHosts {
		f.Add(h)
	}
	f.Fuzz(func(t *testing.T, h string) {
		if len(h) > 255 {
			t.Skip() // the early check bounds the length; the proposals check does not
		}
		agree(t, "host", h, cmcp.CheckCall(mcpPublish(h, "o", "n")), publishCore(h, "o", "n").Validate())
	})
}
