package gittest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/pktline"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/server"
	"github.com/go-git/go-git/v5/storage/memory"
)

// SeenRequest is what the test server saw of one request.
type SeenRequest struct {
	Method, Path, Host string
	// HasAuth is whether an Authorization header arrived. The value is never kept.
	HasAuth bool
}

// GitHTTPServer is a TLS smart-HTTP git server for tests: it serves
// info/refs for both services and git-receive-pack, over in-memory
// repositories, and requires "Authorization: Bearer <Token>". It never touches
// the network beyond the loopback listener and needs no git binary.
type GitHTTPServer struct {
	*httptest.Server
	// Token is the bearer token the server accepts.
	Token string

	mu       sync.Mutex
	repos    map[string]*memory.Storage
	seen     []SeenRequest
	pushes   int
	loseNext bool
	// Wrap, when set, runs before the git handler; return true when it
	// answered the request itself (used to script redirects and failures).
	Wrap func(w http.ResponseWriter, r *http.Request) bool
}

// NewGitHTTPServer starts the server and closes it with the test.
func NewGitHTTPServer(t *testing.T) *GitHTTPServer {
	t.Helper()
	s := &GitHTTPServer{Token: "test-token-not-a-secret", repos: map[string]*memory.Storage{}}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// AddRepo creates an empty repository at path ("/owner/name").
func (s *GitHTTPServer) AddRepo(path string) *memory.Storage {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := memory.NewStorage()
	s.repos[path] = st
	return st
}

// Storage returns the in-memory repository at path.
func (s *GitHTTPServer) Storage(path string) *memory.Storage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.repos[path]
}

// Pushes is the number of git-receive-pack requests that were applied.
func (s *GitHTTPServer) Pushes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pushes
}

// Requests returns every request seen, in order.
func (s *GitHTTPServer) Requests() []SeenRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SeenRequest(nil), s.seen...)
}

// LoseNextReply makes the next applied push drop the connection without a reply.
func (s *GitHTTPServer) LoseNextReply() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loseNext = true
}

// Head returns the head of branch in the repository, "" when absent.
func (s *GitHTTPServer) Head(path, branch string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ref, err := s.repos[path].Reference(plumbing.NewBranchReferenceName(branch))
	if err != nil {
		return ""
	}
	return ref.Hash().String()
}

// Diverge points branch at a commit the pushing repo does not have.
func (s *GitHTTPServer) Diverge(path, branch string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.repos[path]
	sig := object.Signature{Name: "Grace Test", Email: "grace@example.test", When: time.Unix(1_700_000_000, 0).UTC()}
	tree := &object.Tree{}
	to := st.NewEncodedObject()
	if err := tree.Encode(to); err != nil {
		panic(err)
	}
	treeHash, err := st.SetEncodedObject(to)
	if err != nil {
		panic(err)
	}
	c := &object.Commit{Author: sig, Committer: sig, Message: "elsewhere\n", TreeHash: treeHash}
	co := st.NewEncodedObject()
	if err := c.Encode(co); err != nil {
		panic(err)
	}
	h, err := st.SetEncodedObject(co)
	if err != nil {
		panic(err)
	}
	if err := st.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName(branch), h)); err != nil {
		panic(err)
	}
	return h.String()
}

type mapLoader struct{ s *GitHTTPServer }

func (l mapLoader) Load(ep *transport.Endpoint) (storer.Storer, error) {
	l.s.mu.Lock()
	defer l.s.mu.Unlock()
	if st, ok := l.s.repos[ep.Path]; ok {
		return st, nil
	}
	return nil, transport.ErrRepositoryNotFound
}

func (s *GitHTTPServer) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.seen = append(s.seen, SeenRequest{Method: r.Method, Path: r.URL.Path, Host: r.Host, HasAuth: r.Header.Get("Authorization") != ""})
	wrap := s.Wrap
	s.mu.Unlock()
	if wrap != nil && wrap(w, r) {
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+s.Token {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	srv := server.NewServer(mapLoader{s})
	path, svc := "", ""
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/info/refs"):
		path, svc = strings.TrimSuffix(r.URL.Path, "/info/refs"), r.URL.Query().Get("service")
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git-receive-pack"):
		path, svc = strings.TrimSuffix(r.URL.Path, "/git-receive-pack"), transport.ReceivePackServiceName
	default:
		w.WriteHeader(http.StatusNotImplemented)
		return
	}
	ep, err := transport.NewEndpoint("https://git.example.test" + path)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodGet {
		var ar *packp.AdvRefs
		switch svc {
		case transport.ReceivePackServiceName:
			sess, err := srv.NewReceivePackSession(ep, nil)
			if err != nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			ar, err = sess.AdvertisedReferences()
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
		case transport.UploadPackServiceName:
			sess, err := srv.NewUploadPackSession(ep, nil)
			if err != nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			ar, err = sess.AdvertisedReferences()
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
		default:
			w.WriteHeader(http.StatusForbidden)
			return
		}
		ar.Prefix = [][]byte{[]byte("# service=" + svc), pktline.Flush}
		w.Header().Set("Content-Type", "application/x-"+svc+"-advertisement")
		_ = ar.Encode(w)
		return
	}

	sess, err := srv.NewReceivePackSession(ep, nil)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if _, err := sess.AdvertisedReferences(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	req := packp.NewReferenceUpdateRequest()
	if err := req.Decode(r.Body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	report, _ := sess.ReceivePack(r.Context(), req)
	s.mu.Lock()
	s.pushes++
	lose := s.loseNext
	s.loseNext = false
	s.mu.Unlock()
	if lose {
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				_ = conn.Close()
				return
			}
		}
		panic(http.ErrAbortHandler)
	}
	w.Header().Set("Content-Type", "application/x-git-receive-pack-result")
	if report != nil {
		_ = report.Encode(w)
	}
}

// OriginAuthorizer is a git.Authorizer that sets a bearer token for one origin
// only and records the authority of every request it was asked about.
type OriginAuthorizer struct {
	// Origin is "https://host:port".
	Origin string
	Token  string

	mu    sync.Mutex
	hosts []string
}

// Authorize sets the Authorization header for the instance origin and refuses
// every other host.
func (a *OriginAuthorizer) Authorize(_ context.Context, req *http.Request) error {
	a.mu.Lock()
	a.hosts = append(a.hosts, req.URL.Scheme+"://"+req.URL.Host)
	a.mu.Unlock()
	if req.URL.Scheme+"://"+req.URL.Host != a.Origin {
		return errors.New("not the instance origin")
	}
	req.Header.Set("Authorization", "Bearer "+a.Token)
	return nil
}

// Seen returns the authority of every request the authorizer was asked about.
func (a *OriginAuthorizer) Seen() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.hosts...)
}
