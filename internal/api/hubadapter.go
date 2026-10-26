package api

import (
	"sync/atomic"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/tasks"
)

// This file is Go-only wiring and is excluded from tygo (tygo.yaml).

// HubAdapter implements tasks.Hub on the server's WebSocket hub. The tasks
// service is built before the server (the server needs it in Deps), so the
// adapter is created first and bound once the server exists:
//
//	hub := &api.HubAdapter{}
//	svc := tasks.New(tasks.Config{Hub: hub, ...})
//	srv := api.New(api.Deps{Tasks: svc, ...})
//	hub.Bind(srv)
//
// Until Bind, and after the server is closed, messages are dropped. Neither
// method blocks: the hub drops a client that cannot keep up.
type HubAdapter struct{ srv atomic.Pointer[Server] }

var _ tasks.Hub = (*HubAdapter)(nil)

// Bind connects the adapter to the server. Call it once, before the service
// starts any task.
func (h *HubAdapter) Bind(s *Server) { h.srv.Store(s) }

// Status tells clients a task changed status: on the "tasks" topic (title and
// status only, docs/AGENTS.md 5.9) and on the "board" topic, so the board is
// refetched.
func (h *HubAdapter) Status(m tasks.StatusMessage) {
	s := h.srv.Load()
	if s == nil {
		return
	}
	s.hub.publishNext(tasksTopic{}, TaskStatusMessage{TaskID: m.TaskID, Title: m.Title, Status: m.Status})
	s.hub.publishNext(boardTopic{}, BoardChangedMessage{TaskID: m.TaskID})
}

// Event sends a stored transcript event on the task's topic with the sequence
// number it was stored under.
func (h *HubAdapter) Event(task core.TaskID, seq int64, e agents.Event) {
	s := h.srv.Load()
	if s == nil {
		return
	}
	s.hub.publishSeq(taskTopic{ID: task}, seq, e)
}
