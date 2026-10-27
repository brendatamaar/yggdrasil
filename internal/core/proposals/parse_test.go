package proposals

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

const (
	h40 = "0123456789abcdef0123456789abcdef01234567"
	h64 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

// valid argument objects, one per kind.
var validArgs = map[string]string{
	"commit":        `{"worktree":"w1","paths":["src/a.go","docs/b c.md"],"message":"fix it","expect_head":"` + h40 + `","expect_diff_hash":"` + h64 + `"}`,
	"push":          `{"worktree":"w1","branch":"ygg/swift-fern","remote":"origin","expect_head":"` + h40 + `"}`,
	"merge_request": `{"worktree":"w1","branch":"ygg/swift-fern","remote":"origin","target":"main","title":"Add thing","description":"one line","expect_head":"` + h40 + `"}`,
	"agent_file":    `{"worktree":"w1","path":".claude/settings.json","content":"{}","expect_hash":"` + h64 + `"}`,
	"delete_files":  `{"repo":"r1","paths":["old/a.txt"]}`,
	"install":       `{"repo":"r1","step":"install"}`,
}

func TestParseActionAcceptsValid(t *testing.T) {
	for kind, args := range validArgs {
		a, err := ParseAction(kind, []byte(args))
		if err != nil {
			t.Errorf("%s: %v", kind, err)
			continue
		}
		if string(a.Kind()) != kind {
			t.Errorf("%s: kind %s", kind, a.Kind())
		}
	}
}

// mod rewrites a valid object of a kind, to build one rejection per rule.
func mod(t *testing.T, kind string, f func(m map[string]any)) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(validArgs[kind]), &m); err != nil {
		t.Fatal(err)
	}
	f(m)
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func set(k string, v any) func(map[string]any) { return func(m map[string]any) { m[k] = v } }

func TestParseActionRejectionRules(t *testing.T) {
	long := strings.Repeat("x", 300)
	manyPaths := make([]string, MaxPaths+1)
	for i := range manyPaths {
		manyPaths[i] = fmt.Sprintf("f%d.txt", i)
	}
	tests := []struct {
		name string
		kind string
		f    func(map[string]any)
		want string // substring of the plain-language error
	}{
		{"unknown field", "commit", set("force", true), "unknown field"},
		{"unknown field on push", "push", set("delete", true), "unknown field"},
		{"missing target", "commit", func(m map[string]any) { delete(m, "worktree") }, "say where to act"},
		{"both targets", "commit", set("repo", "r1"), "not both"},
		{"bad worktree id", "commit", set("worktree", "a b"), "worktree id"},
		{"no paths", "commit", set("paths", []string{}), "no paths"},
		{"absolute path", "commit", set("paths", []string{"/etc/passwd"}), "absolute"},
		{"windows drive path", "commit", set("paths", []string{`C:\Users\x`}), "backslash"},
		{"drive with slash", "commit", set("paths", []string{"C:/Users/x"}), "drive or stream"},
		{"drive relative", "commit", set("paths", []string{"C:foo"}), "drive or stream"},
		{"dotdot", "commit", set("paths", []string{"a/../../b"}), "leaves the worktree"},
		{"leading dotdot", "commit", set("paths", []string{"../b"}), "leaves the worktree"},
		{"backslash dotdot", "commit", set("paths", []string{`..\b`}), "backslash"},
		{"dot segment", "commit", set("paths", []string{"./a"}), "repeats itself"},
		{"empty path", "commit", set("paths", []string{""}), "empty"},
		{"empty segment", "commit", set("paths", []string{"a//b"}), "empty part"},
		{"trailing slash", "commit", set("paths", []string{"a/"}), "empty part"},
		{"stream", "commit", set("paths", []string{"a.txt:hidden"}), "drive or stream"},
		{"device CON", "commit", set("paths", []string{"CON"}), "device"},
		{"device with ext", "commit", set("paths", []string{"src/nul.txt"}), "device"},
		{"device COM1 lower", "commit", set("paths", []string{"com1"}), "device"},
		{"device LPT9", "commit", set("paths", []string{"a/LPT9.log"}), "device"},
		{"trailing dot", "commit", set("paths", []string{"a."}), "dot or space"},
		{"dot git", "commit", set("paths", []string{".git/config"}), "inside .git"},
		{"dot GIT upper", "commit", set("paths", []string{"sub/.GIT/hooks/x"}), "inside .git"},
		{"windows reserved char", "commit", set("paths", []string{"a?b"}), "Windows does not allow"},
		{"control char", "commit", set("paths", []string{"a\x01b"}), "not allowed"},
		{"duplicate path", "commit", set("paths", []string{"a.txt", "A.TXT"}), "twice"},
		{"too many paths", "commit", set("paths", manyPaths), "at most"},
		{"empty message", "commit", set("message", "  \n"), "message is empty"},
		{"message too long", "commit", set("message", strings.Repeat("m", MaxMessageSize+1)), "message is over"},
		{"head not hex", "commit", set("expect_head", "HEAD"), "expect_head"},
		{"diff hash missing", "commit", set("expect_diff_hash", ""), "expect_diff_hash"},
		{"branch empty", "push", set("branch", ""), "branch name"},
		{"branch option", "push", set("branch", "--force"), "branch name"},
		{"branch dash", "push", set("branch", "-x"), "branch name"},
		{"branch space", "push", set("branch", "a b"), "branch name"},
		{"branch colon (refspec)", "push", set("branch", "a:b"), "branch name"},
		{"branch plus", "push", set("branch", "+a"), "branch name"},
		{"branch dotdot", "push", set("branch", "a..b"), "branch name"},
		{"branch lock", "push", set("branch", "a.lock"), "branch name"},
		{"branch lock in dir", "push", set("branch", "a.lock/b"), "branch name"},
		{"branch hidden component", "push", set("branch", "a/.b"), "branch name"},
		{"branch ends slash", "push", set("branch", "a/"), "branch name"},
		{"branch at brace", "push", set("branch", "a@{b"), "branch name"},
		{"branch at", "push", set("branch", "@"), "branch name"},
		{"branch tilde", "push", set("branch", "a~1"), "branch name"},
		{"branch backslash", "push", set("branch", `a\b`), "branch name"},
		{"branch too long", "push", set("branch", long+long), "branch name"},
		{"remote url", "push", set("remote", "https://x@host/r.git"), "remote name"},
		{"remote option", "push", set("remote", "--mirror"), "remote name"},
		{"remote space", "push", set("remote", "or igin"), "remote name"},
		{"remote empty", "push", set("remote", ""), "remote name"},
		{"push head missing", "push", set("expect_head", ""), "expect_head"},
		{"merge request head missing", "merge_request", set("expect_head", ""), "expect_head"},
		{"commit head missing", "commit", set("expect_head", ""), "expect_head"},
		{"target branch bad", "merge_request", set("target", "-m"), "branch name"},
		{"title empty", "merge_request", set("title", ""), "title is empty"},
		{"title too long", "merge_request", set("title", long), "title is over"},
		{"title newline", "merge_request", set("title", "a\nb"), "one line"},
		{"description newline", "merge_request", set("description", "a\nb"), "one line"},
		{"description CR", "merge_request", set("description", "a\rb"), "one line"},
		{"agent file not protected", "agent_file", set("path", "src/main.go"), "not an agent configuration"},
		{"agent file dir only", "agent_file", set("path", ".claude"), "not an agent configuration"},
		{"agent file dir slash", "agent_file", set("path", ".claude/"), "empty part"},
		{"agent file escape", "agent_file", set("path", ".claude/../x"), "leaves the worktree"},
		{"agent file content too big", "agent_file", set("content", strings.Repeat("c", MaxContentSize+1)), "content is over"},
		{"agent file bad hash", "agent_file", set("expect_hash", "xyz"), "expect_hash"},
		{"delete absolute", "delete_files", set("paths", []string{"/x"}), "absolute"},
		{"delete dotdot", "delete_files", set("paths", []string{"../x"}), "leaves the worktree"},
		{"install bad repo", "install", set("repo", ""), "repo id"},
		{"install option step", "install", set("step", "--ignore-scripts"), "install step"},
		{"install space step", "install", set("step", "a b"), "install step"},
		{"install with worktree", "install", set("worktree", "w1"), "unknown field"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseAction(tc.kind, mod(t, tc.kind, tc.f))
			if err == nil {
				t.Fatal("accepted")
			}
			if !errors.Is(err, ErrInvalidAction) {
				t.Fatalf("error does not wrap ErrInvalidAction: %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
			// the stored-row path has the same rules
			if _, err := ActionFromRow(core.ProposalKind(tc.kind), mod(t, tc.kind, tc.f)); err == nil {
				t.Error("ActionFromRow accepted what ParseAction rejects")
			}
		})
	}
}

func TestParseActionAcceptsEdgeCases(t *testing.T) {
	ok := []struct {
		kind string
		f    func(map[string]any)
	}{
		{"commit", set("paths", []string{"a b/c d.txt", "dir/.gitignore", "dir/.github/x.yml", "ünï/çode.txt"})},
		{"commit", set("paths", []string{"CONSOLE.txt", "nulls/x", "com10", "COM", "a.b.c"})},
		{"commit", set("message", strings.Repeat("m", MaxMessageSize))},
		{"push", set("branch", "feature/a-b_c.d")},
		{"push", set("remote", "my.remote_1-x")},
		{"merge_request", set("description", "")},
		{"merge_request", set("title", strings.Repeat("t", MaxTitleLen))},
		{"agent_file", set("path", ".opencode/agents/x.md")},
		{"agent_file", set("path", "claude.MD")},
		{"agent_file", set("path", "GEMINI.md")},
		{"agent_file", set("path", "opencode.json")},
		{"agent_file", set("content", strings.Repeat("c", MaxContentSize))},
		{"agent_file", func(m map[string]any) { delete(m, "expect_hash") }},
	}
	for i, tc := range ok {
		if _, err := ParseAction(tc.kind, mod(t, tc.kind, tc.f)); err != nil {
			t.Errorf("case %d (%s): err = %v", i, tc.kind, err)
		}
	}
	paths := make([]string, MaxPaths)
	for i := range paths {
		paths[i] = fmt.Sprintf("f%d.txt", i)
	}
	if _, err := ParseAction("commit", mod(t, "commit", set("paths", paths))); err != nil {
		t.Errorf("exactly %d paths rejected: %v", MaxPaths, err)
	}
}

func TestParseActionRejectsBadJSON(t *testing.T) {
	for name, in := range map[string]string{
		"empty":      ``,
		"not object": `[]`,
		"null":       `null`,
		"trailing":   validArgs["install"] + ` {}`,
		"wrong type": `{"repo":1,"step":"i"}`,
		"truncated":  `{"repo":"r1"`,
	} {
		if _, err := ParseAction("install", []byte(in)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestParseActionRefusesUnavailableKinds(t *testing.T) {
	for _, k := range []string{"publish", "subtasks", "", "force_push", "reset", "Commit"} {
		if _, err := ParseAction(k, []byte(`{}`)); !errors.Is(err, ErrUnsupportedKind) {
			t.Errorf("ParseAction(%q) = %v, want ErrUnsupportedKind", k, err)
		}
		if _, err := ActionFromRow(core.ProposalKind(k), []byte(`{}`)); !errors.Is(err, ErrUnsupportedKind) {
			t.Errorf("ActionFromRow(%q) = %v, want ErrUnsupportedKind", k, err)
		}
	}
}

func TestActionFromRowKindPayloadMismatch(t *testing.T) {
	for kind, args := range validArgs {
		for other := range validArgs {
			if other == kind {
				continue
			}
			if _, err := ActionFromRow(core.ProposalKind(other), []byte(args)); err == nil {
				t.Errorf("payload of %s accepted as %s", kind, other)
			}
		}
	}
}

func TestRowRoundTrip(t *testing.T) {
	for kind, args := range validArgs {
		a, err := ParseAction(kind, []byte(args))
		if err != nil {
			t.Fatal(err)
		}
		k, payload := RowOf(a)
		if k != a.Kind() || len(payload) == 0 {
			t.Fatalf("%s: RowOf = %s %q", kind, k, payload)
		}
		back, err := ActionFromRow(k, payload)
		if err != nil || !reflect.DeepEqual(back, a) {
			t.Errorf("%s: round trip = %#v, %v; want %#v", kind, back, err, a)
		}
	}
	if k, p := RowOf(nil); k != "" || p != nil {
		t.Error("RowOf(nil)")
	}
}

func TestPropRowRoundTripAndAcceptedImpliesValid(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		a := genAction(r)
		k, payload := RowOf(a)
		back, err := ActionFromRow(k, payload)
		if err != nil || !reflect.DeepEqual(back, a) {
			t.Fatalf("round trip of %#v = %#v, %v", a, back, err)
		}
		if Hash(back) != Hash(a) {
			t.Fatal("hash differs after round trip")
		}
		checkValid(t, back)
		// the same payload through the MCP boundary
		viaMCP, err := ParseAction(string(k), payload)
		if err != nil || !reflect.DeepEqual(viaMCP, a) {
			t.Fatalf("ParseAction(RowOf) = %#v, %v", viaMCP, err)
		}
	})
}

// checkValid re-checks, with independent code, what an accepted action must satisfy.
func checkValid(t *testing.T, a Action) {
	t.Helper()
	badPath := func(p RelPath) bool {
		s := string(p)
		if s == "" || strings.HasPrefix(s, "/") || strings.ContainsAny(s, `\:`) {
			return true
		}
		for _, seg := range strings.Split(s, "/") {
			if seg == ".." || seg == "." || seg == "" {
				return true
			}
		}
		return false
	}
	switch v := a.(type) {
	case Commit:
		if len(v.Paths) == 0 || len(v.Paths) > MaxPaths || strings.TrimSpace(v.Message) == "" || len(v.Message) > MaxMessageSize {
			t.Errorf("invalid commit accepted: %#v", v)
		}
		for _, p := range v.Paths {
			if badPath(p) {
				t.Errorf("bad path %q accepted", p)
			}
		}
	case DeleteFiles:
		for _, p := range v.Paths {
			if badPath(p) {
				t.Errorf("bad path %q accepted", p)
			}
		}
	case Push:
		if strings.HasPrefix(v.Branch, "-") || strings.HasPrefix(v.Remote, "-") {
			t.Errorf("option-like name accepted: %#v", v)
		}
	case MergeRequest:
		if strings.ContainsAny(v.Description, "\r\n") || strings.ContainsAny(v.Title, "\r\n") {
			t.Errorf("multi-line text accepted: %#v", v)
		}
	case AgentFile:
		if !IsProtectedPath(v.Path) || badPath(v.Path) {
			t.Errorf("unprotected agent file accepted: %q", v.Path)
		}
	}
}

func TestIsProtectedPath(t *testing.T) {
	for p, want := range map[string]bool{
		".claude/settings.json": true, ".claude/a/b": true, ".mcp.json": true, "CLAUDE.md": true,
		"AGENTS.md": true, "opencode.json": true, ".opencode/x": true, "GEMINI.md": true,
		"claude.md": true, ".CLAUDE/x": true,
		".claude": true, ".claude/": true, "src/CLAUDE.md": true, "README.md": false, "claudex/a": false,
	} {
		if got := IsProtectedPath(RelPath(p)); got != want {
			t.Errorf("IsProtectedPath(%q) = %v", p, got)
		}
	}
}

func TestCheckBranchAndRemoteName(t *testing.T) {
	for _, b := range []string{"main", "a/b/c", "feat-1", "v1.2.3", "ünï"} {
		if err := CheckBranch(b); err != nil {
			t.Errorf("CheckBranch(%q) = %v", b, err)
		}
	}
	for _, r := range []string{"origin", "up-stream", "a.b", "A_1"} {
		if err := CheckRemoteName(r); err != nil {
			t.Errorf("CheckRemoteName(%q) = %v", r, err)
		}
	}
}
