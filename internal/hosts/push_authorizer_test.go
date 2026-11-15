package hosts

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

type pushRoundTripFunc func(*http.Request) (*http.Response, error)

func (f pushRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func authorizerFixture() *ConfirmedHosts {
	cfg := ParseConfig(map[string]Var{"YGG_HOST_1": {Value: "github https://git.acme.test:8443/root"}, "YGG_HOST_1_TOKEN": {Value: "sentinel-secret"}})
	return NewConfirmedHosts(New(Deps{Config: cfg}), nil, nil)
}
func TestPushAuthorizerOriginAndOnlyAuthorizationMutation(t *testing.T) {
	h := authorizerFixture()
	for _, path := range []string{"/root/acme/shop.git/git-receive-pack", "/root/acme/shop.git/info/refs?service=git-receive-pack"} {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://git.acme.test:8443"+path, strings.NewReader("push data"))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Fixture", "keep")
		req.Header.Set("Authorization", "old")
		before := *req
		beforeURL := *req.URL
		before.URL = &beforeURL
		beforeGetBody := reflect.ValueOf(req.GetBody).Pointer()
		headers := req.Header.Clone()
		delete(headers, "Authorization")
		if err := h.Authorize(context.Background(), "git.acme.test:8443", req); err != nil {
			t.Fatal(err)
		}
		if req.Header.Get("Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("x-access-token:sentinel-secret")) {
			t.Fatal("token absent")
		}
		actualHeaders := req.Header.Clone()
		delete(actualHeaders, "Authorization")
		if !reflect.DeepEqual(headers, actualHeaders) {
			t.Fatal("changed other headers")
		}
		if reflect.ValueOf(req.GetBody).Pointer() != beforeGetBody {
			t.Fatal("changed body factory")
		}
		actual := *req
		before.GetBody = nil
		actual.GetBody = nil
		before.Header = nil
		actual.Header = nil
		if !reflect.DeepEqual(before, actual) {
			t.Fatal("changed request outside headers")
		}
	}
}
func TestPushAuthorizerRejectsUnsafeDestinationsWithoutMutation(t *testing.T) {
	cases := map[string]func(*http.Request){
		"foreign host":      func(r *http.Request) { r.URL.Host = "other.acme.test:8443" },
		"api host":          func(r *http.Request) { r.URL.Host = "api.github.com" },
		"wrong port":        func(r *http.Request) { r.URL.Host = "git.acme.test:443" },
		"missing port":      func(r *http.Request) { r.URL.Host = "git.acme.test" },
		"http":              func(r *http.Request) { r.URL.Scheme = "http" },
		"userinfo":          func(r *http.Request) { r.URL.User = url.UserPassword("user", "secret") },
		"fragment":          func(r *http.Request) { r.URL.Fragment = "secret" },
		"query credential":  func(r *http.Request) { r.URL.RawQuery = "token=secret" },
		"multiple query":    func(r *http.Request) { r.URL.RawQuery = "service=git-receive-pack&service=git-upload-pack" },
		"traversal":         func(r *http.Request) { r.URL.Path = "/root/../shop.git" },
		"encoded traversal": func(r *http.Request) { r.URL.Path = "/root/../shop.git"; r.URL.RawPath = "/root/%2e%2e/shop.git" },
		"double escape":     func(r *http.Request) { r.URL.Path = "/root/%2e%2e/shop.git" },
		"wrong prefix":      func(r *http.Request) { r.URL.Path = "/roots/shop.git" },
		"host override":     func(r *http.Request) { r.Host = "other.acme.test" },
		"opaque":            func(r *http.Request) { r.URL.Opaque = "//other.acme.test/shop.git" },
		"control":           func(r *http.Request) { r.URL.Path = "/root/a\x00b" },
		"request uri":       func(r *http.Request) { r.RequestURI = "/foreign" },
		"nil url":           func(r *http.Request) { r.URL = nil },
	}
	h := authorizerFixture()
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://git.acme.test:8443/root/shop.git", nil)
			req.Header.Set("Authorization", "unchanged")
			mutate(req)
			before := req.Clone(req.Context())
			if err := h.Authorize(context.Background(), "git.acme.test:8443", req); err == nil {
				t.Fatal("unsafe destination accepted")
			}
			if !reflect.DeepEqual(req, before) {
				t.Fatal("rejected request mutated")
			}
		})
	}
	if err := h.Authorize(context.Background(), "git.acme.test:8443", nil); err == nil {
		t.Fatal("nil request")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://git.acme.test:8443/root/shop.git", nil)
	if err := h.Authorize(ctx, "git.acme.test:8443", req); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if req.Header.Get("Authorization") != "" {
		t.Fatal("canceled request authorized")
	}
}
func TestPushTransportRejectsForeignRedirect(t *testing.T) {
	h := authorizerFixture()
	calls := 0
	base := pushRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "git.acme.test:8443" || r.Header.Get("Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("x-access-token:sentinel-secret")) {
			t.Fatal("foreign or unauthenticated network call")
		}
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://other.acme.test/root/shop.git"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})
	client := &http.Client{Transport: NewPushTransport(h, "git.acme.test:8443", "/root/shop.git", base)}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://git.acme.test:8443/root/shop.git", nil)
	res, err := client.Do(req)
	if res != nil {
		res.Body.Close()
	}
	if err == nil || calls != 1 {
		t.Fatal("foreign redirect sent", err, calls)
	}
	if req.Header.Get("Authorization") != "" {
		t.Fatal("transport mutated caller's headers")
	}
}
func FuzzPushDestination(f *testing.F) {
	for _, raw := range []string{"https://git.acme.test:8443/root/shop.git", "http://git.acme.test:8443/root/shop.git", "https://other.acme.test/root/shop.git"} {
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, raw, nil)
		if err != nil {
			return
		}
		if !validPushDestination("https://git.acme.test:8443/root", req) {
			return
		}
		u := req.URL
		if u.Scheme != "https" || u.Host != "git.acme.test:8443" || u.User != nil || u.Fragment != "" || !strings.HasPrefix(u.Path, "/root/") {
			t.Fatal("invalid accepted", raw)
		}
		parsed, err := url.Parse(u.String())
		if err != nil || !validPushDestination("https://git.acme.test:8443/root", &http.Request{URL: parsed}) {
			t.Fatal("accepted URL failed round trip")
		}
	})
}

type pushObservedBody struct {
	io.Reader
	closed bool
}

func (b *pushObservedBody) Close() error { b.closed = true; return nil }
func TestPushTransportClosesRejectedRequestBody(t *testing.T) {
	body := &pushObservedBody{Reader: strings.NewReader("payload")}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://foreign.acme.test/shop.git", body)
	base := pushRoundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("unauthorized request sent"); return nil, nil })
	resp, err := NewPushTransport(authorizerFixture(), "git.acme.test:8443", "/root/shop.git", base).RoundTrip(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil || !body.closed {
		t.Fatal("rejected request leaked its body", err)
	}
}

type pushAuthorizerSpy struct{ calls int }

func (s *pushAuthorizerSpy) Authorize(_ context.Context, _ corehosts.InstanceID, req *http.Request) error {
	s.calls++
	req.Header.Set("Authorization", "Basic spy")
	return nil
}

// A credential must reach only the one repository the transport was built for:
// every other path on the same clone origin is refused before the authorizer
// runs, so there is no moment at which a header could be set.
func TestPushTransportBindsCredentialToRepositoryPath(t *testing.T) {
	const repo = "/root/acme/shop.git"
	inside := []string{repo, repo + "/info/refs?service=git-receive-pack", repo + "/git-receive-pack", repo + "/git-upload-pack"}
	outside := map[string]string{
		"sibling repo":           "/root/acme/other.git/info/refs?service=git-receive-pack",
		"owner root":             "/root/acme/",
		"deployment root":        "/root/",
		"origin root":            "/",
		"name prefix collision":  repo + ".evil/git-receive-pack",
		"suffix collision":       "/root/acme/shop.gitx/info/refs",
		"traversal out":          repo + "/../other.git/git-receive-pack",
		"traversal in":           "/root/acme/other.git/../shop.git/git-receive-pack",
		"dot segment":            repo + "/./git-receive-pack",
		"encoded dot dot":        repo + "/%2e%2e/other.git/git-receive-pack",
		"encoded slash":          repo + "%2fx",
		"encoded name":           "/root/acme/shop%2egit/info/refs",
		"double slash":           repo + "//git-receive-pack",
		"backslash":              repo + "/..\\other.git",
		"missing deployment":     "/acme/shop.git/info/refs",
		"case change":            "/root/acme/SHOP.git/info/refs",
		"different owner prefix": "/root/other/shop.git/info/refs",
	}
	for _, path := range inside {
		spy := &pushAuthorizerSpy{}
		var sent *http.Request
		base := pushRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			sent = r
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		})
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://git.acme.test:8443"+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := NewPushTransport(spy, "git.acme.test:8443", repo, base).RoundTrip(req)
		if err != nil || spy.calls != 1 || sent == nil || sent.Header.Get("Authorization") == "" {
			t.Fatalf("%s: repository request was not authorized: %v", path, err)
		}
		_ = res.Body.Close()
	}
	for name, path := range outside {
		t.Run(name, func(t *testing.T) {
			spy := &pushAuthorizerSpy{}
			base := pushRoundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("request sent"); return nil, nil })
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://git.acme.test:8443/", nil)
			if err != nil {
				t.Fatal(err)
			}
			// Build the URL by hand: NewRequest would clean nothing, but a table
			// entry with an escape needs RawPath to stay exactly as written.
			u, err := url.Parse("https://git.acme.test:8443" + path)
			if err != nil {
				t.Fatal(err)
			}
			req.URL = u
			res, err := NewPushTransport(spy, "git.acme.test:8443", repo, base).RoundTrip(req)
			if res != nil {
				_ = res.Body.Close()
			}
			if !errors.Is(err, ErrContextScope) || spy.calls != 0 || req.Header.Get("Authorization") != "" {
				t.Fatalf("path outside the repository reached the authorizer: err=%v calls=%d", err, spy.calls)
			}
		})
	}
}

// A same-origin redirect to another repository gets no credential either.
func TestPushTransportSameOriginRedirectToOtherRepoSeesNoCredential(t *testing.T) {
	h := authorizerFixture()
	var seen []string
	base := pushRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		seen = append(seen, r.URL.Path+" auth="+r.Header.Get("Authorization"))
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://git.acme.test:8443/root/other.git/info/refs"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})
	client := &http.Client{Transport: NewPushTransport(h, "git.acme.test:8443", "/root/shop.git", base)}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://git.acme.test:8443/root/shop.git/info/refs", nil)
	res, err := client.Do(req)
	if res != nil {
		res.Body.Close()
	}
	if err == nil || len(seen) != 1 || !strings.HasPrefix(seen[0], "/root/shop.git/info/refs auth=Basic ") {
		t.Fatal("redirect to another repository was sent", err, seen)
	}
}

func TestPushTransportRefusesEverythingWithInvalidRepoPath(t *testing.T) {
	for _, bad := range []string{"", "/", "root/shop.git", "/root/shop", "/root/shop.git/", "/root//shop.git", "/root/../shop.git", "/root/%2e/shop.git", "/root/a b.git"} {
		spy := &pushAuthorizerSpy{}
		base := pushRoundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("request sent"); return nil, nil })
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://git.acme.test:8443/root/shop.git/info/refs", nil)
		res, err := NewPushTransport(spy, "git.acme.test:8443", bad, base).RoundTrip(req)
		if res != nil {
			_ = res.Body.Close()
		}
		if err == nil || spy.calls != 0 {
			t.Fatalf("invalid repo path %q accepted", bad)
		}
	}
}

func FuzzWithinRepo(f *testing.F) {
	for _, raw := range []string{"https://h/root/o/n.git/info/refs", "https://h/root/o/n.git", "https://h/root/o/n.git/../x.git", "https://h/root/o/n.git/%2e%2e/x"} {
		f.Add(raw)
	}
	const repo = "/root/o/n.git"
	f.Fuzz(func(t *testing.T, raw string) {
		u, err := url.Parse(raw)
		if err != nil || !withinRepo(repo, u) {
			return
		}
		if u.Path != repo && !strings.HasPrefix(u.Path, repo+"/") {
			t.Fatal("accepted a path outside the repository", raw)
		}
		for _, segment := range strings.Split(u.Path, "/")[1:] {
			if segment == ".." || segment == "." {
				t.Fatal("accepted a dot segment", raw)
			}
		}
		if strings.ContainsAny(u.EscapedPath(), "%\\") {
			t.Fatal("accepted an escape", raw)
		}
	})
}
