package gittest

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/git"
)

// Harness arranges the world a Writer works in. Dir is a repo on the task
// branch Branch() (not the default branch), with one commit and a remote
// "origin" that has no copy of Branch() yet.
type Harness interface {
	Dir() string
	Branch() string
	// WriteFile creates or changes a working file.
	WriteFile(rel, content string)
	// RemoteTip is the commit the remote has for branch ("" when none).
	RemoteTip(branch string) string
	// AdvanceRemote makes the remote's branch move, as another person's push.
	AdvanceRemote(branch string)
	// SetOriginURL points remote "origin" at url.
	SetOriginURL(url string)
	// InstallHooks installs hooks that record that they ran, in .git/hooks and
	// through core.hooksPath. ok is false when the world has no hooks.
	InstallHooks() (ran func() bool, ok bool)
	// UnsetAuthor makes git unable to know the user; false when not possible.
	UnsetAuthor() bool
}

// GitWriterContract runs the clauses of docs/design/proposals.md "GitWriter"
// against newWorld, which returns a fresh Writer and Harness per call. Run it
// against the fake and against real git (NewReal).
func GitWriterContract(t *testing.T, newWorld func(t *testing.T) (git.Writer, Harness)) {
	t.Helper()
	ctx := context.Background()
	run := func(name string, fn func(t *testing.T, w git.Writer, h Harness)) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w, h := newWorld(t)
			fn(t, w, h)
		})
	}

	run("commit creates one commit with the message and parent", func(t *testing.T, w git.Writer, h Harness) {
		before, err := w.Head(ctx, h.Dir())
		must(t, err)
		h.WriteFile("src/a.txt", "one\n")
		h.WriteFile("b.txt", "not in this commit\n")
		must(t, w.Add(ctx, h.Dir(), []string{"src/a.txt"}))
		id, err := w.Commit(ctx, h.Dir(), []string{"src/a.txt"}, "feat: add a\n\nbody", before)
		must(t, err)
		info, err := w.HeadInfo(ctx, h.Dir())
		must(t, err)
		if info.ID != id || info.Parent != before || info.Message != "feat: add a\n\nbody" {
			t.Fatalf("HEAD = %+v, want id %s parent %s message %q", info, id, before, "feat: add a\n\nbody")
		}
		// b.txt was not part of the commit: it can still be committed on its own.
		must(t, w.Add(ctx, h.Dir(), []string{"b.txt"}))
		if _, err := w.Commit(ctx, h.Dir(), []string{"b.txt"}, "chore: add b", id); err != nil {
			t.Fatalf("b.txt was consumed by the first commit: %v", err)
		}
	})

	run("commit with a stale head is refused and changes nothing", func(t *testing.T, w git.Writer, h Harness) {
		before, err := w.Head(ctx, h.Dir())
		must(t, err)
		h.WriteFile("a.txt", "one\n")
		must(t, w.Add(ctx, h.Dir(), []string{"a.txt"}))
		_, err = w.Commit(ctx, h.Dir(), []string{"a.txt"}, "feat: a", strings.Repeat("a", 40))
		if !errors.Is(err, git.ErrStaleHead) {
			t.Fatalf("stale head = %v, want ErrStaleHead", err)
		}
		if after, _ := w.Head(ctx, h.Dir()); after != before {
			t.Fatalf("HEAD moved from %s to %s", before, after)
		}
	})

	run("commit with nothing to commit is refused", func(t *testing.T, w git.Writer, h Harness) {
		h.WriteFile("a.txt", "one\n")
		must(t, w.Add(ctx, h.Dir(), []string{"a.txt"}))
		head, _ := w.Head(ctx, h.Dir())
		if _, err := w.Commit(ctx, h.Dir(), []string{"a.txt"}, "feat: a", head); err != nil {
			t.Fatal(err)
		}
		_, err := w.Commit(ctx, h.Dir(), []string{"a.txt"}, "feat: again", "")
		if !errors.Is(err, git.ErrNothingToCommit) {
			t.Fatalf("second commit of the same file = %v, want ErrNothingToCommit", err)
		}
	})

	run("commit with an unknown author says so", func(t *testing.T, w git.Writer, h Harness) {
		if !h.UnsetAuthor() {
			t.Skip("this world cannot unset the author")
		}
		h.WriteFile("a.txt", "one\n")
		must(t, w.Add(ctx, h.Dir(), []string{"a.txt"}))
		_, err := w.Commit(ctx, h.Dir(), []string{"a.txt"}, "feat: a", "")
		if !errors.Is(err, git.ErrNoAuthor) {
			t.Fatalf("commit without author = %v, want ErrNoAuthor", err)
		}
		if msg := git.UserMessage(err); !strings.Contains(msg, "user.name") && !strings.Contains(strings.ToLower(msg), "who you are") {
			t.Errorf("message does not say how to set the author: %q", msg)
		}
	})

	run("diff hash follows the content of the paths", func(t *testing.T, w git.Writer, h Harness) {
		h.WriteFile("a.txt", "one\n")
		h1, err := w.DiffHash(ctx, h.Dir(), []string{"a.txt"})
		must(t, err)
		h2, _ := w.DiffHash(ctx, h.Dir(), []string{"a.txt"})
		if h1 != h2 || h1 == "" {
			t.Fatalf("hash not stable: %q vs %q", h1, h2)
		}
		h.WriteFile("a.txt", "two\n")
		if h3, _ := w.DiffHash(ctx, h.Dir(), []string{"a.txt"}); h3 == h1 {
			t.Fatal("hash did not change when the file changed")
		}
		h.WriteFile("other.txt", "x\n")
		h5, _ := w.DiffHash(ctx, h.Dir(), []string{"a.txt"})
		h.WriteFile("other.txt", "y\n")
		if h6, _ := w.DiffHash(ctx, h.Dir(), []string{"a.txt"}); h6 != h5 {
			t.Fatal("a file outside the paths changed the hash")
		}
	})

	commitOne := func(t *testing.T, w git.Writer, h Harness) string {
		t.Helper()
		h.WriteFile("a.txt", "one\n")
		must(t, w.Add(ctx, h.Dir(), []string{"a.txt"}))
		id, err := w.Commit(ctx, h.Dir(), []string{"a.txt"}, "feat: a", "")
		must(t, err)
		return id
	}

	run("push moves the remote ref", func(t *testing.T, w git.Writer, h Harness) {
		id := commitOne(t, w, h)
		res, err := w.Push(ctx, h.Dir(), git.PushRequest{Remote: "origin", Branch: h.Branch(), ExpectHead: id})
		must(t, err)
		_ = res
		if got := h.RemoteTip(h.Branch()); got != id {
			t.Fatalf("remote tip = %q, want %q", got, id)
		}
		if got, err := w.LsRemote(ctx, h.Dir(), "origin", h.Branch()); err != nil || got != id {
			t.Fatalf("LsRemote = %q, %v; want %q", got, err, id)
		}
		if got, err := w.LsRemote(ctx, h.Dir(), "origin", "ygg/never-pushed"); err != nil || got != "" {
			t.Fatalf("LsRemote of a missing branch = %q, %v; want empty", got, err)
		}
	})

	run("push of a non-fast-forward is refused and nothing moves", func(t *testing.T, w git.Writer, h Harness) {
		id := commitOne(t, w, h)
		_, err := w.Push(ctx, h.Dir(), git.PushRequest{Remote: "origin", Branch: h.Branch(), ExpectHead: id})
		must(t, err)
		h.AdvanceRemote(h.Branch())
		theirs := h.RemoteTip(h.Branch())
		h.WriteFile("c.txt", "two\n")
		must(t, w.Add(ctx, h.Dir(), []string{"c.txt"}))
		id2, err := w.Commit(ctx, h.Dir(), []string{"c.txt"}, "feat: c", id)
		must(t, err)
		_, err = w.Push(ctx, h.Dir(), git.PushRequest{Remote: "origin", Branch: h.Branch(), ExpectHead: id2})
		var pe *git.PushError
		if !errors.As(err, &pe) || pe.Kind != git.PushNonFastForward || !errors.Is(err, git.ErrPushRejected) {
			t.Fatalf("non-ff push = %v, want PushError{NonFastForward}", err)
		}
		if got := h.RemoteTip(h.Branch()); got != theirs {
			t.Fatalf("remote tip moved to %q (was %q): a refused push must change nothing", got, theirs)
		}
	})

	run("push with a stale branch tip is refused", func(t *testing.T, w git.Writer, h Harness) {
		commitOne(t, w, h)
		_, err := w.Push(ctx, h.Dir(), git.PushRequest{Remote: "origin", Branch: h.Branch(), ExpectHead: strings.Repeat("b", 40)})
		if !errors.Is(err, git.ErrStaleHead) {
			t.Fatalf("stale push = %v, want ErrStaleHead", err)
		}
		if got := h.RemoteTip(h.Branch()); got != "" {
			t.Fatalf("remote has %q after a refused push", got)
		}
	})

	run("push to the default branch is refused", func(t *testing.T, w git.Writer, h Harness) {
		_, err := w.Push(ctx, h.Dir(), git.PushRequest{Remote: "origin", Branch: "main"})
		if !errors.Is(err, git.ErrDefaultBranch) {
			t.Fatalf("push to main = %v, want ErrDefaultBranch", err)
		}
	})

	run("credentials in the remote URL are refused and nothing is sent", func(t *testing.T, w git.Writer, h Harness) {
		id := commitOne(t, w, h)
		for _, u := range []string{"https://user:s3cret@git.example.test/acme/shop-api.git", "https://s3cr3ttoken@git.example.test/acme/shop-api.git"} {
			h.SetOriginURL(u)
			_, err := w.Push(ctx, h.Dir(), git.PushRequest{Remote: "origin", Branch: h.Branch(), ExpectHead: id})
			if !errors.Is(err, git.ErrUnsafeRemote) {
				t.Fatalf("push to %q = %v, want ErrUnsafeRemote", "credential URL", err)
			}
			if strings.Contains(err.Error(), "s3cr") || strings.Contains(git.UserMessage(err), "s3cr") {
				t.Fatalf("error holds the credential: %v", err)
			}
			if _, err := w.LsRemote(ctx, h.Dir(), "origin", h.Branch()); !errors.Is(err, git.ErrUnsafeRemote) {
				t.Fatalf("ls-remote of %q = %v, want ErrUnsafeRemote", "credential URL", err)
			}
		}
	})

	run("hooks do not run", func(t *testing.T, w git.Writer, h Harness) {
		ran, ok := h.InstallHooks()
		if !ok {
			t.Skip("this world has no hooks")
		}
		id := commitOne(t, w, h)
		_, err := w.Push(ctx, h.Dir(), git.PushRequest{Remote: "origin", Branch: h.Branch(), ExpectHead: id})
		must(t, err)
		if ran() {
			t.Fatal("a repo hook ran during a proposal commit or push")
		}
	})
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// NewFakeWorld returns a world on the in-memory fake.
func NewFakeWorld(t *testing.T) (git.Writer, Harness) {
	f := NewFake()
	const dir, branch, url = "/fake/shop-api", "ygg/swift-fern", "https://git.example.test/acme/shop-api.git"
	f.AddRepo(dir, "main")
	f.AddRemote(dir, "origin", url)
	f.SetBranch(dir, branch)
	return f, &fakeHarness{f: f, dir: dir, branch: branch, url: url}
}

type fakeHarness struct {
	f           *Fake
	dir, branch string
	url         string
}

func (h *fakeHarness) Dir() string                       { return h.dir }
func (h *fakeHarness) Branch() string                    { return h.branch }
func (h *fakeHarness) WriteFile(rel, c string)           { h.f.Write(h.dir, rel, c) }
func (h *fakeHarness) RemoteTip(b string) string         { return h.f.RemoteTip(h.url, b) }
func (h *fakeHarness) AdvanceRemote(b string)            { h.f.AdvanceRemote(h.url, b) }
func (h *fakeHarness) SetOriginURL(u string)             { h.f.SetRemoteURL(h.dir, "origin", u) }
func (h *fakeHarness) InstallHooks() (func() bool, bool) { return nil, false }
func (h *fakeHarness) UnsetAuthor() bool                 { h.f.SetNoAuthor(h.dir, true); return true }
