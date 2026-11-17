package git_test

import (
	"context"
	"crypto/x509"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/git/gittest"
)

func realWorld(t *testing.T) *gittest.RealPublishWorld {
	t.Helper()
	return gittest.NewRealPublishWorld(t).(*gittest.RealPublishWorld)
}

// What lands on the remote is the real history, byte for byte (CRLF kept).
func TestPublishSendsTheExactObjects(t *testing.T) {
	w := realWorld(t)
	req := w.Request()
	if _, err := w.T.PublishBranch(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	st := w.Server.Storage("/acme/shop-api")
	c, err := object.GetCommit(st, plumbing.NewHash(req.ExpectedHead))
	if err != nil {
		t.Fatalf("the commit is not on the remote: %v", err)
	}
	f, err := c.File("README.md")
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.Contents()
	if err != nil || got != "# Shop API\r\nsecond line\r\n" {
		t.Fatalf("README on the remote = %q, %v", got, err)
	}
	if _, err := c.File("docs/notes.txt"); err != nil {
		t.Fatalf("nested file missing: %v", err)
	}
}

// Invariants 3 and 4: the credential goes only into request headers, only for
// the instance origin, and the Authorizer is asked for nothing else.
func TestInvariant_4_PublishAuthorizerSeesOnlyTheInstanceOrigin(t *testing.T) {
	w := realWorld(t)
	req := w.Request()
	ctx := context.Background()
	if _, err := w.T.RemoteBranch(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, err := w.T.PublishBranch(ctx, req); err != nil {
		t.Fatal(err)
	}
	seen := w.Authz.Seen()
	if len(seen) < 3 {
		t.Fatalf("expected a request per step, got %v", seen)
	}
	for _, s := range seen {
		if s != w.Server.URL {
			t.Fatalf("the authorizer was asked about %q, not the instance origin %q", s, w.Server.URL)
		}
	}
	for _, r := range w.Server.Requests() {
		if !r.HasAuth {
			t.Fatalf("request without credentials: %+v", r)
		}
		if strings.Contains(r.Path, w.Server.Token) {
			t.Fatalf("token in a URL path: %q", r.Path)
		}
	}
	// The token is not in the repo's config either (nothing persists it).
	cfg, err := os.ReadFile(filepath.Join(req.Dir, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cfg), w.Server.Token) {
		t.Fatal("the token reached the repo config")
	}
}

// A redirect to another host is refused and no Authorization header is ever
// sent there. The client trusts both servers, so only Yggdrasil's own policy
// can stop the second hop.
func TestPublishRefusesRedirectToAnotherHost(t *testing.T) {
	other := gittest.NewGitHTTPServer(t)
	other.AddRepo("/acme/shop-api")
	w := realWorld(t)
	w.Server.Wrap = func(rw http.ResponseWriter, r *http.Request) bool {
		http.Redirect(rw, r, other.URL+r.URL.Path+"?"+r.URL.RawQuery, http.StatusFound)
		return true
	}
	pool := x509.NewCertPool()
	pool.AddCert(w.Server.Certificate())
	pool.AddCert(other.Certificate())
	base := w.Server.Client()
	base.Transport.(*http.Transport).TLSClientConfig.RootCAs = pool
	tr, err := git.NewGoGitTransport(w.Authz, func() *http.Client { return base })
	if err != nil {
		t.Fatal(err)
	}
	req := w.Request()
	ctx := context.Background()
	if _, err := tr.PublishBranch(ctx, req); err == nil {
		t.Fatal("a cross-host redirect was followed")
	}
	if _, err := tr.RemoteBranch(ctx, req); err == nil {
		t.Fatal("a cross-host redirect was followed on read")
	}
	if n := len(other.Requests()); n != 0 {
		t.Fatalf("the other host received %d requests: %+v", n, other.Requests())
	}
	for _, s := range w.Authz.Seen() {
		if s != w.Server.URL {
			t.Fatalf("authorizer asked about %q", s)
		}
	}
	if other.Pushes() != 0 || w.Pushes() != 0 {
		t.Fatal("something was pushed")
	}
}

// A redirect to another repository on the SAME host is refused too: the
// credential is for the one validated repository path, not for the whole host.
func TestPublishRefusesRedirectToAnotherRepositoryOnTheSameHost(t *testing.T) {
	w := realWorld(t)
	w.Server.AddRepo("/acme/other-repo")
	w.Server.Wrap = func(rw http.ResponseWriter, r *http.Request) bool {
		if strings.HasPrefix(r.URL.Path, "/acme/other-repo") {
			return false
		}
		rest := strings.TrimPrefix(r.URL.Path, "/acme/shop-api")
		http.Redirect(rw, r, w.Server.URL+"/acme/other-repo"+rest+"?"+r.URL.RawQuery, http.StatusFound)
		return true
	}
	tr, err := git.NewGoGitTransport(w.Authz, func() *http.Client { return w.Server.Client() })
	if err != nil {
		t.Fatal(err)
	}
	req := w.Request()
	ctx := context.Background()
	if _, err := tr.PublishBranch(ctx, req); err == nil {
		t.Fatal("a same-host redirect to another repository was followed")
	}
	if _, err := tr.RemoteBranch(ctx, req); err == nil {
		t.Fatal("a same-host redirect to another repository was followed on read")
	}
	for _, r := range w.Server.Requests() {
		if strings.HasPrefix(r.Path, "/acme/other-repo") {
			t.Fatalf("the other repository received a request: %+v", r)
		}
	}
	if w.Pushes() != 0 {
		t.Fatal("something was pushed")
	}
}

// The publish path starts no process: it works with an empty PATH, where no git
// binary can be found, and no token can reach a child's environment.
func TestPublishStartsNoSubprocess(t *testing.T) {
	w := realWorld(t)
	empty := t.TempDir()
	t.Setenv("PATH", empty)
	t.Setenv("GIT_EXEC_PATH", empty)
	req := w.Request()
	if _, err := w.T.PublishBranch(context.Background(), req); err != nil {
		t.Fatalf("publish with an empty PATH: %v", err)
	}
	if w.RemoteHead() != req.ExpectedHead {
		t.Fatal("not published")
	}
}

func TestPublishAuthorizerFailureSendsNothing(t *testing.T) {
	w := realWorld(t)
	tr, err := git.NewGoGitTransport(failingAuthorizer{}, func() *http.Client { return w.Server.Client() })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tr.PublishBranch(context.Background(), w.Request()); err == nil {
		t.Fatal("publish without credentials succeeded")
	}
	if n := len(w.Server.Requests()); n != 0 {
		t.Fatalf("%d requests were sent without credentials", n)
	}
}

type failingAuthorizer struct{}

func (failingAuthorizer) Authorize(context.Context, *http.Request) error {
	return errors.New("no token")
}

// An authorizer may set only Authorization.
type greedyAuthorizer struct{}

func (greedyAuthorizer) Authorize(_ context.Context, r *http.Request) error {
	r.Header.Set("Authorization", "Bearer x")
	r.Header.Set("X-Other", "1")
	return nil
}

func TestPublishAuthorizerMaySetOnlyAuthorization(t *testing.T) {
	w := realWorld(t)
	tr, _ := git.NewGoGitTransport(greedyAuthorizer{}, func() *http.Client { return w.Server.Client() })
	if _, err := tr.RemoteBranch(context.Background(), w.Request()); err == nil {
		t.Fatal("an authorizer setting other headers was accepted")
	}
	if n := len(w.Server.Requests()); n != 0 {
		t.Fatalf("%d requests were sent", n)
	}
}

func TestPublishWrongTokenIsADefiniteFailure(t *testing.T) {
	w := realWorld(t)
	tr, _ := git.NewGoGitTransport(&gittest.OriginAuthorizer{Origin: w.Server.URL, Token: "wrong"}, func() *http.Client { return w.Server.Client() })
	_, err := tr.PublishBranch(context.Background(), w.Request())
	if err == nil || errors.Is(err, git.ErrPublishUnknown) {
		t.Fatalf("err = %v, want a definite failure", err)
	}
	if strings.Contains(err.Error()+git.UserMessage(err), "wrong") {
		t.Fatalf("the token is echoed: %v", err)
	}
	if w.Pushes() != 0 {
		t.Fatal("pushed")
	}
}

func TestPublishCancelledContextIsNotAClaimOfSuccess(t *testing.T) {
	w := realWorld(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := w.T.PublishBranch(ctx, w.Request())
	if err == nil {
		t.Fatal("a cancelled publish succeeded")
	}
	if w.Pushes() != 0 {
		t.Fatal("pushed")
	}
}

func TestPublishUnsupportedFormats(t *testing.T) {
	ctx := context.Background()

	t.Run("sha256 repository", func(t *testing.T) {
		dir := gittest.MakeLocalRepo(t, "x\n")
		cfgPath := filepath.Join(dir, ".git", "config")
		cfg, err := os.ReadFile(cfgPath)
		if err != nil {
			t.Fatal(err)
		}
		text := strings.Replace(string(cfg), "[core]", "[core]\n\trepositoryformatversion = 1", 1) + "[extensions]\n\tobjectformat = sha256\n"
		if err := os.WriteFile(cfgPath, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		w := realWorldWithDir(t, dir)
		req := w.Request()
		req.Dir = dir
		req.ExpectedHead = strings.Repeat("a", 40)
		if _, err := w.T.PublishBranch(ctx, req); !errors.Is(err, git.ErrUnsupportedFormat) {
			t.Fatalf("err = %v, want ErrUnsupportedFormat", err)
		}
		if w.Pushes() != 0 {
			t.Fatal("pushed")
		}
	})

	t.Run("submodule link", func(t *testing.T) {
		dir := gittest.MakeLocalRepo(t, "x\n")
		head := addGitlink(t, dir)
		w := realWorldWithDir(t, dir)
		req := w.Request()
		req.ExpectedHead = head
		if _, err := w.T.PublishBranch(ctx, req); !errors.Is(err, git.ErrUnsupportedFormat) {
			t.Fatalf("err = %v, want ErrUnsupportedFormat", err)
		}
		if w.Pushes() != 0 || w.RemoteHead() != "" {
			t.Fatal("something was pushed")
		}
	})

	t.Run("content filter", func(t *testing.T) {
		dir := gittest.MakeLocalRepo(t, "x\n")
		head := gittest.CommitInRepo(t, dir, map[string]string{".gitattributes": "*.bin filter=lfs diff=lfs merge=lfs -text\n"}, "lfs")
		w := realWorldWithDir(t, dir)
		req := w.Request()
		req.ExpectedHead = head
		if _, err := w.T.PublishBranch(ctx, req); !errors.Is(err, git.ErrUnsupportedFormat) {
			t.Fatalf("err = %v, want ErrUnsupportedFormat", err)
		}
	})

	t.Run("shallow repository", func(t *testing.T) {
		dir := gittest.MakeLocalRepo(t, "x\n")
		if err := os.WriteFile(filepath.Join(dir, ".git", "shallow"), []byte(strings.Repeat("b", 40)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		w := realWorldWithDir(t, dir)
		if _, err := w.T.PublishBranch(ctx, w.Request()); !errors.Is(err, git.ErrUnsupportedFormat) {
			t.Fatalf("err = %v, want ErrUnsupportedFormat", err)
		}
	})
}

func realWorldWithDir(t *testing.T, dir string) *gittest.RealPublishWorld {
	t.Helper()
	return gittest.NewRealPublishWorldFor(t, dir).(*gittest.RealPublishWorld)
}

// addGitlink commits a tree holding a submodule entry and moves main to it.
func addGitlink(t *testing.T, dir string) string {
	t.Helper()
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	head, _ := repo.Head()
	parent, _ := repo.CommitObject(head.Hash())
	tree, _ := parent.Tree()
	entries := append([]object.TreeEntry(nil), tree.Entries...)
	entries = append(entries, object.TreeEntry{Name: "vendor", Mode: filemode.Submodule, Hash: plumbing.NewHash(strings.Repeat("c", 40))})
	nt := &object.Tree{Entries: entries}
	o := repo.Storer.NewEncodedObject()
	if err := nt.Encode(o); err != nil {
		t.Fatal(err)
	}
	th, err := repo.Storer.SetEncodedObject(o)
	if err != nil {
		t.Fatal(err)
	}
	c := &object.Commit{Author: parent.Author, Committer: parent.Committer, Message: "add submodule\n", TreeHash: th, ParentHashes: []plumbing.Hash{parent.Hash}}
	co := repo.Storer.NewEncodedObject()
	if err := c.Encode(co); err != nil {
		t.Fatal(err)
	}
	ch, err := repo.Storer.SetEncodedObject(co)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName("main"), ch)); err != nil {
		t.Fatal(err)
	}
	return ch.String()
}

// Interop: a repo made by the git CLI (through the safe wrapper) publishes too.
func TestPublishRepoMadeByGitCLI(t *testing.T) {
	iw := gittest.NewRealInitWorld(t).(*gittest.RealInitWorld)
	dir := iw.NewDir()
	if err := iw.Git().InitRepo(context.Background(), dir, "main"); err != nil {
		t.Fatal(err)
	}
	iw.WriteFile(dir, "README.md", "# Shop API\r\ncrlf\r\n")
	iw.WriteFile(dir, ".gitignore", "node_modules/\n")
	fc, err := iw.Git().CommitFirst(context.Background(), dir, []string{"README.md", ".gitignore"}, "Initial commit")
	if err != nil {
		t.Fatal(err)
	}
	w := realWorldWithDir(t, dir)
	req := w.Request()
	if req.ExpectedHead != fc.Commit {
		t.Fatalf("head %s != first commit %s", req.ExpectedHead, fc.Commit)
	}
	if _, err := w.T.PublishBranch(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if w.RemoteHead() != fc.Commit {
		t.Fatal("not published")
	}
}
