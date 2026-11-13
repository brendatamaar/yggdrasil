package tasks

import (
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// ParseSubmission applies stored identity to a follow-up before parsing. A nil
// existing task means a new submission. It does not admit or start a turn.
func ParseSubmission(project core.ProjectID, raw RawMessage, existing *core.Task) (core.ProjectID, ParsedMessage, error) {
	if existing != nil {
		project, raw.Agent, raw.Model = existing.ProjectID, existing.Agent, existing.Model
		raw.Scope = string(existing.Scope)
		raw.Repos = nil
		if existing.Scope != core.ScopeProject {
			raw.Repos = submissionRepoStrings(existing.RepoIDs)
		}
	}
	check := raw
	if existing != nil && existing.Scope == core.ScopeProject && raw.Mode == string(core.ModeAsk) {
		// The pairing rule applies to the first message. An Ask follow-up reads
		// the stored project scope, validated using its first repo.
		check.Scope = string(core.ScopeRepo)
		check.Repos = submissionRepoStrings(existing.RepoIDs[:min(1, len(existing.RepoIDs))])
	}
	pm, err := ParseMessage(check)
	if err != nil {
		return project, ParsedMessage{}, err
	}
	// Restore the actual scope after validating the project Ask exception.
	pm.Scope = core.TaskScope(raw.Scope)
	pm.Repos = make([]core.RepoID, len(raw.Repos))
	for i, id := range raw.Repos {
		pm.Repos[i] = core.RepoID(id)
	}
	if existing != nil {
		pm.Repos = existing.RepoIDs
	}
	return project, pm, nil
}

func submissionRepoStrings(ids []core.RepoID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = string(id)
	}
	return out
}

// WritingModeRefusal checks the deployment switch after message parsing and
// before project lookup. Admission, trust and mode-switch rules remain in guard.
func WritingModeRefusal(mode core.TaskMode, enabled bool) *Reject {
	if mode != core.ModeAsk && !enabled {
		return &Reject{Reason: "write_modes_off", Message: "Modes that edit files are switched off in this Yggdrasil setup. Ask a question instead, or turn on write modes in the setup and restart."}
	}
	return nil
}

// ScopeRefusal checks loaded facts for a new submission. The shell loads either
// all project repos or the single requested repo; lookup failures stay in shell.
// Resumed tasks instead reload their stored IDs and retain that exact scope.
func ScopeRefusal(project core.ProjectID, scope core.TaskScope, repos []core.Repo) *Reject {
	if scope == core.ScopeProject {
		if len(repos) == 0 {
			return &Reject{Reason: "no_repos", Message: "This project has no tracked repos."}
		}
	} else if repos[0].ProjectID != project {
		return &Reject{Reason: "repo_not_in_project", Message: "That repo isn't part of the project. Choose a repo that the project tracks."}
	}
	return nil
}

// NewSubmissionTask supplies the ordinary task defaults from parsed input and
// resolved repos. ID and time are supplied by the shell. The task still needs
// admission and Step's normal transition before any effects may run.
func NewSubmissionTask(id core.TaskID, project core.ProjectID, pm ParsedMessage, repos []core.Repo, now time.Time) core.Task {
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
	return core.Task{ID: id, ProjectID: project, Title: title, Prompt: pm.Prompt, Scope: pm.Scope, RepoIDs: ids,
		Mode: pm.Mode, Agent: pm.Agent, Model: pm.Model, Status: core.TaskDraft, CreatedAt: now, UpdatedAt: now}
}
