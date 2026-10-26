package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/tasks"
)

// The runs themselves belong to internal/tasks (docs/design/tasks.md); these
// handlers parse the request, call the TaskService and translate its errors.

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	status := core.TaskStatus(q.Get("status"))
	if status != "" && status.Validate() != nil {
		badRequest(w, "That status filter isn't known.", "Use one of: draft, queued, running, needs_you, review, done, failed, cancelled.")
		return
	}
	var projects []core.ProjectID
	if p := q.Get("project"); p != "" {
		// An untracked project is invisible: its tasks are history, not a list.
		if _, err := s.store.GetProject(r.Context(), core.ProjectID(p)); err != nil {
			s.fail(w, r, taskErr(err))
			return
		}
		projects = []core.ProjectID{core.ProjectID(p)}
	} else {
		all, err := s.store.ListProjects(r.Context())
		if err != nil {
			s.fail(w, r, taskErr(err))
			return
		}
		for _, p := range all {
			projects = append(projects, p.ID)
		}
	}
	out := []core.Task{}
	for _, p := range projects {
		tasks, err := s.store.ListTasks(r.Context(), p)
		if err != nil {
			s.fail(w, r, taskErr(err))
			return
		}
		for _, t := range tasks {
			if status == "" || t.Status == status {
				out = append(out, t)
			}
		}
	}
	writeJSON(w, http.StatusOK, TaskListResponse{Tasks: out})
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	task, ok := s.lookupTask(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, TaskResponse{Task: task})
}

// lookupTask reads the {id} of the path and loads the task.
func (s *Server) lookupTask(w http.ResponseWriter, r *http.Request) (core.Task, bool) {
	id := core.TaskID(r.PathValue("id"))
	if id.Validate() != nil {
		badRequest(w, "That task ID isn't valid.", "")
		return core.Task{}, false
	}
	task, err := s.store.GetTask(r.Context(), id)
	if err != nil {
		s.fail(w, r, taskErr(err))
		return core.Task{}, false
	}
	return task, true
}

func (s *Server) taskEvents(w http.ResponseWriter, r *http.Request) {
	task, ok := s.lookupTask(w, r)
	if !ok {
		return
	}
	var after int64
	if v := r.URL.Query().Get("after"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			badRequest(w, "The 'after' value must be a number of 0 or more.", "")
			return
		}
		after = n
	}
	rows, err := s.store.ListTaskEvents(r.Context(), task.ID, after)
	if err != nil {
		s.fail(w, r, taskErr(err))
		return
	}
	events := make([]agents.Event, 0, len(rows))
	for _, row := range rows {
		var e agents.Event
		if err := json.Unmarshal(row.Data, &e); err != nil {
			s.fail(w, r, fmt.Errorf("decode stored event %d of %s: %w", row.Seq, task.ID, err))
			return
		}
		events = append(events, e)
	}
	writeJSON(w, http.StatusOK, TaskEventsResponse{Events: events})
}


// errWriteModes is shown when a write mode is asked for while the flag is off.
const errWriteModes = "Code mode is not enabled in this version."

// writeModesHint is the hint that goes with errWriteModes.
const writeModesHint = "Use Ask mode (read-only) for now."

// parseCreate turns the body of POST /api/tasks into a service request. It
// applies only the shape rules of the API (JSON, known fields, the scope
// default) and leaves every message rule to coretasks.ParseMessage, which it
// calls so a bad request is a 400 before the service is touched. The returned
// error is always a *coretasks.UserError.
func parseCreate(body []byte) (tasks.MessageRequest, error) {
	var req CreateTaskRequest
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return tasks.MessageRequest{}, &coretasks.UserError{Message: "The request body isn't valid. Send a JSON object with the documented fields."}
	}
	scope := req.Scope
	if scope == "" {
		scope = string(core.ScopeRepo)
		if req.Mode == core.ModeProject {
			scope = string(core.ScopeProject)
		}
	}
	repos := make([]string, len(req.RepoIDs))
	for i, id := range req.RepoIDs {
		repos[i] = string(id)
	}
	raw := coretasks.RawMessage{
		Prompt: req.Prompt, Mode: string(req.Mode), Scope: scope, Agent: req.Agent,
		Model: req.Model, Repos: repos, Title: req.Title,
	}
	pm, err := coretasks.ParseMessage(raw)
	if err != nil {
		return tasks.MessageRequest{}, err
	}
	if err := req.ProjectID.Validate(); err != nil {
		return tasks.MessageRequest{}, &coretasks.UserError{Message: "Choose a project for the task."}
	}
	// The service parses again; it gets the raw strings so both read the same.
	return tasks.MessageRequest{
		ProjectID: req.ProjectID, Prompt: req.Prompt, Mode: string(pm.Mode), Scope: string(pm.Scope),
		Repos: repos, Agent: pm.Agent, Model: pm.Model, Title: req.Title, OptIn: req.OptIn,
	}, nil
}

// writeModeAllowed reports whether the mode may be used in this version.
func (s *Server) writeModeAllowed(mode string) bool {
	return s.writeModes || mode == string(core.ModeAsk)
}

// service returns the task service, or answers 501 when none is wired.
func (s *Server) service(w http.ResponseWriter, what string) (TaskService, bool) {
	if s.tasks == nil {
		notReady(w, what)
		return nil, false
	}
	return s.tasks, true
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.service(w, "Creating tasks")
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r.Body); err != nil {
		badRequest(w, "The request body isn't valid.", "Send a JSON object with the documented fields.")
		return
	}
	req, err := parseCreate(buf.Bytes())
	if err != nil {
		s.serviceErr(w, r, err)
		return
	}
	if !s.writeModeAllowed(req.Mode) {
		badRequest(w, errWriteModes, writeModesHint)
		return
	}
	res, err := svc.Message(r.Context(), req)
	if err != nil {
		s.serviceErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, TaskResponse{Task: res.Task, Warning: res.Warning})
}

func (s *Server) messageTask(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.service(w, "Sending messages")
	if !ok {
		return
	}
	id := core.TaskID(r.PathValue("id"))
	if id.Validate() != nil {
		badRequest(w, "That task ID isn't valid.", "")
		return
	}
	var req MessageRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if !s.writeModeAllowed(string(req.Mode)) {
		badRequest(w, errWriteModes, writeModesHint)
		return
	}
	res, err := svc.Message(r.Context(), tasks.MessageRequest{
		TaskID: id, Prompt: req.Prompt, Mode: string(req.Mode), Model: req.Model, OptIn: req.OptIn,
	})
	if err != nil {
		s.serviceErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, TaskResponse{Task: res.Task, Warning: res.Warning})
}

func (s *Server) cancelTask(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.service(w, "Cancelling tasks")
	if !ok {
		return
	}
	id := core.TaskID(r.PathValue("id"))
	if id.Validate() != nil {
		badRequest(w, "That task ID isn't valid.", "")
		return
	}
	task, err := svc.Cancel(r.Context(), id)
	if err != nil {
		s.serviceErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, TaskResponse{Task: task})
}

func (s *Server) dismissTask(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.service(w, "Marking tasks done")
	if !ok {
		return
	}
	id := core.TaskID(r.PathValue("id"))
	if id.Validate() != nil {
		badRequest(w, "That task ID isn't valid.", "")
		return
	}
	task, err := svc.Dismiss(r.Context(), id)
	if err != nil {
		s.serviceErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, TaskResponse{Task: task})
}


// taskErr translates what the store reports about tasks, projects and repos
// into an error class with a message for people. Other errors pass through
// unchanged and become a generic 500 in fail.
func taskErr(err error) error {
	switch {
	case errors.Is(err, db.ErrNotFound):
		return apperr.New(apperr.NotFound, "That item doesn't exist (any more).", err)
	case errors.Is(err, db.ErrSubTaskDepth):
		return apperr.New(apperr.Conflict, "Sub-tasks can only be one level deep. Create the task without a parent.", err)
	case errors.Is(err, db.ErrConflict):
		return apperr.New(apperr.Conflict, "That item changed in the meantime.", err)
	}
	return err
}
