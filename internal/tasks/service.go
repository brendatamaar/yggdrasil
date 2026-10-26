package tasks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/sync/errgroup"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/guard"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// Service runs tasks: it owns the scheduler loop, the runs and their
// goroutines. Create it with New, call Start once, and Close it on shutdown.
type Service struct {
	c Config

	ctx    context.Context
	cancel context.CancelFunc
	group  *errgroup.Group

	mu       sync.Mutex // protects the maps and closed
	closed   bool
	started  bool
	entries  map[core.TaskID]*entry
	attempts map[core.TaskID]*attempt
	releases map[core.TaskID]Release
	prep     map[core.TaskID]*sync.Mutex

	boardMu sync.Mutex
	board   coretasks.Board
	order   atomic.Uint64

	wake chan struct{}

	// hooks for tests; nil in production.
	afterCommit func(id core.TaskID, cmds []coretasks.Command) (skip bool)
}

// New returns a Service. Nothing runs until Start.
func New(c Config) (*Service, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	g, gctx := errgroup.WithContext(ctx)
	s := &Service{
		c: c, ctx: gctx, cancel: cancel, group: g,
		entries:  map[core.TaskID]*entry{},
		attempts: map[core.TaskID]*attempt{},
		releases: map[core.TaskID]Release{},
		prep:     map[core.TaskID]*sync.Mutex{},
		board: coretasks.Board{
			Max: c.DefaultMax, Tasks: map[core.TaskID]coretasks.Entry{}, Locks: map[core.RepoID]core.TaskID{},
		},
		wake: make(chan struct{}, 1),
	}
	return s, nil
}

// Close stops accepting work, cancels every run (they finish as Cancelled and
// their final state is stored) and waits for all goroutines.
func (s *Service) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.cancel()
	err := s.group.Wait()
	if errors.Is(err, context.Canceled) {
		err = nil
	}
	return err
}

func (s *Service) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// spawn runs f in a goroutine owned by the service group. The closed check and
// the Go call share s.mu with Close, so nothing is added after Close started
// waiting. It reports whether f was started.
func (s *Service) spawn(f func()) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.group.Go(func() error {
		f()
		return nil
	})
	return true
}

// bgCtx is the context for writes that must survive a cancel or a shutdown.
func (s *Service) bgCtx() context.Context { return context.WithoutCancel(s.ctx) }

// MessageRequest is a new message from the API: a new task (TaskID empty) or
// another turn of an idle task.
type MessageRequest struct {
	TaskID    core.TaskID
	ProjectID core.ProjectID
	Prompt    string
	Mode      string
	Scope     string
	Repos     []string
	Agent     string
	Model     string
	Title     string
	// OptIn is the user's explicit consent to a mode that edits main checkouts
	// (current_branch, project).
	OptIn bool
}

// MessageResult is what a message produced.
type MessageResult struct {
	Task core.Task
	Turn coretasks.TurnID
	// Warning is "", "edits_main_checkout" or "edits_all_repos".
	Warning string
}

// Message creates a task or adds a turn to an idle one. It parses the request
// at the boundary, asks guard.Admit (with the trust of every repo the mode
// touches), runs the core step, commits the state and its audit entries in
// one transaction, and then enqueues the turn. Errors that are the person's to
// fix are *coretasks.UserError (bad input) or *RejectedError (refused).
func (s *Service) Message(ctx context.Context, req MessageRequest) (MessageResult, error) {
	if s.isClosed() {
		return MessageResult{}, &RejectedError{Reason: "shutting_down", Message: "Yggdrasil is shutting down. Start it again, then retry."}
	}
	var existing *db.TaskState
	if req.TaskID != "" {
		if err := req.TaskID.Validate(); err != nil {
			return MessageResult{}, &RejectedError{Reason: "invalid_task", Message: "That task ID isn't valid."}
		}
		st, err := s.c.Store.GetTaskState(ctx, req.TaskID)
		if err != nil {
			return MessageResult{}, err
		}
		existing = &st
		req.ProjectID, req.Agent, req.Model = st.Row.ProjectID, st.Row.Agent, st.Row.Model
		req.Scope = string(st.Row.Scope)
		req.Repos = repoStrings(st.Row.RepoIDs)
	}
	raw := coretasks.RawMessage{Prompt: req.Prompt, Mode: req.Mode, Scope: req.Scope, Agent: req.Agent, Model: req.Model, Repos: req.Repos, Title: req.Title}
	check := raw
	if existing != nil && existing.Row.Scope == core.ScopeProject && req.Mode == string(core.ModeAsk) {
		// An Ask turn in a whole-project task reads the task's scope; the
		// scope/mode pairing rule is for the first message.
		check.Scope, check.Repos = string(core.ScopeRepo), req.Repos[:min(1, len(req.Repos))]
	}
	pm, err := coretasks.ParseMessage(check)
	if err != nil {
		return MessageResult{}, err
	}
	pm.Scope, pm.Repos = core.TaskScope(raw.Scope), idsOf(raw.Repos)
	if existing != nil {
		pm.Repos = existing.Row.RepoIDs
	}

	project, err := s.c.Store.GetProject(ctx, req.ProjectID)
	if err != nil {
		return MessageResult{}, err
	}
	repos, err := s.scopeRepos(ctx, project.ID, pm, existing)
	if err != nil {
		return MessageResult{}, err
	}
	adapter, ok := s.c.Adapters.Get(pm.Agent)
	if !ok {
		return MessageResult{}, &RejectedError{Reason: "agent_unavailable", Message: fmt.Sprintf("The agent %q isn't available. Pick one of the agents listed in Settings.", pm.Agent)}
	}
	if err := adapter.Check(ctx); err != nil {
		return MessageResult{}, &RejectedError{Reason: "agent_unavailable", Message: userMessage(err, "The agent can't be used right now. Run `ygg doctor` to see what is missing.")}
	}

	var prev *core.TaskMode
	if existing != nil && existing.Task.Turns > 0 {
		m := existing.Row.Mode
		prev = &m
	}
	adm, err := s.admit(ctx, adapter, pm.Mode, prev, req.OptIn, repos)
	if err != nil {
		return MessageResult{}, err
	}

	id := req.TaskID
	turn := coretasks.TurnID(s.c.NewID("trn"))
	now := s.c.Now().UTC()
	opts := applyOpts{actor: "user"}
	if existing == nil {
		id = core.TaskID(s.c.NewID("tsk"))
		title := pm.Title
		if title == "" {
			short := string(id)
			if len(short) > 6 {
				short = short[len(short)-6:]
			}
			title = "Task " + short
		}
		ids := make([]core.RepoID, len(repos))
		for i, r := range repos {
			ids[i] = r.ID
		}
		if pm.Scope == core.ScopeRepo {
			ids = ids[:1]
		}
		opts.create = &core.Task{
			ID: id, ProjectID: project.ID, Title: title, Prompt: pm.Prompt, Scope: pm.Scope, RepoIDs: ids,
			Mode: pm.Mode, Agent: pm.Agent, Model: pm.Model, Status: core.TaskDraft, CreatedAt: now, UpdatedAt: now,
		}
	}
	var edits []core.RepoID
	if pm.Mode == core.ModeCurrentBranch {
		edits = []core.RepoID{repos[0].ID}
	}
	msg := coretasks.Message{
		Turn: turn, Mode: pm.Mode, Prompt: pm.Prompt, Now: now, Caps: s.c.Caps, EditsMain: edits,
		Admission: coretasks.Admission{
			Admitted: adm.Admitted, Warning: string(adm.Warning), Reason: string(adm.Refusal), Message: refusalText(adm),
		},
	}
	res, err := s.apply(ctx, id, func(db.TaskState) coretasks.Input { return msg }, opts)
	if err != nil {
		return MessageResult{}, err
	}
	if res.Reject != nil {
		return MessageResult{}, &RejectedError{Reason: res.Reject.Reason, Message: res.Reject.Message}
	}
	if len(res.Cmds) == 0 {
		return MessageResult{}, &RejectedError{Reason: "task_busy", Message: "This task is still working. Wait for it to finish or cancel it, then send your message."}
	}
	s.interpret(id, res)
	return MessageResult{Task: res.Row, Turn: turn, Warning: string(adm.Warning)}, nil
}

func refusalText(a guard.Admission) string {
	if a.Admitted {
		return ""
	}
	return a.Refusal.Message()
}

func repoStrings(ids []core.RepoID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = string(id)
	}
	return out
}

func idsOf(ss []string) []core.RepoID {
	out := make([]core.RepoID, len(ss))
	for i, s := range ss {
		out[i] = core.RepoID(s)
	}
	return out
}

// userMessage returns the plain-language text an error carries, or def.
func userMessage(err error, def string) string {
	var um interface{ UserMessage() string }
	if errors.As(err, &um) {
		if m := strings.TrimSpace(um.UserMessage()); m != "" {
			return m
		}
	}
	return def
}

// scopeRepos returns the tracked repos a message covers: the one repo of a
// repo-scope task, every tracked repo of the project for a whole-project task.
// Only tracked repos of the project are visible (invariant 6).
func (s *Service) scopeRepos(ctx context.Context, project core.ProjectID, pm coretasks.ParsedMessage, existing *db.TaskState) ([]core.Repo, error) {
	if existing != nil {
		out := make([]core.Repo, 0, len(existing.Row.RepoIDs))
		for _, id := range existing.Row.RepoIDs {
			r, err := s.c.Store.GetRepo(ctx, id)
			if err != nil {
				return nil, &RejectedError{Reason: "repo_untracked", Message: "A repo of this task isn't tracked any more. Track it again or start a new task."}
			}
			out = append(out, r)
		}
		return out, nil
	}
	if pm.Scope == core.ScopeProject {
		all, err := s.c.Store.ListRepos(ctx, project)
		if err != nil {
			return nil, err
		}
		if len(all) == 0 {
			return nil, &RejectedError{Reason: "no_repos", Message: "This project has no tracked repos."}
		}
		return all, nil
	}
	r, err := s.c.Store.GetRepo(ctx, pm.Repos[0])
	if err != nil {
		return nil, err
	}
	if r.ProjectID != project {
		return nil, &RejectedError{Reason: "repo_not_in_project", Message: "That repo isn't part of the project. Choose a repo that the project tracks."}
	}
	return []core.Repo{r}, nil
}

// Cancel asks a task to stop. A task that is idle does nothing.
func (s *Service) Cancel(ctx context.Context, id core.TaskID) (core.Task, error) {
	res, err := s.apply(ctx, id, func(db.TaskState) coretasks.Input { return coretasks.CancelRequested{Now: s.c.Now().UTC()} }, applyOpts{actor: "user"})
	if err != nil {
		return core.Task{}, err
	}
	s.interpret(id, res)
	return res.Row, nil
}

// Dismiss marks a finished task done. Only a task waiting for review or for
// the user can be dismissed.
func (s *Service) Dismiss(ctx context.Context, id core.TaskID) (core.Task, error) {
	res, err := s.apply(ctx, id, func(db.TaskState) coretasks.Input { return coretasks.DismissRequested{Now: s.c.Now().UTC()} }, applyOpts{actor: "user"})
	if err != nil {
		return core.Task{}, err
	}
	if len(res.Cmds) == 0 {
		return res.Row, &RejectedError{Reason: "not_dismissable", Message: "Only a task that is ready for review or needs you can be marked done."}
	}
	s.interpret(id, res)
	return res.Row, nil
}

// adapterOf returns the adapter of a task's agent.
func (s *Service) adapterOf(name string) (agents.Adapter, error) {
	a, ok := s.c.Adapters.Get(name)
	if !ok {
		return nil, fmt.Errorf("the agent %q is not available", name)
	}
	return a, nil
}
