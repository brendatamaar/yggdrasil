package gittest

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/git"
)

// LandingWorld is what the landing contract exercises.
type LandingWorld interface {
	git.LandingGit
	git.BaseDiffer
}

// LandingHarness arranges the world for LandingGitContract. Dir is a main
// checkout on branch "main" with README.md ("hello\n") committed and no change.
// Its remote (when it has one) must never receive anything from the code under
// test: RemoteRefs lets the contract check that.
type LandingHarness interface {
	Dir() string
	// CommitOn commits one file on branch (made from the tip of main when it
	// does not exist) and leaves the main checkout on main, unchanged. It
	// returns the new tip.
	CommitOn(branch, rel, content string) string
	// CommitMain commits one file on main in the main checkout.
	CommitMain(rel, content string) string
	// Worktree adds a linked worktree on a new branch started from main and
	// returns its folder and the base commit.
	Worktree(name string) (dir, base string)
	// MoveFile renames a working file (no commit).
	MoveFile(dir, from, to string)
	// WriteFile writes a working file (no commit), byte for byte.
	WriteFile(dir, rel, content string)
	// ReadFile reads a working file, byte for byte.
	ReadFile(dir, rel string) (string, bool)
	// CommitAll commits every change of the checkout at dir.
	CommitAll(dir, msg string) string
	// TreeUnchanged: no change from HEAD, no untracked file, no merge in progress.
	TreeUnchanged(dir string) bool
	Parents(commit string) []string
	RemoteRefs() map[string]string
	// UnsetIdentity makes git unable to know the user; false when impossible.
	UnsetIdentity() bool
}

// LandingGitContract runs the clauses of docs/design/landing.md "LandingGit"
// and docs/design/changesets.md / verify.md (DiffSource, DiffProbe) against
// newWorld, which returns a fresh world per call. Run it against the fake and
// against real git.
func LandingGitContract(t *testing.T, newWorld func(t *testing.T) (LandingWorld, LandingHarness)) {
	t.Helper()
	ctx := context.Background()
	run := func(name string, fn func(t *testing.T, w LandingWorld, h LandingHarness)) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w, h := newWorld(t)
			fn(t, w, h)
		})
	}
	head := func(t *testing.T, w LandingWorld, h LandingHarness) string {
		t.Helper()
		id, err := w.BranchHead(ctx, h.Dir(), "main")
		must(t, err)
		if id == "" {
			t.Fatal("main has no head")
		}
		return id
	}
	same := func(t *testing.T, got, want []string, what string) {
		t.Helper()
		if !slices.Equal(got, want) {
			t.Fatalf("%s = %q, want %q", what, got, want)
		}
	}

	run("a merge without conflicts makes a two-parent commit and pushes nothing", func(t *testing.T, w LandingWorld, h LandingHarness) {
		before := h.RemoteRefs()
		main0 := head(t, w, h)
		tip := h.CommitOn("feature", "a.txt", "one\n")
		id, err := w.MergeNoFF(ctx, h.Dir(), tip, "Merge feature")
		must(t, err)
		if got := head(t, w, h); got != id || id == main0 || id == tip {
			t.Fatalf("main = %s, merge returned %s (before %s, tip %s)", got, id, main0, tip)
		}
		same(t, h.Parents(id), []string{main0, tip}, "parents")
		if got, ok := h.ReadFile(h.Dir(), "a.txt"); !ok || got != "one\n" {
			t.Fatalf("a.txt after the merge = %q, %v", got, ok)
		}
		if !h.TreeUnchanged(h.Dir()) {
			t.Fatal("the tree is not at the merge commit")
		}
		if ok, err := w.IsAncestor(ctx, h.Dir(), tip, id); err != nil || !ok {
			t.Fatalf("branch tip is not an ancestor of the merge: %v, %v", ok, err)
		}
		if in, _, err := w.MergeInProgress(ctx, h.Dir()); err != nil || in {
			t.Fatalf("merge still in progress: %v, %v", in, err)
		}
		if after := h.RemoteRefs(); len(after) != len(before) {
			t.Fatalf("remote refs changed: %v -> %v", before, after)
		} else {
			for k, v := range before {
				if after[k] != v {
					t.Fatalf("remote ref %s moved: %s -> %s", k, v, after[k])
				}
			}
		}
	})

	run("a merge of a commit already in HEAD changes nothing", func(t *testing.T, w LandingWorld, h LandingHarness) {
		tip := h.CommitOn("feature", "a.txt", "one\n")
		id, err := w.MergeNoFF(ctx, h.Dir(), tip, "Merge feature")
		must(t, err)
		again, err := w.MergeNoFF(ctx, h.Dir(), tip, "Merge feature again")
		must(t, err)
		if again != id {
			t.Fatalf("second merge moved main: %s -> %s", id, again)
		}
	})

	conflictWorld := func(t *testing.T, h LandingHarness) (tip, mainTip string) {
		t.Helper()
		h.CommitMain("f.txt", "base\n")
		tip = h.CommitOn("feature", "f.txt", "feature side\n")
		tip = h.CommitOn("feature", "sp ace/b.txt", "feature b\n")
		mainTip = h.CommitMain("f.txt", "main side\n")
		mainTip = h.CommitMain("sp ace/b.txt", "main b\n")
		return tip, mainTip
	}

	run("a conflict reports the files and the abort restores the tree", func(t *testing.T, w LandingWorld, h LandingHarness) {
		tip, mainTip := conflictWorld(t, h)
		pred, perr := w.MergeTreeWriteTree(ctx, h.Dir(), "main", "feature")
		if perr != nil && !errors.Is(perr, git.ErrMergeTreeUnsupported) {
			t.Fatalf("merge-tree: %v", perr)
		}
		_, err := w.MergeNoFF(ctx, h.Dir(), tip, "Merge feature")
		var ce *git.MergeConflictError
		if !errors.As(err, &ce) {
			t.Fatalf("merge = %v, want *MergeConflictError", err)
		}
		same(t, ce.Files, []string{"f.txt", "sp ace/b.txt"}, "conflicted files")
		if perr == nil {
			if pred.Clean {
				t.Fatal("merge-tree said the merge is fine")
			}
			same(t, pred.Files, ce.Files, "merge-tree files")
		}
		in, mh, err := w.MergeInProgress(ctx, h.Dir())
		must(t, err)
		if !in || mh != tip {
			t.Fatalf("MergeInProgress = %v, %q; want true, %s", in, mh, tip)
		}
		if h.TreeUnchanged(h.Dir()) {
			t.Fatal("a conflicted merge left the tree unchanged: nothing to abort")
		}
		must(t, w.MergeAbort(ctx, h.Dir()))
		if !h.TreeUnchanged(h.Dir()) {
			t.Fatal("the tree is not restored after the abort")
		}
		if in, _, _ := w.MergeInProgress(ctx, h.Dir()); in {
			t.Fatal("merge still in progress after the abort")
		}
		if got := head(t, w, h); got != mainTip {
			t.Fatalf("main moved from %s to %s", mainTip, got)
		}
		if got, _ := h.ReadFile(h.Dir(), "f.txt"); got != "main side\n" {
			t.Fatalf("f.txt after the abort = %q", got)
		}
		// A second abort is a no-op.
		must(t, w.MergeAbort(ctx, h.Dir()))
	})

	run("merge-tree predicts a merge without touching the tree", func(t *testing.T, w LandingWorld, h LandingHarness) {
		h.CommitOn("feature", "a.txt", "one\n")
		h.CommitMain("b.txt", "two\n")
		p, err := w.MergeTreeWriteTree(ctx, h.Dir(), "main", "feature")
		if errors.Is(err, git.ErrMergeTreeUnsupported) {
			t.Skip("git is older than 2.38")
		}
		must(t, err)
		if !p.Clean || len(p.Files) != 0 || p.Tree == "" {
			t.Fatalf("prediction = %+v, want clean", p)
		}
		if !h.TreeUnchanged(h.Dir()) {
			t.Fatal("the prediction changed the tree")
		}
		if in, _, _ := w.MergeInProgress(ctx, h.Dir()); in {
			t.Fatal("the prediction started a merge")
		}
		if _, err := w.MergeTreeWriteTree(ctx, h.Dir(), "-x", "feature"); err == nil {
			t.Fatal("an option as a revision was accepted")
		}
		if _, err := w.MergeTreeWriteTree(ctx, h.Dir(), "no-such-branch", "feature"); err == nil {
			t.Fatal("an unknown revision was accepted")
		}
	})

	run("abort with no merge in progress does nothing", func(t *testing.T, w LandingWorld, h LandingHarness) {
		before := head(t, w, h)
		must(t, w.MergeAbort(ctx, h.Dir()))
		must(t, w.MergeAbort(ctx, h.Dir()))
		if !h.TreeUnchanged(h.Dir()) || head(t, w, h) != before {
			t.Fatal("an abort with nothing to abort changed the repo")
		}
	})

	run("an unknown author is its own error and nothing changes", func(t *testing.T, w LandingWorld, h LandingHarness) {
		tip := h.CommitOn("feature", "a.txt", "one\n")
		main0 := head(t, w, h)
		if !h.UnsetIdentity() {
			t.Skip("this world cannot unset the identity")
		}
		_, err := w.MergeNoFF(ctx, h.Dir(), tip, "Merge feature")
		if !errors.Is(err, git.ErrIdentityMissing) {
			t.Fatalf("merge without identity = %v, want ErrIdentityMissing", err)
		}
		if msg := git.UserMessage(err); !strings.Contains(msg, "user.name") {
			t.Errorf("message does not say how to set the identity: %q", msg)
		}
		if in, _, _ := w.MergeInProgress(ctx, h.Dir()); in || !h.TreeUnchanged(h.Dir()) || head(t, w, h) != main0 {
			t.Fatal("a refused merge changed the repo")
		}
	})

	run("IsAncestor and BranchHead", func(t *testing.T, w LandingWorld, h LandingHarness) {
		main0 := head(t, w, h)
		tip := h.CommitOn("feature", "a.txt", "one\n")
		if got, err := w.BranchHead(ctx, h.Dir(), "feature"); err != nil || got != tip {
			t.Fatalf("BranchHead(feature) = %q, %v; want %s", got, err, tip)
		}
		if got, err := w.BranchHead(ctx, h.Dir(), "no-such-branch"); err != nil || got != "" {
			t.Fatalf("BranchHead of a missing branch = %q, %v; want empty and nil", got, err)
		}
		if got, err := w.BranchHead(ctx, h.Dir(), "feature/never"); err != nil || got != "" {
			t.Fatalf("BranchHead of a missing nested name = %q, %v", got, err)
		}
		for _, bad := range []string{"", "-x", "a b", "feature^", "main~1", "a..b"} {
			if _, err := w.BranchHead(ctx, h.Dir(), bad); err == nil {
				t.Errorf("BranchHead(%q) accepted", bad)
			}
		}
		for _, c := range []struct {
			a, d string
			want bool
		}{
			{main0, tip, true}, {tip, tip, true}, {tip, main0, false}, {main0, main0, true},
		} {
			if got, err := w.IsAncestor(ctx, h.Dir(), c.a, c.d); err != nil || got != c.want {
				t.Errorf("IsAncestor(%s, %s) = %v, %v; want %v", c.a[:7], c.d[:7], got, err, c.want)
			}
		}
		if _, err := w.IsAncestor(ctx, h.Dir(), strings.Repeat("e", 40), tip); err == nil {
			t.Error("IsAncestor of an unknown commit is not an error")
		}
		if _, err := w.IsAncestor(ctx, h.Dir(), "-x", tip); err == nil {
			t.Error("IsAncestor accepted an option")
		}
	})

	run("MergeNoFF refuses what is not an exact commit id", func(t *testing.T, w LandingWorld, h LandingHarness) {
		main0 := head(t, w, h)
		h.CommitOn("feature", "a.txt", "one\n")
		for _, bad := range []string{"", "feature", "HEAD", "-x", strings.Repeat("A", 40), strings.Repeat("a", 39)} {
			if _, err := w.MergeNoFF(ctx, h.Dir(), bad, "m"); !errors.Is(err, git.ErrInvalidArg) {
				t.Errorf("MergeNoFF(%q) = %v, want ErrInvalidArg", bad, err)
			}
		}
		if _, err := w.MergeNoFF(ctx, h.Dir(), strings.Repeat("e", 40), "m"); err == nil {
			t.Error("an unknown commit was merged")
		}
		tip, _ := w.BranchHead(ctx, h.Dir(), "feature")
		if _, err := w.MergeNoFF(ctx, h.Dir(), tip, "  "); !errors.Is(err, git.ErrInvalidArg) {
			t.Errorf("an empty message = %v, want ErrInvalidArg", err)
		}
		if head(t, w, h) != main0 || !h.TreeUnchanged(h.Dir()) {
			t.Fatal("a refused merge changed the repo")
		}
	})

	run("CRLF files merge unchanged", func(t *testing.T, w LandingWorld, h LandingHarness) {
		h.CommitMain("keep.txt", "k1\r\nk2\r\n")
		tip := h.CommitOn("feature", "crlf.txt", "l1\r\nl2\r\n")
		h.CommitMain("other.txt", "o1\r\n")
		_, err := w.MergeNoFF(ctx, h.Dir(), tip, "Merge feature")
		must(t, err)
		for rel, want := range map[string]string{"crlf.txt": "l1\r\nl2\r\n", "keep.txt": "k1\r\nk2\r\n", "other.txt": "o1\r\n"} {
			if got, ok := h.ReadFile(h.Dir(), rel); !ok || got != want {
				t.Errorf("%s = %q, %v; want %q", rel, got, ok, want)
			}
		}
		if !h.TreeUnchanged(h.Dir()) {
			t.Error("line endings made the tree differ from the merge commit")
		}
	})

	run("paths with spaces and non-ASCII characters merge", func(t *testing.T, w LandingWorld, h LandingHarness) {
		rel := "dir with space/ü file.txt"
		tip := h.CommitOn("feature", rel, "x\n")
		h.CommitMain("main only.txt", "m\n")
		_, err := w.MergeNoFF(ctx, h.Dir(), tip, "Merge feature")
		must(t, err)
		if got, ok := h.ReadFile(h.Dir(), rel); !ok || got != "x\n" {
			t.Fatalf("%s = %q, %v", rel, got, ok)
		}
	})

	// --- DiffAgainstBase and HasChanges ---

	diff := func(t *testing.T, w LandingWorld, dir, base string) git.BaseDiff {
		t.Helper()
		d, err := w.DiffAgainstBase(ctx, dir, base, git.BaseDiffOptions{})
		must(t, err)
		if has, err := w.HasChanges(ctx, dir, base); err != nil || has != d.HasChanges {
			t.Fatalf("HasChanges = %v, %v; DiffAgainstBase.HasChanges = %v", has, err, d.HasChanges)
		}
		if d.HasChanges != (len(d.Files) > 0) {
			t.Fatalf("HasChanges %v with %d files", d.HasChanges, len(d.Files))
		}
		return d
	}
	onlyFile := func(t *testing.T, d git.BaseDiff, path, status string) git.BaseFile {
		t.Helper()
		if len(d.Files) != 1 || d.Files[0].Path != path || d.Files[0].Status != status {
			t.Fatalf("files = %+v, want only %s %s", d.Files, status, path)
		}
		return d.Files[0]
	}

	run("a worktree with no change has no diff", func(t *testing.T, w LandingWorld, h LandingHarness) {
		dir, base := h.Worktree("none")
		d := diff(t, w, dir, base)
		if d.HasChanges || len(d.Files) != 0 || d.Truncated {
			t.Fatalf("diff = %+v, want empty", d)
		}
	})

	run("committed changes only", func(t *testing.T, w LandingWorld, h LandingHarness) {
		dir, base := h.Worktree("committed")
		h.WriteFile(dir, "a.txt", "one\ntwo\n")
		h.CommitAll(dir, "add a")
		f := onlyFile(t, diff(t, w, dir, base), "a.txt", git.FileAdded)
		if f.Added != 2 || f.Deleted != 0 || f.Binary || f.EOLOnly || f.Hash == "" || !strings.Contains(f.Patch, "+one") {
			t.Fatalf("file = %+v", f)
		}
	})

	run("uncommitted changes only", func(t *testing.T, w LandingWorld, h LandingHarness) {
		dir, base := h.Worktree("uncommitted")
		h.WriteFile(dir, "README.md", "changed\n")
		f := onlyFile(t, diff(t, w, dir, base), "README.md", git.FileModified)
		if f.Added != 1 || f.Deleted != 1 || f.EOLOnly || !strings.Contains(f.Patch, "-hello") || !strings.Contains(f.Patch, "+changed") {
			t.Fatalf("file = %+v", f)
		}
	})

	run("untracked files appear as added files", func(t *testing.T, w LandingWorld, h LandingHarness) {
		dir, base := h.Worktree("untracked")
		rel := "new dir/ü new.txt"
		h.WriteFile(dir, rel, "a\nb\n")
		f := onlyFile(t, diff(t, w, dir, base), rel, git.FileAdded)
		if f.Added != 2 || f.Deleted != 0 || !strings.Contains(f.Patch, "+a") {
			t.Fatalf("file = %+v", f)
		}
	})

	run("committed, uncommitted and untracked together; a rename is a removal plus an addition", func(t *testing.T, w LandingWorld, h LandingHarness) {
		h.CommitMain("old name.txt", "same content\nline two\nline three\n")
		dir, base := h.Worktree("mixed")
		h.WriteFile(dir, "c.txt", "committed\n")
		h.CommitAll(dir, "add c")
		h.WriteFile(dir, "README.md", "edited\n")
		h.WriteFile(dir, "u.txt", "untracked\n")
		h.MoveFile(dir, "old name.txt", "z new name.txt")
		d := diff(t, w, dir, base)
		var got []string
		for _, f := range d.Files {
			got = append(got, f.Status+" "+f.Path)
		}
		if !slices.IsSortedFunc(d.Files, func(a, b git.BaseFile) int { return strings.Compare(a.Path, b.Path) }) {
			t.Fatalf("files not sorted by path: %q", got)
		}
		want := []string{"M README.md", "A c.txt", "A u.txt", "A z new name.txt", "D old name.txt"}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("files = %q, want %q", got, want)
		}
	})

	run("a line-ending-only change is flagged and still counts as a change", func(t *testing.T, w LandingWorld, h LandingHarness) {
		h.CommitMain("eol.txt", "l1\nl2\nl3\n")
		h.CommitMain("both.txt", "l1\nl2\nl3\n")
		dir, base := h.Worktree("eol")
		h.WriteFile(dir, "eol.txt", "l1\r\nl2\r\nl3\r\n")
		h.WriteFile(dir, "both.txt", "l1\r\nCHANGED\r\nl3\r\n")
		d := diff(t, w, dir, base)
		if len(d.Files) != 2 || !d.HasChanges {
			t.Fatalf("diff = %+v", d)
		}
		for _, f := range d.Files {
			switch f.Path {
			case "both.txt":
				if f.EOLOnly {
					t.Errorf("a real change was flagged EOL-only: %+v", f)
				}
			case "eol.txt":
				if !f.EOLOnly || f.Status != git.FileModified {
					t.Errorf("a pure line-ending change was not flagged: %+v", f)
				}
			default:
				t.Errorf("unexpected file %+v", f)
			}
		}
		// The user's files and settings are untouched.
		if got, _ := h.ReadFile(dir, "eol.txt"); got != "l1\r\nl2\r\nl3\r\n" {
			t.Errorf("DiffAgainstBase rewrote eol.txt: %q", got)
		}
	})

	run("the file hash is stable and follows the change", func(t *testing.T, w LandingWorld, h LandingHarness) {
		dir, base := h.Worktree("hash")
		h.WriteFile(dir, "README.md", "one\n")
		h.WriteFile(dir, "u.txt", "untracked\n")
		hashes := func() map[string]string {
			m := map[string]string{}
			for _, f := range diff(t, w, dir, base).Files {
				m[f.Path] = f.Hash
			}
			return m
		}
		h1, h2 := hashes(), hashes()
		if len(h1) != 2 || h1["README.md"] == "" || h1["u.txt"] == "" || h1["README.md"] == h1["u.txt"] {
			t.Fatalf("hashes = %v", h1)
		}
		for k, v := range h1 {
			if h2[k] != v {
				t.Fatalf("hash of %s not stable: %s vs %s", k, v, h2[k])
			}
		}
		h.WriteFile(dir, "README.md", "two\n")
		h3 := hashes()
		if h3["README.md"] == h1["README.md"] {
			t.Error("tracked file: hash did not change with the patch")
		}
		if h3["u.txt"] != h1["u.txt"] {
			t.Error("untracked file: hash changed although the file did not")
		}
		h.WriteFile(dir, "u.txt", "changed\n")
		if h4 := hashes(); h4["u.txt"] == h3["u.txt"] {
			t.Error("untracked file: hash did not change with the content")
		}
		// Committing the same content keeps the hash: the patch against the base is the same.
		h.WriteFile(dir, "README.md", "two\n")
		before := hashes()["README.md"]
		h.CommitAll(dir, "commit all")
		if after := hashes()["README.md"]; after != before {
			t.Error("committing the same content changed the hash")
		}
	})

	run("a binary file has no patch and its hash follows the content", func(t *testing.T, w LandingWorld, h LandingHarness) {
		dir, base := h.Worktree("bin")
		h.WriteFile(dir, "img.bin", "\x00\x01\x02 one")
		f := onlyFile(t, diff(t, w, dir, base), "img.bin", git.FileAdded)
		if !f.Binary || f.Patch != "" || f.Hash == "" {
			t.Fatalf("file = %+v", f)
		}
		h.WriteFile(dir, "img.bin", "\x00\x01\x02 two")
		g := onlyFile(t, diff(t, w, dir, base), "img.bin", git.FileAdded)
		if g.Hash == f.Hash {
			t.Fatal("binary file: hash did not change with the content")
		}
	})

	run("patches and file lists are capped and say so", func(t *testing.T, w LandingWorld, h LandingHarness) {
		dir, base := h.Worktree("caps")
		big := strings.Repeat("a long line of text\n", 500)
		h.WriteFile(dir, "big.txt", big)
		d, err := w.DiffAgainstBase(ctx, dir, base, git.BaseDiffOptions{PatchLimit: 200})
		must(t, err)
		f := onlyFile(t, d, "big.txt", git.FileAdded)
		if !f.PatchTruncated || !d.Truncated || len(f.Patch) > 200 || f.Hash == "" {
			t.Fatalf("file = %d bytes of patch, truncated %v/%v", len(f.Patch), f.PatchTruncated, d.Truncated)
		}
		// A change past the cut still changes the hash.
		h.WriteFile(dir, "big.txt", big+"one more line\n")
		d2, err := w.DiffAgainstBase(ctx, dir, base, git.BaseDiffOptions{PatchLimit: 200})
		must(t, err)
		if d2.Files[0].Hash == f.Hash {
			t.Fatal("a change past the patch limit left the hash unchanged")
		}
		for _, n := range []string{"f1.txt", "f2.txt", "f3.txt"} {
			h.WriteFile(dir, n, "x\n")
		}
		d3, err := w.DiffAgainstBase(ctx, dir, base, git.BaseDiffOptions{MaxFiles: 2})
		must(t, err)
		if len(d3.Files) != 2 || !d3.Truncated || !d3.HasChanges {
			t.Fatalf("file cap: %d files, truncated %v", len(d3.Files), d3.Truncated)
		}
	})

	run("DiffAgainstBase refuses a base that is not an exact commit id", func(t *testing.T, w LandingWorld, h LandingHarness) {
		dir, _ := h.Worktree("badbase")
		for _, bad := range []string{"", "main", "HEAD", "-x", strings.Repeat("A", 40)} {
			if _, err := w.DiffAgainstBase(ctx, dir, bad, git.BaseDiffOptions{}); !errors.Is(err, git.ErrInvalidArg) {
				t.Errorf("DiffAgainstBase(%q) = %v, want ErrInvalidArg", bad, err)
			}
			if _, err := w.HasChanges(ctx, dir, bad); !errors.Is(err, git.ErrInvalidArg) {
				t.Errorf("HasChanges(%q) = %v, want ErrInvalidArg", bad, err)
			}
		}
	})
}
