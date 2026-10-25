package proposals

import (
	"errors"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core/guard"
)

// M1: 8.3 short names (GIT~1, CLAUDE~1) can hide .git and agent config.
func TestCheckRelPathRefusesShortNames(t *testing.T) {
	for _, p := range []string{
		"GIT~1", "git~1/config", "a/GIT~1/hooks/x", "CLAUDE~1", "CLAUDE~1.MD", ".CLAUDE~1/x", "x~12.txt", "~1", "a/~9",
		"PROGRA~1/x", "a~1b",
	} {
		if err := CheckRelPath(p); err == nil || !errors.Is(err, ErrInvalidAction) {
			t.Errorf("CheckRelPath(%q) = %v", p, err)
		}
	}
	for _, p := range []string{"~foo", "a~b", "x~", "a/~", "tilde~.txt", "src/main.go", "a~/b"} {
		if err := CheckRelPath(p); err != nil {
			t.Errorf("CheckRelPath(%q) = %v", p, err)
		}
	}
}

func TestIsProtectedPathAtAnyDepth(t *testing.T) {
	for p, want := range map[string]bool{
		"sub/CLAUDE.md": true, "pkg/.claude/x": true, "pkg/.mcp.json": true, "a/b/c/AGENTS.md": true,
		"a/.opencode/x/y": true, "x/GEMINI.md": true, "x/opencode.json": true,
		".codex/config.toml": true, "p/.gemini/settings.json": true, ".cursor/rules/a": true, "CLAUDE.local.md": true,
		"deep/claude.LOCAL.md": true, ".gitmodules": true, "p/.gitmodules": true, ".husky/pre-commit": true, "q/.githooks/x": true,
		// a bare folder name is protected too: committing it stages everything below
		".claude": true, "pkg/.husky": true,
		"README.md": false, "src/main.go": false, "claudex/a": false, "docs/CLAUDE.md.bak": false, "gitmodules.md": false,
		".claudex/a": false, "x/.codexx": false,
	} {
		if got := IsProtectedPath(RelPath(p)); got != want {
			t.Errorf("IsProtectedPath(%q) = %v, want %v", p, got, want)
		}
	}
}

// The guard (writes through tools) and the proposals core (what an
// agent_file proposal may name) must protect the same names.
func TestProtectedListsMatchTheGuard(t *testing.T) {
	sorted := func(s []string) []string {
		out := append([]string(nil), s...)
		for i := range out {
			out[i] = strings.ToLower(out[i])
		}
		sort.Strings(out)
		return out
	}
	// ".git" is refused by CheckRelPath, not by IsProtectedPath.
	dirs := append([]string(nil), protectedDirNames...)
	guardDirs := guard.ProtectedDirNames()
	var guardNoGit []string
	for _, d := range guardDirs {
		if d != ".git" {
			guardNoGit = append(guardNoGit, d)
		}
	}
	if !reflect.DeepEqual(sorted(dirs), sorted(guardNoGit)) {
		t.Errorf("protected folders differ:\n proposals %v\n guard     %v", sorted(dirs), sorted(guardNoGit))
	}
	if !reflect.DeepEqual(sorted(protectedFileNames), sorted(guard.ProtectedFileNames())) {
		t.Errorf("protected files differ:\n proposals %v\n guard     %v", sorted(protectedFileNames), sorted(guard.ProtectedFileNames()))
	}
	// and every guard name is protected in IsProtectedPath at any depth
	for _, n := range guard.ProtectedFileNames() {
		for _, p := range []string{n, "a/" + n, "a/b/" + strings.ToUpper(n)} {
			if !IsProtectedPath(RelPath(p)) {
				t.Errorf("guard protects %q but IsProtectedPath does not", p)
			}
		}
	}
	for _, n := range guardNoGit {
		for _, p := range []string{n + "/x", "a/" + n + "/x/y", "a/" + strings.ToUpper(n) + "/x"} {
			if !IsProtectedPath(RelPath(p)) {
				t.Errorf("guard protects %q but IsProtectedPath does not", p)
			}
		}
	}
}

func TestAgentFileOnlyForProtectedFiles(t *testing.T) {
	ok := func(p string) bool {
		_, err := ParseAction("agent_file", []byte(`{"repo":"r1","path":"`+p+`","content":"x"}`))
		return err == nil
	}
	for p, want := range map[string]bool{
		"CLAUDE.md": true, "pkg/CLAUDE.md": true, ".claude/settings.json": true, ".codex/config.toml": true, "CLAUDE.local.md": true,
		".claude": false, "README.md": false, "src/a.go": false, "CLAUDE~1.MD": false,
	} {
		if got := ok(p); got != want {
			t.Errorf("agent_file path %q accepted=%v, want %v", p, got, want)
		}
	}
}

// Should-fix: a leading "-" in the ssh user or host can become an option.
func TestCheckRemoteRefusesLeadingDash(t *testing.T) {
	for _, in := range []string{
		"ssh://-oProxyCommand=evil@host/x.git", "ssh://git@-oProxyCommand/x", "ssh://-v@host/x",
		"-oProxyCommand=evil@host:x.git", "git@-oBad:x.git", "--x@host:path", "ssh://git@-host/x", "https://-host/x.git",
		"http://-a.example/x", "git@-host:x.git",
	} {
		if _, err := CheckRemote(in); !errors.Is(err, ErrUnsafeURL) {
			t.Errorf("CheckRemote(%q) err = %v", in, err)
		}
	}
	for _, in := range []string{"ssh://git@gl-1.example.com/x.git", "git@my-host.example:x.git", "ssh://a-b@h-1/x", "https://a-b.example/x"} {
		if _, err := CheckRemote(in); err != nil {
			t.Errorf("CheckRemote(%q) = %v", in, err)
		}
	}
}

// Should-fix: an MR link is accepted only on the remote's own host.
func TestParsePushOutputChecksTheRemoteHost(t *testing.T) {
	out := func(u string) string { return "remote: View merge request for x:\nremote:   " + u + "\n" }
	for _, c := range []struct {
		name, url, host string
		want            string
		err             error
	}{
		{"same host", "https://gitlab.example.com/a/b/-/merge_requests/1", "gitlab.example.com", "https://gitlab.example.com/a/b/-/merge_requests/1", nil},
		{"case differs", "https://GitLab.Example.com/m/1", "gitlab.example.com", "https://GitLab.Example.com/m/1", nil},
		{"remote has a port, link does not", "https://gitlab.example.com/m/1", "gitlab.example.com:2222", "https://gitlab.example.com/m/1", nil},
		{"link has a port, remote does not", "https://gitlab.example.com:8443/m/1", "gitlab.example.com", "https://gitlab.example.com:8443/m/1", nil},
		{"other host", "https://evil.example.net/m/1", "gitlab.example.com", "", ErrUnsafeURL},
		{"suffix host", "https://gitlab.example.com.evil.net/m/1", "gitlab.example.com", "", ErrUnsafeURL},
		{"prefix host", "https://evilgitlab.example.com/m/1", "gitlab.example.com", "", ErrUnsafeURL},
		{"subdomain", "https://x.gitlab.example.com/m/1", "gitlab.example.com", "", ErrUnsafeURL},
		{"no remote host", "https://gitlab.example.com/m/1", "", "", ErrUnsafeURL},
		{"ipv6 same", "http://[::1]:80/m/1", "[::1]:3000", "http://[::1]:80/m/1", nil},
		{"ipv6 other", "http://[::2]/m/1", "[::1]", "", ErrUnsafeURL},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, err := ParsePushOutput(out(c.url), c.host)
			if c.err != nil {
				if !errors.Is(err, c.err) || r.MRURL != "" {
					t.Fatalf("got %+v, %v", r, err)
				}
				return
			}
			if err != nil || r.MRURL != c.want {
				t.Fatalf("got %+v, %v", r, err)
			}
		})
	}
	if _, err := ParsePushOutput("remote: nothing\n", "gitlab.example.com"); !errors.Is(err, ErrNoMRURL) {
		t.Errorf("no link: %v", err)
	}
}

// Should-fix: Step re-validates the action, because an exported struct can be
// built without ParseAction.
func TestStepCreatedRevalidatesTheAction(t *testing.T) {
	bad := map[string]Action{
		"commit with ..":         Commit{Where: InWorktree{ID: "w1"}, Paths: []RelPath{"../x"}, Message: "m", ExpectDiffHash: "abcdef0"},
		"commit with short name": Commit{Where: InWorktree{ID: "w1"}, Paths: []RelPath{"GIT~1/config"}, Message: "m", ExpectDiffHash: "abcdef0"},
		"commit no paths":        Commit{Where: InWorktree{ID: "w1"}, Message: "m", ExpectDiffHash: "abcdef0"},
		"commit empty message":   Commit{Where: InWorktree{ID: "w1"}, Paths: []RelPath{"a"}, ExpectDiffHash: "abcdef0"},
		"commit no target":       Commit{Paths: []RelPath{"a"}, Message: "m", ExpectDiffHash: "abcdef0"},
		"push leading dash":      Push{Where: InWorktree{ID: "w1"}, Branch: "-x", Remote: "origin", ExpectHead: "abcdef0"},
		"push dash remote":       Push{Where: InWorktree{ID: "w1"}, Branch: "x", Remote: "--upload-pack=evil", ExpectHead: "abcdef0"},
		"push bad target id":     Push{Where: InWorktree{ID: "a b"}, Branch: "x", Remote: "origin", ExpectHead: "abcdef0"},
		"mr multi-line":          MergeRequest{Where: InWorktree{ID: "w1"}, Branch: "x", Remote: "origin", TargetBranch: "main", Title: "t", Description: "a\nb", ExpectHead: "abcdef0"},
		"agent file unprotected": AgentFile{Where: InWorktree{ID: "w1"}, Path: "src/a.go", Content: "x"},
		"agent file escape":      AgentFile{Where: InWorktree{ID: "w1"}, Path: ".claude/../x", Content: "x"},
		"delete none":            DeleteFiles{Where: InWorktree{ID: "w1"}},
		"install bad step":       Install{Repo: "r1", Step: "-x"},
		"install bad repo":       Install{Repo: "", Step: "install"},
	}
	for name, a := range bad {
		t.Run(name, func(t *testing.T) {
			p := base()
			p.Action = a
			got, cmds := Step(Proposal{}, Created{Proposal: p, Facts: okFacts(), Now: epoch})
			if got.State != nil {
				t.Fatalf("created %+v", got.State)
			}
			refusal(t, cmds, RefuseBadFacts)
			if err := a.Validate(); !errors.Is(err, ErrInvalidAction) {
				t.Errorf("Validate() = %v", err)
			}
		})
	}
	// every valid generated action still validates
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		a := genAction(r)
		if err := a.Validate(); err != nil {
			t.Fatalf("%#v: %v", a, err)
		}
	})
}

// Validate accepts what ParseAction accepts and nothing more: every parsed
// action validates.
func TestParsedActionsValidate(t *testing.T) {
	for kind, args := range map[string]string{
		"commit":        `{"repo":"r1","paths":["a/b.go"],"message":"m","expect_diff_hash":"abcdef0"}`,
		"push":          `{"repo":"r1","branch":"x","remote":"origin","expect_head":"abcdef0"}`,
		"agent_file":    `{"repo":"r1","path":"CLAUDE.md","content":"x"}`,
		"delete_files":  `{"worktree":"w1","paths":["a.txt"]}`,
		"install":       `{"repo":"r1","step":"install"}`,
		"merge_request": `{"repo":"r1","branch":"x","remote":"origin","target":"main","title":"t","expect_head":"abcdef0"}`,
	} {
		a, err := ParseAction(kind, []byte(args))
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if err := a.Validate(); err != nil {
			t.Errorf("%s: parsed action fails Validate: %v", kind, err)
		}
	}
}
