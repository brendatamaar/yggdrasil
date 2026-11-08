package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
)

// The per-launch UI session (SF-01, invariants 2 and 7; phase-3.md "Human
// decision for M2"). ygg makes a random launch token at start and shows it
// only on the terminal, inside the URL it opens. Loading that URL trades the
// token for an HttpOnly cookie; every /api/ and /ws route then needs the
// cookie. Without this, any local process (or any request without an Origin
// header, such as curl) could confirm a proposal.
//
// This file is the shell: randomness, cookies and the in-memory session set.
// The decisions are one-line comparisons, so there is nothing worth modelling
// in internal/core.

const (
	// SessionCookie is the cookie the UI session lives in.
	SessionCookie = "ygg_session"
	// launchParam is the query parameter that carries the launch token.
	launchParam = "k"
	// maxSessions bounds memory: only the holder of the launch token can add
	// one, and the oldest goes first.
	maxSessions = 64
)

// sessions holds the launch token and the live session ids, in memory only.
type sessions struct {
	mu    sync.Mutex
	token string   // the launch token; empty when sessions are disabled
	ids   []string // oldest first
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// tokenValid compares in constant time.
func (s *sessions) tokenValid(got string) bool {
	return s.token != "" && got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1
}

// open creates a session and returns its id.
func (s *sessions) open() (string, error) {
	id, err := randomHex(32)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.ids) >= maxSessions {
		s.ids = s.ids[1:]
	}
	s.ids = append(s.ids, id)
	return id, nil
}

// valid reports whether id is a live session, comparing in constant time.
func (s *sessions) valid(id string) bool {
	if id == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ok := 0
	for _, have := range s.ids {
		ok |= subtle.ConstantTimeCompare([]byte(id), []byte(have))
	}
	return ok == 1
}

// LaunchToken is the secret for this run, or "" when sessions are switched
// off. The caller may print it to the terminal and nowhere else.
func (s *Server) LaunchToken() string { return s.sess.token }

// sessionExempt lists what needs no session: the health check (the dev proxy
// and smoke tests poll it) and the MCP mount, which has its own per-task
// bearer. Static assets are outside /api/ and /ws.
func sessionExempt(path string) bool {
	return path == "/api/health" || path == "/mcp" || strings.HasPrefix(path, "/mcp/")
}

func sessionGated(path string) bool {
	if sessionExempt(path) {
		return false
	}
	return path == "/ws" || path == "/ws/terminal" || path == "/api" || strings.HasPrefix(path, "/api/")
}

// session is the middleware: it trades the launch token for a cookie and
// refuses gated routes without one.
func (s *Server) session(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.insecure {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/" && r.Method == http.MethodGet && r.URL.Query().Has(launchParam) {
			s.startSession(w, r)
			return
		}
		if sessionGated(r.URL.Path) {
			c, err := r.Cookie(SessionCookie)
			if err != nil || !s.sess.valid(c.Value) {
				writeError(w, http.StatusUnauthorized, ErrSessionRequired,
					"Open Yggdrasil from the link it printed when it started.",
					"Use the http://127.0.0.1 link with ?k=... shown in the terminal where ygg is running.")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// startSession handles GET /?k=<token>: a valid token sets the cookie; either
// way the browser is sent to / so the token leaves the address bar.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request) {
	if s.sess.tokenValid(r.URL.Query().Get(launchParam)) {
		id, err := s.sess.open()
		if err != nil {
			s.log.Error("create session", "err", err)
			writeError(w, http.StatusInternalServerError, ErrInternal,
				"Something went wrong inside Yggdrasil.", "Try again. If it keeps happening, check the Yggdrasil log.")
			return
		}
		// No Secure flag: the UI is plain http on loopback, where browsers
		// would not send a Secure cookie back.
		http.SetCookie(w, &http.Cookie{ //nolint:gosec // see above
			Name: SessionCookie, Value: id, Path: "/",
			HttpOnly: true, SameSite: http.SameSiteStrictMode,
		})
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
