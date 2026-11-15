package hosts

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

type postStub struct {
	calls  atomic.Int32
	status int
	body   io.Reader
	err    error
}

func (s *postStub) RoundTrip(r *http.Request) (*http.Response, error) {
	s.calls.Add(1)
	if s.err != nil {
		return nil, s.err
	}
	body := s.body
	if body == nil {
		body = strings.NewReader(`{"message":"sentinel-secret leaked by the host"}`)
	}
	return &http.Response{StatusCode: s.status, Body: io.NopCloser(body), Header: http.Header{}, Request: r}, nil
}

type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

func postWith(t *testing.T, stub *postStub) ([]byte, error) {
	t.Helper()
	h := &ConfirmedHosts{transport: stub}
	in := corehosts.Instance{ID: "git.acme.test", Kind: corehosts.KindGitHub, Base: "https://git.acme.test"}
	return h.post(context.Background(), in, "sentinel-secret", hostAPIRoot(in)+"/user/repos", map[string]string{"name": "shop"})
}

// 400/422 are a definite "nothing was created"; 401/403 are a token or
// permission failure; every other non-2xx stays an unknown outcome. In every
// case exactly one request is made (no retry).
func TestConfirmedPostClassifiesStatuses(t *testing.T) {
	type want int
	const (
		unknown want = iota
		rejected
		denied
	)
	cases := map[int]want{
		400: rejected, 422: rejected,
		401: denied, 403: denied,
		301: unknown, 302: unknown, 303: unknown, 307: unknown, 308: unknown, 304: unknown,
		404: unknown, 405: unknown, 408: unknown, 409: unknown, 418: unknown, 429: unknown,
		500: unknown, 502: unknown, 503: unknown, 504: unknown, 100: unknown,
	}
	for status, w := range cases {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			stub := &postStub{status: status}
			_, err := postWith(t, stub)
			if stub.calls.Load() != 1 {
				t.Fatalf("%d requests, want exactly one", stub.calls.Load())
			}
			if err == nil {
				t.Fatal("non-2xx accepted")
			}
			if strings.Contains(err.Error(), "sentinel-secret") {
				t.Fatalf("error leaks a secret: %v", err)
			}
			var fe *FetchError
			switch w {
			case rejected:
				if !errors.Is(err, ErrWriteRejected) || errors.Is(err, ErrOutcomeUnknown) {
					t.Fatalf("%d must be a definite rejection, got %v", status, err)
				}
			case denied:
				if !errors.As(err, &fe) || fe.Kind != corehosts.FailUnauthorized || errors.Is(err, ErrOutcomeUnknown) {
					t.Fatalf("%d must be a token or permission error, got %v", status, err)
				}
			default:
				if !errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, ErrWriteRejected) {
					t.Fatalf("%d must stay unknown, got %v", status, err)
				}
			}
		})
	}
}

func TestConfirmedPostUnknownOnTransportAndLostReply(t *testing.T) {
	for name, stub := range map[string]*postStub{
		"transport error": {err: errors.New("dial tcp: connection refused")},
		"unreadable 2xx":  {status: 201, body: failingBody{}},
		"oversized 2xx":   {status: 201, body: strings.NewReader(strings.Repeat("x", corehosts.ContextMaxResponseBytes+10))},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := postWith(t, stub)
			if !errors.Is(err, ErrOutcomeUnknown) || stub.calls.Load() != 1 {
				t.Fatalf("got %v after %d requests", err, stub.calls.Load())
			}
		})
	}
	if body, err := postWith(t, &postStub{status: 201, body: strings.NewReader(`{"id":1}`)}); err != nil || string(body) != `{"id":1}` {
		t.Fatal(body, err)
	}
}

// A definite refusal reaches the caller of CreateRepo unchanged, after exactly
// one POST, and an unparseable 2xx stays unknown.
func TestConfirmedCreateRepoDefiniteAndUnknownReplies(t *testing.T) {
	description := "Created by Yggdrasil [ygg:" + strings.Repeat("a", 16) + "]"
	for _, c := range []struct {
		status  int
		body    string
		rejects bool
		denies  bool
		unknown bool
	}{
		{status: 422, rejects: true},
		{status: 400, rejects: true},
		{status: 403, denies: true},
		{status: 401, denies: true},
		{status: 502, unknown: true},
		{status: 201, body: "not json", unknown: true},
	} {
		t.Run(fmt.Sprint(c.status, c.body), func(t *testing.T) {
			var posts atomic.Int32
			srv, in, cfg := contextFixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v3/users/acme":
					fmt.Fprint(w, `{"login":"acme","type":"User"}`)
				case "/api/v3/user":
					fmt.Fprint(w, `{"login":"acme"}`)
				case "/api/v3/user/repos":
					posts.Add(1)
					w.WriteHeader(c.status)
					fmt.Fprint(w, c.body)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
			})
			h := NewConfirmedHosts(New(Deps{Config: cfg}), nil, srv.Client())
			spec := CreateRepoSpec{Instance: in.ID, Owner: "acme", OwnerKind: "user", Name: "shop", Description: description, Private: true}
			_, err := h.CreateRepo(context.Background(), spec)
			var fe *FetchError
			switch {
			case posts.Load() != 1:
				t.Fatalf("%d POSTs", posts.Load())
			case c.rejects && !errors.Is(err, ErrWriteRejected):
				t.Fatal(err)
			case c.denies && !(errors.As(err, &fe) && fe.Kind == corehosts.FailUnauthorized):
				t.Fatal(err)
			case c.unknown && !errors.Is(err, ErrOutcomeUnknown):
				t.Fatal(err)
			}
		})
	}
}
