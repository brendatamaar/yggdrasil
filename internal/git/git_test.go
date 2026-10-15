package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/platform"
)

func TestNewRefusesNilEnv(t *testing.T) {
	if _, err := New(nil); !errors.Is(err, platform.ErrNoEnv) {
		t.Fatalf("New(nil) = %v, want ErrNoEnv", err)
	}
}

func TestStatusSpacesUnicodeCRLF(t *testing.T) {
	f := newFx(t)
	dir := filepath.Join(t.TempDir(), "my repo with spaces")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	f.git(dir, "init", "-q", "-b", "main")
	f.commit(dir, "crlf.txt", "one\r\ntwo\r\n", "add crlf")

	f.write(dir, "crlf.txt", "one\r\ntwo\r\nthree\r\n")
	f.write(dir, "sub dir/café ✓ 日本.txt", "x")
	f.write(dir, "staged.txt", "s")
	f.git(dir, "add", "staged.txt")

	st, err := f.g.Status(bg(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Branch != "main" || st.Detached || st.HasUpstream || st.OID == "" {
		t.Errorf("header: %+v", st)
	}
	if st.Staged != 1 || st.Unstaged != 1 || st.Untracked != 1 {
		t.Errorf("counts: %+v", st)
	}
	got := map[string]FileChange{}
	for _, fc := range st.Files {
		got[fc.Path] = fc
	}
	if got["crlf.txt"].Worktree != "M" || got["staged.txt"].Index != "A" ||
		got["sub dir/café ✓ 日本.txt"].Worktree != "?" {
		t.Errorf("files: %+v", st.Files)
	}
}

func TestStatusRename(t *testing.T) {
	f := newFx(t)
	dir := f.repo("r")
	f.commit(dir, "old.txt", "some content that is long enough to match\n", "init")
	f.git(dir, "mv", "old.txt", "new.txt")
	st, err := f.g.Status(bg(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Files) != 1 || st.Files[0].Path != "new.txt" || st.Files[0].OrigPath != "old.txt" || st.Files[0].Index != "R" {
		t.Errorf("files: %+v", st.Files)
	}
}

func TestStatusLongPath(t *testing.T) {
	f := newFx(t)
	dir := f.repo("lp")
	rel := ""
	for range 6 {
		rel += strings.Repeat("d", 50) + "/"
	}
	rel += "file.txt"
	f.write(dir, rel, "x")
	if len(filepath.Join(dir, rel)) < 300 {
		t.Fatalf("path too short for the test: %d", len(filepath.Join(dir, rel)))
	}
	st, err := f.g.Status(bg(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Files) != 1 || st.Files[0].Path != rel {
		t.Errorf("files: %+v", st.Files)
	}
	f.git(dir, "add", "-A") // needs core.longpaths on Windows
	f.git(dir, "commit", "-q", "-m", "long")
}

func TestStatusUntrackedHeavy(t *testing.T) {
	f := newFx(t)
	dir := f.repo("bulk")
	f.commit(dir, "a.txt", "a", "init")
	const n = maxStatusFiles + 500
	if err := os.MkdirAll(filepath.Join(dir, "bulk"), 0o750); err != nil {
		t.Fatal(err)
	}
	for i := range n {
		if err := os.WriteFile(filepath.Join(dir, "bulk", fmt.Sprintf("f%05d.txt", i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	st, err := f.g.Status(bg(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Untracked != n || len(st.Files) != maxStatusFiles || !st.Truncated {
		t.Errorf("untracked=%d files=%d truncated=%v", st.Untracked, len(st.Files), st.Truncated)
	}
	if st.Dirty() {
		t.Error("untracked files must not make the tree dirty")
	}
}

func TestStatusNotARepo(t *testing.T) {
	f := newFx(t)
	_, err := f.g.Status(bg(), t.TempDir())
	if err == nil {
		t.Fatal("want error")
	}
	if msg := UserMessage(err); !strings.Contains(msg, "not a git repository") {
		t.Errorf("user message = %q", msg)
	}
}

func TestDetachedHead(t *testing.T) {
	f := newFx(t)
	dir := f.repo("d")
	f.commit(dir, "a.txt", "a", "one")
	f.commit(dir, "a.txt", "b", "two")
	f.git(dir, "checkout", "-q", "--detach", "HEAD~1")

	st, err := f.g.Status(bg(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Detached || st.Branch != "" {
		t.Errorf("status: %+v", st)
	}
	brs, err := f.g.Branches(bg(), dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range brs {
		if b.Current {
			t.Errorf("no branch should be current: %+v", b)
		}
	}
	var ff *FFError
	if err := f.g.PullFFOnly(bg(), dir); !errors.As(err, &ff) || ff.Reason != FFDetached {
		t.Errorf("PullFFOnly = %v", err)
	}
}

func TestNoUpstream(t *testing.T) {
	f := newFx(t)
	dir := f.repo("n")
	f.commit(dir, "a.txt", "a", "one")
	st, _ := f.g.Status(bg(), dir)
	if st.HasUpstream || st.Ahead != 0 || st.Behind != 0 {
		t.Errorf("status: %+v", st)
	}
	err := f.g.PullFFOnly(bg(), dir)
	var ff *FFError
	if !errors.As(err, &ff) || ff.Reason != FFNoUpstream {
		t.Fatalf("PullFFOnly = %v", err)
	}
	if !strings.Contains(UserMessage(err), "does not track") {
		t.Errorf("user message = %q", UserMessage(err))
	}
}

func TestDivergedAheadBehind(t *testing.T) {
	f := newFx(t)
	a, b := f.pair()
	f.commit(a, "a.txt", "from a", "remote work")
	f.git(a, "push", "-q")
	f.commit(b, "b.txt", "from b", "local work")

	if err := f.g.Fetch(bg(), b, ""); err != nil {
		t.Fatal(err)
	}
	st, err := f.g.Status(bg(), b)
	if err != nil {
		t.Fatal(err)
	}
	if st.Ahead != 1 || st.Behind != 1 || st.Upstream != "origin/main" {
		t.Fatalf("status: %+v", st)
	}

	head := f.git(b, "rev-parse", "HEAD")
	err = f.g.PullFFOnly(bg(), b)
	var ff *FFError
	if !errors.As(err, &ff) || ff.Reason != FFDiverged || ff.Ahead != 1 || ff.Behind != 1 {
		t.Fatalf("PullFFOnly = %v", err)
	}
	msg := UserMessage(err)
	if !strings.Contains(msg, "1 ahead, 1 behind") || !strings.Contains(msg, "merge or rebase") {
		t.Errorf("user message = %q", msg)
	}
	if f.git(b, "rev-parse", "HEAD") != head {
		t.Error("failed pull moved HEAD")
	}
}

func TestPullFFOnlyAdvances(t *testing.T) {
	f := newFx(t)
	a, b := f.pair()
	f.commit(a, "new.txt", "n", "next")
	f.git(a, "push", "-q")
	if err := f.g.PullFFOnly(bg(), b); err != nil {
		t.Fatal(err)
	}
	log, err := f.g.Log(bg(), b, 10)
	if err != nil || len(log) != 2 || log[0].Subject != "next" {
		t.Errorf("log = %+v, err %v", log, err)
	}
}

func TestPullFFOnlyDirtyOverwrite(t *testing.T) {
	f := newFx(t)
	a, b := f.pair()
	f.commit(a, "README.md", "changed upstream\n", "edit")
	f.git(a, "push", "-q")
	f.write(b, "README.md", "local edit\n")

	err := f.g.PullFFOnly(bg(), b)
	var ff *FFError
	if !errors.As(err, &ff) || ff.Reason != FFDirty {
		t.Fatalf("PullFFOnly = %v", err)
	}
	if !strings.Contains(UserMessage(err), "Commit or stash") {
		t.Errorf("user message = %q", UserMessage(err))
	}
}

func TestCheckoutRefusesDirtyTree(t *testing.T) {
	f := newFx(t)
	dir := f.repo("c")
	f.commit(dir, "a.txt", "a", "one")
	f.git(dir, "branch", "other")

	f.write(dir, "untracked.txt", "u") // does not block
	f.write(dir, "a.txt", "changed")
	err := f.g.Checkout(bg(), dir, "other")
	if !errors.Is(err, ErrDirty) {
		t.Fatalf("Checkout = %v, want ErrDirty", err)
	}
	if !strings.Contains(UserMessage(err), "Commit or stash") {
		t.Errorf("user message = %q", UserMessage(err))
	}
	if st, _ := f.g.Status(bg(), dir); st.Branch != "main" {
		t.Errorf("branch = %q after refused checkout", st.Branch)
	}

	f.git(dir, "checkout", "-q", "--", "a.txt")
	if err := f.g.Checkout(bg(), dir, "other"); err != nil {
		t.Fatal(err)
	}
	if st, _ := f.g.Status(bg(), dir); st.Branch != "other" {
		t.Errorf("branch = %q", st.Branch)
	}
	if err := f.g.Checkout(bg(), dir, "-x"); !errors.Is(err, ErrInvalidArg) {
		t.Errorf("Checkout(-x) = %v", err)
	}
	if err := f.g.Checkout(bg(), dir, "no-such-branch"); err == nil {
		t.Error("Checkout of a missing branch succeeded")
	}
}

func TestBranches(t *testing.T) {
	f := newFx(t)
	_, b := f.pair()
	f.git(b, "branch", "feature")
	brs, err := f.g.Branches(bg(), b)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Branch{}
	for _, br := range brs {
		got[br.Name] = br
	}
	if len(got) != 3 || !got["main"].Current || got["main"].Upstream != "origin/main" ||
		got["feature"].Current || !got["origin/main"].Remote {
		t.Errorf("branches: %+v", brs)
	}
}

func TestLogAndShow(t *testing.T) {
	f := newFx(t)
	dir := f.repo("l")
	if log, err := f.g.Log(bg(), dir, 5); err != nil || len(log) != 0 {
		t.Fatalf("empty repo log = %+v, %v", log, err)
	}
	f.commit(dir, "old.txt", "some content that is long enough to match\n", "first")
	f.git(dir, "mv", "old.txt", "café.txt")
	f.write(dir, "extra.txt", "e")
	f.git(dir, "add", "-A")
	f.git(dir, "commit", "-q", "-m", "second: subject", "-m", "Body line one.\n\nBody line two.")

	log, err := f.g.Log(bg(), dir, 1)
	if err != nil || len(log) != 1 || log[0].Subject != "second: subject" || log[0].Author != "Ada Test" ||
		len(log[0].Parents) != 1 || log[0].Date.IsZero() {
		t.Fatalf("log = %+v, %v", log, err)
	}
	if log, _ := f.g.Log(bg(), dir, 10); len(log) != 2 {
		t.Errorf("log len = %d", len(log))
	}

	d, err := f.g.Show(bg(), dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(d.Body, "Body line one.") || !strings.Contains(d.Body, "Body line two.") {
		t.Errorf("body = %q", d.Body)
	}
	files := map[string]CommitFile{}
	for _, cf := range d.Files {
		files[cf.Path] = cf
	}
	if files["café.txt"].Status != "R" || files["café.txt"].OrigPath != "old.txt" || files["extra.txt"].Status != "A" {
		t.Errorf("files: %+v", d.Files)
	}
	root, err := f.g.Show(bg(), dir, "HEAD~1")
	if err != nil || len(root.Files) != 1 || root.Files[0].Path != "old.txt" {
		t.Errorf("root commit: %+v, %v", root, err)
	}
	if _, err := f.g.Show(bg(), dir, "--output=x"); !errors.Is(err, ErrInvalidArg) {
		t.Errorf("Show(option) = %v", err)
	}
	if _, err := f.g.Show(bg(), dir, "nosuchrev"); err == nil {
		t.Error("Show of a missing rev succeeded")
	}
}

func TestDiff(t *testing.T) {
	f := newFx(t)
	dir := f.repo("df")
	f.commit(dir, "crlf.txt", "one\r\ntwo\r\n", "init")
	f.commit(dir, "other.txt", "o", "second")

	f.write(dir, "crlf.txt", "one\r\nTWO\r\n")
	d, err := f.g.Diff(bg(), dir, DiffOptions{Mode: DiffWorktree})
	if err != nil || !strings.Contains(d, "+TWO\r\n") {
		t.Errorf("worktree diff = %q, %v", d, err)
	}
	if d, _ := f.g.Diff(bg(), dir, DiffOptions{Mode: DiffStaged}); d != "" {
		t.Errorf("staged diff before add = %q", d)
	}
	f.git(dir, "add", "crlf.txt")
	if d, _ := f.g.Diff(bg(), dir, DiffOptions{Mode: DiffStaged}); !strings.Contains(d, "+TWO") {
		t.Errorf("staged diff = %q", d)
	}
	d, err = f.g.Diff(bg(), dir, DiffOptions{Mode: DiffRefs, From: "HEAD~1", To: "HEAD"})
	if err != nil || !strings.Contains(d, "other.txt") {
		t.Errorf("refs diff = %q, %v", d, err)
	}
	d, _ = f.g.Diff(bg(), dir, DiffOptions{Mode: DiffRefs, From: "HEAD~1", To: "HEAD", Path: "crlf.txt"})
	if d != "" {
		t.Errorf("path-limited diff = %q", d)
	}
	if _, err := f.g.Diff(bg(), dir, DiffOptions{Mode: DiffRefs, From: "--output=x"}); !errors.Is(err, ErrInvalidArg) {
		t.Errorf("Diff(option) = %v", err)
	}
}

func TestFetch(t *testing.T) {
	f := newFx(t)
	a, b := f.pair()
	f.commit(a, "x.txt", "x", "more")
	f.git(a, "push", "-q")
	if err := f.g.Fetch(bg(), b, "origin"); err != nil {
		t.Fatal(err)
	}
	if st, _ := f.g.Status(bg(), b); st.Behind != 1 {
		t.Errorf("behind = %d", st.Behind)
	}
	if err := f.g.Fetch(bg(), b, "--upload-pack=x"); !errors.Is(err, ErrInvalidArg) {
		t.Errorf("Fetch(option) = %v", err)
	}
}

func TestWorktreeAddAndList(t *testing.T) {
	f := newFx(t)
	dir := f.repo("w")
	f.commit(dir, "a.txt", "a", "one")
	wt := filepath.Join(t.TempDir(), "wt "+platform.ShortDirName("feature/very-long-branch-name-for-agent", 16))

	w, err := f.g.WorktreeAdd(bg(), dir, wt, "agent/task-1", "")
	if err != nil {
		t.Fatal(err)
	}
	// Compare long forms: the temp dir may be an 8.3 short path on Windows.
	wantPath, _ := platform.LongPath(wt)
	gotPath, _ := platform.LongPath(w.Path)
	if w.Branch != "agent/task-1" || !strings.EqualFold(gotPath, wantPath) {
		t.Errorf("worktree: %+v (want path %s)", w, wt)
	}
	list, err := f.g.WorktreeList(bg(), dir)
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if list[0].Branch != "main" {
		t.Errorf("main worktree: %+v", list[0])
	}
	if _, err := f.g.WorktreeAdd(bg(), dir, filepath.Join(t.TempDir(), "again"), "agent/task-1", ""); err == nil {
		t.Error("reusing an existing branch name succeeded")
	}
	if _, err := f.g.WorktreeAdd(bg(), dir, "relative/path", "b2", ""); !errors.Is(err, ErrInvalidArg) {
		t.Errorf("relative path = %v", err)
	}
}

func TestCancelledContext(t *testing.T) {
	f := newFx(t)
	dir := f.repo("x")
	c, cancel := context.WithCancel(bg())
	cancel()
	if _, err := f.g.Status(c, dir); err == nil {
		t.Fatal("want error")
	}
}
