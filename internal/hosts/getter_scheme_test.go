package hosts

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// A token must not cross a network unencrypted: http is for loopback only.
func TestGetterRefusesPlainHTTPToNonLoopbackHosts(t *testing.T) {
	cases := map[string]bool{
		"https://example.test/api":           true,
		"https://127.0.0.1/api":              true,
		"http://127.0.0.1:8080/api":          true,
		"http://127.1.2.3/api":               true,
		"http://localhost:3000/api":          true,
		"http://LOCALHOST/api":               true,
		"http://[::1]:8080/api":              true,
		"http://example.test/api":            false,
		"http://example.test:8080/api":       false,
		"http://127.0.0.1.example.test/api":  false,
		"http://localhost.example.test/api":  false,
		"http://10.0.0.5/api":                false,
		"http://192.168.1.10/api":            false,
		"http://[::ffff:8.8.8.8]/api":        false,
		"http://[2001:db8::1]/api":           false,
		"http://0.0.0.0/api":                 false,
		"http://user:pw@127.0.0.1/api":       false,
		"https://user:pw@example.test/api":   false,
		"ftp://127.0.0.1/api":                false,
		"//example.test/api":                 false,
		"http:///api":                        false,
		"http://127.0.0.1%2eexample.test/ap": false,
	}
	for raw, accepted := range cases {
		t.Run(raw, func(t *testing.T) {
			calls := 0
			stub := pushRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}")), Header: http.Header{}, Request: r}, nil
			})
			g := NewGetter(&http.Client{Transport: stub}, nil)
			_, err := g.Get(context.Background(), raw, "sentinel-secret", AuthBearer, "")
			if accepted && err != nil {
				t.Fatalf("refused: %v", err)
			}
			if !accepted {
				if err == nil || calls != 0 {
					t.Fatalf("sent a token over %s (calls=%d, err=%v)", raw, calls, err)
				}
				if strings.Contains(err.Error(), "sentinel-secret") || strings.Contains(err.Error(), "pw") {
					t.Fatalf("error leaks a secret: %v", err)
				}
			}
		})
	}
}

func TestGetterBoundedAndLogPathsShareTheSchemeRule(t *testing.T) {
	stub := pushRoundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("request sent"); return nil, nil })
	g := NewGetter(&http.Client{Transport: stub}, nil)
	if _, err := g.GetBounded(context.Background(), "http://example.test/x", "t", AuthBearer, 10); err == nil {
		t.Fatal("GetBounded accepted http to a remote host")
	}
	if _, err := g.GetLogBounded(context.Background(), "http://example.test/x", "t", AuthBearer, 10); err == nil {
		t.Fatal("GetLogBounded accepted http to a remote host")
	}
	for _, origin := range []string{"http://example.test", "http://10.0.0.1"} {
		if _, err := NewGetterWithLogOrigins(nil, nil, []string{origin}); err == nil {
			t.Fatalf("log origin %s accepted", origin)
		}
	}
	for _, origin := range []string{"https://logs.example.test", "http://127.0.0.1:9", "http://localhost:9", "http://[::1]:9"} {
		if _, err := NewGetterWithLogOrigins(nil, nil, []string{origin}); err != nil {
			t.Fatalf("log origin %s refused: %v", origin, err)
		}
	}
}
