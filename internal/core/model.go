package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The data model is Project -> Repo -> Worktree -> Task -> Change set
// (docs/AGENTS.md 5.1). A task may have a parent task (sub-tasks, AG-23):
// that is a relation between tasks, not another level.

// Project is a folder with one or more tracked repos.
type Project struct {
	ID        ProjectID `json:"id"`
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"createdAt"`
}

// Validate checks the fields that must hold before a project is stored.
func (p Project) Validate() error {
	if err := p.ID.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("project %s has no name", p.ID)
	}
	return nil
}

// Repo is a tracked git repository inside a project.
type Repo struct {
	ID            RepoID    `json:"id"`
	ProjectID     ProjectID `json:"projectId"`
	Name          string    `json:"name"`
	Path          string    `json:"path"`
	DefaultBranch string    `json:"defaultBranch"`
	// Trusted is set once the user has accepted the repo's own agent hooks
	// and MCP servers (SF-09).
	Trusted   bool      `json:"trusted"`
	CreatedAt time.Time `json:"createdAt"`
}

// Validate checks the fields that must hold before a repo is stored. The
// path is checked by its shape only (this package touches no filesystem):
// it must look absolute on Windows or Unix.
func (r Repo) Validate() error {
	if err := r.ID.Validate(); err != nil {
		return err
	}
	if err := r.ProjectID.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("repo %s has no name", r.ID)
	}
	if err := checkAbsPath(r.Path); err != nil {
		return fmt.Errorf("repo %s: %w", r.ID, err)
	}
	return nil
}

// checkAbsPath accepts a path that looks absolute: "/x", `\x` (including UNC
// and extended-length paths) or `C:\x` / `C:/x`. It is a shape check; whether
// the folder exists is the caller's business.
func checkAbsPath(p string) error {
	if strings.TrimSpace(p) == "" {
		return errors.New("the path is empty")
	}
	if strings.ContainsRune(p, 0) {
		return errors.New("the path holds a NUL character")
	}
	if p[0] == '/' || p[0] == '\\' {
		return nil
	}
	if len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') &&
		(p[0] >= 'A' && p[0] <= 'Z' || p[0] >= 'a' && p[0] <= 'z') {
		return nil
	}
	return fmt.Errorf("the path %q isn't absolute", p)
}

// Worktree is a git worktree created by Yggdrasil for a task.
type Worktree struct {
	ID        WorktreeID     `json:"id"`
	RepoID    RepoID         `json:"repoId"`
	TaskID    TaskID         `json:"taskId"`
	Name      string         `json:"name"`
	Path      string         `json:"path"`
	Branch    string         `json:"branch"`
	Status    WorktreeStatus `json:"status"`
	CreatedAt time.Time      `json:"createdAt"`
}

// Task is one unit of agent work.
type Task struct {
	ID        TaskID    `json:"id"`
	ProjectID ProjectID `json:"projectId"`
	// ParentTaskID is set for sub-tasks. One level only (AG-24): a task with
	// a parent can never be a parent.
	ParentTaskID *TaskID   `json:"parentTaskId,omitempty"`
	Title        string    `json:"title"`
	Prompt       string    `json:"prompt"`
	Scope        TaskScope `json:"scope"`
	// RepoIDs are the repos in scope: one for ScopeRepo, all tracked repos of
	// the project for ScopeProject.
	RepoIDs []RepoID   `json:"repoIds"`
	Mode    TaskMode   `json:"mode"`
	Agent   string     `json:"agent"`
	Model   string     `json:"model,omitempty"`
	Status  TaskStatus `json:"status"`
	// SessionID is the agent's resumable session, when it reports one.
	SessionID  string     `json:"sessionId,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	// Version is the row version for expected-version updates. It is a
	// storage detail and never leaves the server.
	Version int64 `json:"-"`
}

// Validate checks the fields that must hold before a task is stored.
func (t Task) Validate() error {
	if err := t.ID.Validate(); err != nil {
		return err
	}
	if err := t.Mode.Validate(); err != nil {
		return err
	}
	if err := t.Scope.Validate(); err != nil {
		return err
	}
	if err := t.Status.Validate(); err != nil {
		return err
	}
	if t.ParentTaskID != nil && *t.ParentTaskID == t.ID {
		return fmt.Errorf("task %s cannot be its own parent", t.ID)
	}
	if len(t.RepoIDs) == 0 {
		return fmt.Errorf("task %s has no repo in scope", t.ID)
	}
	return nil
}

// ChangeSet groups all proposals one task produced across repos (RV-03).
type ChangeSet struct {
	ID     ChangeSetID     `json:"id"`
	TaskID TaskID          `json:"taskId"`
	Status ChangeSetStatus `json:"status"`
	// MergeOrder lists repos in the order they should be merged (RV-04).
	MergeOrder []RepoID  `json:"mergeOrder"`
	CreatedAt  time.Time `json:"createdAt"`
}

// Proposal is a pending action that needs UI confirmation.
type Proposal struct {
	ID          ProposalID     `json:"id"`
	ChangeSetID *ChangeSetID   `json:"changeSetId,omitempty"`
	TaskID      *TaskID        `json:"taskId,omitempty"`
	RepoID      *RepoID        `json:"repoId,omitempty"`
	Kind        ProposalKind   `json:"kind"`
	Status      ProposalStatus `json:"status"`
	Summary     string         `json:"summary"`
	// Payload is kind-specific and never holds tokens.
	Payload   json.RawMessage `json:"payload,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
	DecidedAt *time.Time      `json:"decidedAt,omitempty"`
	// Version is the row version for expected-version updates. It is a
	// storage detail and never leaves the server.
	Version int64 `json:"-"`
}

// BuildStep is one command of a build.
type BuildStep struct {
	Index      int        `json:"index"`
	RepoID     RepoID     `json:"repoId"`
	Name       string     `json:"name"`
	Status     StepStatus `json:"status"`
	ExitCode   *int       `json:"exitCode,omitempty"`
	LogPath    string     `json:"logPath,omitempty"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	// Argv is the command line, Attempt the run counter of the step and
	// Outcome how the last attempt ended; Outcome is empty until the step has
	// finished.
	Argv    []string    `json:"argv,omitempty"`
	Attempt int         `json:"attempt,omitempty"`
	Outcome StepOutcome `json:"outcome,omitempty"`
}

// Build is an ordered sequence of build steps.
type Build struct {
	ID         BuildID     `json:"id"`
	WorktreeID *WorktreeID `json:"worktreeId,omitempty"`
	TaskID     *TaskID     `json:"taskId,omitempty"`
	Status     BuildStatus `json:"status"`
	Steps      []BuildStep `json:"steps"`
	CreatedAt  time.Time   `json:"createdAt"`
	FinishedAt *time.Time  `json:"finishedAt,omitempty"`
	// Version is the row version for expected-version updates. It is a
	// storage detail and never leaves the server.
	Version int64 `json:"-"`
}

// AuditEntry is one row of the audit log (SF-05). Detail is redacted before
// it is written and never holds tokens.
type AuditEntry struct {
	ID AuditID   `json:"id"`
	At time.Time `json:"at"`
	// Actor is "user", "agent:<name>" or "system".
	Actor  string `json:"actor"`
	Action string `json:"action"`
	// Subject names the thing acted on, e.g. "task:<id>".
	Subject string          `json:"subject"`
	Detail  json.RawMessage `json:"detail,omitempty"`
}
