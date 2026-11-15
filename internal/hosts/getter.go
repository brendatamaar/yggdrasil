package hosts

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

// AuthStyle is how a host wants the token in a request header. The token never
// goes in a URL (invariant 4).
type AuthStyle int

const (
	// AuthBearer sends "Authorization: Bearer <token>" (GitHub, GitLab, Gitea
	// accept it; Gitea also takes "token ...").
	AuthBearer AuthStyle = iota
	// AuthPrivateToken sends "PRIVATE-TOKEN: <token>" (GitLab).
	AuthPrivateToken
)

// maxBody bounds a response body.
const maxBody = 4 << 20

// Resp is a successful (2xx) response.
type Resp struct {
	readBytes int
	Header    http.Header
	Body      []byte
}

// getOnly is the RoundTripper that refuses every request but GET (S1). It sits
// under every client so that no code path can write to a host.
type getOnly struct{ next http.RoundTripper }

func (g getOnly) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodGet {
		return nil, fmt.Errorf("host clients are read-only: refusing %s", r.Method)
	}
	return g.next.RoundTrip(r)
}

// Getter makes authenticated GET requests and classifies the outcome. It has
// no method that sends anything but GET.
type Getter struct {
	client     *http.Client
	now        func() time.Time
	mu         sync.Mutex
	lanes      map[string]chan struct{}
	limited    map[string]time.Time
	logOrigins map[string]bool
}

// NewGetter wraps base (or a default client) so that only GET is possible and
// redirects to another host are refused (the token must not follow them).
func NewGetter(base *http.Client, now func() time.Time) *Getter {
	if base == nil {
		base = &http.Client{}
	}
	if now == nil {
		now = time.Now
	}
	rt := base.Transport
	if rt == nil {
		rt = http.DefaultTransport
	}
	c := *base
	c.Transport = getOnly{next: rt}
	c.Timeout = 0 // callers use a context deadline
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if req.URL.Host != via[0].URL.Host || req.URL.Scheme != via[0].URL.Scheme {
			return errors.New("refusing a redirect to another host or scheme")
		}
		return nil
	}
	return &Getter{client: &c, now: now, lanes: map[string]chan struct{}{}, limited: map[string]time.Time{}}
}

// Get requests url. A 304 is returned as ErrNotModified; other non-2xx
// statuses and network errors become *FetchError. etag may be empty.
func (g *Getter) Get(ctx context.Context, url, token string, auth AuthStyle, etag string) (Resp, error) {
	return g.get(ctx, url, token, auth, etag, maxBody, false)
}

// GetBounded enforces an operation's remaining raw response budget while
// sharing the same per-origin concurrency gate as ordinary polling.
func (g *Getter) GetBounded(ctx context.Context, rawURL, token string, auth AuthStyle, limit int) (Resp, error) {
	if limit < 0 || limit > maxBody {
		return Resp{}, errors.New("invalid response budget")
	}
	return g.get(ctx, rawURL, token, auth, "", limit, false)
}

func (g *Getter) get(ctx context.Context, rawURL, token string, auth AuthStyle, etag string, limit int, logRedirects bool) (result Resp, resultErr error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.User != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return Resp{}, errors.New("invalid host request URL")
	}
	g.mu.Lock()
	origin := u.Scheme + "://" + u.Host
	if until := g.limited[origin]; until.After(g.now()) {
		g.mu.Unlock()
		return Resp{}, &FetchError{Kind: corehosts.FailRateLimited, RetryAt: &until, Err: errors.New("host rate limit is still active")}
	}
	lane := g.lanes[origin]
	if lane == nil {
		lane = make(chan struct{}, 4)
		g.lanes[u.Scheme+"://"+u.Host] = lane
	}
	g.mu.Unlock()
	select {
	case lane <- struct{}{}:
		defer func() { <-lane }()
	case <-ctx.Done():
		return Resp{}, &FetchError{Kind: corehosts.FailTransient, Err: ctx.Err()}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return Resp{}, fetchErr(corehosts.FailTransient, "build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ygg")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if token != "" {
		if strings.ContainsAny(token, "\r\n") {
			return Resp{}, fetchErr(corehosts.FailUnauthorized, "the token has a line break")
		}
		switch auth {
		case AuthPrivateToken:
			req.Header.Set("PRIVATE-TOKEN", token)
		default:
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	client := g.client
	if logRedirects {
		copyClient := *client
		copyClient.CheckRedirect = func(next *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many log redirects")
			}
			origin := next.URL.Scheme + "://" + next.URL.Host
			own := via[0].URL.Scheme + "://" + via[0].URL.Host
			if next.URL.User != nil || (origin != own && !g.logOrigins[origin]) {
				return errors.New("log redirect origin is not allowed")
			}
			// Signed redirect credentials are carried by the short-lived URL only.
			// Never forward the host's headers, including on a later return hop.
			next.Header.Del("Authorization")
			next.Header.Del("PRIVATE-TOKEN")
			next.Header.Del("Cookie")
			return nil
		}
		client = &copyClient
	}
	res, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Resp{}, &FetchError{Kind: corehosts.FailTransient, Err: ctx.Err()}
		}
		if logRedirects {
			return Resp{}, fetchErr(corehosts.FailTransient, "host log fetch failed")
		}
		// url.Error holds the URL, never a header: no token here.
		return Resp{}, &FetchError{Kind: corehosts.FailTransient, Err: fmt.Errorf("request failed: %w", err)}
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, int64(limit)+1))
	defer func() { result.readBytes = len(body) }()
	if err != nil {
		return Resp{}, &FetchError{Kind: corehosts.FailTransient, Err: fmt.Errorf("read response: %w", err)}
	}
	if res.StatusCode == http.StatusTooManyRequests || (res.StatusCode == http.StatusForbidden && rateLimited(res.Header)) {
		until := g.now().Add(corehosts.DefaultRateLimitWait)
		if parsed := ParseRetryAt(res.Header, g.now()); parsed != nil {
			until = *parsed
		}
		g.mu.Lock()
		if until.After(g.limited[origin]) {
			g.limited[origin] = until
		}
		g.mu.Unlock()
	}
	switch {
	case res.StatusCode >= 200 && res.StatusCode < 300:
		if len(body) > limit {
			if limit == maxBody {
				return Resp{}, fetchErr(corehosts.FailTransient, "response too large")
			}
			return Resp{}, ErrContextTooLarge
		}
		return Resp{Header: res.Header, Body: body}, nil
	case res.StatusCode == http.StatusNotModified:
		return Resp{Header: res.Header}, ErrNotModified
	case res.StatusCode == http.StatusUnauthorized:
		return Resp{}, fetchErr(corehosts.FailUnauthorized, "status 401")
	case res.StatusCode == http.StatusForbidden && rateLimited(res.Header):
		return Resp{}, &FetchError{Kind: corehosts.FailRateLimited, RetryAt: ParseRetryAt(res.Header, g.now()), Err: errors.New("status 403 with a rate limit")}
	case res.StatusCode == http.StatusForbidden:
		return Resp{}, fetchErr(corehosts.FailUnauthorized, "status 403")
	case res.StatusCode == http.StatusTooManyRequests:
		return Resp{}, &FetchError{Kind: corehosts.FailRateLimited, RetryAt: ParseRetryAt(res.Header, g.now()), Err: errors.New("status 429")}
	case res.StatusCode == http.StatusNotFound:
		return Resp{}, fetchErr(corehosts.FailNotFound, "status 404")
	}
	return Resp{}, fetchErr(corehosts.FailTransient, "status %d", res.StatusCode)
}

// Request is one GET of a multi-request fetch.
type Request struct {
	URL  string
	Auth AuthStyle
	// Optional: a 404 is an empty answer, not a failure (a branch that isn't
	// on the host has no pipeline).
	Optional bool
}

// GetAll runs the requests in order with a composite ETag (one part per
// request, joined by "|"). When every request answers 304 it returns
// ErrNotModified. When only some do, the 304 ones are fetched again without
// the ETag, since the caller holds no body for them. It returns the new
// composite ETag.
func (g *Getter) GetAll(ctx context.Context, token string, reqs []Request, etag string) ([]Resp, string, error) {
	parts := make([]string, len(reqs))
	if etag != "" {
		if sp := strings.Split(etag, "|"); len(sp) == len(reqs) {
			parts = sp
		}
	}
	out := make([]Resp, len(reqs))
	fresh := make([]string, len(reqs))
	var stale []int
	for i, r := range reqs {
		resp, err := g.Get(ctx, r.URL, token, r.Auth, parts[i])
		switch {
		case errors.Is(err, ErrNotModified):
			stale = append(stale, i)
			fresh[i] = parts[i]
		case err != nil && r.Optional && isNotFound(err):
			// out[i] stays empty
		case err != nil:
			return nil, "", err
		default:
			out[i] = resp
			fresh[i] = resp.Header.Get("ETag")
		}
	}
	if len(stale) == len(reqs) {
		return nil, "", ErrNotModified
	}
	for _, i := range stale {
		resp, err := g.Get(ctx, reqs[i].URL, token, reqs[i].Auth, "")
		if err != nil && reqs[i].Optional && isNotFound(err) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		out[i] = resp
		fresh[i] = resp.Header.Get("ETag")
	}
	joined := strings.Join(fresh, "|")
	if strings.Trim(joined, "|") == "" {
		joined = ""
	}
	return out, joined, nil
}

func isNotFound(err error) bool {
	var fe *FetchError
	return errors.As(err, &fe) && fe.Kind == corehosts.FailNotFound
}

// NewGetterWithLogOrigins permits explicitly configured signed-log origins.
// The default constructor permits no external redirect. Origins are immutable,
// contain no path/credentials/query, and are never inferred from a response.
func NewGetterWithLogOrigins(base *http.Client, now func() time.Time, origins []string) (*Getter, error) {
	g := NewGetter(base, now)
	g.logOrigins = map[string]bool{}
	for _, origin := range origins {
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && (u.Scheme != "http" || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1"))) {
			return nil, errors.New("invalid signed log origin")
		}
		g.logOrigins[origin] = true
	}
	return g, nil
}

// GetLogBounded allows credential-free signed redirects only to the injected
// allowlist. Error text never contains a signed URL.
func (g *Getter) GetLogBounded(ctx context.Context, rawURL, token string, auth AuthStyle, limit int) (Resp, error) {
	if limit < 0 || limit > corehosts.ContextMaxResponseBytes {
		return Resp{}, ErrContextTooLarge
	}
	return g.get(ctx, rawURL, token, auth, "", limit, true)
}
