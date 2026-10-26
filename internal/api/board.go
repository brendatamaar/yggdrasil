package api

import (
	"net/http"
	"strconv"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
	"github.com/brendatamaar/yggdrasil/internal/tasks"
)

// The board endpoints (AG-11, AG-12, AG-15) read through TaskService. The DTOs
// are in types_board.go. The file is excluded from tygo (tygo.yaml): it holds
// Go-only wiring.

// routesBoard registers the board endpoints.
func (s *Server) routesBoard(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/board", s.handleGetBoard)
	mux.HandleFunc("GET /api/board/max-agents", s.handleGetMaxAgents)
	mux.HandleFunc("PUT /api/board/max-agents", s.handleSetMaxAgents)
}

func (s *Server) handleGetBoard(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.service(w, "The agent board")
	if !ok {
		return
	}
	project := core.ProjectID(r.URL.Query().Get("project"))
	if project != "" && project.Validate() != nil {
		badRequest(w, "That project ID isn't valid.", "")
		return
	}
	view, err := svc.Board(r.Context(), project)
	if err != nil {
		s.serviceErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, boardResponse(view))
}

// boardResponse maps the read model to its DTO. Drafts are left out by the
// service; they are skipped here too, so a bug there cannot show them.
func boardResponse(v tasks.BoardView) BoardResponse {
	out := BoardResponse{MaxAgents: v.Max, Active: v.Active, Hint: RoomHintDTO{RaiseTo: v.Hint.RaiseTo}, Cards: []BoardCardDTO{}}
	for _, c := range v.Cards {
		if c.Task.Status == core.TaskDraft {
			continue
		}
		ids := c.Task.RepoIDs
		if ids == nil {
			ids = []core.RepoID{}
		}
		out.Cards = append(out.Cards, BoardCardDTO{
			TaskID: c.Task.ID, Title: c.Task.Title, Mode: c.Task.Mode, Agent: c.Task.Agent,
			Status: c.Task.Status, Column: string(c.Column), RepoNames: []string{}, RepoIDs: ids,
			PendingProposals: c.PendingProposals, UpdatedAt: c.Task.UpdatedAt,
		})
	}
	return out
}

func (s *Server) maxAgentsResponse(r *http.Request, svc TaskService) (MaxAgentsResponse, error) {
	n, err := svc.MaxAgents(r.Context())
	if err != nil {
		return MaxAgentsResponse{}, err
	}
	// The room hint comes with the board view.
	view, err := svc.Board(r.Context(), "")
	if err != nil {
		return MaxAgentsResponse{}, err
	}
	return MaxAgentsResponse{
		MaxAgents: n, Min: coretasks.MinMaxAgents, Max: coretasks.MaxMaxAgents,
		Hint: RoomHintDTO{RaiseTo: view.Hint.RaiseTo},
	}, nil
}

func (s *Server) handleGetMaxAgents(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.service(w, "The number of agents")
	if !ok {
		return
	}
	resp, err := s.maxAgentsResponse(r, svc)
	if err != nil {
		s.serviceErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleSetMaxAgents(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.service(w, "Changing the number of agents")
	if !ok {
		return
	}
	var req SetMaxAgentsRequest
	if !decodeBody(w, r, &req) {
		return
	}
	value := strconv.Itoa(req.MaxAgents)
	if _, err := coretasks.ParseMax(value); err != nil {
		badRequest(w, "The number of agents must be from "+strconv.Itoa(coretasks.MinMaxAgents)+" to "+strconv.Itoa(coretasks.MaxMaxAgents)+".", "")
		return
	}
	if err := svc.SetMaxAgents(r.Context(), value); err != nil {
		s.serviceErr(w, r, err)
		return
	}
	resp, err := s.maxAgentsResponse(r, svc)
	if err != nil {
		s.serviceErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
