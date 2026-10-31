package mcp_test

import (
	"strings"
	"testing"

	cmcp "github.com/brendatamaar/yggdrasil/internal/core/mcp"
	cprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
)

// The MCP argument checks (core/mcp/check.go) are a copy of the proposals
// boundary rules (core/proposals/validate.go) so an agent gets an early plain
// message. A copy can drift; these tests feed the same inputs to both and fail
// if one accepts what the other refuses (a weaker copy would let the early
// check pass a value the later one only catches by luck, or the reverse would
// make a tool unusable).

var driftPaths = []string{
	"", "a", "src/a.go", "src/a b/c.txt", "a/../b", "..", ".", "./a", "a//b", "/abs", "C:/x", "C:x", `a\b`,
	"a:b", "a:stream", "a.", "a ", "dir /a", "x/NUL", "nul.txt", "con", "COM1", "com9.log", "LPT0", "COM10",
	"CONIN$", "clock$", "GIT~1/config", "CLAUDE~1", "a~b", ".git/config", ".GIT/x", "x/.Git", ".gitignore",
	".gitx", "a\x00b", "a\nb", "a\tb", "a\x7fb", "a<b", "a>b", `a"b`, "a|b", "a?b", "a*b", "\xff\xfe",
	"ünï/çödé.txt", "K.git", "\u212a/x", ".git ", ".git.", strings.Repeat("a/", 300), "//server/share", "?/x",
	"a/b/", "/", "~", "x/~1", "x/a~1.txt", "x/a~10", "CON .txt", "aux.", "PRN",
}

var driftBranches = []string{
	"", "main", "feature/x", "ygg/cart-1", "-f", "--force", "+main", "+refs/heads/x", "/x", "x/", "x.", "x.lock",
	"a/.hidden", "a/b.lock/c", "a..b", "a//b", "a@{b", "@", "a b", "a~1", "a^", "a:b", "a?b", "a*b", "a[b", `a\b`,
	"a\x00b", "a\nb", "\xff", strings.Repeat("a", 255), strings.Repeat("a", 256), ".x", "x/.y", "HEAD", "refs/heads/main",
	"feature/ü", "a.lock", "a/.lock", "é",
}

var driftRemotes = []string{
	"", "origin", "up-stream", "a.b_c", "-o", "--upload-pack=x", "a b", "a/b", "a:b", "a\x00", "é", "ORIGIN9",
	strings.Repeat("r", 100), strings.Repeat("r", 101), "https://example.invalid/x.git", "a@b", "a\n",
}

func mcpPathErr(p string) error {
	return cmcp.CheckCall(cmcp.ProposeDeleteFiles{Repo: "shop-api", Paths: []string{p}})
}

func mcpBranchErr(b string) error {
	return cmcp.CheckCall(cmcp.ProposePush{Repo: "shop-api", Branch: b, Remote: "origin"})
}

func mcpRemoteErr(r string) error {
	return cmcp.CheckCall(cmcp.ProposePush{Repo: "shop-api", Branch: "main", Remote: r})
}

func agree(t *testing.T, what, in string, a, b error) {
	t.Helper()
	if (a == nil) != (b == nil) {
		t.Fatalf("%s drift on %q: core/mcp err=%v, core/proposals err=%v", what, in, a, b)
	}
}

func TestDriftPathRulesAgree(t *testing.T) {
	for _, p := range driftPaths {
		agree(t, "path", p, mcpPathErr(p), cprop.CheckRelPath(p))
	}
}

func TestDriftBranchRulesAgree(t *testing.T) {
	for _, b := range driftBranches {
		agree(t, "branch", b, mcpBranchErr(b), cprop.CheckBranch(b))
	}
}

func TestDriftRemoteRulesAgree(t *testing.T) {
	for _, r := range driftRemotes {
		agree(t, "remote", r, mcpRemoteErr(r), cprop.CheckRemoteName(r))
	}
}

// TestDriftLimitsAgree pins the shared numeric limits.
func TestDriftLimitsAgree(t *testing.T) {
	if cmcp.MaxPaths != cprop.MaxPaths || cmcp.MaxTitleLen != cprop.MaxTitleLen ||
		cmcp.MaxMessageBytes != cprop.MaxMessageSize || cmcp.MaxContentBytes != cprop.MaxContentSize {
		t.Fatalf("limits drifted: mcp %d/%d/%d/%d proposals %d/%d/%d/%d",
			cmcp.MaxPaths, cmcp.MaxTitleLen, cmcp.MaxMessageBytes, cmcp.MaxContentBytes,
			cprop.MaxPaths, cprop.MaxTitleLen, cprop.MaxMessageSize, cprop.MaxContentSize)
	}
}

func FuzzDriftPath(f *testing.F) {
	for _, p := range driftPaths {
		f.Add(p)
	}
	f.Fuzz(func(t *testing.T, p string) { agree(t, "path", p, mcpPathErr(p), cprop.CheckRelPath(p)) })
}

func FuzzDriftBranch(f *testing.F) {
	for _, b := range driftBranches {
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b string) { agree(t, "branch", b, mcpBranchErr(b), cprop.CheckBranch(b)) })
}

func FuzzDriftRemote(f *testing.F) {
	for _, r := range driftRemotes {
		f.Add(r)
	}
	f.Fuzz(func(t *testing.T, r string) { agree(t, "remote", r, mcpRemoteErr(r), cprop.CheckRemoteName(r)) })
}
