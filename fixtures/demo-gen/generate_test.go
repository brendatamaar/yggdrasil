package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

var repoNames = []string{"shop-api", "shop-web", "shop-admin"}

var fictionalAuthors = map[string]string{
	"Ada Lindqvist": "ada.lindqvist@example.com",
	"Bram Okafor":   "bram.okafor@example.com",
	"Chen Wei":      "chen.wei@example.com",
	"Dana Ruiz":     "dana.ruiz@example.com",
}

// genDemo generates the demo project under dir and returns its root.
func genDemo(t *testing.T, dir string) (string, []repoSummary) {
	t.Helper()
	out := filepath.Join(dir, outName)
	root, sums, err := generate(out)
	if err != nil {
		t.Fatalf("generate(%q): %v", out, err)
	}
	if len(sums) != len(repoNames) {
		t.Fatalf("got %d repo summaries, want %d", len(sums), len(repoNames))
	}
	return root, sums
}

// TestGenerate generates the project twice (once under a path with spaces)
// and checks it in read-only subtests: generating is slow on Windows.
func TestGenerate(t *testing.T) {
	tmp := t.TempDir()
	root, sums := genDemo(t, filepath.Join(tmp, "folder with spaces"))
	// An existing empty output folder is fine.
	if err := os.MkdirAll(filepath.Join(tmp, "second", outName), 0o755); err != nil {
		t.Fatal(err)
	}
	other, _ := genDemo(t, filepath.Join(tmp, "second"))

	t.Run("Deterministic", func(t *testing.T) { testDeterministic(t, root, other) })
	for _, tc := range []struct {
		name string
		fn   func(*testing.T, string)
	}{
		{"LayoutAndOrigin", testLayoutAndOrigin},
		{"CommitCountsAndAuthors", testCommitCountsAndAuthors},
		{"Branches", testBranches},
		{"AheadBranch", testAheadBranch},
		{"DivergedBranch", testDivergedBranch},
		{"BehindMain", testBehindMain},
		{"DirtyWorkingTree", testDirtyWorkingTree},
		{"CRLFBlob", testCRLFBlob},
		{"Tags", testTags},
	} {
		t.Run(tc.name, func(t *testing.T) { tc.fn(t, root) })
	}
	t.Run("Summary", func(t *testing.T) { testSummary(t, root, sums) })
}

func openLocal(t *testing.T, root, name string) *git.Repository {
	t.Helper()
	r, err := git.PlainOpen(filepath.Join(root, "acme-shop", name))
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	return r
}

func openRemote(t *testing.T, root, name string) *git.Repository {
	t.Helper()
	r, err := git.PlainOpen(filepath.Join(root, "remotes", name+".git"))
	if err != nil {
		t.Fatalf("open remote %s: %v", name, err)
	}
	return r
}

func mustRef(t *testing.T, r *git.Repository, name plumbing.ReferenceName) plumbing.Hash {
	t.Helper()
	h, err := refHash(r, name)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func mustCommit(t *testing.T, r *git.Repository, h plumbing.Hash) *object.Commit {
	t.Helper()
	c, err := r.CommitObject(h)
	if err != nil {
		t.Fatalf("read commit %s: %v", h, err)
	}
	return c
}

func isAncestor(t *testing.T, r *git.Repository, a, b plumbing.Hash) bool {
	t.Helper()
	ok, err := mustCommit(t, r, a).IsAncestor(mustCommit(t, r, b))
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// allRefs maps every ref name of a repo to its hash (symbolic refs to their target).
func allRefs(t *testing.T, r *git.Repository) map[string]string {
	t.Helper()
	refs, err := r.References()
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	err = refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Type() == plumbing.SymbolicReference {
			m[ref.Name().String()] = "-> " + ref.Target().String()
		} else {
			m[ref.Name().String()] = ref.Hash().String()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func testDeterministic(t *testing.T, a, b string) {
	if !strings.Contains(a, " ") {
		t.Fatalf("root %q has no space", a)
	}
	for _, name := range repoNames {
		for _, open := range []func(*testing.T, string, string) *git.Repository{openLocal, openRemote} {
			ra, rb := allRefs(t, open(t, a, name)), allRefs(t, open(t, b, name))
			if len(ra) == 0 {
				t.Fatalf("%s: no refs", name)
			}
			for ref, h := range ra {
				if rb[ref] != h {
					t.Errorf("%s %s: %s vs %s", name, ref, h, rb[ref])
				}
			}
			if len(ra) != len(rb) {
				t.Errorf("%s: %d refs vs %d", name, len(ra), len(rb))
			}
		}
	}
}

func testLayoutAndOrigin(t *testing.T, root string) {
	readme, err := os.ReadFile(filepath.Join(root, "acme-shop", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readme), "fictional") {
		t.Errorf("project README does not say the data is fictional:\n%s", readme)
	}
	wantFiles := map[string][]string{
		"shop-api":   {"pom.xml", "mvnw", "mvnw.cmd", "README.md", "src/main/resources/application.yml", javaPkg + "ShopApiApplication.java"},
		"shop-web":   {"package.json", "pnpm-lock.yaml", "src/main.ts", "README.md"},
		"shop-admin": {"package.json", "src/index.js", "README.md"},
	}
	for _, name := range repoNames {
		for _, p := range wantFiles[name] {
			if _, err := os.Stat(filepath.Join(root, "acme-shop", name, filepath.FromSlash(p))); err != nil {
				t.Errorf("%s: %v", name, err)
			}
		}
		r := openLocal(t, root, name)
		rem, err := r.Remote("origin")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		url := rem.Config().URLs[0]
		if want := filepath.ToSlash(filepath.Join(root, "remotes", name+".git")); url != want {
			t.Errorf("%s: origin URL %q, want %q", name, url, want)
		}
	}
	pom, err := os.ReadFile(filepath.Join(root, "acme-shop", "shop-api", "pom.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pom), "<groupId>com.example.acme</groupId>") {
		t.Error("pom.xml lacks the com.example.acme groupId")
	}
}

func testCommitCountsAndAuthors(t *testing.T, root string) {
	for _, name := range repoNames {
		r := openLocal(t, root, name)
		iter, err := r.Log(&git.LogOptions{All: true})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		n := 0
		seen := map[string]bool{}
		err = iter.ForEach(func(c *object.Commit) error {
			n++
			for _, sig := range []object.Signature{c.Author, c.Committer} {
				if fictionalAuthors[sig.Name] != sig.Email {
					t.Errorf("%s %s: unexpected person %s <%s>", name, c.Hash, sig.Name, sig.Email)
				}
				if sig.When.Year() != 2026 {
					t.Errorf("%s %s: date %s is not in 2026", name, c.Hash, sig.When)
				}
			}
			seen[c.Author.Name] = true
			msg := strings.SplitN(c.Message, "\n", 2)[0]
			if !conventional(msg) {
				t.Errorf("%s: %q is not a conventional commit message", name, msg)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if n < 15 || n > 30 {
			t.Errorf("%s: %d commits, want 15..30", name, n)
		}
		if len(seen) < 3 {
			t.Errorf("%s: %d authors, want 3..4", name, len(seen))
		}
	}
}

func conventional(msg string) bool {
	typ, rest, ok := strings.Cut(msg, ": ")
	if !ok || rest == "" {
		return false
	}
	if i := strings.Index(typ, "("); i >= 0 {
		if !strings.HasSuffix(typ, ")") {
			return false
		}
		typ = typ[:i]
	}
	return slices.Contains([]string{"feat", "fix", "chore", "docs", "test", "build", "refactor", "style", "ci", "perf"}, typ)
}

func testBranches(t *testing.T, root string) {
	want := map[string][]string{
		"shop-api":   {"feature/cart-totals", "fix/order-rounding", "main"},
		"shop-web":   {"feature/checkout-page", "fix/login-timeout", "main"},
		"shop-admin": {"feature/sales-report", "feature/user-roles", "main"},
	}
	for _, name := range repoNames {
		r := openLocal(t, root, name)
		iter, err := r.Branches()
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		_ = iter.ForEach(func(ref *plumbing.Reference) error {
			got = append(got, ref.Name().Short())
			return nil
		})
		slices.Sort(got)
		if !slices.Equal(got, want[name]) {
			t.Errorf("%s: branches %v, want %v", name, got, want[name])
		}
		head, err := r.Head()
		if err != nil {
			t.Fatal(err)
		}
		if head.Name() != plumbing.Main {
			t.Errorf("%s: HEAD is %s, want main", name, head.Name())
		}
	}
}

// checkUpstream asserts branch tracks origin/<branch> and that the local
// remote-tracking ref matches the bare repo.
func checkUpstream(t *testing.T, root, repo, branch string) (local, tracking plumbing.Hash, r *git.Repository) {
	t.Helper()
	r = openLocal(t, root, repo)
	cfg, err := r.Config()
	if err != nil {
		t.Fatal(err)
	}
	bc, ok := cfg.Branches[branch]
	if !ok || bc.Remote != "origin" || bc.Merge != plumbing.NewBranchReferenceName(branch) {
		t.Fatalf("%s %s: upstream config %+v, want origin/%s", repo, branch, bc, branch)
	}
	local = mustRef(t, r, plumbing.NewBranchReferenceName(branch))
	tracking = mustRef(t, r, plumbing.NewRemoteReferenceName("origin", branch))
	bare := mustRef(t, openRemote(t, root, repo), plumbing.NewBranchReferenceName(branch))
	if bare != tracking {
		t.Errorf("%s: origin/%s is %s but the bare repo has %s", repo, branch, tracking, bare)
	}
	return local, tracking, r
}

func testAheadBranch(t *testing.T, root string) {
	local, up, r := checkUpstream(t, root, "shop-api", "feature/cart-totals")
	if local == up || !isAncestor(t, r, up, local) {
		t.Errorf("feature/cart-totals (%s) is not ahead of origin (%s)", local, up)
	}
}

func testDivergedBranch(t *testing.T, root string) {
	local, up, r := checkUpstream(t, root, "shop-admin", "feature/user-roles")
	if local == up || isAncestor(t, r, up, local) || isAncestor(t, r, local, up) {
		t.Errorf("feature/user-roles (%s) has not diverged from origin (%s)", local, up)
	}
	// The bare repo has a commit the local branch lacks, and vice versa.
	bare := openRemote(t, root, "shop-admin")
	if _, err := bare.CommitObject(local); !errors.Is(err, plumbing.ErrObjectNotFound) {
		t.Errorf("bare repo should not have the local-only commit: %v", err)
	}
}

func testBehindMain(t *testing.T, root string) {
	local, up, r := checkUpstream(t, root, "shop-web", "main")
	if local == up || !isAncestor(t, r, local, up) {
		t.Errorf("main (%s) is not behind origin/main (%s)", local, up)
	}
	// The other repos' main is in sync.
	for _, name := range []string{"shop-api", "shop-admin"} {
		if l, u, _ := checkUpstream(t, root, name, "main"); l != u {
			t.Errorf("%s main %s != origin/main %s", name, l, u)
		}
	}
}

func testDirtyWorkingTree(t *testing.T, root string) {
	wt, err := openLocal(t, root, "shop-web").Worktree()
	if err != nil {
		t.Fatal(err)
	}
	st, err := wt.Status()
	if err != nil {
		t.Fatal(err)
	}
	if fs := st.File("src/main.ts"); fs.Worktree != git.Modified {
		t.Errorf("src/main.ts: worktree status %q, want modified", fs.Worktree)
	}
	if fs := st.File("src/debug.ts"); fs.Worktree != git.Untracked {
		t.Errorf("src/debug.ts: worktree status %q, want untracked", fs.Worktree)
	}
	if len(st) != 2 {
		t.Errorf("status has %d entries, want 2:\n%s", len(st), st)
	}

	// shop-admin has no executable or CRLF files, so go-git sees it clean on every OS.
	wt, err = openLocal(t, root, "shop-admin").Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if st, err := wt.Status(); err != nil || !st.IsClean() {
		t.Errorf("shop-admin not clean (%v):\n%s", err, st)
	}
}

func testCRLFBlob(t *testing.T, root string) {
	r := openLocal(t, root, "shop-api")
	c := mustCommit(t, r, mustRef(t, r, plumbing.NewBranchReferenceName("main")))
	read := func(path string) []byte {
		t.Helper()
		fl, err := c.File(path)
		if err != nil {
			t.Fatal(err)
		}
		rd, err := fl.Reader()
		if err != nil {
			t.Fatal(err)
		}
		defer rd.Close()
		b, err := io.ReadAll(rd)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	if b := read("mvnw.cmd"); !bytes.Contains(b, []byte("\r\n")) {
		t.Errorf("mvnw.cmd blob has no CRLF: %q", b)
	}
	if b := read("mvnw"); bytes.Contains(b, []byte("\r")) {
		t.Errorf("mvnw blob has CR bytes: %q", b)
	}
	disk, err := os.ReadFile(filepath.Join(root, "acme-shop", "shop-api", "mvnw.cmd"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(disk, []byte("\r\n")) {
		t.Errorf("checked-out mvnw.cmd has no CRLF: %q", disk)
	}
}

func testTags(t *testing.T, root string) {
	r := openLocal(t, root, "shop-api")

	ann := mustRef(t, r, plumbing.NewTagReferenceName("v0.1.0"))
	tag, err := r.TagObject(ann)
	if err != nil {
		t.Fatalf("v0.1.0 is not annotated: %v", err)
	}
	if fictionalAuthors[tag.Tagger.Name] != tag.Tagger.Email {
		t.Errorf("v0.1.0 tagger %s <%s> is not fictional", tag.Tagger.Name, tag.Tagger.Email)
	}
	if c, err := tag.Commit(); err != nil || !strings.HasPrefix(c.Message, "chore(release): 0.1.0") {
		t.Errorf("v0.1.0 does not point at the release commit (%v)", err)
	}

	light := mustRef(t, r, plumbing.NewTagReferenceName("v0.1.0-rc.1"))
	if _, err := r.TagObject(light); err == nil {
		t.Error("v0.1.0-rc.1 should be a lightweight tag")
	}
	mustCommit(t, r, light)

	if got := mustRef(t, openRemote(t, root, "shop-api"), plumbing.NewTagReferenceName("v0.1.0")); got != ann {
		t.Errorf("origin v0.1.0 is %s, want %s", got, ann)
	}
}

func TestGenerate_RefusesWrongName(t *testing.T) {
	out := filepath.Join(t.TempDir(), "demo")
	if _, _, err := generate(out); err == nil || !strings.Contains(err.Error(), outName) {
		t.Fatalf("generate(%q) = %v, want a refusal naming %s", out, err, outName)
	}
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s was created: %v", out, err)
	}
}

func TestGenerate_RefusesNonEmptyOut(t *testing.T) {
	out := filepath.Join(t.TempDir(), "has space", outName)
	keep := filepath.Join(out, "keep.txt")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keep, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := generate(out); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("generate into non-empty dir = %v, want a refusal", err)
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "keep.txt" {
		t.Errorf("out changed: %v", entries)
	}
	if b, err := os.ReadFile(keep); err != nil || string(b) != "mine\n" {
		t.Errorf("keep.txt changed: %q, %v", b, err)
	}
}

func testSummary(t *testing.T, root string, sums []repoSummary) {
	var buf bytes.Buffer
	printSummary(&buf, root, sums)
	s := buf.String()
	for _, want := range []string{
		"shop-api: 23 commits",
		"shop-web: 20 commits",
		"shop-admin: 21 commits",
		"ahead of origin/feature/cart-totals by 2",
		"diverged from origin/feature/user-roles: 1 ahead, 1 behind",
		"behind origin/main by 2",
		"fix/login-timeout",
		"local only",
		"v0.1.0 (annotated)",
		"v0.1.0-rc.1",
		"CRLF",
		"dirty:",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("summary lacks %q:\n%s", want, s)
		}
	}
}
