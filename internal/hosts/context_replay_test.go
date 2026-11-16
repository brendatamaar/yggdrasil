package hosts

// A replay server for the recorded-style host context fixtures in
// testdata/context (GH-05, TM1-H). The fixtures are fictional acme-shop
// traffic in the shape of the GitLab and GitHub REST APIs. A scenario names
// answers; their exchanges are queued per (path, query) and consumed in order,
// the last one of a queue repeating. An unrecorded request fails the test.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

type recordedExchange struct {
	Path       string            `json:"path"`
	Query      string            `json:"query"`
	Status     int               `json:"status"`
	Headers    map[string]string `json:"headers"`
	Body       json.RawMessage   `json:"body"`
	BodyText   *string           `json:"body_text"`
	BodyRepeat *struct {
		Fill  string `json:"fill"`
		Count int    `json:"count"`
	} `json:"body_repeat"`
}

type recordedProvider struct {
	Provider string                        `json:"provider"`
	Answers  map[string][]recordedExchange `json:"answers"`
}

var (
	recordedMu    sync.Mutex
	recordedCache = map[string]recordedProvider{}
)

func loadRecordedProvider(t testing.TB, provider string) recordedProvider {
	t.Helper()
	recordedMu.Lock()
	defer recordedMu.Unlock()
	if p, ok := recordedCache[provider]; ok {
		return p
	}
	raw, err := os.ReadFile(filepath.Join("testdata", "context", provider+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var p recordedProvider
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("fixture %s: %v", provider, err)
	}
	recordedCache[provider] = p
	return p
}

// seenRequest is what the replay saw of one request.
type seenRequest struct {
	Method string
	Path   string
	Query  string
	Auth   string // Authorization or PRIVATE-TOKEN, whichever came
	Cookie string
}

type contextReplay struct {
	t      testing.TB
	srv    *httptest.Server
	mu     sync.Mutex
	queues map[string][]recordedExchange
	seen   []seenRequest
	// gate, when set, holds a request whose path ends with gatePath until it is
	// closed or the request is cancelled (the client went away).
	gate     chan struct{}
	gatePath string
	// entered receives a value (non-blocking) when a gated request arrives.
	entered chan struct{}
	// cancelled counts gated requests that ended because the client went away.
	cancelled int
}

func newContextReplay(t testing.TB, p recordedProvider, answers []string) *contextReplay {
	t.Helper()
	r := &contextReplay{t: t, queues: map[string][]recordedExchange{}}
	for _, name := range answers {
		exs, ok := p.Answers[name]
		if !ok {
			t.Fatalf("no recorded answer %q for %s", name, p.Provider)
		}
		for _, e := range exs {
			k := e.Path + "?" + e.Query
			r.queues[k] = append(r.queues[k], e)
		}
	}
	r.srv = httptest.NewTLSServer(http.HandlerFunc(r.serve))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *contextReplay) serve(w http.ResponseWriter, req *http.Request) {
	auth := req.Header.Get("Authorization")
	if auth == "" {
		auth = req.Header.Get("PRIVATE-TOKEN")
	}
	r.mu.Lock()
	r.seen = append(r.seen, seenRequest{Method: req.Method, Path: req.URL.Path, Query: req.URL.RawQuery, Auth: auth, Cookie: req.Header.Get("Cookie")})
	gate, gatePath, entered := r.gate, r.gatePath, r.entered
	r.mu.Unlock()
	if gate != nil && strings.HasSuffix(req.URL.Path, gatePath) {
		if entered != nil {
			select {
			case entered <- struct{}{}:
			default:
			}
		}
		select {
		case <-gate:
		case <-req.Context().Done():
			r.mu.Lock()
			r.cancelled++
			r.mu.Unlock()
			return
		}
	}
	key := req.URL.Path + "?" + req.URL.RawQuery
	r.mu.Lock()
	q := r.queues[key]
	var ex recordedExchange
	found := len(q) > 0
	if found {
		ex = q[0]
		if len(q) > 1 {
			r.queues[key] = q[1:]
		}
	}
	r.mu.Unlock()
	if req.Method != http.MethodGet {
		r.t.Errorf("a context read sent %s %s", req.Method, req.URL)
	}
	if !found {
		r.t.Errorf("unrecorded request %s %s", req.Method, key)
		w.WriteHeader(599)
		return
	}
	for k, v := range ex.Headers {
		w.Header().Set(k, v)
	}
	status := ex.Status
	if status == 0 {
		status = 200
	}
	w.WriteHeader(status)
	switch {
	case ex.BodyRepeat != nil:
		chunk := []byte(strings.Repeat(ex.BodyRepeat.Fill, 32<<10))
		for left := ex.BodyRepeat.Count; left > 0; {
			n := min(left, len(chunk))
			if _, err := w.Write(chunk[:n]); err != nil {
				return
			}
			left -= n
		}
	case ex.BodyText != nil:
		_, _ = w.Write([]byte(strings.ReplaceAll(*ex.BodyText, "{{BASE}}", r.srv.URL)))
	case len(ex.Body) > 0:
		_, _ = w.Write([]byte(strings.ReplaceAll(string(ex.Body), "{{BASE}}", r.srv.URL)))
	}
}

// count is the number of requests whose path ends with suffix.
func (r *contextReplay) count(suffix string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, s := range r.seen {
		if strings.HasSuffix(s.Path, suffix) {
			n++
		}
	}
	return n
}

func (r *contextReplay) requests() []seenRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]seenRequest(nil), r.seen...)
}

func TestContextReplayFixturesAreFictionalAndWellFormed(t *testing.T) {
	for _, provider := range []string{"github", "gitlab"} {
		p := loadRecordedProvider(t, provider)
		if len(p.Answers) < 14 {
			t.Errorf("%s: only %d recorded answers", provider, len(p.Answers))
		}
		raw, _ := os.ReadFile(filepath.Join("testdata", "context", provider+".json"))
		if regexp.MustCompile(`[A-Za-z0-9]@[A-Za-z0-9]`).Match(raw) {
			t.Errorf("%s fixture contains an e-mail address: fixtures must be fictional", provider)
		}
		for _, banned := range []string{"gitlab.com", "github.com/", "ghp_", "glpat-"} {
			if strings.Contains(string(raw), banned) {
				t.Errorf("%s fixture contains %q: fixtures must be fictional", provider, banned)
			}
		}
	}
}
