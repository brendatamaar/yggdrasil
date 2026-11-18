package tasks

import (
	"strings"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Waiting drafts (AC-01, E6): a repo whose folder cannot be read gets ONE
// ordinary draft task with a diagnosis brief, so the Needs attention row still
// has an agent action. The draft is a task in the draft status, in Ask mode,
// linked to a tracked READY repo and its project. It has no turn, worktree or
// process: it only starts later through the ordinary explicit start, which
// re-runs admission, the trust scan and the Ask integrity check.

// WaitingDraftSpec is a parsed save request. Brief is already bounded and
// redacted by the action shell; the owning package redacts it again.
type WaitingDraftSpec struct {
	RequestID core.RequestID
	Project   core.ProjectID
	Repo      core.RepoID
	Title     string
	Brief     string
	Agent     string
	Model     string
	// Source is empty for an ordinary waiting draft and core.SourceProposalAnchor
	// for the fixed anchor of a repo (anchor.go). omitempty keeps the digest of
	// every ordinary draft stored before this field existed.
	Source core.TaskSourceKind `json:",omitempty"`
}

// ParseWaitingDraftSpec validates the fields at the boundary.
func ParseWaitingDraftSpec(s WaitingDraftSpec) (WaitingDraftSpec, error) {
	if err := s.RequestID.Validate(); err != nil || len(s.RequestID) > 100 {
		return WaitingDraftSpec{}, badDraft("the request id is not valid")
	}
	// The reserved request IDs belong to anchors, and an anchor has no other ID.
	switch {
	case s.Source != core.SourceUser && s.Source != core.SourceProposalAnchor:
		return WaitingDraftSpec{}, badDraft("the draft kind is not valid")
	case s.Source == core.SourceProposalAnchor && s.RequestID != AnchorRequest(s.Repo):
		return WaitingDraftSpec{}, badDraft("an anchor draft uses its reserved request id")
	case s.Source == core.SourceUser && HasReservedPrefix(s.RequestID):
		return WaitingDraftSpec{}, badDraft("the request id is reserved")
	}
	if s.Project.Validate() != nil || s.Repo.Validate() != nil {
		return WaitingDraftSpec{}, badDraft("the project or repo id is not valid")
	}
	s.Title = strings.TrimSpace(s.Title)
	switch {
	case s.Title == "" || utf8.RuneCountInString(s.Title) > MaxDraftTitle || !utf8.ValidString(s.Title) ||
		strings.ContainsFunc(s.Title, func(c rune) bool { return c < 0x20 || c == 0x7f }):
		return WaitingDraftSpec{}, badDraft("the title is empty, too long or has control characters")
	case strings.TrimSpace(s.Brief) == "" || len(s.Brief) > MaxDraftPrompt || !utf8.ValidString(s.Brief) || strings.ContainsRune(s.Brief, 0):
		return WaitingDraftSpec{}, badDraft("the diagnosis brief is empty or too large")
	case !ValidModelName(s.Agent) || len(s.Agent) > 64 || (s.Model != "" && !ValidModelName(s.Model)):
		return WaitingDraftSpec{}, badDraft("choose an agent")
	}
	return s, nil
}

// WaitingMessage is the plain-language text of a refusal of a waiting draft.
func (r DraftRefusal) WaitingMessage() string {
	switch r {
	case DraftRepoNotReady:
		return "This repo isn't set up yet. Finish its setup, then save the draft."
	case DraftRepoUntracked:
		return "This repo isn't tracked in the project. Track it, then save the draft."
	case DraftPayloadChanged:
		return "This request ID was already used for a different draft. Start again."
	default:
		return "Yggdrasil couldn't prepare this draft. Try again."
	}
}

// PlanWaitingDraft decides what a save does, in the order of PlanDraft: scope
// first (an untracked or reserved repo never reveals a stored draft nor that
// the request ID exists), valid shell facts, replay of the request ID (changed
// payload refuses, same payload returns the stored draft and writes nothing),
// then create. Only DraftCreate, DraftReplayed and DraftRefuse are returned.
// The facts' Existing field is ignored: a waiting draft has no dedup key.
func PlanWaitingDraft(s WaitingDraftSpec, f DraftFacts) DraftOutcome {
	if !f.RepoTracked {
		return DraftRefuse{Reason: DraftRepoUntracked}
	}
	if !f.RepoReady {
		return DraftRefuse{Reason: DraftRepoNotReady}
	}
	if f.Now.IsZero() || f.TaskID.Validate() != nil {
		return DraftRefuse{Reason: DraftBadFacts}
	}
	if r, ok := f.Replay.(Replayed); ok {
		switch {
		case r.Digest != f.Digest:
			return DraftRefuse{Reason: DraftPayloadChanged}
		case r.Task.Validate() != nil:
			return DraftRefuse{Reason: DraftBadFacts}
		}
		return DraftReplayed{Task: r.Task}
	}
	t := core.Task{
		ID: f.TaskID, ProjectID: s.Project, RepoIDs: []core.RepoID{s.Repo},
		Title: s.Title, Prompt: s.Brief, Scope: core.ScopeRepo, Mode: core.ModeAsk,
		Agent: s.Agent, Model: s.Model, Status: core.TaskDraft,
		CreatedAt: f.Now, UpdatedAt: f.Now,
	}
	if s.Source == core.SourceProposalAnchor {
		t.Source = &core.TaskSource{Kind: core.SourceProposalAnchor}
	}
	return DraftCreate{Task: t}
}
