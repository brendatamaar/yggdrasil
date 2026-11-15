package hosts

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

// ContextAPI shares the bounded provider boundary between the GitHub and
// GitLab adapters. Root is derived by the adapter from the configured instance.
type ContextAPI struct {
	Getter *Getter
	Root   string
	GitLab bool
}
type contextRead struct {
	api       ContextAPI
	ctx       context.Context
	token     string
	remaining int
	more      bool
}

func (a ContextAPI) begin(ctx context.Context, token string, project corehosts.ProjectPath) (*contextRead, context.CancelFunc, error) {
	if !validContextProject(project) {
		return nil, nil, ErrContextScope
	}
	if token == "" {
		return nil, nil, NeedToken()
	}
	ctx, cancel := context.WithTimeout(ctx, corehosts.ContextRequestTimeout)
	return &contextRead{api: a, ctx: ctx, token: token, remaining: corehosts.ContextMaxResponseBytes}, cancel, nil
}
func (r *contextRead) get(path string, dst interface{}) ([]byte, error) {
	if r.remaining <= 0 {
		return nil, ErrContextTooLarge
	}
	auth := AuthBearer
	if r.api.GitLab {
		auth = AuthPrivateToken
	}
	resp, err := r.api.Getter.GetBounded(r.ctx, r.api.Root+path, r.token, auth, r.remaining)
	r.remaining -= resp.readBytes
	if err != nil {
		return nil, err
	}
	r.more = hasNextContextPage(resp.Header)
	if dst != nil {
		if strings.TrimSpace(string(resp.Body)) == "null" {
			return nil, errors.New("missing host context response")
		}
		if err = json.Unmarshal(resp.Body, dst); err != nil {
			return nil, errors.New("invalid host context response")
		}
	}
	return resp.Body, nil
}
func (a ContextAPI) project(p corehosts.ProjectPath) string {
	if a.GitLab {
		return "/projects/" + url.PathEscape(string(p))
	}
	return "/repos/" + PathSegments(p)
}
func (a ContextAPI) issuePath() string { return "/issues" }

type contextIssueWire struct {
	Number      int       `json:"number"`
	IID         int       `json:"iid"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	Description string    `json:"description"`
	HTMLURL     string    `json:"html_url"`
	WebURL      string    `json:"web_url"`
	Updated     time.Time `json:"updated_at"`
	User        struct {
		Login string `json:"login"`
	} `json:"user"`
	Author struct {
		Username string `json:"username"`
	} `json:"author"`
	Labels []json.RawMessage `json:"labels"`
	Pull   json.RawMessage   `json:"pull_request"`
}

func (a ContextAPI) issue(w contextIssueWire) (corehosts.IssueDetail, error) {
	n := w.Number
	author := w.User.Login
	body := w.Body
	link := w.HTMLURL
	if a.GitLab {
		n = w.IID
		author = w.Author.Username
		body = w.Description
		link = w.WebURL
	}
	if n <= 0 || w.Title == "" || len(w.Pull) > 0 {
		return corehosts.IssueDetail{}, errors.New("invalid issue identity")
	}
	// Host links are display-only, but must not carry credentials or leave origin.
	if link != "" {
		u, err := url.Parse(link)
		root, _ := url.Parse(a.Root)
		if a.Root == "https://api.github.com" {
			root, _ = url.Parse("https://github.com")
		}
		if err != nil || u.User != nil || u.Host != root.Host || u.Scheme != root.Scheme {
			link = ""
		}
	}
	labels := make([]string, 0, len(w.Labels))
	for _, raw := range w.Labels {
		var label string
		if a.GitLab {
			if json.Unmarshal(raw, &label) != nil {
				return corehosts.IssueDetail{}, errors.New("invalid issue label")
			}
		} else {
			var obj struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(raw, &obj) != nil {
				return corehosts.IssueDetail{}, errors.New("invalid issue label")
			}
			label = obj.Name
		}
		labels = append(labels, label)
	}
	return corehosts.IssueDetail{Summary: corehosts.IssueSummary{Number: n, Title: w.Title, Author: author, Labels: labels, UpdatedAt: w.Updated}, Body: body, URL: link}, nil
}
func (a ContextAPI) Issues(ctx context.Context, inst corehosts.Instance, token string, p corehosts.ProjectPath) (ContextResult, error) {
	r, cancel, err := a.begin(ctx, token, p)
	if err != nil {
		return ContextResult{}, err
	}
	defer cancel()
	out := corehosts.IssueList{}
	for page := 1; page <= corehosts.ContextMaxPages; page++ {
		state := "open"
		if a.GitLab {
			state = "opened"
		}
		var items []contextIssueWire
		_, err = r.get(a.project(p)+a.issuePath()+"?state="+state+"&per_page=50&page="+strconv.Itoa(page), &items)
		if err != nil {
			return ContextResult{}, err
		}
		if len(items) > 50 {
			return ContextResult{}, errors.New("host issue page exceeds limit")
		}
		for _, w := range items {
			if len(w.Pull) > 0 {
				continue
			}
			d, e := a.issue(w)
			if e != nil {
				return ContextResult{}, e
			}
			out.Items = append(out.Items, d.Summary)
		}
		if len(items) < 50 && !r.more {
			break
		}
		if page == corehosts.ContextMaxPages {
			out.Truncated = true
		}
	}
	return redactContext(ContextResult{Value: out, Expires: a.Getter.now().Add(corehosts.ContextCacheTTL)}, token), nil
}
func (a ContextAPI) Issue(ctx context.Context, inst corehosts.Instance, token string, p corehosts.ProjectPath, number int) (ContextResult, error) {
	if number <= 0 {
		return ContextResult{}, ErrContextScope
	}
	r, cancel, err := a.begin(ctx, token, p)
	if err != nil {
		return ContextResult{}, err
	}
	defer cancel()
	var w contextIssueWire
	_, err = r.get(a.project(p)+a.issuePath()+"/"+strconv.Itoa(number), &w)
	if err != nil {
		return ContextResult{}, err
	}
	d, err := a.issue(w)
	if err != nil {
		return ContextResult{}, err
	}
	if d.Summary.Number != number {
		return ContextResult{}, ErrContextScope
	}
	return redactContext(ContextResult{Value: d, Expires: a.Getter.now().Add(corehosts.ContextCacheTTL)}, token), nil
}

type contextMRWire struct {
	Draft          bool   `json:"draft"`
	WorkInProgress bool   `json:"work_in_progress"`
	Merged         bool   `json:"merged"`
	Number         int    `json:"number"`
	IID            int    `json:"iid"`
	Title          string `json:"title"`
	State          string `json:"state"`
	Head           struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"base"`
	SHA      string `json:"sha"`
	Source   string `json:"source_branch"`
	Target   string `json:"target_branch"`
	DiffRefs struct {
		Head string `json:"head_sha"`
	} `json:"diff_refs"`
}

func (a ContextAPI) MRDiff(ctx context.Context, inst corehosts.Instance, token string, p corehosts.ProjectPath, number int) (ContextResult, error) {
	if number <= 0 {
		return ContextResult{}, ErrContextScope
	}
	r, cancel, err := a.begin(ctx, token, p)
	if err != nil {
		return ContextResult{}, err
	}
	defer cancel()
	path := a.project(p) + "/pulls/" + strconv.Itoa(number)
	if a.GitLab {
		path = a.project(p) + "/merge_requests/" + strconv.Itoa(number)
	}
	var w contextMRWire
	_, err = r.get(path, &w)
	if err != nil {
		return ContextResult{}, err
	}
	n := w.Number
	head := w.Head.SHA
	source := w.Head.Ref
	target := w.Base.Ref
	if a.GitLab {
		n = w.IID
		head = w.DiffRefs.Head
		source = w.Source
		target = w.Target
	}
	if n != number {
		return ContextResult{}, ErrContextScope
	}
	state := corehosts.MROpen
	switch w.State {
	case "open", "opened":
		if w.Draft || w.WorkInProgress {
			state = corehosts.MRDraft
		}
	case "closed", "locked":
		state = corehosts.MRClosed
		if w.Merged {
			state = corehosts.MRMerged
		}
	case "merged":
		state = corehosts.MRMerged
	default:
		return ContextResult{}, errors.New("invalid merge request status")
	}
	value := corehosts.MRContext{MR: corehosts.MergeRequest{Number: n, Title: w.Title, Source: source, Target: target, State: state}, Head: head}
	key := corehosts.ContextKey{Repo: "rep_context", Kind: corehosts.ContextMRDiff, ObjectID: strconv.Itoa(number), Head: head}
	if head == "" || key.Validate() != nil {
		return ContextResult{}, errors.New("invalid immutable merge request head")
	}
	// Diff by immutable commit range rather than the mutable pull request route.
	if !a.GitLab {
		// GitHub comparison must be pinned on both sides.
		baseKey := key
		baseKey.Head = w.Base.SHA
		if w.Base.SHA == "" || baseKey.Validate() != nil {
			return ContextResult{}, errors.New("invalid immutable base")
		}
		var comparison struct {
			Files []struct {
				Filename string `json:"filename"`
				Patch    string `json:"patch"`
			} `json:"files"`
		}
		_, err = r.get(a.project(p)+"/compare/"+w.Base.SHA+"..."+head, &comparison)
		if err != nil {
			return ContextResult{}, err
		}
		for _, f := range comparison.Files {
			value.Diff += "\n" + f.Filename + "\n" + f.Patch + "\n"
			if f.Patch == "" {
				value.DiffTruncated = true
			}
		}
		if len(comparison.Files) >= 300 {
			value.DiffTruncated = true
		}
	} else {
		// Resolve an immutable GitLab diff version. Never attach raw_diffs from
		// a mutable MR route to a previously fetched head.
		var versions []struct {
			ID   int64  `json:"id"`
			Head string `json:"head_commit_sha"`
		}
		_, err = r.get(path+"/versions?per_page=50", &versions)
		if err != nil {
			return ContextResult{}, err
		}
		if len(versions) > 50 {
			return ContextResult{}, ErrContextTooLarge
		}
		version := int64(0)
		for _, v := range versions {
			if v.Head == head && v.ID > 0 {
				version = v.ID
				break
			}
		}
		if version == 0 {
			return ContextResult{}, ErrContextUnsupported
		}
		var diff struct {
			Head     string `json:"head_commit_sha"`
			Overflow bool   `json:"overflow"`
			Diffs    []struct {
				Old       string `json:"old_path"`
				New       string `json:"new_path"`
				Diff      string `json:"diff"`
				TooLarge  bool   `json:"too_large"`
				Collapsed bool   `json:"collapsed"`
			} `json:"diffs"`
		}
		_, err = r.get(path+"/versions/"+strconv.FormatInt(version, 10), &diff)
		if err != nil {
			return ContextResult{}, err
		}
		if diff.Head != head {
			return ContextResult{}, ErrContextScope
		}
		value.DiffTruncated = diff.Overflow
		for _, d := range diff.Diffs {
			value.Diff += "\n" + d.Old + " -> " + d.New + "\n" + d.Diff + "\n"
			value.DiffTruncated = value.DiffTruncated || d.TooLarge || d.Collapsed
		}
	}
	var after contextMRWire
	_, err = r.get(path, &after)
	if err != nil {
		return ContextResult{}, err
	}
	next := after.Head.SHA
	if a.GitLab {
		next = after.DiffRefs.Head
	}
	if next != head {
		return ContextResult{}, ErrContextScope
	}
	return redactContext(ContextResult{Value: value, Expires: a.Getter.now().Add(corehosts.ContextCacheTTL)}, token), nil
}

func (a ContextAPI) FailedJobs(ctx context.Context, inst corehosts.Instance, token string, p corehosts.ProjectPath, runID, head string) (ContextResult, error) {
	key := corehosts.ContextKey{Repo: "rep_context", Kind: corehosts.ContextFailedJobs, ObjectID: runID, Head: head}
	if key.Validate() != nil {
		return ContextResult{}, ErrContextScope
	}
	r, cancel, err := a.begin(ctx, token, p)
	if err != nil {
		return ContextResult{}, err
	}
	defer cancel()
	path := a.project(p) + "/actions/runs/" + runID
	if a.GitLab {
		path = a.project(p) + "/pipelines/" + runID
	}
	var run struct {
		ID      int64  `json:"id"`
		SHA     string `json:"sha"`
		HeadSHA string `json:"head_sha"`
	}
	_, err = r.get(path, &run)
	if err != nil {
		return ContextResult{}, err
	}
	actual := run.HeadSHA
	if a.GitLab {
		actual = run.SHA
	}
	if strconv.FormatInt(run.ID, 10) != runID || (head != "" && actual != head) {
		return ContextResult{}, ErrContextScope
	}
	key.Head = actual
	if actual == "" || key.Validate() != nil {
		return ContextResult{}, ErrContextScope
	}
	out := corehosts.JobLogs{RunID: runID, Head: actual}
	var gaps []corehosts.ContextUnavailable
	type jobWire struct {
		ID         int64  `json:"id"`
		Name       string `json:"name"`
		Stage      string `json:"stage"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
	}
	for page := 1; page <= corehosts.ContextMaxPages; page++ {
		jobPath := path + "/jobs?per_page=50&page=" + strconv.Itoa(page)
		var jobs []jobWire
		if a.GitLab {
			_, err = r.get(jobPath, &jobs)
		} else {
			var w struct {
				Jobs []jobWire `json:"jobs"`
			}
			_, err = r.get(jobPath, &w)
			jobs = w.Jobs
			if err == nil && w.Jobs == nil {
				err = errors.New("missing job list")
			}
		}
		if err != nil {
			return ContextResult{}, err
		}
		if len(jobs) > 50 {
			return ContextResult{}, errors.New("host jobs page exceeds limit")
		}
		for _, j := range jobs {
			failed := j.Status == "failed" || j.Conclusion == "failure" || j.Conclusion == "timed_out"
			if !failed {
				continue
			}
			if j.ID <= 0 {
				return ContextResult{}, errors.New("invalid job identity")
			}
			log := corehosts.JobLog{JobID: strconv.FormatInt(j.ID, 10), Name: j.Name, Stage: j.Stage}
			logPath := a.project(p) + "/actions/jobs/" + log.JobID + "/logs"
			if a.GitLab {
				logPath = a.project(p) + "/jobs/" + log.JobID + "/trace"
			}
			auth := AuthBearer
			if a.GitLab {
				auth = AuthPrivateToken
			}
			resp, e := a.Getter.GetLogBounded(r.ctx, a.Root+logPath, token, auth, r.remaining)
			body := resp.Body
			r.remaining -= resp.readBytes
			if e != nil {
				log.LogUnavailable = "Log unavailable or redirect unsupported"
				reason := corehosts.ContextUnsupported
				var fe *FetchError
				if errors.Is(e, ErrContextTooLarge) {
					reason = corehosts.ContextTooLarge
					out.Truncated = true
				}
				if errors.As(e, &fe) {
					switch fe.Kind {
					case corehosts.FailUnauthorized:
						reason = corehosts.ContextForbidden
					case corehosts.FailNotFound:
						reason = corehosts.ContextNotFound
					case corehosts.FailTransient, corehosts.FailRateLimited:
						reason = corehosts.ContextRetryExhausted
					}
				}
				gaps = append(gaps, corehosts.ContextUnavailable{Source: "job log " + log.JobID, Reason: reason})
			} else {
				log.Log = strings.ToValidUTF8(string(body), "")
			}
			out.Jobs = append(out.Jobs, log)
			if r.remaining <= 0 {
				out.Truncated = true
				return redactContext(ContextResult{Value: out, Unavailable: gaps, Expires: a.Getter.now().Add(corehosts.ContextCacheTTL)}, token), nil
			}
		}
		if len(jobs) < 50 && !r.more {
			break
		}
		if page == corehosts.ContextMaxPages {
			out.Truncated = true
		}
	}
	if !a.GitLab {
		gaps = append(gaps, corehosts.ContextUnavailable{Source: "external checks", Reason: corehosts.ContextUnsupported})
	}
	return redactContext(ContextResult{Value: out, Unavailable: gaps, Expires: a.Getter.now().Add(corehosts.ContextCacheTTL)}, token), nil
}

var _ ContextClient = ContextAPI{}

func validContextProject(p corehosts.ProjectPath) bool {
	clean, err := cleanProject(string(p))
	return err == nil && clean == p
}

// ErrContextTooLarge is a bounded read refusal, not a retryable network error.
var ErrContextTooLarge = errContext("host context exceeds the response budget")

func hasNextContextPage(header http.Header) bool {
	return header.Get("X-Next-Page") != "" || strings.Contains(header.Get("Link"), `rel="next"`) || strings.Contains(header.Get("Link"), "rel=next")
}
