package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/coder/websocket"
)

//go:embed index.html
var indexHTML []byte

// Wire protocol on /ws:
//   - binary frames carry terminal data in both directions;
//   - text frames carry JSON control messages: the client sends
//     {"type":"resize","cols":N,"rows":N}; the server sends
//     {"type":"started","pid":N} and {"type":"exit","code":N}.
//
// After the exit message the server closes with 1000 and reason "exit code N".
type message struct {
	Type string `json:"type"`
	Pid  int    `json:"pid,omitempty"`
	Code *int   `json:"code,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

func init() {
	// Windows takes MIME types from the registry, where .mjs is often
	// missing or text/plain; browsers refuse module scripts served that way.
	_ = mime.AddExtensionType(".mjs", "text/javascript")
}

type server struct {
	start Starter
	log   *slog.Logger
}

// newServer returns the demo handler. xtermDir is web/node_modules/@xterm;
// when empty the page loads but cannot start xterm.js.
func newServer(start Starter, xtermDir string, log *slog.Logger) http.Handler {
	s := &server{start: start, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /ws", s.handleWS)
	if xtermDir != "" {
		for _, pkg := range []string{"xterm", "addon-fit"} {
			files := http.StripPrefix("/assets/"+pkg+"/", http.FileServer(http.Dir(filepath.Join(xtermDir, pkg))))
			mux.Handle("GET /assets/"+pkg+"/lib/", files)
			mux.Handle("GET /assets/"+pkg+"/css/", files)
		}
	}
	return loopbackOnly(mux)
}

// loopbackOnly rejects requests whose Host isn't a loopback name, against
// DNS rebinding. The WebSocket Origin check is coder/websocket's default.
func loopbackOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		switch host {
		case "127.0.0.1", "localhost", "::1":
			next.ServeHTTP(w, r)
		default:
			http.Error(w, "forbidden host", http.StatusForbidden)
		}
	})
}

func (s *server) handleIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(indexHTML)
}

func (s *server) handleWS(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	path, args, err := shellCommand(q.Get("shell"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	size := Size{Cols: 80, Rows: 24}
	if v := q.Get("cols"); v != "" {
		size.Cols, _ = strconv.Atoi(v)
	}
	if v := q.Get("rows"); v != "" {
		size.Rows, _ = strconv.Atoi(v)
	}
	if err := size.validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		s.log.Warn("websocket accept", "err", err)
		return
	}
	defer func() { _ = c.CloseNow() }()
	// The default 32 KiB limit is too small for a large paste.
	c.SetReadLimit(1 << 20)

	p, err := s.start(Options{Path: path, Args: args, Size: size})
	if err != nil {
		s.log.Error("start shell", "shell", path, "err", err)
		_ = c.Close(websocket.StatusInternalError, "could not start the shell")
		return
	}
	defer func() {
		if err := p.Close(); err != nil {
			s.log.Warn("close pty", "pid", p.Pid(), "err", err)
		}
	}()
	s.log.Info("shell started", "shell", path, "pid", p.Pid(), "size", size)
	s.bridge(r.Context(), c, p)
}

// bridge copies data between the socket and the PTY until the process exits
// (exit message, then a normal close) or the socket goes away (the caller's
// deferred Close kills the process tree).
func (s *server) bridge(ctx context.Context, c *websocket.Conn, p PTY) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	if err := writeJSON(ctx, c, message{Type: "started", Pid: p.Pid()}); err != nil {
		return
	}

	var wg sync.WaitGroup
	outDone := make(chan struct{})
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer close(outDone)
		buf := make([]byte, 32*1024)
		socketOK := true
		for {
			n, err := p.Read(buf)
			if n > 0 && socketOK {
				if werr := c.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
					// Keep draining: ConPTY can block on a full output pipe.
					socketOK = false
					cancel()
				}
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		defer cancel()
		for {
			typ, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			switch typ {
			case websocket.MessageBinary:
				if _, err := p.Write(data); err != nil {
					return
				}
			case websocket.MessageText:
				var m message
				if err := json.Unmarshal(data, &m); err != nil || m.Type != "resize" {
					s.log.Warn("bad control message", "data", string(data))
					continue
				}
				if err := p.Resize(Size{Cols: m.Cols, Rows: m.Rows}); err != nil {
					s.log.Warn("resize", "err", err)
				}
			}
		}
	}()

	select {
	case <-outDone:
		code, err := p.Wait()
		if err != nil {
			s.log.Warn("wait", "pid", p.Pid(), "err", err)
		}
		s.log.Info("shell exited", "pid", p.Pid(), "code", code)
		if err := writeJSON(ctx, c, message{Type: "exit", Code: &code}); err == nil {
			_ = c.Close(websocket.StatusNormalClosure, fmt.Sprintf("exit code %d", code))
		}
	case <-ctx.Done():
		s.log.Info("socket closed, killing shell", "pid", p.Pid())
		if err := p.Close(); err != nil {
			s.log.Warn("close pty", "pid", p.Pid(), "err", err)
		}
	}
	cancel()
	wg.Wait()
}

func writeJSON(ctx context.Context, c *websocket.Conn, m message) error {
	b, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal %s message: %w", m.Type, err)
	}
	return c.Write(ctx, websocket.MessageText, b)
}
