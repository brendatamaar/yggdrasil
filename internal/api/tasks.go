package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// Sub-agent caps applied to every run (AG-22 defaults).
const (
	maxSubagentDepth       = 2
	maxConcurrentSubagents = 4
)

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	status := core.TaskStatus(q.Get("status"))
	if status != "" && status.Validate() != nil {
		badRequest(w, "That status filter isn't known.", "Use one of: draft, queued, running, needs_you, review, done, failed, cancelled.")
		return
	}
	var projects []core.ProjectID
	if p := q.Get("project"); p != "" {
		projects = []core.ProjectID{core.ProjectID(p)}
	} else {
		all, err := s.store.ListProjects(r.Context())
		if err != nil {
			s.fail(w, r, err)
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
			s.fail(w, r, err)
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
		s.fail(w, r, err)
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
		s.fail(w, r, err)
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

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	var req CreateTaskRequest
	if !decodeBody(w, r, &req) {
		return
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		badRequest(w, "The prompt is empty.", "Write what the agent should look into.")
		return
	}
	// Safety decision: only Ask mode until worktrees, the path guard and the
	// command allowlist exist (SF-06). Do not relax this in the API.
	if req.Mode != core.ModeAsk {
		badRequest(w, "Only Ask mode (read-only) is available in this version.",
			"Code mode needs isolated worktrees and enforced limits, which arrive in a later version.")
		return
	}
	if s.ctx.Err() != nil {
		writeError(w, http.StatusServiceUnavailable, ErrUnavailable, "Yggdrasil is shutting down.", "Start it again, then retry.")
		return
	}
	adapter, ok := s.adapters[req.Agent]
	if !ok {
		badRequest(w, fmt.Sprintf("The agent %q isn't available.", req.Agent), "Pick one of the agents listed in Settings.")
		return
	}
	// The model name ends up on a command line: only plain names.
	if req.Model != "" && !modelName.MatchString(req.Model) {
		badRequest(w, "That model name isn't valid.", "Use letters, digits, dots, dashes and colons, up to 64 characters.")
		return
	}
	if len(req.RepoIDs) != 1 {
		badRequest(w, "Choose exactly one repo for now.", "Tasks across several repos arrive in a later version.")
		return
	}
	ctx := r.Context()
	project, err := s.store.GetProject(ctx, req.ProjectID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	repo, err := s.store.GetRepo(ctx, req.RepoIDs[0])
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// Only tracked repos of this project are visible to agents (invariant 6).
	if repo.ProjectID != project.ID {
		badRequest(w, "That repo isn't part of the project.", "Choose a repo that the project tracks.")
		return
	}
	if err := adapter.Check(ctx); err != nil {
		// Only an error that carries its own plain-language message is shown;
		// anything else could hold internal detail.
		msg := "The agent can't be used right now."
		var um interface{ UserMessage() string }
		if errors.As(err, &um) {
			msg = um.UserMessage()
		} else {
			s.log.Warn("agent check failed", "agent", req.Agent, "err", err)
		}
		writeError(w, http.StatusServiceUnavailable, ErrUnavailable, msg, "Run `ygg doctor` to see what is missing.")
		return
	}

	id := core.TaskID(db.NewID("tsk"))
	task, err := s.store.CreateTask(ctx, core.Task{
		ID: id, ProjectID: project.ID, Title: taskTitle(req.Title, id), Prompt: prompt, Scope: core.ScopeRepo,
		RepoIDs: []core.RepoID{repo.ID}, Mode: core.ModeAsk, Agent: req.Agent, Model: req.Model, Status: core.TaskQueued,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.record(ctx, "user", "task.create", task.ID, map[string]any{
		"agent": task.Agent, "mode": task.Mode, "repo": repo.ID, "promptChars": utf8.RuneCountInString(prompt),
	}); err != nil {
		s.failTask(task.ID, err)
		s.fail(w, r, err)
		return
	}
	s.start(task, repo, adapter)
	writeJSON(w, http.StatusCreated, TaskResponse{Task: task})
}

// modelName is what a model name may look like: it goes into a command line.
var modelName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)

// maxTitleRunes bounds a task title.
const maxTitleRunes = 80

// taskTitle is the name shown in lists and notifications (docs/AGENTS.md 5.9:
// task name and status only). It is what the person typed, on one line, and
// never taken from the prompt, which can hold code, paths or secrets. Without
// one the task is "Task <short id>".
func taskTitle(given string, id core.TaskID) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(given) {
		if unicode.IsControl(r) {
			r = ' ' // one line: a newline or tab becomes a space
		}
		b.WriteRune(r)
	}
	title := strings.Join(strings.Fields(b.String()), " ")
	if title == "" {
		short := string(id)
		if len(short) > 6 {
			short = short[len(short)-6:]
		}
		return "Task " + short
	}
	if utf8.RuneCountInString(title) > maxTitleRunes {
		title = string([]rune(title)[:maxTitleRunes]) + "…"
	}
	return title
}

func (s *Server) cancelTask(w http.ResponseWriter, r *http.Request) {
	task, ok := s.lookupTask(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	cancel, running := s.cancel[task.ID]
	s.mu.Unlock()
	if running {
		if err := s.record(r.Context(), "user", "task.cancel", task.ID, nil); err != nil {
			s.fail(w, r, err)
			return
		}
		cancel()
	}
	writeJSON(w, http.StatusAccepted, TaskResponse{Task: task})
}

// record writes an audit entry (SF-05). Detail never holds the prompt, only
// its length, and the writer redacts it anyway.
func (s *Server) record(ctx context.Context, actor, action string, task core.TaskID, detail map[string]any) error {
	raw := json.RawMessage("{}")
	if detail != nil {
		b, err := json.Marshal(detail)
		if err != nil {
			return fmt.Errorf("encode audit detail: %w", err)
		}
		raw = b
	}
	if err := s.audit.Write(ctx, core.AuditEntry{Actor: actor, Action: action, Subject: "task:" + string(task), Detail: raw}); err != nil {
		return fmt.Errorf("write audit entry %s: %w", action, err)
	}
	return nil
}

// setStatus stores a status and tells the clients (the "tasks" topic holds
// only the ID, title and status).
func (s *Server) setStatus(ctx context.Context, task core.Task, status core.TaskStatus) error {
	if err := s.store.UpdateTaskStatus(ctx, task.ID, status); err != nil {
		return err
	}
	s.hub.publish(TopicTasks, s.hub.nextSeq(TopicTasks), TaskStatusMessage{TaskID: task.ID, Title: task.Title, Status: status})
	return nil
}

// failTask marks a task failed after something went wrong outside its run.
// The audit entry comes first (invariant 8), as everywhere else.
func (s *Server) failTask(id core.TaskID, cause error) {
	s.log.Error("task failed", "task", id, "err", cause)
	bg := context.WithoutCancel(s.ctx)
	if err := s.record(bg, "system", "task.fail", id, nil); err != nil {
		s.log.Error("audit task failure", "task", id, "err", err)
	}
	if task, err := s.store.GetTask(bg, id); err == nil {
		_ = s.setStatus(bg, task, core.TaskFailed)
	}
}

// start runs the task in a goroutine owned by the server's group. Cancelling
// the task's context (or closing the server) stops the agent and every
// process it started. The check and the Go call share s.mu with Close, so a
// task can never be added to the group after Close started waiting for it.
func (s *Server) start(task core.Task, repo core.Repo, adapter agents.Adapter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		s.failTask(task.ID, errors.New("the server was shutting down"))
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.cancel[task.ID] = cancel
	s.group.Go(func() error {
		defer func() {
			cancel()
			s.mu.Lock()
			delete(s.cancel, task.ID)
			s.mu.Unlock()
		}()
		// A panic in a run must fail that task, not crash the whole process.
		defer func() {
			if v := recover(); v != nil {
				s.log.Error("task goroutine panic", "task", task.ID, "panic", v)
				s.failTask(task.ID, fmt.Errorf("panic: %v", v))
			}
		}()
		s.runTask(ctx, task, repo, adapter)
		return nil
	})
}

func (s *Server) runTask(ctx context.Context, task core.Task, repo core.Repo, adapter agents.Adapter) {
	bg := context.WithoutCancel(ctx) // final writes must survive a cancel
	final := core.TaskFailed
	defer func() {
		// Audit first: once the status is visible, its audit entry exists.
		if err := s.record(bg, "system", "task.finish", task.ID, map[string]any{"status": final}); err != nil {
			s.log.Error("audit task finish", "task", task.ID, "err", err)
		}
		if err := s.setStatus(bg, task, final); err != nil {
			s.log.Error("store final task status", "task", task.ID, "err", err)
		}
	}()

	// These writes use bg: a cancel or a shutdown must not stop them half way.
	if err := s.setStatus(bg, task, core.TaskRunning); err != nil {
		s.log.Error("set task running", "task", task.ID, "err", err)
		return
	}
	if err := s.record(bg, "system", "task.start", task.ID, map[string]any{"agent": task.Agent}); err != nil {
		s.log.Error("audit task start", "task", task.ID, "err", err)
		return
	}
	if ctx.Err() != nil { // cancelled before the agent started
		final = core.TaskCancelled
		return
	}
	env, err := s.env.Build(safety.EnvOptions{})
	if err != nil {
		s.log.Error("build clean environment", "task", task.ID, "err", err)
		return
	}
	dir, err := platform.LongPath(repo.Path)
	if err != nil {
		s.log.Error("expand repo path", "task", task.ID, "err", err)
		return
	}
	res, err := agents.Execute(ctx, adapter, agents.StartOptions{
		TaskID: task.ID, Prompt: task.Prompt, Mode: task.Mode, Model: task.Model, Dir: dir, Env: env,
		MaxSubagentDepth: maxSubagentDepth, MaxConcurrentSubagents: maxConcurrentSubagents,
	}, sink{s})
	if err != nil {
		s.log.Error("run task", "task", task.ID, "err", err)
		return
	}
	if res.SessionID != "" {
		if err := s.record(bg, "system", "task.session", task.ID, nil); err != nil {
			s.log.Error("audit task session", "task", task.ID, "err", err)
		} else if err := s.store.SetTaskSession(bg, task.ID, res.SessionID); err != nil {
			s.log.Error("store task session", "task", task.ID, "err", err)
		}
	}
	switch res.Status {
	case agents.RunCompleted:
		final = core.TaskDone
	case agents.RunCancelled:
		final = core.TaskCancelled
	default:
		if res.Err != nil {
			s.log.Warn("agent run failed", "task", task.ID, "err", res.Err)
		}
	}
}

// redactEvent masks tokens in every free-text field of an event. The event is
// copied first: the runner's value is not modified.
func (s *Server) redactEvent(e agents.Event) agents.Event {
	r := s.redactor
	e.Text, e.Error = r.Redact(e.Text), r.Redact(e.Error)
	if e.Tool != nil {
		t := *e.Tool
		if len(t.Input) > 0 {
			red := r.Redact(string(t.Input))
			if json.Valid([]byte(red)) {
				t.Input = json.RawMessage(red)
			} else {
				t.Input = json.RawMessage(`{"redacted":true}`)
			}
		}
		e.Tool = &t
	}
	if e.File != nil {
		f := *e.File
		f.Path, f.Diff = r.Redact(f.Path), r.Redact(f.Diff)
		e.File = &f
	}
	if e.Command != nil {
		c := *e.Command
		c.Line, c.Output = r.Redact(c.Line), r.Redact(c.Output)
		e.Command = &c
	}
	return e
}

// sink persists every event and then publishes it, in that order, so a client
// that backfills over REST never misses an event it was not sent live.
type sink struct{ s *Server }

func (k sink) Append(ctx context.Context, id core.TaskID, e agents.Event) error {
	// What an agent reads can hold secrets (an .env file in the repo): known
	// token shapes are masked before the event is stored or sent anywhere.
	e = k.s.redactEvent(e)
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encode event: %w", err)
	}
	if err := k.s.store.AppendTaskEvent(ctx, db.TaskEvent{TaskID: id, Seq: e.Seq, Kind: string(e.Kind), At: e.At, Data: data}); err != nil {
		return err
	}
	k.s.hub.publish(TopicTaskPrefix+string(id), e.Seq, e)
	return nil
}
