package db

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db/gen"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// ErrSubTaskDepth is returned when a task would become a sub-task of a
// sub-task, or a sub-task would get sub-tasks (AG-24).
var ErrSubTaskDepth = errors.New("sub-tasks are one level only: a sub-task cannot have sub-tasks")

// timeLayout is RFC 3339 UTC with fixed millisecond width, so text order is
// time order.
const timeLayout = "2006-01-02T15:04:05.000Z"

// Store maps the generated queries to internal/core types.
type Store struct{ db *DB }

// NewStore returns a Store on d.
func NewStore(d *DB) *Store { return &Store{db: d} }

// NewID returns an opaque ID such as "tsk_9f86d081884c7d65".
func NewID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand does not fail on supported platforms
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}

func fmtTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("bad time %q in database: %w", s, err)
	}
	return t, nil
}

func fmtNullTime(t *time.Time) sql.NullString {
	if t == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: fmtTime(*t), Valid: true}
}

func parseNullTime(s sql.NullString) (*time.Time, error) {
	if !s.Valid {
		return nil, nil
	}
	t, err := parseTime(s.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func nullStr[T ~string](p *T) sql.NullString {
	if p == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: string(*p), Valid: true}
}

func strPtr[T ~string](s sql.NullString) *T {
	if !s.Valid {
		return nil
	}
	v := T(s.String)
	return &v
}

// jsonText returns raw as JSON text, or def when raw is empty.
func jsonText(raw json.RawMessage, def string) string {
	if len(raw) == 0 {
		return def
	}
	return string(raw)
}

// found maps sql.ErrNoRows to ErrNotFound.
func found(what string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w", what, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}

// updated turns a zero-row update into ErrNotFound.
func updated(what string, n int64, err error) error {
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if n == 0 {
		return fmt.Errorf("%s: %w", what, ErrNotFound)
	}
	return nil
}

// Projects

func projectFrom(r gen.Project) (core.Project, error) {
	at, err := parseTime(r.CreatedAt)
	return core.Project{ID: core.ProjectID(r.ID), Name: r.Name, Path: r.Path, CreatedAt: at}, err
}

// CreateProject stores p. An empty ID or zero CreatedAt is filled in.
func (s *Store) CreateProject(ctx context.Context, p core.Project) (core.Project, error) {
	if p.ID == "" {
		p.ID = core.ProjectID(NewID("prj"))
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now()
	}
	p.CreatedAt = p.CreatedAt.UTC().Truncate(time.Millisecond)
	if err := p.ID.Validate(); err != nil {
		return p, err
	}
	err := s.db.Writer().CreateProject(ctx, gen.CreateProjectParams{
		ID: string(p.ID), Name: p.Name, Path: p.Path, CreatedAt: fmtTime(p.CreatedAt),
	})
	if err != nil {
		return p, fmt.Errorf("create project: %w", err)
	}
	return p, nil
}

// GetProject returns one project.
func (s *Store) GetProject(ctx context.Context, id core.ProjectID) (core.Project, error) {
	r, err := s.db.Reader().GetProject(ctx, string(id))
	if err := found("get project "+string(id), err); err != nil {
		return core.Project{}, err
	}
	return projectFrom(r)
}

// ListProjects returns all projects by name.
func (s *Store) ListProjects(ctx context.Context) ([]core.Project, error) {
	rows, err := s.db.Reader().ListProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	out := make([]core.Project, 0, len(rows))
	for _, r := range rows {
		p, err := projectFrom(r)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// Repos

func repoFrom(r gen.Repo) (core.Repo, error) {
	at, err := parseTime(r.CreatedAt)
	return core.Repo{
		ID: core.RepoID(r.ID), ProjectID: core.ProjectID(r.ProjectID), Name: r.Name, Path: r.Path,
		DefaultBranch: r.DefaultBranch, Trusted: r.Trusted != 0, CreatedAt: at,
	}, err
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// CreateRepo stores r. An empty ID or zero CreatedAt is filled in.
func (s *Store) CreateRepo(ctx context.Context, r core.Repo) (core.Repo, error) {
	if r.ID == "" {
		r.ID = core.RepoID(NewID("rep"))
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	r.CreatedAt = r.CreatedAt.UTC().Truncate(time.Millisecond)
	if err := r.ID.Validate(); err != nil {
		return r, err
	}
	err := s.db.Writer().CreateRepo(ctx, gen.CreateRepoParams{
		ID: string(r.ID), ProjectID: string(r.ProjectID), Name: r.Name, Path: r.Path,
		DefaultBranch: r.DefaultBranch, Trusted: b2i(r.Trusted), CreatedAt: fmtTime(r.CreatedAt),
	})
	if err != nil {
		return r, fmt.Errorf("create repo: %w", err)
	}
	return r, nil
}

// GetRepo returns one repo.
func (s *Store) GetRepo(ctx context.Context, id core.RepoID) (core.Repo, error) {
	r, err := s.db.Reader().GetRepo(ctx, string(id))
	if err := found("get repo "+string(id), err); err != nil {
		return core.Repo{}, err
	}
	return repoFrom(r)
}

// ListRepos returns the repos of a project by name.
func (s *Store) ListRepos(ctx context.Context, project core.ProjectID) ([]core.Repo, error) {
	rows, err := s.db.Reader().ListReposByProject(ctx, string(project))
	if err != nil {
		return nil, fmt.Errorf("list repos: %w", err)
	}
	out := make([]core.Repo, 0, len(rows))
	for _, r := range rows {
		v, err := repoFrom(r)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// SetRepoTrusted records the user's answer to the repo-hooks prompt (SF-09).
func (s *Store) SetRepoTrusted(ctx context.Context, id core.RepoID, trusted bool) error {
	n, err := s.db.Writer().SetRepoTrusted(ctx, gen.SetRepoTrustedParams{Trusted: b2i(trusted), ID: string(id)})
	return updated("set repo trusted "+string(id), n, err)
}

// Tasks

func (s *Store) taskFrom(ctx context.Context, q *gen.Queries, r gen.Task) (core.Task, error) {
	created, err := parseTime(r.CreatedAt)
	if err != nil {
		return core.Task{}, err
	}
	upd, err := parseTime(r.UpdatedAt)
	if err != nil {
		return core.Task{}, err
	}
	fin, err := parseNullTime(r.FinishedAt)
	if err != nil {
		return core.Task{}, err
	}
	repos, err := q.ListTaskRepos(ctx, r.ID)
	if err != nil {
		return core.Task{}, fmt.Errorf("list repos of task %s: %w", r.ID, err)
	}
	ids := make([]core.RepoID, len(repos))
	for i, id := range repos {
		ids[i] = core.RepoID(id)
	}
	return core.Task{
		ID: core.TaskID(r.ID), ProjectID: core.ProjectID(r.ProjectID), ParentTaskID: strPtr[core.TaskID](r.ParentTaskID),
		Title: r.Title, Prompt: r.Prompt, Scope: core.TaskScope(r.Scope), RepoIDs: ids,
		Mode: core.TaskMode(r.Mode), Agent: r.Agent, Model: r.Model, Status: core.TaskStatus(r.Status),
		SessionID: r.SessionID, CreatedAt: created, UpdatedAt: upd, FinishedAt: fin,
	}, nil
}

// CreateTask validates and stores t with its repo scope. An empty ID or zero
// times are filled in; an empty status becomes draft. A sub-task of a
// sub-task returns ErrSubTaskDepth.
func (s *Store) CreateTask(ctx context.Context, t core.Task) (core.Task, error) {
	if t.ID == "" {
		t.ID = core.TaskID(NewID("tsk"))
	}
	if t.Status == "" {
		t.Status = core.TaskDraft
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	if t.UpdatedAt.IsZero() {
		t.UpdatedAt = t.CreatedAt
	}
	t.CreatedAt, t.UpdatedAt = t.CreatedAt.UTC().Truncate(time.Millisecond), t.UpdatedAt.UTC().Truncate(time.Millisecond)
	if err := t.Validate(); err != nil {
		return t, err
	}
	err := s.db.WithTx(ctx, func(q *gen.Queries) error {
		err := q.CreateTask(ctx, gen.CreateTaskParams{
			ID: string(t.ID), ProjectID: string(t.ProjectID), ParentTaskID: nullStr(t.ParentTaskID),
			Title: t.Title, Prompt: t.Prompt, Scope: string(t.Scope), Mode: string(t.Mode), Agent: t.Agent,
			Model: t.Model, Status: string(t.Status), SessionID: t.SessionID,
			CreatedAt: fmtTime(t.CreatedAt), UpdatedAt: fmtTime(t.UpdatedAt), FinishedAt: fmtNullTime(t.FinishedAt),
		})
		if err != nil {
			return err
		}
		for _, r := range t.RepoIDs {
			if err := q.AddTaskRepo(ctx, gen.AddTaskRepoParams{TaskID: string(t.ID), RepoID: string(r)}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if strings.Contains(err.Error(), "sub-tasks are one level only") {
			return t, fmt.Errorf("create task: %w", ErrSubTaskDepth)
		}
		return t, fmt.Errorf("create task: %w", err)
	}
	return t, nil
}

// GetTask returns one task with its repo scope.
func (s *Store) GetTask(ctx context.Context, id core.TaskID) (core.Task, error) {
	q := s.db.Reader()
	r, err := q.GetTask(ctx, string(id))
	if err := found("get task "+string(id), err); err != nil {
		return core.Task{}, err
	}
	return s.taskFrom(ctx, q, r)
}

func (s *Store) tasksFrom(ctx context.Context, q *gen.Queries, rows []gen.Task) ([]core.Task, error) {
	out := make([]core.Task, 0, len(rows))
	for _, r := range rows {
		t, err := s.taskFrom(ctx, q, r)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// ListTasks returns the tasks of a project, oldest first.
func (s *Store) ListTasks(ctx context.Context, project core.ProjectID) ([]core.Task, error) {
	q := s.db.Reader()
	rows, err := q.ListTasksByProject(ctx, string(project))
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	return s.tasksFrom(ctx, q, rows)
}

// ListSubTasks returns the sub-tasks of a task, oldest first.
func (s *Store) ListSubTasks(ctx context.Context, parent core.TaskID) ([]core.Task, error) {
	q := s.db.Reader()
	rows, err := q.ListSubTasks(ctx, nullStr(&parent))
	if err != nil {
		return nil, fmt.Errorf("list sub-tasks: %w", err)
	}
	return s.tasksFrom(ctx, q, rows)
}

// UpdateTaskStatus sets the status; finished_at is set when the status is terminal.
func (s *Store) UpdateTaskStatus(ctx context.Context, id core.TaskID, status core.TaskStatus) error {
	if err := status.Validate(); err != nil {
		return err
	}
	now := time.Now()
	var fin *time.Time
	if status.Terminal() {
		fin = &now
	}
	n, err := s.db.Writer().UpdateTaskStatus(ctx, gen.UpdateTaskStatusParams{
		Status: string(status), UpdatedAt: fmtTime(now), FinishedAt: fmtNullTime(fin), ID: string(id),
	})
	return updated("update task "+string(id), n, err)
}

// SetTaskSession records the agent's resumable session ID.
func (s *Store) SetTaskSession(ctx context.Context, id core.TaskID, session string) error {
	n, err := s.db.Writer().UpdateTaskSession(ctx, gen.UpdateTaskSessionParams{
		SessionID: session, UpdatedAt: fmtTime(time.Now()), ID: string(id),
	})
	return updated("update task session "+string(id), n, err)
}

// AddTaskDependency makes task start after dependsOn (AG-25). The caller
// validates that the order is acyclic.
func (s *Store) AddTaskDependency(ctx context.Context, task, dependsOn core.TaskID) error {
	err := s.db.Writer().AddTaskDependency(ctx, gen.AddTaskDependencyParams{TaskID: string(task), DependsOnTaskID: string(dependsOn)})
	if err != nil {
		return fmt.Errorf("add task dependency: %w", err)
	}
	return nil
}

// TaskDependencies returns the tasks that must finish before task starts.
func (s *Store) TaskDependencies(ctx context.Context, task core.TaskID) ([]core.TaskID, error) {
	ids, err := s.db.Reader().ListTaskDependencies(ctx, string(task))
	if err != nil {
		return nil, fmt.Errorf("list task dependencies: %w", err)
	}
	out := make([]core.TaskID, len(ids))
	for i, id := range ids {
		out[i] = core.TaskID(id)
	}
	return out, nil
}

// Worktrees

func worktreeFrom(r gen.Worktree) (core.Worktree, error) {
	at, err := parseTime(r.CreatedAt)
	return core.Worktree{
		ID: core.WorktreeID(r.ID), RepoID: core.RepoID(r.RepoID), TaskID: core.TaskID(r.TaskID), Name: r.Name,
		Path: r.Path, Branch: r.Branch, Status: core.WorktreeStatus(r.Status), CreatedAt: at,
	}, err
}

// CreateWorktree stores w. An empty ID, status or zero CreatedAt is filled in.
func (s *Store) CreateWorktree(ctx context.Context, w core.Worktree) (core.Worktree, error) {
	if w.ID == "" {
		w.ID = core.WorktreeID(NewID("wt"))
	}
	if w.Status == "" {
		w.Status = core.WorktreeActive
	}
	if w.CreatedAt.IsZero() {
		w.CreatedAt = time.Now()
	}
	w.CreatedAt = w.CreatedAt.UTC().Truncate(time.Millisecond)
	if err := w.ID.Validate(); err != nil {
		return w, err
	}
	err := s.db.Writer().CreateWorktree(ctx, gen.CreateWorktreeParams{
		ID: string(w.ID), RepoID: string(w.RepoID), TaskID: string(w.TaskID), Name: w.Name, Path: w.Path,
		Branch: w.Branch, Status: string(w.Status), CreatedAt: fmtTime(w.CreatedAt),
	})
	if err != nil {
		return w, fmt.Errorf("create worktree: %w", err)
	}
	return w, nil
}

// GetWorktree returns one worktree.
func (s *Store) GetWorktree(ctx context.Context, id core.WorktreeID) (core.Worktree, error) {
	r, err := s.db.Reader().GetWorktree(ctx, string(id))
	if err := found("get worktree "+string(id), err); err != nil {
		return core.Worktree{}, err
	}
	return worktreeFrom(r)
}

// ListWorktrees returns the worktrees of a task.
func (s *Store) ListWorktrees(ctx context.Context, task core.TaskID) ([]core.Worktree, error) {
	rows, err := s.db.Reader().ListWorktreesByTask(ctx, string(task))
	if err != nil {
		return nil, fmt.Errorf("list worktrees: %w", err)
	}
	out := make([]core.Worktree, 0, len(rows))
	for _, r := range rows {
		w, err := worktreeFrom(r)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, nil
}

// UpdateWorktreeStatus sets the status of a worktree.
func (s *Store) UpdateWorktreeStatus(ctx context.Context, id core.WorktreeID, status core.WorktreeStatus) error {
	n, err := s.db.Writer().UpdateWorktreeStatus(ctx, gen.UpdateWorktreeStatusParams{Status: string(status), ID: string(id)})
	return updated("update worktree "+string(id), n, err)
}

// Task events

// TaskEvent is one stored agent event. Data is the whole agents.Event as
// JSON; this package does not depend on internal/agents.
type TaskEvent struct {
	TaskID core.TaskID
	Seq    int64
	Kind   string
	At     time.Time
	Data   json.RawMessage
}

// AppendTaskEvent stores one event of a task.
func (s *Store) AppendTaskEvent(ctx context.Context, e TaskEvent) error {
	err := s.db.Writer().AppendTaskEvent(ctx, gen.AppendTaskEventParams{
		TaskID: string(e.TaskID), Seq: e.Seq, Kind: e.Kind, At: fmtTime(e.At), Data: jsonText(e.Data, "{}"),
	})
	if err != nil {
		return fmt.Errorf("append task event: %w", err)
	}
	return nil
}

// ListTaskEvents returns the events of a task with Seq greater than after.
func (s *Store) ListTaskEvents(ctx context.Context, task core.TaskID, after int64) ([]TaskEvent, error) {
	rows, err := s.db.Reader().ListTaskEvents(ctx, gen.ListTaskEventsParams{TaskID: string(task), Seq: after})
	if err != nil {
		return nil, fmt.Errorf("list task events: %w", err)
	}
	out := make([]TaskEvent, 0, len(rows))
	for _, r := range rows {
		at, err := parseTime(r.At)
		if err != nil {
			return nil, err
		}
		out = append(out, TaskEvent{TaskID: core.TaskID(r.TaskID), Seq: r.Seq, Kind: r.Kind, At: at, Data: json.RawMessage(r.Data)})
	}
	return out, nil
}

// Change sets

func changeSetFrom(r gen.ChangeSet) (core.ChangeSet, error) {
	at, err := parseTime(r.CreatedAt)
	if err != nil {
		return core.ChangeSet{}, err
	}
	order := []core.RepoID{}
	if err := json.Unmarshal([]byte(r.MergeOrder), &order); err != nil {
		return core.ChangeSet{}, fmt.Errorf("bad merge order in database: %w", err)
	}
	return core.ChangeSet{
		ID: core.ChangeSetID(r.ID), TaskID: core.TaskID(r.TaskID), Status: core.ChangeSetStatus(r.Status),
		MergeOrder: order, CreatedAt: at,
	}, nil
}

// CreateChangeSet stores c. An empty ID, status or zero CreatedAt is filled in.
func (s *Store) CreateChangeSet(ctx context.Context, c core.ChangeSet) (core.ChangeSet, error) {
	if c.ID == "" {
		c.ID = core.ChangeSetID(NewID("cs"))
	}
	if c.Status == "" {
		c.Status = core.ChangeSetOpen
	}
	if c.MergeOrder == nil {
		c.MergeOrder = []core.RepoID{}
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now()
	}
	c.CreatedAt = c.CreatedAt.UTC().Truncate(time.Millisecond)
	if err := c.ID.Validate(); err != nil {
		return c, err
	}
	order, err := json.Marshal(c.MergeOrder)
	if err != nil {
		return c, fmt.Errorf("encode merge order: %w", err)
	}
	err = s.db.Writer().CreateChangeSet(ctx, gen.CreateChangeSetParams{
		ID: string(c.ID), TaskID: string(c.TaskID), Status: string(c.Status), MergeOrder: string(order), CreatedAt: fmtTime(c.CreatedAt),
	})
	if err != nil {
		return c, fmt.Errorf("create change set: %w", err)
	}
	return c, nil
}

// GetChangeSet returns one change set.
func (s *Store) GetChangeSet(ctx context.Context, id core.ChangeSetID) (core.ChangeSet, error) {
	r, err := s.db.Reader().GetChangeSet(ctx, string(id))
	if err := found("get change set "+string(id), err); err != nil {
		return core.ChangeSet{}, err
	}
	return changeSetFrom(r)
}

// ListChangeSets returns the change sets of a task.
func (s *Store) ListChangeSets(ctx context.Context, task core.TaskID) ([]core.ChangeSet, error) {
	rows, err := s.db.Reader().ListChangeSetsByTask(ctx, string(task))
	if err != nil {
		return nil, fmt.Errorf("list change sets: %w", err)
	}
	out := make([]core.ChangeSet, 0, len(rows))
	for _, r := range rows {
		c, err := changeSetFrom(r)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// UpdateChangeSetStatus sets the status of a change set.
func (s *Store) UpdateChangeSetStatus(ctx context.Context, id core.ChangeSetID, status core.ChangeSetStatus) error {
	n, err := s.db.Writer().UpdateChangeSetStatus(ctx, gen.UpdateChangeSetStatusParams{Status: string(status), ID: string(id)})
	return updated("update change set "+string(id), n, err)
}

// Proposals

func proposalFrom(r gen.Proposal) (core.Proposal, error) {
	at, err := parseTime(r.CreatedAt)
	if err != nil {
		return core.Proposal{}, err
	}
	dec, err := parseNullTime(r.DecidedAt)
	if err != nil {
		return core.Proposal{}, err
	}
	return core.Proposal{
		ID: core.ProposalID(r.ID), ChangeSetID: strPtr[core.ChangeSetID](r.ChangeSetID), TaskID: strPtr[core.TaskID](r.TaskID),
		RepoID: strPtr[core.RepoID](r.RepoID), Kind: core.ProposalKind(r.Kind), Status: core.ProposalStatus(r.Status),
		Summary: r.Summary, Payload: json.RawMessage(r.Payload), CreatedAt: at, DecidedAt: dec,
	}, nil
}

// CreateProposal stores p. An empty ID, status or zero CreatedAt is filled in.
func (s *Store) CreateProposal(ctx context.Context, p core.Proposal) (core.Proposal, error) {
	if p.ID == "" {
		p.ID = core.ProposalID(NewID("prop"))
	}
	if p.Status == "" {
		p.Status = core.ProposalPending
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now()
	}
	p.CreatedAt = p.CreatedAt.UTC().Truncate(time.Millisecond)
	if err := p.ID.Validate(); err != nil {
		return p, err
	}
	if err := p.Kind.Validate(); err != nil {
		return p, err
	}
	if err := p.Status.Validate(); err != nil {
		return p, err
	}
	err := s.db.Writer().CreateProposal(ctx, gen.CreateProposalParams{
		ID: string(p.ID), ChangeSetID: nullStr(p.ChangeSetID), TaskID: nullStr(p.TaskID), RepoID: nullStr(p.RepoID),
		Kind: string(p.Kind), Status: string(p.Status), Summary: p.Summary, Payload: jsonText(p.Payload, "{}"),
		CreatedAt: fmtTime(p.CreatedAt), DecidedAt: fmtNullTime(p.DecidedAt),
	})
	if err != nil {
		return p, fmt.Errorf("create proposal: %w", err)
	}
	return p, nil
}

// GetProposal returns one proposal.
func (s *Store) GetProposal(ctx context.Context, id core.ProposalID) (core.Proposal, error) {
	r, err := s.db.Reader().GetProposal(ctx, string(id))
	if err := found("get proposal "+string(id), err); err != nil {
		return core.Proposal{}, err
	}
	return proposalFrom(r)
}

func proposalsFrom(rows []gen.Proposal) ([]core.Proposal, error) {
	out := make([]core.Proposal, 0, len(rows))
	for _, r := range rows {
		p, err := proposalFrom(r)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// ListProposals returns the proposals with the given status, oldest first.
func (s *Store) ListProposals(ctx context.Context, status core.ProposalStatus) ([]core.Proposal, error) {
	rows, err := s.db.Reader().ListProposalsByStatus(ctx, string(status))
	if err != nil {
		return nil, fmt.Errorf("list proposals: %w", err)
	}
	return proposalsFrom(rows)
}

// ListChangeSetProposals returns the proposals of a change set, oldest first.
func (s *Store) ListChangeSetProposals(ctx context.Context, cs core.ChangeSetID) ([]core.Proposal, error) {
	rows, err := s.db.Reader().ListProposalsByChangeSet(ctx, nullStr(&cs))
	if err != nil {
		return nil, fmt.Errorf("list proposals: %w", err)
	}
	return proposalsFrom(rows)
}

// UpdateProposalStatus sets the status; decided_at is set for every status
// except pending.
func (s *Store) UpdateProposalStatus(ctx context.Context, id core.ProposalID, status core.ProposalStatus) error {
	if err := status.Validate(); err != nil {
		return err
	}
	var decided *time.Time
	if status != core.ProposalPending {
		now := time.Now()
		decided = &now
	}
	n, err := s.db.Writer().UpdateProposalStatus(ctx, gen.UpdateProposalStatusParams{
		Status: string(status), DecidedAt: fmtNullTime(decided), ID: string(id),
	})
	return updated("update proposal "+string(id), n, err)
}

// Builds

func buildStepFrom(r gen.BuildStep) (core.BuildStep, error) {
	start, err := parseNullTime(r.StartedAt)
	if err != nil {
		return core.BuildStep{}, err
	}
	fin, err := parseNullTime(r.FinishedAt)
	if err != nil {
		return core.BuildStep{}, err
	}
	var code *int
	if r.ExitCode.Valid {
		c := int(r.ExitCode.Int64)
		code = &c
	}
	return core.BuildStep{
		Index: int(r.Idx), RepoID: core.RepoID(r.RepoID), Name: r.Name, Status: core.BuildStatus(r.Status),
		ExitCode: code, LogPath: r.LogPath, StartedAt: start, FinishedAt: fin,
	}, nil
}

func nullInt(p *int) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*p), Valid: true}
}

// CreateBuild stores b and its steps in one transaction. An empty ID, status
// or zero CreatedAt is filled in; step indexes are set to their position.
func (s *Store) CreateBuild(ctx context.Context, b core.Build) (core.Build, error) {
	if b.ID == "" {
		b.ID = core.BuildID(NewID("bld"))
	}
	if b.Status == "" {
		b.Status = core.BuildQueued
	}
	if b.CreatedAt.IsZero() {
		b.CreatedAt = time.Now()
	}
	b.CreatedAt = b.CreatedAt.UTC().Truncate(time.Millisecond)
	if err := b.ID.Validate(); err != nil {
		return b, err
	}
	if b.Steps == nil {
		b.Steps = []core.BuildStep{}
	}
	err := s.db.WithTx(ctx, func(q *gen.Queries) error {
		err := q.CreateBuild(ctx, gen.CreateBuildParams{
			ID: string(b.ID), WorktreeID: nullStr(b.WorktreeID), TaskID: nullStr(b.TaskID), Status: string(b.Status),
			CreatedAt: fmtTime(b.CreatedAt), FinishedAt: fmtNullTime(b.FinishedAt),
		})
		if err != nil {
			return err
		}
		for i, st := range b.Steps {
			b.Steps[i].Index = i
			err := q.CreateBuildStep(ctx, gen.CreateBuildStepParams{
				BuildID: string(b.ID), Idx: int64(i), RepoID: string(st.RepoID), Name: st.Name, Status: string(st.Status),
				ExitCode: nullInt(st.ExitCode), LogPath: st.LogPath, StartedAt: fmtNullTime(st.StartedAt), FinishedAt: fmtNullTime(st.FinishedAt),
			})
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return b, fmt.Errorf("create build: %w", err)
	}
	return b, nil
}

func (s *Store) buildFrom(ctx context.Context, q *gen.Queries, r gen.Build) (core.Build, error) {
	at, err := parseTime(r.CreatedAt)
	if err != nil {
		return core.Build{}, err
	}
	fin, err := parseNullTime(r.FinishedAt)
	if err != nil {
		return core.Build{}, err
	}
	rows, err := q.ListBuildSteps(ctx, r.ID)
	if err != nil {
		return core.Build{}, fmt.Errorf("list steps of build %s: %w", r.ID, err)
	}
	steps := make([]core.BuildStep, 0, len(rows))
	for _, sr := range rows {
		st, err := buildStepFrom(sr)
		if err != nil {
			return core.Build{}, err
		}
		steps = append(steps, st)
	}
	return core.Build{
		ID: core.BuildID(r.ID), WorktreeID: strPtr[core.WorktreeID](r.WorktreeID), TaskID: strPtr[core.TaskID](r.TaskID),
		Status: core.BuildStatus(r.Status), Steps: steps, CreatedAt: at, FinishedAt: fin,
	}, nil
}

// GetBuild returns one build with its steps.
func (s *Store) GetBuild(ctx context.Context, id core.BuildID) (core.Build, error) {
	q := s.db.Reader()
	r, err := q.GetBuild(ctx, string(id))
	if err := found("get build "+string(id), err); err != nil {
		return core.Build{}, err
	}
	return s.buildFrom(ctx, q, r)
}

// ListBuilds returns the builds of a task with their steps, oldest first.
func (s *Store) ListBuilds(ctx context.Context, task core.TaskID) ([]core.Build, error) {
	q := s.db.Reader()
	rows, err := q.ListBuildsByTask(ctx, nullStr(&task))
	if err != nil {
		return nil, fmt.Errorf("list builds: %w", err)
	}
	out := make([]core.Build, 0, len(rows))
	for _, r := range rows {
		b, err := s.buildFrom(ctx, q, r)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

// UpdateBuildStatus sets the status; finished_at is set when the build ended.
func (s *Store) UpdateBuildStatus(ctx context.Context, id core.BuildID, status core.BuildStatus) error {
	if err := status.Validate(); err != nil {
		return err
	}
	var fin *time.Time
	if status != core.BuildQueued && status != core.BuildRunning {
		now := time.Now()
		fin = &now
	}
	n, err := s.db.Writer().UpdateBuildStatus(ctx, gen.UpdateBuildStatusParams{Status: string(status), FinishedAt: fmtNullTime(fin), ID: string(id)})
	return updated("update build "+string(id), n, err)
}

// UpdateBuildStep replaces the mutable fields of step st.Index of a build.
func (s *Store) UpdateBuildStep(ctx context.Context, id core.BuildID, st core.BuildStep) error {
	if err := st.Status.Validate(); err != nil {
		return err
	}
	n, err := s.db.Writer().UpdateBuildStep(ctx, gen.UpdateBuildStepParams{
		Status: string(st.Status), ExitCode: nullInt(st.ExitCode), LogPath: st.LogPath,
		StartedAt: fmtNullTime(st.StartedAt), FinishedAt: fmtNullTime(st.FinishedAt), BuildID: string(id), Idx: int64(st.Index),
	})
	return updated(fmt.Sprintf("update build %s step %d", id, st.Index), n, err)
}

// Audit log

func auditFrom(r gen.AuditLog) (core.AuditEntry, error) {
	at, err := parseTime(r.At)
	return core.AuditEntry{
		ID: core.AuditID(r.ID), At: at, Actor: r.Actor, Action: r.Action, Subject: r.Subject, Detail: json.RawMessage(r.Detail),
	}, err
}

// InsertAudit stores e as it is. Callers redact first: use internal/audit.
func (s *Store) InsertAudit(ctx context.Context, e core.AuditEntry) error {
	err := s.db.Writer().InsertAuditEntry(ctx, gen.InsertAuditEntryParams{
		ID: string(e.ID), At: fmtTime(e.At), Actor: e.Actor, Action: e.Action, Subject: e.Subject, Detail: jsonText(e.Detail, "{}"),
	})
	if err != nil {
		return fmt.Errorf("write audit entry: %w", err)
	}
	return nil
}

// ListAudit returns the newest audit entries, newest first. A non-empty
// subject filters by it.
func (s *Store) ListAudit(ctx context.Context, subject string, limit int) ([]core.AuditEntry, error) {
	q := s.db.Reader()
	var rows []gen.AuditLog
	var err error
	if subject == "" {
		rows, err = q.ListAuditEntries(ctx, int64(limit))
	} else {
		rows, err = q.ListAuditEntriesBySubject(ctx, gen.ListAuditEntriesBySubjectParams{Subject: subject, Limit: int64(limit)})
	}
	if err != nil {
		return nil, fmt.Errorf("list audit entries: %w", err)
	}
	out := make([]core.AuditEntry, 0, len(rows))
	for _, r := range rows {
		e, err := auditFrom(r)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}
