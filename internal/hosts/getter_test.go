package hosts

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

var fixedNow = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

func newGetter() *Getter { return NewGetter(nil, func() time.Time { return fixedNow }) }

// S1: the wrapper refuses every method but GET, even if code tried to send one.
func TestGetOnlyTransportRefusesWrites(t *testing.T) {
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	defer srv.Close()
	g := newGetter()
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead} {
		req, _ := http.NewRequest(m, srv.URL, strings.NewReader("x"))
		if res, err := g.client.Do(req); err == nil {
			_ = res.Body.Close()
			t.Errorf("%s was sent", m)
		}
	}
	if hit {
		t.Fatal("the server saw a non-GET request")
	}
	if _, err := g.Get(context.Background(), srv.URL, "", AuthBearer, ""); err != nil {
		t.Fatalf("GET: %v", err)
	}
	if !hit {
		t.Fatal("GET did not reach the server")
	}
}

func TestGetSendsTokenOnlyInHeader(t *testing.T) {
	var mu sync.Mutex
	var seen []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Clone(context.Background()))
		mu.Unlock()
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()
	g := newGetter()
	for _, style := range []AuthStyle{AuthBearer, AuthPrivateToken} {
		if _, err := g.Get(context.Background(), srv.URL+"/x?a=1", "SENTINEL-tok", style, `"e1"`); err != nil {
			t.Fatal(err)
		}
	}
	if seen[0].Header.Get("Authorization") != "Bearer SENTINEL-tok" || seen[1].Header.Get("PRIVATE-TOKEN") != "SENTINEL-tok" {
		t.Errorf("headers = %v / %v", seen[0].Header, seen[1].Header)
	}
	for _, r := range seen {
		if strings.Contains(r.RequestURI, "SENTINEL") || r.Header.Get("If-None-Match") != `"e1"` {
			t.Errorf("request %s %v", r.RequestURI, r.Header)
		}
	}
	if _, err := g.Get(context.Background(), srv.URL, "bad\r\ntoken", AuthBearer, ""); err == nil {
		t.Error("a token with a line break was sent")
	}
}

func TestGetClassifiesStatuses(t *testing.T) {
	cases := []struct {
		status  int
		headers map[string]string
		kind    corehosts.FailKind
		retry   bool
		notMod  bool
	}{
		{status: 401, kind: corehosts.FailUnauthorized},
		{status: 403, kind: corehosts.FailUnauthorized},
		{status: 403, headers: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": strconv.FormatInt(fixedNow.Add(time.Minute).Unix(), 10)}, kind: corehosts.FailRateLimited, retry: true},
		{status: 403, headers: map[string]string{"Retry-After": "30"}, kind: corehosts.FailRateLimited, retry: true},
		{status: 429, headers: map[string]string{"Retry-After": "30"}, kind: corehosts.FailRateLimited, retry: true},
		{status: 429, kind: corehosts.FailRateLimited},
		{status: 404, kind: corehosts.FailNotFound},
		{status: 500, kind: corehosts.FailTransient},
		{status: 502, kind: corehosts.FailTransient},
		{status: 422, kind: corehosts.FailTransient},
		{status: 304, notMod: true},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			for k, v := range c.headers {
				w.Header().Set(k, v)
			}
			w.WriteHeader(c.status)
		}))
		_, err := newGetter().Get(context.Background(), srv.URL, "t", AuthBearer, "")
		srv.Close()
		if c.notMod {
			if !errors.Is(err, ErrNotModified) {
				t.Errorf("%d: %v", c.status, err)
			}
			continue
		}
		var fe *FetchError
		if !errors.As(err, &fe) || fe.Kind != c.kind || (fe.RetryAt != nil) != c.retry {
			t.Errorf("status %d %v: error = %v", c.status, c.headers, err)
		}
	}
}

func TestGetNetworkErrorIsTransientAndDoesNotLeakTheToken(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	_, err := newGetter().Get(context.Background(), url, "SENTINEL-tok", AuthBearer, "")
	var fe *FetchError
	if !errors.As(err, &fe) || fe.Kind != corehosts.FailTransient || strings.Contains(err.Error(), "SENTINEL") {
		t.Fatalf("error = %v", err)
	}
}

func TestGetRefusesRedirectToAnotherHost(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the redirect was followed, with headers %v", r.Header)
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusFound)
	}))
	defer srv.Close()
	if _, err := newGetter().Get(context.Background(), srv.URL, "SENTINEL-tok", AuthBearer, ""); err == nil {
		t.Fatal("a redirect to another host was followed")
	}
}

func TestGetAllPartialNotModifiedRefetches(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.URL.Path+" inm="+r.Header.Get("If-None-Match"))
		mu.Unlock()
		if r.URL.Path == "/a" && r.Header.Get("If-None-Match") == `"a1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"`+strings.TrimPrefix(r.URL.Path, "/")+`2"`)
		_, _ = w.Write([]byte("body" + r.URL.Path))
	}))
	defer srv.Close()
	g := newGetter()
	reqs := []Request{{URL: srv.URL + "/a"}, {URL: srv.URL + "/b"}}
	out, etag, err := g.GetAll(context.Background(), "t", reqs, `"a1"|"b1"`)
	if err != nil || string(out[0].Body) != "body/a" || string(out[1].Body) != "body/b" || etag != `"a2"|"b2"` {
		t.Fatalf("out %v etag %q err %v (calls %v)", out, etag, err, calls)
	}
	// Everything 304: ErrNotModified.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(304) }))
	defer srv2.Close()
	if _, _, err := g.GetAll(context.Background(), "t", []Request{{URL: srv2.URL + "/a"}, {URL: srv2.URL + "/b"}}, `"x"|"y"`); !errors.Is(err, ErrNotModified) {
		t.Fatalf("err = %v", err)
	}
}

func TestGetAllOptional404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gone" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	out, _, err := newGetter().GetAll(context.Background(), "", []Request{{URL: srv.URL + "/ok"}, {URL: srv.URL + "/gone", Optional: true}}, "")
	if err != nil || string(out[0].Body) != "ok" || len(out[1].Body) != 0 {
		t.Fatalf("out %v err %v", out, err)
	}
	if _, _, err := newGetter().GetAll(context.Background(), "", []Request{{URL: srv.URL + "/gone"}}, ""); err == nil {
		t.Fatal("a required 404 was swallowed")
	}
}

func TestParseRetryAt(t *testing.T) {
	h := func(kv ...string) http.Header {
		out := http.Header{}
		for i := 0; i < len(kv); i += 2 {
			out.Set(kv[i], kv[i+1])
		}
		return out
	}
	future := fixedNow.Add(90 * time.Second)
	cases := []struct {
		name string
		h    http.Header
		want *time.Time
	}{
		{"retry-after seconds", h("Retry-After", "90"), &future},
		{"retry-after date", h("Retry-After", future.Format(http.TimeFormat)), &future},
		{"unix reset", h("X-RateLimit-Reset", strconv.FormatInt(future.Unix(), 10)), &future},
		{"ietf reset seconds", h("RateLimit-Reset", "90"), &future},
		{"negative", h("Retry-After", "-5"), nil},
		{"garbage", h("Retry-After", "soon"), nil},
		{"past date", h("Retry-After", fixedNow.Add(-time.Hour).Format(http.TimeFormat)), nil},
		{"past reset", h("X-RateLimit-Reset", strconv.FormatInt(fixedNow.Add(-time.Hour).Unix(), 10)), nil},
		{"absurd", h("Retry-After", "999999999999"), nil},
		{"none", h(), nil},
	}
	for _, c := range cases {
		got := ParseRetryAt(c.h, fixedNow)
		if (got == nil) != (c.want == nil) || (got != nil && !got.Equal(*c.want)) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func FuzzParseRetryAt(f *testing.F) {
	f.Add("90", "")
	f.Add("-1", "99999999999999999999")
	f.Add("Mon, 02 Jan 2006 15:04:05 GMT", "1792411500")
	f.Fuzz(func(t *testing.T, retryAfter, reset string) {
		h := http.Header{}
		h.Set("Retry-After", retryAfter)
		h.Set("X-RateLimit-Reset", reset)
		if got := ParseRetryAt(h, fixedNow); got != nil && (!got.After(fixedNow) || got.Sub(fixedNow) > maxRetryWait) {
			t.Fatalf("accepted %v", got)
		}
	})
}
