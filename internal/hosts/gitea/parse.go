package gitea

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/hosts"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

// Boundary parsers: Gitea JSON to core types.

type pullJSON struct {
	Number    *int    `json:"number"`
	Title     *string `json:"title"`
	HTMLURL   string  `json:"html_url"`
	State     string  `json:"state"`
	Merged    bool    `json:"merged"`
	Draft     bool    `json:"draft"`
	UpdatedAt string  `json:"updated_at"`
	User      *struct {
		Login string `json:"login"`
	} `json:"user"`
	Head *struct {
		Ref string `json:"ref"`
	} `json:"head"`
	Base *struct {
		Ref string `json:"ref"`
	} `json:"base"`
	// Issue search results.
	Repository *struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	PullRequest *struct {
		Merged bool `json:"merged"`
		Draft  bool `json:"draft"`
	} `json:"pull_request"`
}

func isDraftTitle(t string) bool {
	l := strings.ToLower(t)
	return strings.HasPrefix(l, "wip:") || strings.HasPrefix(l, "[wip]") || strings.HasPrefix(l, "draft:") || strings.HasPrefix(l, "[draft]")
}

func (p pullJSON) toMR(needBranches bool) (corehosts.MergeRequest, error) {
	if p.Number == nil || *p.Number <= 0 || p.Title == nil || hosts.SafeWebURL(p.HTMLURL) == "" || p.State == "" || p.UpdatedAt == "" {
		return corehosts.MergeRequest{}, errors.New("a pull request is missing number, title, html_url, state or updated_at")
	}
	merged, draft := p.Merged, p.Draft || isDraftTitle(*p.Title)
	if p.PullRequest != nil {
		merged, draft = merged || p.PullRequest.Merged, draft || p.PullRequest.Draft
	}
	var state corehosts.MRState
	switch p.State {
	case "open":
		state = corehosts.MROpen
		if draft {
			state = corehosts.MRDraft
		}
	case "closed":
		state = corehosts.MRClosed
		if merged {
			state = corehosts.MRMerged
		}
	default:
		return corehosts.MergeRequest{}, fmt.Errorf("unknown pull request state %q", p.State)
	}
	at, err := hosts.ParseTime(p.UpdatedAt)
	if err != nil {
		return corehosts.MergeRequest{}, err
	}
	out := corehosts.MergeRequest{Number: *p.Number, Title: *p.Title, URL: p.HTMLURL, State: state, UpdatedAt: at}
	if p.User != nil {
		out.Author = p.User.Login
	}
	if p.Head != nil {
		out.Source = p.Head.Ref
	}
	if p.Base != nil {
		out.Target = p.Base.Ref
	}
	if needBranches && (out.Source == "" || out.Target == "") {
		return corehosts.MergeRequest{}, errors.New("a pull request is missing its branches")
	}
	return out, nil
}

// parsePulls parses a pull request list and keeps those whose head is branch.
func parsePulls(body []byte, branch string) ([]corehosts.MergeRequest, error) {
	if len(body) == 0 {
		return nil, nil
	}
	var raw []pullJSON
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse pull requests: %w", err)
	}
	var out []corehosts.MergeRequest
	for _, r := range raw {
		mr, err := r.toMR(true)
		if err != nil {
			return nil, err
		}
		if mr.Source == branch {
			out = append(out, mr)
		}
	}
	return out, nil
}

func parseSearch(body []byte, role corehosts.Role) ([]corehosts.InboxItem, error) {
	var raw []pullJSON
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse search: %w", err)
	}
	out := make([]corehosts.InboxItem, 0, len(raw))
	for _, r := range raw {
		mr, err := r.toMR(false)
		if err != nil {
			return nil, err
		}
		if r.Repository == nil || r.Repository.FullName == "" {
			return nil, errors.New("a search result is missing repository.full_name")
		}
		out = append(out, corehosts.InboxItem{MR: mr, Repo: r.Repository.FullName, Role: role})
	}
	return out, nil
}

type statusJSON struct {
	State    string `json:"state"`
	Statuses []struct {
		TargetURL string `json:"target_url"`
		UpdatedAt string `json:"updated_at"`
	} `json:"statuses"`
}

// parseStatus turns the combined commit status of the branch head into a
// pipeline. No statuses is nil.
func parseStatus(body []byte) (*corehosts.Pipeline, error) {
	if len(body) == 0 {
		return nil, nil
	}
	var s statusJSON
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, fmt.Errorf("parse commit status: %w", err)
	}
	if len(s.Statuses) == 0 {
		return nil, nil
	}
	var st corehosts.PipelineStatus
	switch s.State {
	case "success":
		st = corehosts.PipelineSuccess
	case "pending":
		st = corehosts.PipelineRunning
	case "failure", "error":
		st = corehosts.PipelineFailed
	case "warning":
		st = corehosts.PipelineSuccess
	default:
		st = corehosts.PipelineUnknown
	}
	var latest time.Time
	link := ""
	for _, x := range s.Statuses {
		if x.UpdatedAt != "" {
			at, err := hosts.ParseTime(x.UpdatedAt)
			if err != nil {
				return nil, err
			}
			if at.After(latest) {
				latest = at
			}
		}
		if link == "" {
			link = hosts.SafeWebURL(x.TargetURL)
		}
	}
	return &corehosts.Pipeline{Status: st, URL: link, UpdatedAt: latest}, nil
}
