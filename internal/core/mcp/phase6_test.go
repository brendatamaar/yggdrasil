package mcp

import (
	"math/rand"
	"slices"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// The Phase 6 proposal tools (MCP-05, NR-04, WS-09, GH-06): parse rules,
// offering and the closed-set properties.

const head40 = "0123456789abcdef0123456789abcdef01234567"

func TestParsePhase6Rejects(t *testing.T) {
	manyEntries := `["` + strings.Join(many(MaxIgnoreEntries+1, func(i int) string { return "/d" + strings.Repeat("x", i+1) + "/" }), `","`) + `"]`
	bigEntries := `["` + strings.Join(many(10, func(i int) string { return "/" + strings.Repeat(string(rune('a'+i)), 500) + "/" }), `","`) + `"]`
	pub := func(kv ...string) string {
		m := map[string]string{"repo": "r", "host": "git.acme.dev", "owner": "acme", "owner_kind": "org", "name": "shop-new"}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		var b strings.Builder
		b.WriteString("{")
		first := true
		for k, v := range m {
			if !first {
				b.WriteString(",")
			}
			first = false
			b.WriteString(`"` + k + `":"` + v + `"`)
		}
		b.WriteString("}")
		return b.String()
	}
	rc := func(extra string) string {
		return `{"repo":"r","mr":3,"expected_head":"` + head40 + `","body":"hi"` + extra + `}`
	}
	cases := []struct{ name, tool, args, want string }{
		{"ignore none", "propose_ignore", `{"repo":"r","entries":[]}`, "between 1 and"},
		{"ignore missing", "propose_ignore", `{"repo":"r"}`, "between 1 and"},
		{"ignore too many", "propose_ignore", `{"repo":"r","entries":` + manyEntries + `}`, "between 1 and"},
		{"ignore too big", "propose_ignore", `{"repo":"r","entries":` + bigEntries + `}`, "over 4 KiB"},
		{"ignore dup", "propose_ignore", `{"repo":"r","entries":["/a/","/a/"]}`, "twice"},
		{"ignore unanchored", "propose_ignore", `{"repo":"r","entries":["dist/"]}`, "anchored"},
		{"ignore not a folder", "propose_ignore", `{"repo":"r","entries":["/dist"]}`, "anchored"},
		{"ignore star", "propose_ignore", `{"repo":"r","entries":["/*/"]}`, "pattern character"},
		{"ignore negation", "propose_ignore", `{"repo":"r","entries":["/!x/"]}`, "pattern character"},
		{"ignore comment", "propose_ignore", `{"repo":"r","entries":["/#x/"]}`, "pattern character"},
		{"ignore dotdot", "propose_ignore", `{"repo":"r","entries":["/a/../b/"]}`, "relative or reserved"},
		{"ignore git", "propose_ignore", `{"repo":"r","entries":["/.git/"]}`, "relative or reserved"},
		{"ignore drive", "propose_ignore", `{"repo":"r","entries":["/C:/"]}`, "Windows"},
		{"ignore control", "propose_ignore", `{"repo":"r","entries":["/a\u0000b/"]}`, "control"},
		{"ignore lone backslash", "propose_ignore", `{"repo":"r","entries":["/a\\/"]}`, "lone backslash"},
		{"ignore bare slash", "propose_ignore", `{"repo":"r","entries":["/"]}`, "empty"},
		{"ignore repo path", "propose_ignore", `{"repo":"../x","entries":["/a/"]}`, "not a path"},
		{"ignore extra field", "propose_ignore", `{"repo":"r","entries":["/a/"],"file":".env"}`, "not what this tool accepts"},
		{"comment no mr", "propose_review_comment", `{"repo":"r","expected_head":"` + head40 + `","body":"x"}`, "mr must be"},
		{"comment mr zero", "propose_review_comment", `{"repo":"r","mr":0,"expected_head":"` + head40 + `","body":"x"}`, "mr must be"},
		{"comment mr negative", "propose_review_comment", `{"repo":"r","mr":-4,"expected_head":"` + head40 + `","body":"x"}`, "mr must be"},
		{"comment mr huge", "propose_review_comment", `{"repo":"r","mr":99999999999,"expected_head":"` + head40 + `","body":"x"}`, "mr must be"},
		{"comment no head", "propose_review_comment", `{"repo":"r","mr":3,"body":"x"}`, "expected_head"},
		{"comment short head", "propose_review_comment", `{"repo":"r","mr":3,"expected_head":"abc123","body":"x"}`, "expected_head"},
		{"comment upper head", "propose_review_comment", `{"repo":"r","mr":3,"expected_head":"` + strings.ToUpper(head40) + `","body":"x"}`, "expected_head"},
		{"comment branch as head", "propose_review_comment", `{"repo":"r","mr":3,"expected_head":"main","body":"x"}`, "expected_head"},
		{"comment empty body", "propose_review_comment", `{"repo":"r","mr":3,"expected_head":"` + head40 + `","body":"  \n"}`, "empty"},
		{"comment long body", "propose_review_comment", `{"repo":"r","mr":3,"expected_head":"` + head40 + `","body":"` + strings.Repeat("b", MaxCommentBytes+1) + `"}`, "over 16 KiB"},
		{"comment control", "propose_review_comment", `{"repo":"r","mr":3,"expected_head":"` + head40 + `","body":"a\u001bb"}`, "control"},
		{"comment marker field", "propose_review_comment", rc(`,"marker":"abcdefabcdefabcdef"`), "not what this tool accepts"},
		{"comment summary lines", "propose_review_comment", rc(`,"summary":"a\nb"`), "one line"},
		{"publish no host", "propose_publish", pub("host", ""), "host is not valid"},
		{"publish upper host", "propose_publish", pub("host", "Git.Acme.Dev"), "host is not valid"},
		{"publish host with user", "propose_publish", pub("host", "me@git.acme.dev"), "host is not valid"},
		{"publish host with path", "propose_publish", pub("host", "git.acme.dev/x"), "host is not valid"},
		{"publish owner slash", "propose_publish", pub("owner", "a/b"), "owner"},
		{"publish owner dot", "propose_publish", pub("owner", ".x"), "owner is not valid"},
		{"publish name git", "propose_publish", pub("name", "x.git"), "repository name"},
		{"publish name long", "propose_publish", pub("name", strings.Repeat("n", 101)), "repository name"},
		{"publish kind", "propose_publish", pub("owner_kind", "team"), "owner_kind"},
		{"publish kind missing", "propose_publish", pub("owner_kind", ""), "owner_kind"},
		{"publish visibility", "propose_publish", pub("visibility", "internal"), "visibility"},
		{"publish branch field", "propose_publish", `{"repo":"r","host":"h","owner":"o","owner_kind":"user","name":"n","branch":"x"}`, "not what this tool accepts"},
		{"publish operation field", "propose_publish", `{"repo":"r","host":"h","owner":"o","owner_kind":"user","name":"n","operation":"op_1"}`, "not what this tool accepts"},
		{"publish head field", "propose_publish", `{"repo":"r","host":"h","owner":"o","owner_kind":"user","name":"n","expected_head":"` + head40 + `"}`, "not what this tool accepts"},
		{"publish repo path", "propose_publish", pub("repo", "C:"), "not a path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := ParseCall(tc.tool, []byte(tc.args))
			if err == nil {
				t.Fatalf("accepted %#v", c)
			}
			if c != nil {
				t.Errorf("returned a call with an error: %#v", c)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestParsePhase6Accepts(t *testing.T) {
	entries := `["` + strings.Join(many(MaxIgnoreEntries, func(i int) string { return "/d" + string(rune('a'+i%26)) + strings.Repeat("x", i) + "/" }), `","`) + `"]`
	ok := []struct{ tool, args string }{
		{"propose_ignore", `{"repo":"r","entries":` + entries + `}`},
		{"propose_ignore", `{"repo":"r","entries":["/a b/","/web/dist/","/\\#odd/"]}`},
		{"propose_review_comment", `{"repo":"r","mr":1,"expected_head":"` + strings.Repeat("f", 64) + `","body":"` + strings.Repeat("b", MaxCommentBytes) + `"}`},
		{"propose_review_comment", `{"repo":"r","mr":1073741824,"expected_head":"` + head40 + `","body":"line1\r\nline2\tx"}`},
		{"propose_publish", `{"repo":"r","host":"git.acme.dev","owner":"acme","owner_kind":"org","name":"shop-new"}`},
		{"propose_publish", `{"repo":"r","host":"h","owner":"o","owner_kind":"user","name":"n","visibility":"public"}`},
	}
	for _, tc := range ok {
		if _, err := ParseCall(tc.tool, []byte(tc.args)); err != nil {
			t.Errorf("%s %.50s: %v", tc.tool, tc.args, err)
		}
	}
}

// Private is the default: an omitted visibility is stored as "private", never
// as an empty string a later layer could read as public.
func TestPublishDefaultsToPrivate(t *testing.T) {
	c, err := ParseCall("propose_publish", []byte(`{"repo":"r","host":"h","owner":"o","owner_kind":"user","name":"n"}`))
	if err != nil {
		t.Fatal(err)
	}
	if v := c.(ProposePublish).Visibility; v != VisibilityPrivate {
		t.Fatalf("visibility %q", v)
	}
}

// The wire shapes of the three tools carry no field for a marker, an operation,
// a branch or a commit pinned by the caller of propose_publish: those come from
// the server.
func TestPublishCarriesNoServerOwnedField(t *testing.T) {
	_, args := FormatCall(ProposePublish{Repo: "r", Host: "h", Owner: "o", OwnerKind: "user", Name: "n", Visibility: "private"})
	for _, banned := range []string{"operation", "marker", "branch", "head"} {
		if strings.Contains(string(args), banned) {
			t.Errorf("propose_publish arguments mention %q: %s", banned, args)
		}
	}
}

var phase6Tools = []Tool{ToolProposePublish, ToolProposeReviewComment, ToolProposeIgnore}

func live(mode core.TaskMode, scope []core.RepoID, wt ...core.RepoID) Context {
	c := Context{Task: "t", Live: true, Mode: mode, Scope: scope, Project: []core.RepoID{"a", "b"}, HasWorktree: map[core.RepoID]bool{}}
	for _, r := range wt {
		c.HasWorktree[r] = true
	}
	return c
}

func TestPhase6ToolsOffered(t *testing.T) {
	for _, mode := range []core.TaskMode{core.ModeAsk, core.ModeCode, core.ModeCurrentBranch, core.ModeProject} {
		// Offered in every mode, with or without a worktree, as soon as the task
		// has a repo in scope: creating the proposal touches no tree.
		for _, c := range []Context{live(mode, []core.RepoID{"a"}), live(mode, []core.RepoID{"a"}, "a")} {
			off := Offered(c)
			for _, tool := range phase6Tools {
				if !slices.Contains(off, tool) {
					t.Errorf("%s not offered in %s mode", tool, mode)
				}
				if d := Authorize(c, tool, "a"); !d.Allowed {
					t.Errorf("%s denied for a scope repo in %s mode: %+v", tool, mode, d)
				}
			}
		}
		// No repo in scope: not offered.
		empty := live(mode, nil)
		for _, tool := range phase6Tools {
			if slices.Contains(Offered(empty), tool) {
				t.Errorf("%s offered to a task with no repo in scope (%s)", tool, mode)
			}
			if d := Authorize(empty, tool); d.Reason != DenyNoSuchRepo {
				t.Errorf("%s: %+v", tool, d)
			}
		}
	}
	// A visible repo outside the scope, and an untracked one, are not reachable.
	c := live(core.ModeAsk, []core.RepoID{"a"})
	for _, tool := range phase6Tools {
		for _, r := range []core.RepoID{"b", "untracked", ""} {
			if d := Authorize(c, tool, r); d.Allowed || d.Reason != DenyNoSuchRepo {
				t.Errorf("%s reaches %q: %+v", tool, r, d)
			}
		}
	}
	dead := live(core.ModeCode, []core.RepoID{"a"})
	dead.Live = false
	if len(Offered(dead)) != 0 {
		t.Error("a dead session is offered tools")
	}
}

// restricts: a child may run in the parent's mode or in Ask mode, never in a
// mode that can change more.
func childMode(r *rand.Rand, parent core.TaskMode) core.TaskMode {
	if r.Intn(2) == 0 {
		return parent
	}
	return core.ModeAsk
}

// Invariant 11 and M10: a child never gets a tool its parent lacks, whatever
// the parent's context, when its scope is a subset, its mode the same or Ask,
// and its worktrees a subset.
func TestPropChildNeverGetsMoreTools(t *testing.T) {
	for seed := int64(1); seed <= 1000; seed++ {
		r := rand.New(rand.NewSource(seed))
		parent := randCtx(r)
		parent.Live = true
		if parent.Mode == core.ModeCode || parent.Mode == core.ModeProject {
			// A task in a worktree mode edits in a worktree of each repo it
			// works in; a context without one is not reachable. Without this, an
			// Ask child (which may propose a commit against the main checkout)
			// would trivially exceed a parent that has no worktree and no way to
			// edit anywhere.
			for _, id := range parent.Scope {
				parent.HasWorktree[id] = true
			}
		}
		child := parent
		child.Mode = childMode(r, parent.Mode)
		child.Scope = nil
		for _, id := range parent.Scope {
			if r.Intn(2) == 0 {
				child.Scope = append(child.Scope, id)
			}
		}
		child.HasWorktree = map[core.RepoID]bool{}
		for id := range parent.HasWorktree {
			if r.Intn(2) == 0 {
				child.HasWorktree[id] = true
			}
		}
		po := Offered(parent)
		for _, tool := range Offered(child) {
			if !slices.Contains(po, tool) {
				t.Fatalf("seed %d: child offered %s that its parent lacks\nparent %+v\nchild %+v", seed, tool, parent, child)
			}
		}
		for _, tool := range AllTools() {
			for _, id := range allRepos {
				if Authorize(child, tool, id).Allowed && !Authorize(parent, tool, id).Allowed {
					t.Fatalf("seed %d: child may call %s on %s but its parent may not", seed, tool, id)
				}
			}
		}
	}
}

// M4 over the offered set: whatever a context offers, no tool confirms, rejects,
// executes or creates a repo; every tool that acts on the world is a propose_*
// tool, a build, a fetch or a pull.
func TestPropOfferedToolsNeverConfirm(t *testing.T) {
	allowed := map[Tool]bool{ToolStartBuild: true, ToolRunBuildPreset: true, ToolFetch: true, ToolPull: true}
	for seed := int64(1); seed <= 300; seed++ {
		c := randCtx(rand.New(rand.NewSource(seed)))
		for _, tool := range Offered(c) {
			if ClassOf(tool) != ClassAction {
				continue
			}
			if !strings.HasPrefix(string(tool), "propose_") && !allowed[tool] {
				t.Fatalf("seed %d: action tool %s is neither a proposal nor a known safe action", seed, tool)
			}
			if strings.Contains(string(tool), "init") || strings.Contains(string(tool), "confirm") {
				t.Fatalf("seed %d: %s", seed, tool)
			}
		}
	}
	for _, tool := range AllTools() {
		if tool == "propose_init_repo" || strings.Contains(string(tool), "init") {
			t.Fatalf("the tool set contains %s: initialization starts only from the dialog", tool)
		}
	}
}

func FuzzParsePhase6(f *testing.F) {
	f.Add("propose_ignore", []byte(`{"repo":"r","entries":["/dist/"]}`))
	f.Add("propose_ignore", []byte(`{"repo":"r","entries":["/a/../b/","*","/\\/"]}`))
	f.Add("propose_review_comment", []byte(`{"repo":"r","mr":3,"expected_head":"`+head40+`","body":"hi"}`))
	f.Add("propose_review_comment", []byte(`{"repo":"r","mr":-1,"expected_head":"x","body":"\u0000"}`))
	f.Add("propose_publish", []byte(`{"repo":"r","host":"git.acme.dev","owner":"acme","owner_kind":"org","name":"n"}`))
	f.Add("propose_publish", []byte(`{"repo":"r","host":"H@x/y","owner":"..","owner_kind":"","name":".git","visibility":"x"}`))
	f.Fuzz(func(t *testing.T, name string, args []byte) {
		switch Tool(name) {
		case ToolProposeIgnore, ToolProposeReviewComment, ToolProposePublish:
		default:
			return
		}
		c, err := ParseCall(name, args)
		if err != nil {
			if c != nil {
				t.Fatal("call returned with an error")
			}
			return
		}
		if err := CheckCall(c); err != nil {
			t.Fatalf("accepted but invalid: %v", err)
		}
		switch v := c.(type) {
		case ProposeIgnore:
			if len(v.Entries) == 0 || len(v.Entries) > MaxIgnoreEntries {
				t.Fatalf("%d entries accepted", len(v.Entries))
			}
			total := 0
			for _, e := range v.Entries {
				total += len(e) + 1
				if !strings.HasPrefix(e, "/") || !strings.HasSuffix(e, "/") || strings.Contains(e, "..") || strings.ContainsAny(e, "\x00\n\r") {
					t.Fatalf("entry accepted: %q", e)
				}
			}
			if total > MaxIgnoreBytes {
				t.Fatalf("entries are %d bytes", total)
			}
		case ProposeReviewComment:
			if v.MR < 1 || v.MR > MaxMRNumber || len(v.Body) > MaxCommentBytes || strings.TrimSpace(v.Body) == "" {
				t.Fatalf("comment accepted: %#v", v)
			}
			if n := len(v.ExpectedHead); n != 40 && n != 64 {
				t.Fatalf("head %q", v.ExpectedHead)
			}
		case ProposePublish:
			if v.Visibility != VisibilityPrivate && v.Visibility != VisibilityPublic {
				t.Fatalf("visibility %q", v.Visibility)
			}
			if v.OwnerKind != "user" && v.OwnerKind != "org" {
				t.Fatalf("owner kind %q", v.OwnerKind)
			}
			if strings.ContainsAny(v.Owner+v.Name, "/\\@: ") || strings.ContainsAny(v.Host, "/\\@ ") {
				t.Fatalf("publish accepted: %#v", v)
			}
		}
		n, a := FormatCall(c)
		c2, err := ParseCall(n, a)
		if err != nil || !equalCalls(c, c2) {
			t.Fatalf("round trip: %v %#v %#v", err, c, c2)
		}
	})
}

func equalCalls(a, b Call) bool {
	na, aa := FormatCall(a)
	nb, ab := FormatCall(b)
	return na == nb && string(aa) == string(ab)
}
