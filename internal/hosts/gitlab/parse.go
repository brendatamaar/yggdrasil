package gitlab

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/hosts"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

// Boundary parsers: GitLab JSON to core types. They reject a response with a
// missing required field or an unknown MR state.

type mrJSON struct {
	IID          *int    `json:"iid"`
	Title        *string `json:"title"`
	WebURL       string  `json:"web_url"`
	SourceBranch string  `json:"source_branch"`
	TargetBranch string  `json:"target_branch"`
	State        string  `json:"state"`
	Draft        bool    `json:"draft"`
	WIP          bool    `json:"work_in_progress"`
	UpdatedAt    string  `json:"updated_at"`
	Author       *struct {
		Username string `json:"username"`
	} `json:"author"`
	References *struct {
		Full string `json:"full"`
	} `json:"references"`
}

func (m mrJSON) toMR(needBranches bool) (corehosts.MergeRequest, error) {
	if m.IID == nil || *m.IID <= 0 || m.Title == nil || m.WebURL == "" || m.State == "" || m.UpdatedAt == "" {
		return corehosts.MergeRequest{}, errors.New("a merge request is missing iid, title, web_url, state or updated_at")
	}
	if needBranches && (m.SourceBranch == "" || m.TargetBranch == "") {
		return corehosts.MergeRequest{}, errors.New("a merge request is missing its branches")
	}
	var state corehosts.MRState
	switch m.State {
	case "opened", "locked":
		state = corehosts.MROpen
		if m.Draft || m.WIP {
			state = corehosts.MRDraft
		}
	case "merged":
		state = corehosts.MRMerged
	case "closed":
		state = corehosts.MRClosed
	default:
		return corehosts.MergeRequest{}, fmt.Errorf("unknown merge request state %q", m.State)
	}
	at, err := hosts.ParseTime(m.UpdatedAt)
	if err != nil {
		return corehosts.MergeRequest{}, err
	}
	out := corehosts.MergeRequest{
		Number: *m.IID, Title: *m.Title, URL: m.WebURL, Source: m.SourceBranch, Target: m.TargetBranch,
		State: state, UpdatedAt: at,
	}
	if m.Author != nil {
		out.Author = m.Author.Username
	}
	return out, nil
}

// parseMRs parses a list of merge requests.
func parseMRs(body []byte) ([]corehosts.MergeRequest, error) {
	if len(body) == 0 {
		return nil, nil
	}
	var raw []mrJSON
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse merge requests: %w", err)
	}
	out := make([]corehosts.MergeRequest, 0, len(raw))
	for _, r := range raw {
		mr, err := r.toMR(true)
		if err != nil {
			return nil, err
		}
		out = append(out, mr)
	}
	return out, nil
}

type pipelineJSON struct {
	Status    string `json:"status"`
	WebURL    string `json:"web_url"`
	UpdatedAt string `json:"updated_at"`
}

// parsePipelines parses a newest-first list of pipelines and returns the
// newest, or nil when there is none.
func parsePipelines(body []byte) (*corehosts.Pipeline, error) {
	if len(body) == 0 {
		return nil, nil
	}
	var raw []pipelineJSON
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse pipelines: %w", err)
	}
	if len(raw) == 0 {
		return nil, nil
	}
	p := raw[0]
	if p.Status == "" || p.WebURL == "" || p.UpdatedAt == "" {
		return nil, errors.New("a pipeline is missing status, web_url or updated_at")
	}
	at, err := hosts.ParseTime(p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &corehosts.Pipeline{Status: pipelineStatus(p.Status), URL: p.WebURL, UpdatedAt: at}, nil
}

func pipelineStatus(s string) corehosts.PipelineStatus {
	switch s {
	case "created", "waiting_for_resource", "preparing", "pending", "scheduled":
		return corehosts.PipelinePending
	case "running":
		return corehosts.PipelineRunning
	case "success":
		return corehosts.PipelineSuccess
	case "failed":
		return corehosts.PipelineFailed
	case "canceled", "canceling":
		return corehosts.PipelineCanceled
	case "skipped":
		return corehosts.PipelineSkipped
	}
	return corehosts.PipelineUnknown
}

// parseInbox parses merge requests of the inbox queries and tags them with a role.
func parseInbox(body []byte, role corehosts.Role) ([]corehosts.InboxItem, error) {
	var raw []mrJSON
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse inbox: %w", err)
	}
	out := make([]corehosts.InboxItem, 0, len(raw))
	for _, r := range raw {
		mr, err := r.toMR(false)
		if err != nil {
			return nil, err
		}
		if r.References == nil || r.References.Full == "" {
			return nil, errors.New("an inbox merge request is missing references.full")
		}
		repo := r.References.Full
		if i := strings.LastIndexAny(repo, "!#"); i >= 0 {
			repo = repo[:i]
		}
		if repo == "" {
			return nil, errors.New("an inbox merge request has an empty project")
		}
		out = append(out, corehosts.InboxItem{MR: mr, Repo: repo, Role: role})
	}
	return out, nil
}

// parseUser returns the signed-in user's name.
func parseUser(body []byte) (string, error) {
	var u struct {
		Username string `json:"username"`
	}
	if err := json.Unmarshal(body, &u); err != nil {
		return "", fmt.Errorf("parse user: %w", err)
	}
	if u.Username == "" {
		return "", errors.New("the user has no username")
	}
	return u.Username, nil
}
