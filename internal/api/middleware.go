package api

import (
	"net"
	"net/http"
	"net/url"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"time"
)

// guard rejects requests whose Host or Origin is not ours (SF-01): the Host
// check stops DNS rebinding, the Origin check stops other websites from
// calling the API with the user's browser. A request without an Origin
// header (curl, the page itself loading) is allowed.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, port := splitHost(r.Host)
		if !loopbackName(host) {
			reject(w)
			return
		}
		if p := int(s.port.Load()); p != 0 && port != "" && port != strconv.Itoa(p) {
			reject(w)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !s.originAllowed(origin, r.Host) {
			reject(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func reject(w http.ResponseWriter) {
	writeError(w, http.StatusForbidden, ErrHostRejected,
		"This request was refused because it didn't come from Yggdrasil's own page.",
		"Open Yggdrasil at http://127.0.0.1 or http://localhost on its port.")
}

// originAllowed reports whether origin is the page we serve (same host as the
// request) or one of the extra origins from the configuration.
func (s *Server) originAllowed(origin, requestHost string) bool {
	if slices.Contains(s.allowedOrigins, origin) {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return false // includes the opaque origin "null"
	}
	return strings.EqualFold(u.Host, requestHost)
}

// splitHost splits "host:port", "[::1]:port", "host" and "[::1]".
func splitHost(hostport string) (host, port string) {
	if h, p, err := net.SplitHostPort(hostport); err == nil {
		return h, p
	}
	return strings.Trim(hostport, "[]"), ""
}

// loopbackName accepts the names a user types for this machine: exactly
// "localhost" (not localhost.evil.com) and loopback IP addresses.
func loopbackName(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// recoverPanics turns a panic in a handler into a 500 ErrorBody.
func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler { //nolint:errorlint // sentinel panic value, not an error chain
					panic(v)
				}
				s.log.Error("handler panic", "path", r.URL.Path, "panic", v, "stack", string(debug.Stack()))
				writeError(w, http.StatusInternalServerError, ErrInternal,
					"Something went wrong inside Yggdrasil.", "Try again. If it keeps happening, check the Yggdrasil log.")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// statusWriter records the status for the access log. It keeps the optional
// interfaces the WebSocket upgrade needs.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// accessLog logs one line per request at debug level.
func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		s.log.Debug("request", "method", r.Method, "path", r.URL.Path, "status", sw.status, "ms", time.Since(start).Milliseconds())
	})
}

// contentSecurityPolicy: the page loads only its own files, can't be framed
// and can only talk to its own origin (the WebSocket included; the ws: sources
// cover browsers that don't count 'self' for ws://). Inline styles are allowed
// because the grid and Radix set styles on elements.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; connect-src 'self' ws://127.0.0.1:* ws://localhost:* ws://[::1]:*; " +
	"base-uri 'none'; form-action 'self'; frame-ancestors 'none'; object-src 'none'"

// headers sets the headers every response carries.
func headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
		next.ServeHTTP(w, r)
	})
}
