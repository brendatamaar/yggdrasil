package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/hosts"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

// Boundary parsers: GitHub JSON to core types.

type pullJSON struct {
	Number    *int    `json:"number"`
	Title     *string `json:"title"`
	HTMLURL   string  `json:"html_url"`
	State     string  `json:"state"`
	Draft     bool    `json:"draft"`
	MergedAt  *string `json:"merged_at"`
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
	// Search results only.
	RepositoryURL string `json:"repository_url"`
}

func (p pullJSON) toMR(needBranches bool) (corehosts.MergeRequest, error) {
	if p.Number == nil || *p.Number <= 0 || p.Title == nil || p.HTMLURL == "" || p.State == "" || p.UpdatedAt == "" {
		return corehosts.MergeRequest{}, errors.New("a pull request is missing number, title, html_url, state or updated_at")
	}
	var state corehosts.MRState
	switch p.State {
	case "open":
		state = corehosts.MROpen
		if p.Draft {
			state = corehosts.MRDraft
		}
	case "closed":
		state = corehosts.MRClosed
		if p.MergedAt != nil && *p.MergedAt != "" {
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

func parsePulls(body []byte) ([]corehosts.MergeRequest, error) {
	if len(body) == 0 {
		return nil, nil
	}
	var raw []pullJSON
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse pull requests: %w", err)
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

// parseSearch parses the issue search used for the inbox.
func parseSearch(body []byte, role corehosts.Role) ([]corehosts.InboxItem, error) {
	var raw struct {
		Items []pullJSON `json:"items"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse search: %w", err)
	}
	out := make([]corehosts.InboxItem, 0, len(raw.Items))
	for _, r := range raw.Items {
		mr, err := r.toMR(false)
		if err != nil {
			return nil, err
		}
		repo := repoFromAPIURL(r.RepositoryURL)
		if repo == "" {
			return nil, errors.New("a search result is missing repository_url")
		}
		out = append(out, corehosts.InboxItem{MR: mr, Repo: repo, Role: role})
	}
	return out, nil
}

// repoFromAPIURL turns ".../repos/acme-shop/shop-api" into "acme-shop/shop-api".
func repoFromAPIURL(u string) string {
	_, rest, ok := strings.Cut(u, "/repos/")
	if !ok || strings.Count(rest, "/") != 1 || strings.HasPrefix(rest, "/") || strings.HasSuffix(rest, "/") {
		return ""
	}
	return rest
}

type checkRunsJSON struct {
	CheckRuns []struct {
		Status      string  `json:"status"`
		Conclusion  *string `json:"conclusion"`
		HTMLURL     string  `json:"html_url"`
		StartedAt   *string `json:"started_at"`
		CompletedAt *string `json:"completed_at"`
	} `json:"check_runs"`
}

type combinedStatusJSON struct {
	State      string `json:"state"`
	TotalCount int    `json:"total_count"`
	Statuses   []struct {
		State     string `json:"state"`
		TargetURL string `json:"target_url"`
		UpdatedAt string `json:"updated_at"`
	} `json:"statuses"`
}

// parsePipeline aggregates check runs and the combined commit status of a
// branch head: any failure is failed; else any pending is running; else all
// success is success. No runs and no statuses is nil. fallbackURL is used
// when the host gave no link.
func parsePipeline(checks, status []byte, fallbackURL string) (*corehosts.Pipeline, error) {
	var cr checkRunsJSON
	if len(checks) > 0 {
		if err := json.Unmarshal(checks, &cr); err != nil {
			return nil, fmt.Errorf("parse check runs: %w", err)
		}
	}
	var cs combinedStatusJSON
	if len(status) > 0 {
		if err := json.Unmarshal(status, &cs); err != nil {
			return nil, fmt.Errorf("parse commit status: %w", err)
		}
	}
	var failed, running, success, canceled, skipped, total int
	var link, failLink string
	var latest time.Time
	note := func(t *string) error {
		if t == nil || *t == "" {
			return nil
		}
		at, err := hosts.ParseTime(*t)
		if err != nil {
			return err
		}
		if at.After(latest) {
			latest = at
		}
		return nil
	}
	for _, r := range cr.CheckRuns {
		if r.Status == "" {
			return nil, errors.New("a check run is missing its status")
		}
		if err := note(r.StartedAt); err != nil {
			return nil, err
		}
		if err := note(r.CompletedAt); err != nil {
			return nil, err
		}
		total++
		if link == "" {
			link = r.HTMLURL
		}
		if r.Status != "completed" {
			running++
			continue
		}
		concl := ""
		if r.Conclusion != nil {
			concl = *r.Conclusion
		}
		switch concl {
		case "success":
			success++
		case "failure", "timed_out", "action_required", "startup_failure":
			failed++
			if failLink == "" {
				failLink = r.HTMLURL
			}
		case "cancelled":
			canceled++
		case "skipped", "neutral", "stale":
			skipped++
		default:
			return nil, fmt.Errorf("unknown check run conclusion %q", concl)
		}
	}
	if cs.TotalCount > 0 || len(cs.Statuses) > 0 {
		for _, s := range cs.Statuses {
			t := s.UpdatedAt
			if err := note(&t); err != nil {
				return nil, err
			}
			if link == "" {
				link = s.TargetURL
			}
		}
		total++
		switch cs.State {
		case "success":
			success++
		case "failure", "error":
			failed++
			for _, s := range cs.Statuses {
				if (s.State == "failure" || s.State == "error") && failLink == "" {
					failLink = s.TargetURL
				}
			}
		case "pending":
			running++
		default:
			return nil, fmt.Errorf("unknown commit status state %q", cs.State)
		}
	}
	if total == 0 {
		return nil, nil
	}
	var st corehosts.PipelineStatus
	switch {
	case failed > 0:
		st = corehosts.PipelineFailed
		link = cmpOr(failLink, link)
	case running > 0:
		st = corehosts.PipelineRunning
	case success > 0:
		st = corehosts.PipelineSuccess
	case canceled > 0:
		st = corehosts.PipelineCanceled
	default:
		st = corehosts.PipelineSkipped
	}
	return &corehosts.Pipeline{Status: st, URL: cmpOr(link, fallbackURL), UpdatedAt: latest}, nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
