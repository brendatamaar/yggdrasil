package tasks

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Draft tasks from an imported issue (AC-05, docs/design/phase-6-actions.md).
// Importing an issue creates ONE ordinary task in the draft status, linked to a
// tracked ready repo and project, with provenance (core.TaskSource) and a dedup
// key. It creates no turn, no worktree and no process: starting the draft is a
// separate, explicit submission through the existing Message path, with its own
// admission, trust scan and queue.
//
// The internal repo-setup draft (core.SourceRepoSetup) is a draft that can
// never be started.

// Limits of a draft.
const (
	MaxDraftTitle  = 200
	MaxDraftPrompt = 64 << 10
)

// ErrInvalidDraft wraps every rejection by ParseDraftSpec.
var ErrInvalidDraft = errors.New("invalid draft")

func badDraft(f string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidDraft, fmt.Sprintf(f, a...))
}

// IssueRef is the identity of one host issue as the server resolved it from a
// tracked repo, never from a browser URL.
type IssueRef struct {
	Instance string
	Project  string
	Number   int
}

// Validate checks the shape.
func (r IssueRef) Validate() error {
	if r.Instance == "" || r.Project == "" || r.Number <= 0 || len(r.Instance) > 255 || len(r.Project) > 1024 || (!utf8.ValidString(r.Instance) || !utf8.ValidString(r.Project)) {
		return badDraft("the issue needs its host, project and a positive number")
	}
	if strings.ContainsAny(r.Instance, "/@?#\\") || strings.ContainsAny(r.Project, "@?#\\:") || strings.ContainsFunc(r.Instance+r.Project, func(c rune) bool { return c <= ' ' || c == 0x7f }) {
		return badDraft("the issue identity has blank or control characters")
	}
	return nil
}

// DedupKey is the project-unique key of the issue.
func (r IssueRef) DedupKey() string { return core.IssueDedupKey(r.Instance, r.Project, r.Number) }

// Source returns the provenance stored on the draft task.
func (r IssueRef) Source() core.TaskSource {
	return core.TaskSource{Kind: core.SourceIssue, Instance: r.Instance, Project: r.Project, Number: r.Number, DedupKey: r.DedupKey()}
}

// DraftSpec is a parsed import request. Brief is the plain-text prompt the
// task will start with once the user starts it (the issue text the shell
// fetched, bounded and redacted, labelled by source); it is stored but not run.
type DraftSpec struct {
	RequestID core.RequestID
	Project   core.ProjectID
	Repo      core.RepoID
	Issue     IssueRef
	Title     string
	Brief     string
	Agent     string
	Model     string
}

// ParseDraftSpec validates the fields. The mode of a draft is always Ask until
// the user starts it with an explicit mode.
func ParseDraftSpec(s DraftSpec) (DraftSpec, error) {
	if err := s.RequestID.Validate(); err != nil || len(s.RequestID) > 100 {
		return DraftSpec{}, badDraft("the request id is not valid")
	}
	if s.Project.Validate() != nil || s.Repo.Validate() != nil {
		return DraftSpec{}, badDraft("the project or repo id is not valid")
	}
	if err := s.Issue.Validate(); err != nil {
		return DraftSpec{}, err
	}
	s.Title = strings.TrimSpace(s.Title)
	switch {
	case s.Title == "" || utf8.RuneCountInString(s.Title) > MaxDraftTitle || !utf8.ValidString(s.Title) ||
		strings.ContainsFunc(s.Title, func(c rune) bool { return c < 0x20 || c == 0x7f }):
		return DraftSpec{}, badDraft("the title is empty, too long or has control characters")
	case len(s.Brief) > MaxDraftPrompt || !utf8.ValidString(s.Brief) || strings.ContainsRune(s.Brief, 0):
		return DraftSpec{}, badDraft("the issue text is too large")
	case !ValidModelName(s.Agent) || len(s.Agent) > 64 || (s.Model != "" && !ValidModelName(s.Model)):
		return DraftSpec{}, badDraft("choose an agent")
	}
	return s, nil
}

// DraftOutcome is the result of an import decision (sealed).
//
//sumtype:decl
type DraftOutcome interface{ isDraftOutcome() }

// DraftCreate: a new draft task must be created with Task (status draft,
// Source set, Mode ask), in the same transaction as its audit entry.
type DraftCreate struct{ Task core.Task }

// DraftExists: the issue was already imported into the project; Task is the
// first draft. Nothing is created and the response says so.
type DraftExists struct{ Task core.TaskID }

// DraftRefuse: the import is refused before anything is written.
type DraftRefuse struct{ Reason DraftRefusal }

func (DraftCreate) isDraftOutcome() {}
func (DraftExists) isDraftOutcome() {}
func (DraftRefuse) isDraftOutcome() {}

// DraftRefusal says why an import was refused.
type DraftRefusal string

const (
	DraftRepoNotReady  DraftRefusal = "repo_not_ready"
	DraftRepoUntracked DraftRefusal = "repo_untracked"
	DraftPending       DraftRefusal = "not_implemented"
)

// DraftFacts are what the shell checked before the decision. Existing is the
// ID of a task of the same project with the issue's dedup key, or empty.
type DraftFacts struct {
	RepoTracked bool
	RepoReady   bool
	Existing    core.TaskID
	// TaskID and Now are generated by the shell.
	TaskID core.TaskID
	Now    time.Time
}

// PlanDraft decides what an import does. Scope is checked before deduplication.
func PlanDraft(s DraftSpec, f DraftFacts) DraftOutcome {
	if !f.RepoTracked {
		return DraftRefuse{Reason: DraftRepoUntracked}
	}
	if !f.RepoReady {
		return DraftRefuse{Reason: DraftRepoNotReady}
	}
	if f.Existing != "" {
		return DraftExists{Task: f.Existing}
	}
	source := s.Issue.Source()
	return DraftCreate{Task: core.Task{
		ID: f.TaskID, ProjectID: s.Project, RepoIDs: []core.RepoID{s.Repo},
		Title: s.Title, Prompt: s.Brief, Scope: core.ScopeRepo, Mode: core.ModeAsk,
		Agent: s.Agent, Model: s.Model, Status: core.TaskDraft, Source: &source,
		CreatedAt: f.Now, UpdatedAt: f.Now,
	}}
}

// DraftStart is the explicit submission of an existing draft: it carries the
// task and the turn the user chooses, and it goes through the same admission
// as any message (guard.Admit, trust, queue). It is never implied by an import.
type DraftStart struct {
	Task  core.TaskID
	Mode  core.TaskMode
	OptIn bool
	Model string
}
