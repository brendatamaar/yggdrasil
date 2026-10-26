// Package api is Yggdrasil's HTTP server: REST endpoints, the WebSocket hub
// and the embedded UI, bound to the loopback interface (SF-01).
package api

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// Deps are what the server needs. Store is required.
type Deps struct {
	Store *db.Store
	// Adapters is unused since the tasks service owns the runs.
	//
	Adapters map[string]agents.Adapter
	// Env is unused since the tasks service owns the runs.
	//
	Env safety.EnvBuilder
	// Log defaults to a redacting text logger on stderr.
	Log *slog.Logger
	// Redactor masks tokens in agent output before it is stored or sent.
	// Default: the built-in patterns of safety.NewRedactor().
	Redactor safety.Redactor
	Version  string
	// Assets is the embedded frontend build (the contents of web/dist).
	Assets fs.FS
	// AllowedOrigins are extra browser origins that may call the API, such as
	// the Vite dev server (http://localhost:5173).
	AllowedOrigins []string
	// KillTask is unused since the tasks service owns the runs.
	//
	KillTask func(taskID string) error

	// The services of the Phase 2 areas. A nil service registers no route
	// (each routesX returns early), so the server runs with any subset.
	Tracking TrackingService
	Status   StatusService
	Builds   BuildService
	Hosts    HostsService
	Search   SearchService
	// RepoDetail serves the read-only repo page endpoints (WS-05).
	RepoDetail RepoDetailService
	// GitActions serves fetch, pull, checkout, branch switch, open in editor
	// and the editor setting (WS-06 to WS-08).
	GitActions GitActionsService

	// The services of the Phase 3 areas. Their endpoints are registered
	// whether or not the service is wired; until the owning task replaces a
	// stub handler it answers 501 with a plain message.
	Worktrees WorktreeService
	Proposals ProposalService
	Composer  ComposerService
	MCP       MCPService
	// Tasks runs tasks, the board and repo trust (TaskService). Without it the
	// task, board and trust endpoints answer 501.
	Tasks TaskService
	// WriteModes allows modes other than Ask in POST /api/tasks and /messages.
	// Default false: the lead flips the default after review gate 3.
	WriteModes bool

	// Clock and IDs are the shell's time and randomness for handlers that
	// make core inputs (docs/AGENTS.md 5.11). Defaults: time.Now().UTC() and
	// db.NewID.
	Clock func() time.Time
	IDs   func(prefix string) string
}

// Server serves the API. Create it with New, run it with Run (or use Handler
// in tests) and stop it with Close.
type Server struct {
	store    *db.Store
	log      *slog.Logger
	redactor safety.Redactor
	version  string
	assets   assets

	allowedOrigins []string
	port           atomic.Int32 // the listener's port once Run has started

	hub *hub

	tracking   TrackingService
	status     StatusService
	builds     BuildService
	hosts      HostsService
	search     SearchService
	repoDetail RepoDetailService
	gitActions GitActionsService
	worktrees  WorktreeService
	proposals  ProposalService
	tasks      TaskService
	writeModes bool
	composer   ComposerService
	mcp        MCPService
	clock      func() time.Time
	ids        func(prefix string) string

	// ctx is the parent of background work of the handlers; Close cancels it.
	ctx  context.Context
	stop context.CancelFunc

	closeOnce sync.Once
}

// New builds a server.
func New(d Deps) (*Server, error) {
	if d.Store == nil {
		return nil, errors.New("api: a store is required")
	}
	redactor := d.Redactor
	if redactor == nil {
		redactor = safety.NewRedactor()
	}
	log := d.Log
	if log == nil {
		log = slog.New(safety.NewLogHandler(slog.NewTextHandler(os.Stderr, nil), redactor))
	}
	as, err := loadAssets(d.Assets)
	if err != nil {
		return nil, fmt.Errorf("read the embedded frontend: %w", err)
	}
	ctx, stop := context.WithCancel(context.Background())
	s := &Server{
		store: d.Store, log: log, redactor: redactor,
		version: d.Version, assets: as, allowedOrigins: d.AllowedOrigins,
		hub: newHub(log), ctx: ctx, stop: stop,
	}
	s.tracking, s.status, s.builds, s.hosts, s.search = d.Tracking, d.Status, d.Builds, d.Hosts, d.Search
	s.repoDetail = d.RepoDetail
	s.gitActions = d.GitActions
	s.worktrees, s.proposals, s.composer, s.mcp = d.Worktrees, d.Proposals, d.Composer, d.MCP
	s.tasks, s.writeModes = d.Tasks, d.WriteModes
	s.clock, s.ids = d.Clock, d.IDs
	if s.clock == nil {
		s.clock = func() time.Time { return time.Now().UTC() }
	}
	if s.ids == nil {
		s.ids = db.NewID
	}
	return s, nil
}

// Handler returns the full handler: security headers, panic recovery, access
// log, Host/Origin guard, then the routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/tasks", s.listTasks)
	mux.HandleFunc("POST /api/tasks", s.createTask)
	mux.HandleFunc("POST /api/tasks/{id}/messages", s.messageTask)
	mux.HandleFunc("POST /api/tasks/{id}/dismiss", s.dismissTask)
	mux.HandleFunc("GET /api/tasks/{id}", s.getTask)
	mux.HandleFunc("GET /api/tasks/{id}/events", s.taskEvents)
	mux.HandleFunc("POST /api/tasks/{id}/cancel", s.cancelTask)
	mux.HandleFunc("GET /ws", s.serveWS)
	s.routesRepos(mux)
	s.routesReposActions(mux)
	s.routesBuilds(mux)
	s.routesHosts(mux)
	s.routesSearch(mux)
	s.routesWorktrees(mux)
	s.routesProposals(mux)
	s.routesTrust(mux)
	s.routesBoard(mux)
	s.routesComposer(mux)
	s.routesMCP(mux)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, ErrNotFound, "That API endpoint doesn't exist.", "")
	})
	mux.HandleFunc("/", s.serveUI)
	return headers(s.recoverPanics(s.accessLog(s.guard(mux))))
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, HealthResponse{Status: "ok", Version: s.version})
}

// Run serves on ln until ctx is cancelled, then shuts down: HTTP first, then
// the WebSocket clients, then every running task. ln must be a loopback
// listener (remote access mode, RA-*, does not exist yet).
func (s *Server) Run(ctx context.Context, ln net.Listener) error {
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok || !addr.IP.IsLoopback() {
		return fmt.Errorf("refusing to serve on %s: Yggdrasil only listens on the loopback interface (127.0.0.1)", ln.Addr())
	}
	s.port.Store(int32(addr.Port)) //nolint:gosec // a TCP port fits in int32
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }() // ends when srv is shut down; its result is read below

	var serveErr error
	select {
	case serveErr = <-errc:
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			s.log.Warn("http shutdown", "err", err)
		}
		serveErr = <-errc
	}
	s.Close()
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return fmt.Errorf("serve http: %w", serveErr)
	}
	return nil
}

// Close stops every running task and WebSocket client and waits for them.
// It is safe to call more than once.
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		s.stop()
		s.hub.closeAll()
		s.hub.wait()
	})
}
