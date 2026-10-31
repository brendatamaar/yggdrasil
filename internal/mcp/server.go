package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/brendatamaar/yggdrasil/internal/core"
	cmcp "github.com/brendatamaar/yggdrasil/internal/core/mcp"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// Config wires the server. Every service is required; Logger, Clock, Redactor
// and Sessions have defaults.
type Config struct {
	Workspace  Workspace
	Reader     Reader
	Builds     Builds
	Git        GitActions
	Hosts      Hosts
	DevServers DevServers
	Proposer   Proposer
	Audit      Auditor

	// Sessions is the token registry; the tasks service issues and revokes
	// through it. Default: a new registry that adds tokens to Redactor.
	Sessions *Sessions
	Redactor safety.Redactor
	Clock    func() time.Time
	Logger   *slog.Logger
	// DataDir is Yggdrasil's own data folder; its path never reaches an agent.
	DataDir string
	// Version is reported to MCP clients.
	Version string
	// URL is the address agents are given (for the status page); no token.
	URL string
}

// Server is the MCP server (MCP-01 to MCP-06): a streamable-HTTP handler,
// stateless on the wire, with one bearer token per running task.
type Server struct {
	cfg      Config
	sessions *Sessions
	handler  http.Handler

	gmu   sync.Mutex
	gates map[core.TaskID]*gate
}

// gate is the call-rate state of one session: the token bucket of the core
// limiter and the in-flight semaphore.
type gate struct {
	mu     sync.Mutex
	bucket cmcp.Bucket
	sem    chan struct{}
}

// New checks cfg and returns the server.
func New(cfg Config) (*Server, error) {
	switch {
	case cfg.Workspace == nil, cfg.Reader == nil, cfg.Builds == nil, cfg.Git == nil, cfg.Hosts == nil,
		cfg.DevServers == nil, cfg.Proposer == nil, cfg.Audit == nil:
		return nil, errors.New("mcp: a service is missing")
	}
	if cfg.Redactor == nil {
		cfg.Redactor = safety.NewRedactor()
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.Version == "" {
		cfg.Version = "dev"
	}
	if cfg.Sessions == nil {
		var sink SecretSink
		if s, ok := cfg.Redactor.(SecretSink); ok {
			sink = s
		}
		cfg.Sessions = NewSessions(sink, nil)
	}
	s := &Server{cfg: cfg, sessions: cfg.Sessions, gates: map[core.TaskID]*gate{}}
	sh := sdk.NewStreamableHTTPHandler(s.serverFor, &sdk.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true,
	})
	s.handler = s.authenticate(sh)
	return s, nil
}

// Issue starts a session for a run and returns its bearer token. The token goes
// into the agent's MCP config file and nowhere else.
func (s *Server) Issue(task core.TaskID, attempt int) (string, error) {
	return s.sessions.Issue(task, attempt)
}

// Revoke ends the task's session of that attempt (AnyAttempt: whichever);
// later calls with its token get 401. A session of another attempt is kept.
func (s *Server) Revoke(task core.TaskID, attempt int) {
	if !s.sessions.Revoke(task, attempt) {
		return
	}
	s.gmu.Lock()
	delete(s.gates, task)
	s.gmu.Unlock()
}

// Sessions is the token registry, for the code that starts and ends runs.
func (s *Server) Sessions() *Sessions { return s.sessions }

// Handler serves /mcp. It checks the bearer token itself.
func (s *Server) Handler() http.Handler { return s.handler }

// ActiveSessions is the number of tasks with a live session.
func (s *Server) ActiveSessions() int { return s.sessions.Count() }

// URL is the loopback address agents are given.
func (s *Server) URL() string { return s.cfg.URL }

type sessKey struct{}

// authenticate refuses a request that is not an MCP client of a running task:
// any Origin header (an MCP client is not a browser) gets 403; a missing,
// wrong, revoked or other-task token gets 401 with an empty body, so nothing
// about the task leaks. The body is capped at 1 MiB.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		tok, ok := bearer(r.Header.Get("Authorization"))
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		act, ok := s.sessions.Lookup(tok)
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, cmcp.MaxRequestBytes)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessKey{}, act)))
	})
}

// bearer extracts the token of "Bearer <token>".
func bearer(h string) (string, bool) {
	const p = "bearer "
	if len(h) <= len(p) || !strings.EqualFold(h[:len(p)], p) {
		return "", false
	}
	t := strings.TrimSpace(h[len(p):])
	return t, t != ""
}

// serverFor builds the MCP server for one request: it offers exactly the tools
// the task's context allows (core Offered).
func (s *Server) serverFor(r *http.Request) *sdk.Server {
	act, ok := r.Context().Value(sessKey{}).(Active)
	if !ok {
		return nil
	}
	srv := sdk.NewServer(&sdk.Implementation{Name: "ygg", Version: s.cfg.Version}, &sdk.ServerOptions{
		Instructions: "Yggdrasil's workspace tools. Read, build and fetch freely; everything that changes a repo is a proposal the user confirms.",
	})
	for _, t := range s.offered(r.Context(), act) {
		d := descriptors[t]
		name := string(t)
		srv.AddTool(&sdk.Tool{Name: name, Description: d.desc, InputSchema: schemaOf(d)},
			func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				return s.call(ctx, act, name, req.Params.Arguments)
			})
	}
	return srv
}

// offered is core Offered plus the one shell-only rule: merge requests need a
// GitLab remote on a repo the task works in.
func (s *Server) offered(ctx context.Context, act Active) []cmcp.Tool {
	snap, err := s.cfg.Workspace.Snapshot(ctx, act.Task)
	if err != nil {
		return nil
	}
	cctx := s.coreContext(act, snap)
	out := cmcp.Offered(cctx)
	if i := slices.Index(out, cmcp.ToolProposeMR); i >= 0 && !s.anyGitLab(ctx, snap) {
		out = slices.Delete(out, i, i+1)
	}
	return out
}

func (s *Server) anyGitLab(ctx context.Context, snap Snapshot) bool {
	for _, r := range snap.Repos {
		if !r.InScope {
			continue
		}
		if ri, err := s.cfg.Hosts.Remote(ctx, r.ID); err == nil && ri.GitLab {
			return true
		}
	}
	return false
}

func (s *Server) coreContext(act Active, snap Snapshot) cmcp.Context {
	c := cmcp.Context{
		Task: act.Task, Live: s.sessions.Live(act.Task), Mode: snap.Mode,
		HasWorktree: map[core.RepoID]bool{},
	}
	for _, r := range snap.Repos {
		c.Project = append(c.Project, r.ID)
		if r.InScope {
			c.Scope = append(c.Scope, r.ID)
		}
		if r.Worktree != "" {
			c.HasWorktree[r.ID] = true
		}
	}
	return c
}

// gateFor returns the task's gate, or nil when the session is no longer live:
// a call that was in flight when the session was revoked must not recreate the
// gate Revoke just removed. Revoke ends the session before it takes gmu, so a
// live check under gmu cannot miss it.
func (s *Server) gateFor(task core.TaskID) *gate {
	s.gmu.Lock()
	defer s.gmu.Unlock()
	if !s.sessions.Live(task) {
		return nil
	}
	g := s.gates[task]
	if g == nil {
		g = &gate{sem: make(chan struct{}, cmcp.MaxInFlight)}
		s.gates[task] = g
	}
	return g
}

func (g *gate) allow(now time.Time) cmcp.Allowance {
	g.mu.Lock()
	defer g.mu.Unlock()
	var a cmcp.Allowance
	g.bucket, a = cmcp.StepLimiter(g.bucket, cmcp.Tick{Now: now})
	return a
}

// newCallID makes the ID that ties a generic failure to its log line.
func newCallID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func genericFailure(id string) string {
	return fmt.Sprintf("That tool failed. See the Yggdrasil log (call %s).", id)
}
