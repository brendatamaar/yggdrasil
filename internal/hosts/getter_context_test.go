package hosts

// Getter clauses of host context (GH-05; TM1-H): one four-wide lane per origin
// shared by the poller's Get and the context reads, and signed log redirects
// that never carry the host's credentials on any hop, including a return hop.

import (
	"context"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestGetterLaneIsSharedByPollerAndContextReads(t *testing.T) {
	var active, max atomic.Int32
	srv, _, _ := contextFixture(t, func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		for {
			m := max.Load()
			if n <= m || max.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(4 * time.Millisecond)
		active.Add(-1)
		fmt.Fprint(w, "[]")
	})
	g := NewGetter(srv.Client(), nil)
	var wg sync.WaitGroup
	for i := 0; i < 36; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			switch i % 3 {
			case 0: // the poller's read
				_, err = g.Get(context.Background(), srv.URL, "tok", AuthBearer, "")
			case 1: // a context read
				_, err = g.GetBounded(context.Background(), srv.URL, "tok", AuthBearer, 100)
			default: // a context log read
				_, err = g.GetLogBounded(context.Background(), srv.URL, "tok", AuthBearer, 100)
			}
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if max.Load() > 4 || max.Load() < 2 {
		t.Fatalf("max in flight %d: want the shared lane to allow up to 4 and no more", max.Load())
	}
}

// Every hop of a signed log redirect, including one that returns to the host's
// own origin, arrives without Authorization, PRIVATE-TOKEN or Cookie. A hop to
// an origin that is not approved, or with userinfo, is never made.
func TestGetterLogRedirectChainNeverForwardsCredentials(t *testing.T) {
	for _, auth := range []AuthStyle{AuthBearer, AuthPrivateToken} {
		t.Run(fmt.Sprint("auth", auth), func(t *testing.T) {
			type hop struct{ who, path, authz, private, cookie string }
			var mu sync.Mutex
			var hops []hop
			record := func(who string, r *http.Request) {
				mu.Lock()
				hops = append(hops, hop{who, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("PRIVATE-TOKEN"), r.Header.Get("Cookie")})
				mu.Unlock()
			}
			var ownURL, signedURL, foreignURL string
			foreign := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { record("foreign", r) }))
			defer foreign.Close()
			signed := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				record("signed", r)
				switch r.URL.Path {
				case "/bounce": // return hop to the host's own origin
					http.Redirect(w, r, ownURL+"/after", http.StatusFound)
				case "/leave": // an unapproved origin
					http.Redirect(w, r, foreignURL+"/x", http.StatusFound)
				}
			}))
			defer signed.Close()
			own := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				record("own", r)
				switch r.URL.Path {
				case "/start", "/start-leave":
					to := "/bounce"
					if r.URL.Path == "/start-leave" {
						to = "/leave"
					}
					http.Redirect(w, r, signedURL+to+"?sig=fictional", http.StatusFound)
				case "/after":
					fmt.Fprint(w, "log body")
				}
			}))
			defer own.Close()
			ownURL, signedURL, foreignURL = own.URL, signed.URL, foreign.URL
			// One client that trusts all three test certificates.
			pool := own.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
			for _, s := range []*httptest.Server{signed, foreign} {
				for _, c := range s.TLS.Certificates {
					for _, der := range c.Certificate {
						if cert, err := parseCert(der); err == nil {
							pool.AddCert(cert)
						}
					}
				}
			}
			g, err := NewGetterWithLogOrigins(own.Client(), nil, []string{signed.URL, own.URL})
			if err != nil {
				t.Fatal(err)
			}
			res, err := g.GetLogBounded(context.Background(), own.URL+"/start", "sentinel-secret", auth, 1000)
			if err != nil || string(res.Body) != "log body" {
				t.Fatalf("approved chain: %q %v", res.Body, err)
			}
			mu.Lock()
			if len(hops) != 3 {
				t.Fatalf("hops %+v", hops)
			}
			if hops[0].who != "own" || (hops[0].authz == "" && hops[0].private == "") {
				t.Fatalf("the first request must carry the host credential: %+v", hops[0])
			}
			for _, h := range hops[1:] {
				if h.authz != "" || h.private != "" || h.cookie != "" {
					t.Fatalf("a redirect hop carried credentials: %+v", h)
				}
			}
			hops = nil
			mu.Unlock()
			// Chain into an origin that is not approved: refused, never made.
			g, err = NewGetterWithLogOrigins(own.Client(), nil, []string{signed.URL})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = g.GetLogBounded(context.Background(), own.URL+"/start-leave", "sentinel-secret", auth, 1000); err == nil {
				t.Fatal("a hop to an unapproved origin was followed")
			}
			mu.Lock()
			defer mu.Unlock()
			for _, h := range hops {
				if h.who == "foreign" {
					t.Fatalf("the unapproved origin was contacted: %+v", h)
				}
			}
		})
	}
}

func parseCert(der []byte) (*x509.Certificate, error) { return x509.ParseCertificate(der) }
