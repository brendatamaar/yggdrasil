package hosts

// PushAuthorizer / NewPushTransport contracts (GH-06, NR-04, invariants 3 and 4;
// TM1-H): an independent oracle of the destination rule over generated
// components, "only Authorization changes" on complete request equality, a real
// TLS redirect handshake, and a fuzz target. No external host, no git.

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

const pushWantAuth = "Basic eC1hY2Nlc3MtdG9rZW46c2VudGluZWwtc2VjcmV0" // x-access-token:sentinel-secret

// oraclePush decides the destination rule of the design from components: https
// to exactly the configured authority, no userinfo or fragment, a path below the
// configured prefix without dot segments or unsafe characters, and a query that
// is empty or exactly one service parameter.
func oraclePush(scheme, userinfo, host, path, query, fragment, reqHost string) bool {
	const want = "git.acme.test:8443"
	if scheme != "https" || userinfo != "" || host != want || fragment != "" {
		return false
	}
	if reqHost != "" && reqHost != host {
		return false
	}
	if !strings.HasPrefix(path, "/root/") {
		return false
	}
	for _, r := range path {
		if r <= ' ' || r == 0x7f || r == '\\' || r == '%' {
			return false
		}
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == "." || seg == ".." {
			return false
		}
	}
	return query == "" || query == "service=git-upload-pack" || query == "service=git-receive-pack"
}

func pushRequest(t testing.TB, scheme, userinfo, host, path, query, fragment, reqHost string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://git.acme.test:8443/root/x", strings.NewReader("pack"))
	if err != nil {
		t.Fatal(err)
	}
	req.URL.Scheme, req.URL.Host, req.URL.Path, req.URL.RawQuery, req.URL.Fragment = scheme, host, path, query, fragment
	req.URL.RawPath = ""
	if userinfo != "" {
		req.URL.User = url.UserPassword(userinfo, "pw")
	}
	req.Host = reqHost
	req.Header.Set("X-Keep", "1")
	req.Header.Set("Authorization", "old")
	return req
}

// authorizeContract is run for every PushAuthorizer under test: the clause list
// is the contract; implementations differ only in how they get there.
func authorizeContract(t *testing.T, authorize func(context.Context, *http.Request) error) {
	schemes := []string{"https", "http"}
	users := []string{"", "u"}
	hosts := []string{"git.acme.test:8443", "git.acme.test", "other.acme.test:8443", "api.github.com", "git.acme.test:443", "GIT.ACME.TEST:8443"}
	paths := []string{"/root/acme/shop.git/info/refs", "/root/acme/shop.git/git-receive-pack", "/root/", "/root", "/rootx/a", "/other/a", "/root/../a", "/root/./a", "/root/a b", "/root/a\\b", "/root/a%2fb", "/root/a\x00b", "/", ""}
	queries := []string{"", "service=git-upload-pack", "service=git-receive-pack", "service=git-evil", "token=secret", "service=git-upload-pack&service=git-receive-pack", "service=git-upload-pack&x=1"}
	fragments := []string{"", "frag"}
	reqHosts := []string{"", "git.acme.test:8443", "other.acme.test"}
	accepted, rejected := 0, 0
	for _, scheme := range schemes {
		for _, u := range users {
			for _, h := range hosts {
				for _, p := range paths {
					for _, q := range queries {
						for _, f := range fragments {
							for _, rh := range reqHosts {
								// Sample the product space: every combination is
								// covered for the pieces that matter by the loops
								// above; keep the run short with a stride.
								if (len(h)+len(p)+len(q)+len(f)+len(rh)+len(u))%3 != 0 && !(scheme == "https" && u == "" && h == "git.acme.test:8443") {
									continue
								}
								req := pushRequest(t, scheme, u, h, p, q, f, rh)
								before := req.Clone(req.Context())
								err := authorize(context.Background(), req)
								want := oraclePush(scheme, u, h, p, q, f, rh)
								if want != (err == nil) {
									t.Fatalf("%s://%s@%s%s?%s#%s host=%q: oracle %v, Authorize %v", scheme, u, h, p, q, f, rh, want, err)
								}
								if err != nil {
									rejected++
									if !sameRequest(req, before) {
										t.Fatalf("a rejected request was changed: %s", req.URL)
									}
									continue
								}
								accepted++
								if got := req.Header.Get("Authorization"); got != pushWantAuth {
									t.Fatalf("authorization %q", got)
								}
								// Complete request equality except Authorization.
								req.Header.Set("Authorization", "old")
								if !reflect.DeepEqual(req.Header, before.Header) {
									t.Fatalf("headers other than Authorization changed: %v", req.Header)
								}
								a, b := *req, *before
								a.GetBody, b.GetBody = nil, nil
								if !reflect.DeepEqual(a.URL, b.URL) || a.Method != b.Method || a.Host != b.Host || a.Proto != b.Proto || a.ContentLength != b.ContentLength || a.Body != b.Body {
									t.Fatalf("the request changed outside its headers: %+v", a)
								}
							}
						}
					}
				}
			}
		}
	}
	if accepted < 10 || rejected < 100 {
		t.Fatalf("lopsided space: %d accepted, %d rejected", accepted, rejected)
	}
}

// fakeAuthorizer is the smallest conforming PushAuthorizer: it applies the same
// rule from the oracle and sets only Authorization.
type fakeAuthorizer struct{}

func (fakeAuthorizer) Authorize(_ context.Context, _ corehosts.InstanceID, req *http.Request) error {
	if req == nil || req.URL == nil || req.RequestURI != "" || req.URL.Opaque != "" || req.URL.OmitHost || req.URL.ForceQuery {
		return ErrContextScope
	}
	user := ""
	if req.URL.User != nil {
		user = "u"
	}
	if !oraclePush(req.URL.Scheme, user, req.URL.Host, req.URL.Path, req.URL.RawQuery, req.URL.Fragment, req.Host) {
		return ErrContextScope
	}
	req.Header.Set("Authorization", pushWantAuth)
	return nil
}

func TestPushAuthorizerContract(t *testing.T) {
	t.Run("fake", func(t *testing.T) {
		authorizeContract(t, func(ctx context.Context, r *http.Request) error {
			return fakeAuthorizer{}.Authorize(ctx, "git.acme.test:8443", r)
		})
	})
	t.Run("ConfirmedHosts", func(t *testing.T) {
		h := authorizerFixture()
		authorizeContract(t, func(ctx context.Context, r *http.Request) error { return h.Authorize(ctx, "git.acme.test:8443", r) })
	})
	t.Run("ConfirmedHosts refuses the API origin and another instance", func(t *testing.T) {
		h := authorizerFixture()
		req := pushRequest(t, "https", "", "api.github.com", "/root/acme/shop.git/info/refs", "", "", "")
		if h.Authorize(context.Background(), "git.acme.test:8443", req) == nil || req.Header.Get("Authorization") != "old" {
			t.Fatal("the API origin was authorized")
		}
		req = pushRequest(t, "https", "", "git.acme.test:8443", "/root/a", "", "", "")
		if h.Authorize(context.Background(), "unknown.acme.test", req) == nil || req.Header.Get("Authorization") != "old" {
			t.Fatal("an unknown instance was authorized")
		}
	})
}

// A real TLS handshake: the clone origin redirects to a foreign origin, to
// another repository on the same origin, and to its own repository. The foreign
// server must see nothing; every request the clone server does receive carries
// the credential only for the one repository.
func TestPushTransportRealRedirectHandshake(t *testing.T) {
	var foreignHits int
	var foreignMu sync.Mutex
	foreign := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		foreignMu.Lock()
		foreignHits++
		foreignMu.Unlock()
	}))
	defer foreign.Close()
	type seen struct{ path, auth string }
	var mu sync.Mutex
	var got []seen
	var redirectTo string
	clone := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, seen{r.URL.Path, r.Header.Get("Authorization")})
		to := redirectTo
		mu.Unlock()
		if r.URL.Path == "/acme/shop.git/info/refs" && to != "" {
			http.Redirect(w, r, to, http.StatusFound)
		}
	}))
	defer clone.Close()
	cfg := ParseConfig(map[string]Var{"YGG_HOST_1": {Value: "github " + clone.URL}, "YGG_HOST_1_TOKEN": {Value: "sentinel-secret"}})
	var id corehosts.InstanceID
	for _, in := range cfg.CoreInstances() {
		if in.Base == clone.URL {
			id = in.ID
		}
	}
	h := NewConfirmedHosts(New(Deps{Config: cfg}), nil, nil)
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:sentinel-secret"))
	do := func(redirect string) error {
		mu.Lock()
		got, redirectTo = nil, redirect
		mu.Unlock()
		client := &http.Client{Transport: NewPushTransport(h, id, "/acme/shop.git", clone.Client().Transport)}
		res, err := client.Get(clone.URL + "/acme/shop.git/info/refs?service=git-upload-pack")
		if res != nil {
			res.Body.Close()
		}
		return err
	}
	// Foreign origin: refused, nothing sent there.
	if err := do(foreign.URL + "/acme/shop.git/info/refs"); err == nil {
		t.Fatal("a foreign redirect was followed")
	}
	foreignMu.Lock()
	hits := foreignHits
	foreignMu.Unlock()
	if hits != 0 {
		t.Fatalf("the foreign server saw %d requests", hits)
	}
	mu.Lock()
	if len(got) != 1 || got[0].auth != want {
		t.Fatalf("clone server saw %+v", got)
	}
	mu.Unlock()
	// Another repository on the same origin: refused before the network.
	if err := do(clone.URL + "/acme/other.git/info/refs"); err == nil {
		t.Fatal("a redirect to another repository was followed")
	}
	mu.Lock()
	if len(got) != 1 {
		t.Fatalf("the other repository was contacted: %+v", got)
	}
	mu.Unlock()
	// The same repository: followed, and authorized again on the second hop.
	if err := do(clone.URL + "/acme/shop.git/git-upload-pack"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0].auth != want || got[1].auth != want || got[1].path != "/acme/shop.git/git-upload-pack" {
		t.Fatalf("the redirect hop was not re-authorized: %+v", got)
	}
}

// Fuzz: arbitrary raw URLs and Host headers. Accepted means the rule holds and
// only Authorization changed; rejected means the request is untouched.
func FuzzPushAuthorizeRequest(f *testing.F) {
	for _, raw := range []string{"https://git.acme.test:8443/root/a/b.git/info/refs?service=git-upload-pack", "http://git.acme.test:8443/root/a", "https://u:p@git.acme.test:8443/root/a", "https://git.acme.test:8443/root/%2e%2e/x", "https://git.acme.test:8443/root/a#f", "https://git.acme.test:8443@other.acme.test/root/a"} {
		f.Add(raw, "")
		f.Add(raw, "other.acme.test")
	}
	h := authorizerFixture()
	f.Fuzz(func(t *testing.T, raw, host string) {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, raw, strings.NewReader("x"))
		if err != nil {
			return
		}
		req.Host = host
		req.Header.Set("X-Keep", "1")
		before := req.Clone(req.Context())
		if err := h.Authorize(context.Background(), "git.acme.test:8443", req); err != nil {
			if !sameRequest(req, before) {
				t.Fatalf("rejected %q but changed the request", raw)
			}
			return
		}
		user := ""
		if req.URL.User != nil {
			user = "u"
		}
		if !oraclePush(req.URL.Scheme, user, req.URL.Host, req.URL.Path, req.URL.RawQuery, req.URL.Fragment, req.Host) || req.URL.Opaque != "" {
			t.Fatalf("accepted %q (host %q) against the rule", raw, host)
		}
		req.Header.Set("Authorization", before.Header.Get("Authorization"))
		if before.Header.Get("Authorization") == "" {
			req.Header.Del("Authorization")
		}
		if !reflect.DeepEqual(req.Header, before.Header) || !reflect.DeepEqual(req.URL, before.URL) || req.Host != before.Host {
			t.Fatalf("accepted %q but changed more than Authorization", raw)
		}
	})
}

// sameRequest compares every field of a request that Authorize could touch
// (GetBody is a function and cannot be compared).
func sameRequest(a, b *http.Request) bool {
	return reflect.DeepEqual(a.URL, b.URL) && reflect.DeepEqual(a.Header, b.Header) && a.Host == b.Host && a.Method == b.Method &&
		a.Body == b.Body && a.ContentLength == b.ContentLength && a.RequestURI == b.RequestURI && a.Proto == b.Proto && reflect.DeepEqual(a.Trailer, b.Trailer)
}
