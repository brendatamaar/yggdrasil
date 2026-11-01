package hoststest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// StubTime is the updated_at every stub answer carries.
var StubTime = time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)

// StubMR is a merge request in the stub.
type StubMR struct {
	Project      string
	IID          int
	Title        string
	Description  string
	SourceBranch string
	TargetBranch string
	Draft        bool
	Merged       bool
}

// StubRequest is one request the stub saw.
type StubRequest struct {
	Method  string
	Project string
	// Kind is "create_mr", "update_mr", "read_mr", "list_mrs", "pipelines" or "other".
	Kind string
	IID  int
	// Description is the description a create or update request carried.
	Description string
}

// GitLabStub is a tiny GitLab-shaped API server: merge request create, read,
// list and update, and pipeline reads. It records every request, in order.
// Answers have the shapes of internal/hosts/gitlab/testdata. It checks no
// token and holds no state beyond the merge requests and pipelines it was given.
type GitLabStub struct {
	Server *httptest.Server
	base   string

	mu        sync.Mutex
	mrs       []*StubMR
	next      map[string]int
	pipelines map[string]string // project+"\x00"+ref -> status
	reqs      []StubRequest
}

// NewGitLabStub starts the stub. webBase is the fictional host the web_url
// fields point at, such as "https://git.acme-shop.example".
func NewGitLabStub(t testing.TB, webBase string) *GitLabStub {
	t.Helper()
	s := &GitLabStub{base: strings.TrimSuffix(webBase, "/"), next: map[string]int{}, pipelines: map[string]string{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Server.Close)
	return s
}

// SetPipeline makes the latest pipeline of ref in project have status.
func (s *GitLabStub) SetPipeline(project, ref, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pipelines[project+"\x00"+ref] = status
}

// Requests returns every request seen, oldest first.
func (s *GitLabStub) Requests() []StubRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]StubRequest(nil), s.reqs...)
}

// CreatedOrder returns the projects of the create_mr requests, in order.
func (s *GitLabStub) CreatedOrder() []string {
	var out []string
	for _, r := range s.Requests() {
		if r.Kind == "create_mr" {
			out = append(out, r.Project)
		}
	}
	return out
}

// MR returns a copy of a merge request.
func (s *GitLabStub) MR(project string, iid int) (StubMR, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.mrs {
		if m.Project == project && m.IID == iid {
			return *m, true
		}
	}
	return StubMR{}, false
}

// WebURL is the web_url of a merge request.
func (s *GitLabStub) WebURL(project string, iid int) string {
	return fmt.Sprintf("%s/%s/-/merge_requests/%d", s.base, project, iid)
}

// parseRoute splits /api/v4/projects/<escaped path>/<rest...>.
func parseRoute(r *http.Request) (project string, rest []string, ok bool) {
	p := r.URL.EscapedPath()
	const pre = "/api/v4/projects/"
	if !strings.HasPrefix(p, pre) {
		return "", nil, false
	}
	parts := strings.Split(strings.TrimPrefix(p, pre), "/")
	proj, err := url.PathUnescape(parts[0])
	if err != nil || proj == "" {
		return "", nil, false
	}
	return proj, parts[1:], true
}

func (s *GitLabStub) serve(w http.ResponseWriter, r *http.Request) {
	project, rest, ok := parseRoute(r)
	if !ok {
		s.record(StubRequest{Method: r.Method, Kind: "other"})
		http.NotFound(w, r)
		return
	}
	switch {
	case len(rest) == 1 && rest[0] == "merge_requests" && r.Method == http.MethodPost:
		s.create(w, r, project)
	case len(rest) == 1 && rest[0] == "merge_requests" && r.Method == http.MethodGet:
		s.list(w, r, project)
	case len(rest) == 2 && rest[0] == "merge_requests":
		iid, err := strconv.Atoi(rest[1])
		if err != nil {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			s.read(w, project, iid)
		case http.MethodPut:
			s.update(w, r, project, iid)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	case len(rest) == 1 && rest[0] == "pipelines" && r.Method == http.MethodGet:
		s.pipelineList(w, r, project)
	default:
		s.record(StubRequest{Method: r.Method, Project: project, Kind: "other"})
		http.NotFound(w, r)
	}
}

func (s *GitLabStub) record(q StubRequest) {
	s.mu.Lock()
	s.reqs = append(s.reqs, q)
	s.mu.Unlock()
}

// fields reads a JSON or form body into a flat map.
func fields(r *http.Request) map[string]string {
	out := map[string]string{}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if strings.Contains(r.Header.Get("Content-Type"), "json") {
		var m map[string]any
		if json.Unmarshal(b, &m) == nil {
			for k, v := range m {
				switch x := v.(type) {
				case string:
					out[k] = x
				case bool:
					out[k] = strconv.FormatBool(x)
				}
			}
		}
		return out
	}
	if v, err := url.ParseQuery(string(b)); err == nil {
		for k := range v {
			out[k] = v.Get(k)
		}
	}
	return out
}

func (s *GitLabStub) create(w http.ResponseWriter, r *http.Request, project string) {
	f := fields(r)
	if f["source_branch"] == "" || f["title"] == "" {
		http.Error(w, `{"message":"source_branch and title are required"}`, http.StatusBadRequest)
		return
	}
	target := f["target_branch"]
	if target == "" {
		target = "main"
	}
	s.mu.Lock()
	s.next[project]++
	m := &StubMR{Project: project, IID: s.next[project], Title: f["title"], Description: f["description"], SourceBranch: f["source_branch"], TargetBranch: target, Draft: strings.HasPrefix(f["title"], "Draft:")}
	s.mrs = append(s.mrs, m)
	s.reqs = append(s.reqs, StubRequest{Method: r.Method, Project: project, Kind: "create_mr", IID: m.IID, Description: m.Description})
	cp := *m
	s.mu.Unlock()
	w.WriteHeader(http.StatusCreated)
	s.writeMR(w, cp)
}

func (s *GitLabStub) update(w http.ResponseWriter, r *http.Request, project string, iid int) {
	f := fields(r)
	s.mu.Lock()
	var m *StubMR
	for _, c := range s.mrs {
		if c.Project == project && c.IID == iid {
			m = c
		}
	}
	rec := StubRequest{Method: r.Method, Project: project, Kind: "update_mr", IID: iid}
	if m == nil {
		s.reqs = append(s.reqs, rec)
		s.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	if d, has := f["description"]; has {
		m.Description = d
		rec.Description = d
	}
	if t, has := f["title"]; has {
		m.Title = t
	}
	s.reqs = append(s.reqs, rec)
	cp := *m
	s.mu.Unlock()
	s.writeMR(w, cp)
}

func (s *GitLabStub) read(w http.ResponseWriter, project string, iid int) {
	s.record(StubRequest{Method: http.MethodGet, Project: project, Kind: "read_mr", IID: iid})
	m, ok := s.MR(project, iid)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"404 Not found"}`))
		return
	}
	s.writeMR(w, m)
}

func (s *GitLabStub) list(w http.ResponseWriter, r *http.Request, project string) {
	s.record(StubRequest{Method: r.Method, Project: project, Kind: "list_mrs"})
	src, state := r.URL.Query().Get("source_branch"), r.URL.Query().Get("state")
	s.mu.Lock()
	out := []any{}
	for _, m := range s.mrs {
		if m.Project != project || (src != "" && m.SourceBranch != src) {
			continue
		}
		if state == "opened" && m.Merged {
			continue
		}
		out = append(out, s.mrJSON(*m))
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (s *GitLabStub) pipelineList(w http.ResponseWriter, r *http.Request, project string) {
	s.record(StubRequest{Method: r.Method, Project: project, Kind: "pipelines"})
	ref := r.URL.Query().Get("ref")
	s.mu.Lock()
	status, has := s.pipelines[project+"\x00"+ref]
	s.mu.Unlock()
	out := []any{}
	if has {
		out = append(out, map[string]any{
			"id": 1000, "status": status, "ref": ref, "updated_at": StubTime.Format("2006-01-02T15:04:05.000Z"),
			"web_url": fmt.Sprintf("%s/%s/-/pipelines/1000", s.base, project),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (s *GitLabStub) mrJSON(m StubMR) map[string]any {
	state := "opened"
	if m.Merged {
		state = "merged"
	}
	return map[string]any{
		"id": 9000 + m.IID, "iid": m.IID, "project_id": 12, "title": m.Title, "description": m.Description,
		"state": state, "draft": m.Draft, "updated_at": StubTime.Format("2006-01-02T15:04:05.000Z"),
		"source_branch": m.SourceBranch, "target_branch": m.TargetBranch,
		"author":     map[string]any{"id": 3, "username": "mara"},
		"web_url":    s.WebURL(m.Project, m.IID),
		"references": map[string]any{"full": fmt.Sprintf("%s!%d", m.Project, m.IID)},
	}
}

func (s *GitLabStub) writeMR(w http.ResponseWriter, m StubMR) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.mrJSON(m))
}
