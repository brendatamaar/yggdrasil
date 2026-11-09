package api

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/devservers"
)

// The dev server endpoints (DV-01 to DV-03, DV-05; docs/design/devservers.md).
// The DTOs are in types_devservers.go and types_devservers_req.go. This file
// is Go-only wiring and is excluded from tygo (tygo.yaml).
//
// Clients name a server by its ID, never by a folder: the folder is resolved
// by the service from the tracked repo (invariants 5 and 6).

// DevServerService is implemented over *devservers.Service by an adapter in
// cmd/ygg. Start, Stop, Restart, Logs and ByID are the methods of that type.
// Servers lists the servers of every tracked repo and worktree: the stored
// ones, plus a stopped placeholder for every detected command that was never
// started (so the UI can offer a first start). ByID returns a stored server by
// its ID (NotFound for a placeholder). RepoName gives the display name of a
// repo ("" if unknown).
// Errors carry an apperr class (NotFound, Conflict, Invalid, Unavailable).
type DevServerService interface {
	Start(ctx context.Context, t devservers.Target, command string) (devservers.View, error)
	Stop(ctx context.Context, t devservers.Target, command string) (devservers.View, error)
	Restart(ctx context.Context, t devservers.Target, command string) (devservers.View, error)
	Logs(t devservers.Target, command string, n int) ([]devservers.LogLine, error)
	ByID(ctx context.Context, id string) (devservers.View, error)
	Servers(ctx context.Context) ([]devservers.View, error)
	RepoName(ctx context.Context, repo core.RepoID) string
}

const (
	defaultLogTail = 200
	maxLogTail     = devservers.RingLines
)

func (s *Server) routesDevServers(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/devservers", s.handleListDevServers)
	mux.HandleFunc("POST /api/devservers/{id}/start", s.devServerAction("Starting a dev server", DevServerService.Start))
	mux.HandleFunc("POST /api/devservers/{id}/stop", s.devServerAction("Stopping a dev server", DevServerService.Stop))
	mux.HandleFunc("POST /api/devservers/{id}/restart", s.devServerAction("Restarting a dev server", DevServerService.Restart))
	mux.HandleFunc("GET /api/devservers/{id}/log", s.handleDevServerLog)
}

// devServerDTO converts a view. Ports are the listening ports; a port the
// output announced and a port outside the worktree's block carry their flags.
func devServerDTO(v devservers.View, repoName string) DevServerDTO {
	announced := map[int]bool{}
	for _, p := range v.Announced {
		announced[int(p)] = true
	}
	outside := map[int]bool{}
	for _, p := range v.Warnings {
		outside[int(p)] = true
	}
	ports := make([]DevServerPortDTO, 0, len(v.Ports))
	for _, p := range v.Ports {
		ports = append(ports, DevServerPortDTO{Port: int(p), Announced: announced[int(p)], OutsideBlock: outside[int(p)]})
	}
	msg := v.Message
	if msg == "" {
		msg = v.Warning
	}
	d := DevServerDTO{
		ID: core.DevServerID(v.ID), RepoID: v.Key.Repo, RepoName: repoName, WorktreeID: v.Key.Worktree,
		Command: v.Key.CommandKey, Status: DevServerStatus(v.Status), Ports: ports,
		BlockBase: v.Block.Base, BlockSize: v.Block.Size, Message: msg,
	}
	if !v.Since.IsZero() {
		t := v.Since
		d.Since = &t
	}
	return d
}

func (s *Server) handleListDevServers(w http.ResponseWriter, r *http.Request) {
	svc := s.phase5().DevServers
	if svc == nil {
		notReady(w, "Dev servers")
		return
	}
	views, err := svc.Servers(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	names := map[core.RepoID]string{}
	out := DevServersResponse{Servers: make([]DevServerDTO, 0, len(views))}
	for _, v := range views {
		name, ok := names[v.Key.Repo]
		if !ok {
			name = svc.RepoName(r.Context(), v.Key.Repo)
			names[v.Key.Repo] = name
		}
		out.Servers = append(out.Servers, devServerDTO(v, name))
	}
	writeJSON(w, http.StatusOK, out)
}

func serverNotFound(w http.ResponseWriter) {
	writeError(w, http.StatusNotFound, ErrNotFound, "That dev server doesn't exist (any more).", "Refresh the page and try again.")
}

// devServerTarget finds the target and command a server ID stands for. A body
// naming the target is checked against the ID (so the ID can't point at a
// different server than the body); without a body the ID must belong to a
// server the service lists.
func (s *Server) devServerTarget(w http.ResponseWriter, r *http.Request, svc DevServerService, withBody bool) (core.DevServerID, devservers.Target, string, bool) {
	id := core.DevServerID(r.PathValue("id"))
	if id.Validate() != nil {
		serverNotFound(w)
		return "", devservers.Target{}, "", false
	}
	if withBody {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
		if err != nil {
			badRequest(w, "The request body isn't valid.", "Send a JSON object with the documented fields.")
			return "", devservers.Target{}, "", false
		}
		if len(bytes.TrimSpace(raw)) > 0 {
			var req DevServerActionRequest
			r.Body = io.NopCloser(bytes.NewReader(raw))
			if !decodeBody(w, r, &req) {
				return "", devservers.Target{}, "", false
			}
			key, err := devservers.ParseStart(string(req.RepoID), string(req.WorktreeID), req.Command)
			if err != nil {
				s.fail(w, r, err)
				return "", devservers.Target{}, "", false
			}
			if devservers.IDOf(key) != string(id) {
				badRequest(w, "The server named in the request doesn't match its address.", "Reload the page and try again.")
				return "", devservers.Target{}, "", false
			}
			return id, devservers.Target{Repo: key.Repo, Worktree: key.Worktree}, key.CommandKey, true
		}
	}
	v, err := svc.ByID(r.Context(), string(id))
	if err != nil {
		s.fail(w, r, err)
		return "", devservers.Target{}, "", false
	}
	return id, devservers.Target{Repo: v.Key.Repo, Worktree: v.Key.Worktree}, v.Key.CommandKey, true
}

func (s *Server) devServerAction(what string, do func(DevServerService, context.Context, devservers.Target, string) (devservers.View, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		svc := s.phase5().DevServers
		if svc == nil {
			notReady(w, what)
			return
		}
		_, t, command, ok := s.devServerTarget(w, r, svc, true)
		if !ok {
			return
		}
		v, err := do(svc, r.Context(), t, command)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, devServerDTO(v, svc.RepoName(r.Context(), v.Key.Repo)))
	}
}

func (s *Server) handleDevServerLog(w http.ResponseWriter, r *http.Request) {
	svc := s.phase5().DevServers
	if svc == nil {
		notReady(w, "Dev server logs")
		return
	}
	tail := defaultLogTail
	if q := r.URL.Query().Get("tail"); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil || n < 1 || n > maxLogTail {
			badRequest(w, "The number of log lines isn't valid.", "Use a number from 1 to "+strconv.Itoa(maxLogTail)+".")
			return
		}
		tail = n
	}
	id, t, command, ok := s.devServerTarget(w, r, svc, false)
	if !ok {
		return
	}
	lines, err := svc.Logs(t, command, tail)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// The numbers come from the service's log: the same Seq the live messages
	// carry, so a client can merge the two by replacing on Seq. The stream is
	// "stdout", "stderr", or "system" for a line Yggdrasil wrote itself.
	out := DevServerLogResponse{Lines: make([]DevServerLogMessage, 0, len(lines))}
	for _, l := range lines {
		out.Lines = append(out.Lines, logMessage(id, l))
	}
	writeJSON(w, http.StatusOK, out)
}

func logMessage(id core.DevServerID, l devservers.LogLine) DevServerLogMessage {
	return DevServerLogMessage{ID: id, Epoch: l.Epoch, Seq: l.Seq, Stream: string(l.Stream), Line: l.Line}
}

var _ devservers.Publisher = (*Server)(nil)

// PublishServer implements devservers.Publisher: a server changed. It goes to
// "devservers" and "devserver:<id>" and holds no path.
func (s *Server) PublishServer(id string, v devservers.View) {
	name := ""
	if svc := s.phase5().DevServers; svc != nil {
		name = svc.RepoName(s.ctx, v.Key.Repo)
	}
	msg := DevServerChangedMessage{Server: devServerDTO(v, name)}
	s.hub.publishNext(devserversTopic{}, msg)
	s.hub.publishNext(devserverTopic{ID: core.DevServerID(id)}, msg)
}

// PublishLog implements devservers.Publisher: one redacted log line.
func (s *Server) PublishLog(id string, line devservers.LogLine) {
	s.hub.publishNext(devserverTopic{ID: core.DevServerID(id)}, logMessage(core.DevServerID(id), line))
}
