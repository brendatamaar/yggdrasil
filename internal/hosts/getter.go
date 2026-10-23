package hosts

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
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
	Header http.Header
	Body   []byte
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
	client *http.Client
	now    func() time.Time
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
	return &Getter{client: &c, now: now}
}

// Get requests url. A 304 is returned as ErrNotModified; other non-2xx
// statuses and network errors become *FetchError. etag may be empty.
func (g *Getter) Get(ctx context.Context, url, token string, auth AuthStyle, etag string) (Resp, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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
	res, err := g.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Resp{}, &FetchError{Kind: corehosts.FailTransient, Err: ctx.Err()}
		}
		// url.Error holds the URL, never a header: no token here.
		return Resp{}, &FetchError{Kind: corehosts.FailTransient, Err: fmt.Errorf("request failed: %w", err)}
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, maxBody+1))
	if err != nil {
		return Resp{}, &FetchError{Kind: corehosts.FailTransient, Err: fmt.Errorf("read response: %w", err)}
	}
	switch {
	case res.StatusCode >= 200 && res.StatusCode < 300:
		if len(body) > maxBody {
			return Resp{}, fetchErr(corehosts.FailTransient, "response too large")
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
