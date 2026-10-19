package hosts

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

// FetchError is how a client reports a failed fetch. The service and the core
// never see *http.Response or url.Error: a client translates them (design:
// Failures). Err is the internal cause; it never holds a token.
type FetchError struct {
	Kind    corehosts.FailKind
	RetryAt *time.Time
	Err     error
}

// Error returns the kind and the cause, for logs.
func (e *FetchError) Error() string {
	if e.Err == nil {
		return "host fetch failed: " + string(e.Kind)
	}
	return "host fetch failed (" + string(e.Kind) + "): " + e.Err.Error()
}

// Unwrap returns the cause.
func (e *FetchError) Unwrap() error { return e.Err }

// ErrNotModified is returned by a client when every request got a 304 for the
// ETag it sent.
var ErrNotModified = errors.New("not modified")

func fetchErr(kind corehosts.FailKind, format string, a ...any) *FetchError {
	return &FetchError{Kind: kind, Err: fmt.Errorf(format, a...)}
}

// maxRetryWait bounds what ParseRetryAt accepts from a header.
const maxRetryWait = 24 * time.Hour

// ParseRetryAt reads when a rate-limited request may be retried from the
// response headers: Retry-After (seconds or an HTTP date), then the reset time
// of X-RateLimit-Reset or RateLimit-Reset (Unix seconds). Negative,
// unparseable, past or absurd values give nil, and the core falls back to 60 s.
func ParseRetryAt(h http.Header, now time.Time) *time.Time {
	if v := strings.TrimSpace(h.Get("Retry-After")); v != "" {
		if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
			return within(now, now.Add(time.Duration(clampSecs(secs))*time.Second))
		}
		if t, err := http.ParseTime(v); err == nil {
			return within(now, t)
		}
		return nil
	}
	for _, name := range []string{"X-RateLimit-Reset", "RateLimit-Reset"} {
		v := strings.TrimSpace(h.Get(name))
		if v == "" {
			continue
		}
		secs, err := strconv.ParseInt(v, 10, 64)
		if err != nil || secs < 0 {
			return nil
		}
		// Small numbers are "seconds from now" (RateLimit-Reset in the IETF
		// draft); large ones are Unix times.
		if secs < 1<<30 {
			return within(now, now.Add(time.Duration(clampSecs(secs))*time.Second))
		}
		return within(now, time.Unix(secs, 0).UTC())
	}
	return nil
}

func clampSecs(s int64) int64 {
	if s < 0 {
		return -1
	}
	return min(s, int64(maxRetryWait/time.Second)+1)
}

func within(now, t time.Time) *time.Time {
	if !t.After(now) || t.Sub(now) > maxRetryWait {
		return nil
	}
	return &t
}

// rateLimited reports whether a 403 carries a rate-limit signal.
func rateLimited(h http.Header) bool {
	for _, name := range []string{"X-RateLimit-Remaining", "RateLimit-Remaining"} {
		if v := strings.TrimSpace(h.Get(name)); v == "0" {
			return true
		}
	}
	return h.Get("Retry-After") != ""
}
