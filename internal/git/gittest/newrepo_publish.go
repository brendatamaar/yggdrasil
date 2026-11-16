package gittest

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/brendatamaar/yggdrasil/internal/git"
)

// PublishWorld is what the PublishTransport contract suite needs: a local repo
// with one commit on a branch, a remote that has none of it, and a way to
// observe what reached the remote.
type PublishWorld interface {
	Transport() git.PublishTransport
	// Request is a valid request for the world's repo and remote.
	Request() git.PublishRequest
	// RemoteHead is the remote's head of Request().Branch, "" when absent.
	RemoteHead() string
	// Pushes is the number of pushes that reached the remote and were applied.
	Pushes() int
	// DivergeRemote gives the remote branch a head the local repo does not have.
	DivergeRemote()
	// LoseNextReply makes the next push land on the remote while its reply is lost.
	LoseNextReply()
	// AdvanceLocal moves the local branch, so Request().ExpectedHead is stale.
	AdvanceLocal()
}

// RunPublishTransportContract is the contract of git.PublishTransport. Run it
// against the fake and the real implementation.
func RunPublishTransportContract(t *testing.T, newWorld func(t *testing.T) PublishWorld) {
	ctx := context.Background()

	t.Run("absent branch, publish, then reconcile", func(t *testing.T) {
		w := newWorld(t)
		tr, req := w.Transport(), w.Request()
		ref, err := tr.RemoteBranch(ctx, req)
		if err != nil || ref.Exists {
			t.Fatalf("RemoteBranch before = %+v, %v; want absent", ref, err)
		}
		res, err := tr.PublishBranch(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		if res.Head != req.ExpectedHead || w.RemoteHead() != req.ExpectedHead {
			t.Fatalf("result %q, remote %q, want %q", res.Head, w.RemoteHead(), req.ExpectedHead)
		}
		ref, err = tr.RemoteBranch(ctx, req)
		if err != nil || !ref.Exists || ref.Head != req.ExpectedHead {
			t.Fatalf("RemoteBranch after = %+v, %v", ref, err)
		}
	})

	t.Run("the exact head already there is success and sends nothing", func(t *testing.T) {
		w := newWorld(t)
		tr, req := w.Transport(), w.Request()
		if _, err := tr.PublishBranch(ctx, req); err != nil {
			t.Fatal(err)
		}
		before := w.Pushes()
		res, err := tr.PublishBranch(ctx, req)
		if err != nil || res.Head != req.ExpectedHead {
			t.Fatalf("second publish = %+v, %v", res, err)
		}
		if w.Pushes() != before {
			t.Fatal("the second publish sent a push")
		}
	})

	t.Run("a different remote head is a conflict and is not overwritten", func(t *testing.T) {
		w := newWorld(t)
		tr, req := w.Transport(), w.Request()
		w.DivergeRemote()
		other := w.RemoteHead()
		if _, err := tr.PublishBranch(ctx, req); !errors.Is(err, git.ErrRemoteDiverged) {
			t.Fatalf("err = %v, want ErrRemoteDiverged", err)
		}
		if w.RemoteHead() != other || w.Pushes() != 0 {
			t.Fatal("the remote branch was changed")
		}
		ref, err := tr.RemoteBranch(ctx, req)
		if err != nil || !ref.Exists || ref.Head != other {
			t.Fatalf("RemoteBranch = %+v, %v; want the other head %s", ref, err, other)
		}
	})

	t.Run("an expected head that is not the local head sends nothing", func(t *testing.T) {
		w := newWorld(t)
		tr, req := w.Transport(), w.Request()
		w.AdvanceLocal()
		if _, err := tr.PublishBranch(ctx, req); err == nil {
			t.Fatal("a stale expected head was published")
		}
		if w.Pushes() != 0 || w.RemoteHead() != "" {
			t.Fatal("something reached the remote")
		}
		bad := req
		bad.ExpectedHead = "not-a-commit"
		if _, err := tr.PublishBranch(ctx, bad); err == nil {
			t.Fatal("a malformed expected head was accepted")
		}
	})

	t.Run("a URL with credentials is refused and sends nothing", func(t *testing.T) {
		w := newWorld(t)
		tr, req := w.Transport(), w.Request()
		for _, bad := range []string{
			strings.Replace(req.URL, "https://", "https://user:secret@", 1),
			strings.Replace(req.URL, "https://", "https://secret@", 1),
			req.URL + "?access_token=secret",
			req.URL + "#secret",
			strings.Replace(req.URL, "https://", "http://", 1),
		} {
			r := req
			r.URL = bad
			for name, call := range map[string]func() error{
				"publish": func() error { _, err := tr.PublishBranch(ctx, r); return err },
				"remote":  func() error { _, err := tr.RemoteBranch(ctx, r); return err },
			} {
				err := call()
				if err == nil {
					t.Fatalf("%s accepted %q", name, bad)
				}
				if strings.Contains(err.Error()+git.UserMessage(err), "secret") {
					t.Fatalf("%s error echoes the secret: %v", name, err)
				}
			}
		}
		if w.Pushes() != 0 {
			t.Fatal("something reached the remote")
		}
	})

	t.Run("a lost reply is unknown, and reading the remote settles it", func(t *testing.T) {
		w := newWorld(t)
		tr, req := w.Transport(), w.Request()
		w.LoseNextReply()
		_, err := tr.PublishBranch(ctx, req)
		if !errors.Is(err, git.ErrPublishUnknown) {
			t.Fatalf("err = %v, want ErrPublishUnknown", err)
		}
		ref, err := tr.RemoteBranch(ctx, req)
		if err != nil || !ref.Exists || ref.Head != req.ExpectedHead {
			t.Fatalf("reconcile = %+v, %v; want the pushed head", ref, err)
		}
		res, err := tr.PublishBranch(ctx, req)
		if err != nil || res.Head != req.ExpectedHead || w.Pushes() != 1 {
			t.Fatalf("retry = %+v, %v, pushes %d", res, err, w.Pushes())
		}
	})
}

// FakePublishWorld is the PublishWorld over FakePublishTransport.
type FakePublishWorld struct {
	T   *FakePublishTransport
	req git.PublishRequest
}

var _ PublishWorld = (*FakePublishWorld)(nil)

const fakeHead = "1111111111111111111111111111111111111111"

// NewFakePublishWorld is the factory for the contract suite over the fake.
func NewFakePublishWorld(t *testing.T) PublishWorld {
	t.Helper()
	tr := NewFakePublishTransport()
	req := git.PublishRequest{Dir: filepath.Join("fake root", "acme shop api"), URL: "https://git.example.test/acme/shop-api", Branch: "main", ExpectedHead: fakeHead}
	tr.SetLocalHead(req.Dir, req.Branch, fakeHead)
	return &FakePublishWorld{T: tr, req: req}
}

func (w *FakePublishWorld) Transport() git.PublishTransport { return w.T }
func (w *FakePublishWorld) Request() git.PublishRequest     { return w.req }
func (w *FakePublishWorld) RemoteHead() string              { return w.T.RemoteHead(w.req.URL, w.req.Branch) }
func (w *FakePublishWorld) Pushes() int                     { return w.T.Pushes() }
func (w *FakePublishWorld) DivergeRemote() {
	w.T.SetRemoteHead(w.req.URL, w.req.Branch, "2222222222222222222222222222222222222222")
}
func (w *FakePublishWorld) LoseNextReply() {
	w.T.Faults.Inject("publish", w.T.Faults.Calls("publish")+1, LostReply, git.ErrPublishUnknown)
}
func (w *FakePublishWorld) AdvanceLocal() {
	w.T.SetLocalHead(w.req.Dir, w.req.Branch, "3333333333333333333333333333333333333333")
}

// RealPublishWorld is the PublishWorld over the go-git transport and a local
// TLS smart-HTTP server.
type RealPublishWorld struct {
	T      *git.GoGitTransport
	Server *GitHTTPServer
	Authz  *OriginAuthorizer
	Repo   *gogit.Repository
	req    git.PublishRequest
	path   string
	tt     *testing.T
}

var _ PublishWorld = (*RealPublishWorld)(nil)

// NewRealPublishWorld is the factory for the contract suite over the real
// transport. The local repo is made with go-git, so no git binary is needed.
func NewRealPublishWorld(t *testing.T) PublishWorld {
	t.Helper()
	return NewRealPublishWorldFor(t, MakeLocalRepo(t, "# Shop API\r\nsecond line\r\n"))
}

// MakeLocalRepo creates a repo with one commit on main in a folder whose name
// has a space, and returns its directory.
func MakeLocalRepo(t *testing.T, readme string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "acme shop api")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	repo, err := gogit.PlainInitWithOptions(dir, &gogit.PlainInitOptions{InitOptions: gogit.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")}})
	if err != nil {
		t.Fatal(err)
	}
	commitFiles(t, repo, dir, map[string]string{"README.md": readme, ".gitignore": "node_modules/\n", "docs/notes.txt": "n\n"}, "Initial commit")
	return dir
}

func commitFiles(t *testing.T, repo *gogit.Repository, dir string, files map[string]string, msg string) plumbing.Hash {
	t.Helper()
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	for rel, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := wt.Add(rel); err != nil {
			t.Fatal(err)
		}
	}
	h, err := wt.Commit(msg, &gogit.CommitOptions{Author: &object.Signature{Name: "Ada Test", Email: "ada@example.test", When: time.Unix(1_700_000_000, 0).UTC()}})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// NewRealPublishWorldFor builds the world around an existing local repo dir
// whose branch is main.
func NewRealPublishWorldFor(t *testing.T, dir string) PublishWorld {
	t.Helper()
	srv := NewGitHTTPServer(t)
	const path = "/acme/shop-api"
	srv.AddRepo(path)
	// A repo go-git cannot open (an unsupported format) still gets a world, with
	// no expected head: the transport must refuse it itself.
	var repo *gogit.Repository
	var expected string
	if r, err := gogit.PlainOpen(dir); err == nil {
		repo = r
		if head, err := r.Reference(plumbing.NewBranchReferenceName("main"), true); err == nil {
			expected = head.Hash().String()
		}
	}
	authz := &OriginAuthorizer{Origin: srv.URL, Token: srv.Token}
	tr, err := git.NewGoGitTransport(authz, func() *http.Client { return srv.Client() })
	if err != nil {
		t.Fatal(err)
	}
	return &RealPublishWorld{
		T: tr, Server: srv, Authz: authz, Repo: repo, path: path, tt: t,
		req: git.PublishRequest{Dir: dir, URL: srv.URL + path, Branch: "main", ExpectedHead: expected},
	}
}

func (w *RealPublishWorld) Transport() git.PublishTransport { return w.T }
func (w *RealPublishWorld) Request() git.PublishRequest     { return w.req }
func (w *RealPublishWorld) RemoteHead() string              { return w.Server.Head(w.path, w.req.Branch) }
func (w *RealPublishWorld) Pushes() int                     { return w.Server.Pushes() }
func (w *RealPublishWorld) DivergeRemote()                  { w.Server.Diverge(w.path, w.req.Branch) }
func (w *RealPublishWorld) LoseNextReply()                  { w.Server.LoseNextReply() }
func (w *RealPublishWorld) AdvanceLocal() {
	commitFiles(w.tt, w.Repo, w.req.Dir, map[string]string{"more.txt": "more\n"}, "Second commit")
}

// CommitInRepo adds one commit with files to the repo at dir and returns its ID.
func CommitInRepo(t *testing.T, dir string, files map[string]string, msg string) string {
	t.Helper()
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	return commitFiles(t, repo, dir, files, msg).String()
}
