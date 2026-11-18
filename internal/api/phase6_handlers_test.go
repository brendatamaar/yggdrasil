package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/proposals"
	"github.com/brendatamaar/yggdrasil/internal/tasks"
)

// ---- fakes of the seven Phase 6 services ----

const fakeToken = "ghp_abcdefghijklmnopqrstuvwx1234"

// p6fakes records every call (one line each) and answers every method with
// err when it is set, otherwise with a canned response.
type p6fakes struct {
	mu    sync.Mutex
	calls []string
	err   error
	// out overrides for the bounds and redaction tests.
	preview  *ActionPreviewDTO
	failed   *FailedJobsResponse
	issues   *IssuesResponse
	issue    *IssueResponse
	mrctx    *MRContextResponse
	standup  *StandupResponse
	notices  *NoticesResponse
	noise    *NoiseResponse
	opList   []NewRepoOperationDTO
	catalog  *ActionCatalogueResponse
	publishP *PublishPreviewResponse
}

func (f *p6fakes) rec(format string, a ...any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf(format, a...))
	return f.err
}

func (f *p6fakes) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *p6fakes) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return ""
	}
	return f.calls[len(f.calls)-1]
}

func (f *p6fakes) services() Phase6Services {
	return Phase6Services{
		Actions: (*fakeP6Actions)(f), HostContext: (*fakeP6HostCtx)(f), Drafts: (*fakeP6Drafts)(f), Noise: (*fakeP6Noise)(f),
		Notices: (*fakeP6Notices)(f), Standup: (*fakeP6Standup)(f), NewRepos: (*fakeP6NewRepos)(f),
	}
}

type (
	fakeP6Actions  p6fakes
	fakeP6HostCtx  p6fakes
	fakeP6Drafts   p6fakes
	fakeP6Noise    p6fakes
	fakeP6Notices  p6fakes
	fakeP6Standup  p6fakes
	fakeP6NewRepos p6fakes
)

func (f *fakeP6Actions) Catalogue(_ context.Context, repo core.RepoID) (ActionCatalogueResponse, error) {
	if err := (*p6fakes)(f).rec("catalogue %s", repo); err != nil {
		return ActionCatalogueResponse{}, err
	}
	if f.catalog != nil {
		return *f.catalog, nil
	}
	return ActionCatalogueResponse{RepoID: repo, Offerings: []ActionOfferingDTO{{Kind: ActionDiagnoseRepo, Reason: ReasonDirty, Title: "Diagnose", Context: ContextAvailable}}}, nil
}

func (f *fakeP6Actions) Preview(_ context.Context, req ActionRequestDTO) (ActionPreviewDTO, error) {
	if err := (*p6fakes)(f).rec("preview %s %s %s", req.RepoID, req.Kind, req.SourceID); err != nil {
		return ActionPreviewDTO{}, err
	}
	if f.preview != nil {
		return *f.preview, nil
	}
	return ActionPreviewDTO{RequestID: req.RequestID, Digest: strings.Repeat("a", 64), Kind: req.Kind, Source: "repo status", ContextText: "ctx", Context: ContextAvailable}, nil
}

func (f *fakeP6Actions) Execute(_ context.Context, req ActionExecuteRequest) (ActionResultDTO, error) {
	if err := (*p6fakes)(f).rec("execute %s %s", req.RequestID, req.Digest[:4]); err != nil {
		return ActionResultDTO{}, err
	}
	return ActionResultDTO{RequestID: req.RequestID, State: ActionStateCompleted, TaskID: "task_1", Message: "Started."}, nil
}

func (f *fakeP6Actions) Get(_ context.Context, id core.RequestID) (ActionResultDTO, error) {
	if err := (*p6fakes)(f).rec("get %s", id); err != nil {
		return ActionResultDTO{}, err
	}
	return ActionResultDTO{RequestID: id, State: ActionStateCompleted, Message: "Done."}, nil
}

func (f *fakeP6HostCtx) FailedJobs(_ context.Context, repo core.RepoID, run string) (FailedJobsResponse, error) {
	if err := (*p6fakes)(f).rec("failedjobs %s %s", repo, run); err != nil {
		return FailedJobsResponse{}, err
	}
	if f.failed != nil {
		return *f.failed, nil
	}
	return FailedJobsResponse{RepoID: repo, RunID: run, Jobs: []FailedJobDTO{{JobID: "1", Name: "test", Log: "boom"}}, Status: HostContextStatusDTO{Phase: HostContextReady}}, nil
}

func (f *fakeP6HostCtx) Issues(_ context.Context, repo core.RepoID) (IssuesResponse, error) {
	if err := (*p6fakes)(f).rec("issues %s", repo); err != nil {
		return IssuesResponse{}, err
	}
	if f.issues != nil {
		return *f.issues, nil
	}
	return IssuesResponse{RepoID: repo, Issues: []IssueDTO{{Number: 7, Title: "Cart total wrong"}}, Status: HostContextStatusDTO{Phase: HostContextReady}}, nil
}

func (f *fakeP6HostCtx) Issue(_ context.Context, repo core.RepoID, n int) (IssueResponse, error) {
	if err := (*p6fakes)(f).rec("issue %s %d", repo, n); err != nil {
		return IssueResponse{}, err
	}
	if f.issue != nil {
		return *f.issue, nil
	}
	return IssueResponse{RepoID: repo, Issue: &IssueDTO{Number: n, Title: "Cart total wrong", Body: "steps"}, Status: HostContextStatusDTO{Phase: HostContextReady}}, nil
}

func (f *fakeP6HostCtx) MRContext(_ context.Context, repo core.RepoID, n int) (MRContextResponse, error) {
	if err := (*p6fakes)(f).rec("mrcontext %s %d", repo, n); err != nil {
		return MRContextResponse{}, err
	}
	if f.mrctx != nil {
		return *f.mrctx, nil
	}
	return MRContextResponse{RepoID: repo, Head: strings.Repeat("b", 40), Diff: "diff", Status: HostContextStatusDTO{Phase: HostContextReady}}, nil
}

func (f *fakeP6Drafts) ImportIssueDraft(_ context.Context, repo core.RepoID, n int, req ImportIssueDraftRequest) (DraftTaskResponse, error) {
	if err := (*p6fakes)(f).rec("import %s %d %s", repo, n, req.Agent); err != nil {
		return DraftTaskResponse{}, err
	}
	return DraftTaskResponse{Task: core.Task{ID: "task_d1", Title: "Issue 7"}, Created: true}, nil
}

func (f *fakeP6Drafts) StartDraft(_ context.Context, id core.TaskID, req StartDraftRequest) (TaskResponse, error) {
	if err := (*p6fakes)(f).rec("start %s %s", id, req.Mode); err != nil {
		return TaskResponse{}, err
	}
	return TaskResponse{Task: core.Task{ID: id, Title: "Issue 7"}}, nil
}

func (f *fakeP6Noise) Noise(_ context.Context, repo core.RepoID) (NoiseResponse, error) {
	if err := (*p6fakes)(f).rec("noise %s", repo); err != nil {
		return NoiseResponse{}, err
	}
	if f.noise != nil {
		return *f.noise, nil
	}
	return NoiseResponse{RepoID: repo, Available: true, Candidates: []NoiseCandidateDTO{{Directory: "build", Pattern: "/build/", SampleCount: 3}}}, nil
}

func (f *fakeP6Noise) CreateIgnoreProposal(_ context.Context, repo core.RepoID, req CreateIgnoreProposalRequest) (ProposalDTO, error) {
	if err := (*p6fakes)(f).rec("ignore %s %v", repo, req.Entries); err != nil {
		return ProposalDTO{}, err
	}
	return ProposalDTO{ID: "prop_1", RepoID: repo, Kind: core.ProposalIgnoreEntries, Status: core.ProposalPending, Source: "user", Summary: "Ignore build", Lines: []string{"add /build/"}}, nil
}

func (f *fakeP6Notices) Notices(_ context.Context, after int64, limit int) (NoticesResponse, error) {
	if err := (*p6fakes)(f).rec("notices %d %d", after, limit); err != nil {
		return NoticesResponse{}, err
	}
	if f.notices != nil {
		return *f.notices, nil
	}
	return NoticesResponse{Notices: []NoticeDTO{{ID: "ntc_1", TaskID: "task_1", TaskTitle: "Fix cart", Status: NoticeDone, Cursor: 5}}, Oldest: 5, Newest: 5}, nil
}

func (f *fakeP6Standup) Get(_ context.Context, p core.ProjectID, from, to time.Time) (StandupResponse, error) {
	if err := (*p6fakes)(f).rec("standup %s %s %s", p, from.Format(time.RFC3339), to.Format(time.RFC3339)); err != nil {
		return StandupResponse{}, err
	}
	if f.standup != nil {
		return *f.standup, nil
	}
	return StandupResponse{ProjectID: p, From: from, To: to, Phase: StandupReady, Text: "Yesterday: nothing."}, nil
}

func (f *fakeP6Standup) Polish(_ context.Context, p core.ProjectID, req StandupPolishRequest) (ActionResultDTO, error) {
	if err := (*p6fakes)(f).rec("polish %s %s", p, req.Agent); err != nil {
		return ActionResultDTO{}, err
	}
	return ActionResultDTO{RequestID: req.RequestID, State: ActionStateCompleted, TaskID: "task_p", Message: "Started."}, nil
}

func op6(id core.OperationID, st NewRepoState) NewRepoOperationDTO {
	return NewRepoOperationDTO{OperationID: id, ProjectID: "proj_1", RepoID: "repo_n", SetupTaskID: "task_s", Leaf: "shop-new", State: st}
}

func (f *fakeP6NewRepos) Preview(_ context.Context, p core.ProjectID, req NewRepoPreviewRequest) (NewRepoPreviewResponse, error) {
	if err := (*p6fakes)(f).rec("nr-preview %s %s", p, req.Leaf); err != nil {
		return NewRepoPreviewResponse{}, err
	}
	return NewRepoPreviewResponse{Spec: NewRepoSpecDTO{Leaf: req.Leaf, Branch: "main"}, Hash: strings.Repeat("c", 64), Lines: []string{"Create shop-new"}}, nil
}

func (f *fakeP6NewRepos) Reserve(_ context.Context, p core.ProjectID, req ReserveNewRepoRequest) (NewRepoOperationDTO, error) {
	if err := (*p6fakes)(f).rec("nr-reserve %s %s", p, req.Leaf); err != nil {
		return NewRepoOperationDTO{}, err
	}
	o := op6("op_1", NewRepoAwaitingConfirm)
	o.ProposalID = "prop_9"
	return o, nil
}

func (f *fakeP6NewRepos) Get(_ context.Context, id core.OperationID) (NewRepoOperationDTO, error) {
	if err := (*p6fakes)(f).rec("nr-get %s", id); err != nil {
		return NewRepoOperationDTO{}, err
	}
	return op6(id, NewRepoReady), nil
}

func (f *fakeP6NewRepos) List(_ context.Context, p core.ProjectID) ([]NewRepoOperationDTO, error) {
	if err := (*p6fakes)(f).rec("nr-list %s", p); err != nil {
		return nil, err
	}
	if f.opList != nil {
		return f.opList, nil
	}
	return []NewRepoOperationDTO{op6("op_1", NewRepoReady)}, nil
}

func (f *fakeP6NewRepos) PublishPreview(_ context.Context, id core.OperationID, req PublishPreviewRequest) (PublishPreviewResponse, error) {
	if err := (*p6fakes)(f).rec("nr-pubpreview %s %s", id, req.Owner); err != nil {
		return PublishPreviewResponse{}, err
	}
	if f.publishP != nil {
		return *f.publishP, nil
	}
	return PublishPreviewResponse{Hash: strings.Repeat("d", 64), Lines: []string{"Publish to acme"}, Visibility: VisibilityPrivate, ExpectedHead: strings.Repeat("e", 40)}, nil
}

func (f *fakeP6NewRepos) Publish(_ context.Context, id core.OperationID, req PublishNewRepoRequest) (ProposalDTO, error) {
	if err := (*p6fakes)(f).rec("nr-publish %s %s", id, req.Owner); err != nil {
		return ProposalDTO{}, err
	}
	return ProposalDTO{ID: "prop_10", Kind: core.ProposalPublish, Status: core.ProposalPending, Source: "user", Lines: []string{"publish"}}, nil
}

func (f *fakeP6NewRepos) Retry(_ context.Context, id core.OperationID, hash string) (NewRepoOperationDTO, error) {
	if err := (*p6fakes)(f).rec("nr-retry %s %s", id, hash[:4]); err != nil {
		return NewRepoOperationDTO{}, err
	}
	return op6(id, NewRepoInitializing), nil
}

// ---- the route table ----

type p6route struct {
	name   string // the Route constant
	method string
	path   string // a concrete path
	body   any    // a valid body, nil for a GET
	want   string // a field the happy response must carry
	call   string // the prefix of the fake call it must make
}

func hex64(c string) string { return strings.Repeat(c, 64) }

func p6routes(e *env) []p6route {
	repo, proj := string(e.repo.ID), string(e.project.ID)
	execBody := ActionExecuteRequest{
		ActionRequestDTO: ActionRequestDTO{RequestID: "req_1", RepoID: e.repo.ID, Kind: ActionDiagnoseRepo, Agent: "claude"},
		Digest:           hex64("a"),
	}
	rg := "?from=2026-03-01T00:00:00Z&to=2026-03-02T00:00:00Z"
	return []p6route{
		{"RouteRepoActions", "GET", "/api/repos/" + repo + "/actions", nil, `"offerings"`, "catalogue"},
		{"RouteActionPreview", "POST", "/api/actions/preview", execBody.ActionRequestDTO, `"digest"`, "preview"},
		{"RouteActionExecute", "POST", "/api/actions/execute", execBody, `"completed"`, "execute"},
		{"RouteActionGet", "GET", "/api/actions/req_1", nil, `"Done."`, "get"},
		{"RouteFailedJobs", "GET", "/api/repos/" + repo + "/ci/runs/1234/failed-jobs", nil, `"jobs"`, "failedjobs"},
		{"RouteIssues", "GET", "/api/repos/" + repo + "/issues", nil, `"issues"`, "issues"},
		{"RouteIssue", "GET", "/api/repos/" + repo + "/issues/7", nil, `"issue"`, "issue "},
		{"RouteMRContext", "GET", "/api/repos/" + repo + "/mrs/12/context", nil, `"diff"`, "mrcontext"},
		{"RouteImportIssueDraft", "POST", "/api/repos/" + repo + "/issues/7/draft", ImportIssueDraftRequest{RequestID: "req_2", Agent: "claude"}, `"created":true`, "import"},
		{"RouteStartDraft", "POST", "/api/tasks/task_d1/start", StartDraftRequest{Mode: core.ModeAsk}, `"task"`, "start"},
		{"RouteNoise", "GET", "/api/repos/" + repo + "/noise", nil, `"candidates"`, "noise"},
		{"RouteNoiseProposal", "POST", "/api/repos/" + repo + "/noise/proposals", CreateIgnoreProposalRequest{RequestID: "req_3", Entries: []string{"/build/"}}, `"prop_1"`, "ignore"},
		{"RouteNotices", "GET", "/api/notifications?after=3&limit=10", nil, `"notices"`, "notices 3 10"},
		{"RouteStandup", "GET", "/api/projects/" + proj + "/standup" + rg, nil, `"text"`, "standup"},
		{"RouteStandupPolish", "POST", "/api/projects/" + proj + "/standup/polish", StandupPolishRequest{
			RequestID: "req_4", From: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC), Agent: "claude"}, `"task_p"`, "polish"},
		{"RouteNewRepoPreview", "POST", "/api/projects/" + proj + "/new-repos/preview", NewRepoPreviewRequest{Leaf: "shop-new"}, `"hash"`, "nr-preview"},
		{"RouteNewRepoReserve", "POST", "/api/projects/" + proj + "/new-repos", ReserveNewRepoRequest{
			RequestID: "req_5", NewRepoPreviewRequest: NewRepoPreviewRequest{Leaf: "shop-new"}, Hash: hex64("c")}, `"awaiting_confirm"`, "nr-reserve"},
		{"RouteNewRepoGet", "GET", "/api/new-repos/op_1", nil, `"ready"`, "nr-get"},
		{"RouteNewRepoList", "GET", "/api/projects/" + proj + "/new-repos", nil, `"operationId"`, "nr-list"},
		{"RoutePublishPreview", "POST", "/api/new-repos/op_1/publish/preview", PublishPreviewRequest{HostInstance: "gitlab", Owner: "acme", OwnerKind: OwnerOrg, Name: "shop-new"}, `"hash"`, "nr-pubpreview"},
		{"RoutePublishNewRepo", "POST", "/api/new-repos/op_1/publish", PublishNewRepoRequest{
			RequestID: "req_6", PublishPreviewRequest: PublishPreviewRequest{HostInstance: "gitlab", Owner: "acme", OwnerKind: OwnerOrg, Name: "shop-new"}, Hash: hex64("d")}, `"prop_10"`, "nr-publish"},
		{"RouteNewRepoRetry", "POST", "/api/new-repos/op_1/retry", RetryNewRepoRequest{Hash: hex64("c")}, `"initializing"`, "nr-retry"},
	}
}

func p6env(t *testing.T, f *p6fakes, extra ...func(*Deps)) *env {
	t.Helper()
	return newEnv(t, append([]func(*Deps){func(d *Deps) {
		if f != nil {
			d.Phase6 = f.services()
		}
	}}, extra...)...)
}

// phase6RouteConsts returns the Route* constants declared in phase6.go, so a
// new constant cannot be forgotten by the handler table.
func phase6RouteConsts(t *testing.T) map[string]string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "phase6.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, d := range file.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST {
			continue
		}
		for _, sp := range g.Specs {
			vs := sp.(*ast.ValueSpec)
			for i, n := range vs.Names {
				if strings.HasPrefix(n.Name, "Route") {
					lit := vs.Values[i].(*ast.BasicLit)
					out[n.Name] = strings.Trim(lit.Value, `"`)
				}
			}
		}
	}
	return out
}

// Every Route constant of phase6.go has a handler: with no service bound each
// answers 501, never the 404 of the catch-all; and the table above names it.
func TestPhase6EveryRouteIsRegistered(t *testing.T) {
	consts := phase6RouteConsts(t)
	if len(consts) < 22 {
		t.Fatalf("found %d Route constants, expected at least 22", len(consts))
	}
	e := p6env(t, nil)
	table := map[string]p6route{}
	for _, rt := range p6routes(e) {
		table[rt.name] = rt
	}
	for name, pattern := range consts {
		rt, ok := table[name]
		if !ok {
			t.Errorf("%s (%s) has no row in the test table", name, pattern)
			continue
		}
		if !strings.HasPrefix(pattern, rt.method+" ") {
			t.Errorf("%s: pattern %q does not use method %s", name, pattern, rt.method)
		}
		resp, data := e.do(t, rt.method, rt.path, rt.body)
		if resp.StatusCode != http.StatusNotImplemented {
			t.Errorf("%s unbound: status %d, want 501: %s", name, resp.StatusCode, data)
		}
		var eb ErrorBody
		if err := json.Unmarshal(data, &eb); err != nil || eb.Code != ErrUnavailable || eb.Message == "" {
			t.Errorf("%s unbound: body %s is not a plain unavailable message", name, data)
		}
	}
	if len(table) != len(consts) {
		t.Errorf("the table has %d rows for %d constants", len(table), len(consts))
	}
}

func TestPhase6HappyPaths(t *testing.T) {
	f := &p6fakes{}
	e := p6env(t, f)
	for _, rt := range p6routes(e) {
		before := f.count()
		resp, data := e.do(t, rt.method, rt.path, rt.body)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status %d: %s", rt.name, resp.StatusCode, data)
			continue
		}
		if !strings.Contains(string(data), rt.want) {
			t.Errorf("%s: body %s lacks %s", rt.name, data, rt.want)
		}
		if f.count() != before+1 || !strings.HasPrefix(f.last(), rt.call) {
			t.Errorf("%s: call %q, want prefix %q", rt.name, f.last(), rt.call)
		}
		if resp.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("%s: Cache-Control = %q", rt.name, resp.Header.Get("Cache-Control"))
		}
	}
}

// A list response is a JSON array; an empty one is [] and never null.
func TestPhase6EmptyListsAreArrays(t *testing.T) {
	f := &p6fakes{opList: []NewRepoOperationDTO{}, catalog: &ActionCatalogueResponse{RepoID: "x"}, noise: &NoiseResponse{}, notices: &NoticesResponse{}, standup: &StandupResponse{}, issues: &IssuesResponse{}, failed: &FailedJobsResponse{}}
	e := p6env(t, f)
	for _, c := range []struct{ path, field string }{
		{"/api/projects/" + string(e.project.ID) + "/new-repos", ""},
		{"/api/repos/" + string(e.repo.ID) + "/actions", `"offerings":[]`},
		{"/api/repos/" + string(e.repo.ID) + "/noise", `"candidates":[]`},
		{"/api/notifications", `"notices":[]`},
		{"/api/repos/" + string(e.repo.ID) + "/issues", `"issues":[]`},
		{"/api/repos/" + string(e.repo.ID) + "/ci/runs/9/failed-jobs", `"jobs":[]`},
		{"/api/projects/" + string(e.project.ID) + "/standup?from=2026-03-01T00:00:00Z&to=2026-03-02T00:00:00Z", `"commits":[]`},
	} {
		_, data := e.do(t, "GET", c.path, nil)
		got := strings.ReplaceAll(string(data), " ", "")
		if c.field == "" {
			if strings.TrimSpace(got) != "[]" {
				t.Errorf("%s = %s, want []", c.path, data)
			}
		} else if !strings.Contains(got, c.field) {
			t.Errorf("%s = %s, want %s", c.path, data, c.field)
		}
	}
}

func TestPhase6UnboundServiceAnswers501ForEachArea(t *testing.T) {
	// Only one service bound: the others still answer 501.
	f := &p6fakes{}
	s := f.services()
	e := p6env(t, nil, func(d *Deps) { d.Phase6 = Phase6Services{Notices: s.Notices} })
	for _, rt := range p6routes(e) {
		resp, _ := e.do(t, rt.method, rt.path, rt.body)
		want := http.StatusNotImplemented
		if rt.name == "RouteNotices" {
			want = http.StatusOK
		}
		if resp.StatusCode != want {
			t.Errorf("%s: status %d, want %d", rt.name, resp.StatusCode, want)
		}
	}
	// Binding later (SetPhase6Services) takes effect without a restart.
	e.srv.SetPhase6Services(f.services())
	if resp, _ := e.do(t, "GET", "/api/actions/req_1", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("after binding: status %d", resp.StatusCode)
	}
}

// ---- boundary rejections ----

func TestPhase6BoundaryRejections(t *testing.T) {
	f := &p6fakes{}
	e := p6env(t, f)
	repo, proj := string(e.repo.ID), string(e.project.ID)
	okExec := ActionExecuteRequest{
		ActionRequestDTO: ActionRequestDTO{RequestID: "req_1", RepoID: e.repo.ID, Kind: ActionDiagnoseRepo, Agent: "claude"}, Digest: hex64("a"),
	}
	with := func(mut func(*ActionExecuteRequest)) ActionExecuteRequest { c := okExec; mut(&c); return c }
	okPub := PublishPreviewRequest{HostInstance: "gitlab", Owner: "acme", OwnerKind: OwnerOrg, Name: "shop-new"}
	withPub := func(mut func(*PublishPreviewRequest)) PublishPreviewRequest { c := okPub; mut(&c); return c }
	okNew := NewRepoPreviewRequest{Leaf: "shop-new"}
	withNew := func(mut func(*NewRepoPreviewRequest)) NewRepoPreviewRequest { c := okNew; mut(&c); return c }
	pol := func(mut func(*StandupPolishRequest)) StandupPolishRequest {
		c := StandupPolishRequest{RequestID: "req_4", From: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC), Agent: "claude"}
		mut(&c)
		return c
	}
	big := strings.Repeat("x", maxBody+10)
	rg := "?from=2026-03-01T00:00:00Z&to=2026-03-02T00:00:00Z"
	cases := []struct {
		name, method, path string
		body               any
		raw                string
	}{
		// path IDs
		{"repo id with a slash", "GET", "/api/repos/a%2Fb/actions", nil, ""},
		{"repo id with a space", "GET", "/api/repos/a%20b/noise", nil, ""},
		{"project id with a slash", "GET", "/api/projects/a%2Fb/standup" + rg, nil, ""},
		{"project id on new repos", "GET", "/api/projects/a%5Cb/new-repos", nil, ""},
		{"request id with a slash", "GET", "/api/actions/a%2Fb", nil, ""},
		{"request id too long", "GET", "/api/actions/" + strings.Repeat("r", 101), nil, ""},
		{"operation id with a slash", "GET", "/api/new-repos/a%2Fb", nil, ""},
		{"task id with a slash", "POST", "/api/tasks/a%2Fb/start", StartDraftRequest{Mode: core.ModeAsk}, ""},
		// numbers and run IDs
		{"issue number zero", "GET", "/api/repos/" + repo + "/issues/0", nil, ""},
		{"issue number negative", "GET", "/api/repos/" + repo + "/issues/-1", nil, ""},
		{"issue number text", "GET", "/api/repos/" + repo + "/issues/abc", nil, ""},
		{"issue number with a plus", "GET", "/api/repos/" + repo + "/issues/%2B1", nil, ""},
		{"issue number leading zero", "GET", "/api/repos/" + repo + "/issues/07", nil, ""},
		{"issue number too large", "GET", "/api/repos/" + repo + "/issues/9999999999", nil, ""},
		{"mr number text", "GET", "/api/repos/" + repo + "/mrs/x/context", nil, ""},
		{"draft number zero", "POST", "/api/repos/" + repo + "/issues/0/draft", ImportIssueDraftRequest{RequestID: "r", Agent: "claude"}, ""},
		{"run id a url", "GET", "/api/repos/" + repo + "/ci/runs/https:%2F%2Fevil.test%2F1/failed-jobs", nil, ""},
		{"run id text", "GET", "/api/repos/" + repo + "/ci/runs/abc/failed-jobs", nil, ""},
		{"run id too long", "GET", "/api/repos/" + repo + "/ci/runs/" + strings.Repeat("1", 40) + "/failed-jobs", nil, ""},
		// action bodies
		{"preview unknown kind", "POST", "/api/actions/preview", ActionRequestDTO{RequestID: "r", RepoID: e.repo.ID, Kind: "rm_rf", Agent: "claude"}, ""},
		{"preview url source", "POST", "/api/actions/preview", ActionRequestDTO{RequestID: "r", RepoID: e.repo.ID, Kind: ActionDiagnosePipeline, SourceID: "https://evil.test/1", Agent: "claude"}, ""},
		{"preview empty request id", "POST", "/api/actions/preview", ActionRequestDTO{RepoID: e.repo.ID, Kind: ActionDiagnoseRepo, Agent: "claude"}, ""},
		{"preview bad repo id", "POST", "/api/actions/preview", ActionRequestDTO{RequestID: "r", RepoID: "a b", Kind: ActionDiagnoseRepo, Agent: "claude"}, ""},
		{"preview unknown field", "POST", "/api/actions/preview", nil, `{"requestId":"r","repoId":"x","kind":"diagnose_repo","agent":"c","path":"C:\\x"}`},
		{"preview not json", "POST", "/api/actions/preview", nil, `nope`},
		{"preview two objects", "POST", "/api/actions/preview", nil, `{} {}`},
		{"preview oversized body", "POST", "/api/actions/preview", nil, `{"requestId":"` + big + `"}`},
		{"execute bad digest", "POST", "/api/actions/execute", with(func(r *ActionExecuteRequest) { r.Digest = "xyz" }), ""},
		{"execute uppercase digest", "POST", "/api/actions/execute", with(func(r *ActionExecuteRequest) { r.Digest = hex64("A") }), ""},
		{"execute bad head", "POST", "/api/actions/execute", with(func(r *ActionExecuteRequest) { r.ExpectedHead = "main" }), ""},
		{"execute unknown kind", "POST", "/api/actions/execute", with(func(r *ActionExecuteRequest) { r.Kind = "nope" }), ""},
		{"execute missing agent", "POST", "/api/actions/execute", with(func(r *ActionExecuteRequest) { r.Agent = "" }), ""},
		// Valid in every other way: only the size is wrong, so only the body bound refuses it.
		{"execute valid but padded past the body bound", "POST", "/api/actions/execute", nil, `{"requestId":"r","repoId":"` + string(e.repo.ID) + `","kind":"diagnose_repo","agent":"claude","digest":"` + hex64("a") + `"}` + strings.Repeat(" ", maxBody+10)},
		{"retry valid but padded past the body bound", "POST", "/api/new-repos/op_1/retry", nil, `{"hash":"` + hex64("c") + `"}` + strings.Repeat(" ", maxBody+10)},
		{"execute oversized body", "POST", "/api/actions/execute", nil, `{"digest":"` + big + `"}`},
		// drafts
		{"draft missing agent", "POST", "/api/repos/" + repo + "/issues/7/draft", ImportIssueDraftRequest{RequestID: "r"}, ""},
		{"draft agent with a path", "POST", "/api/repos/" + repo + "/issues/7/draft", ImportIssueDraftRequest{RequestID: "r", Agent: "../claude"}, ""},
		{"draft model control char", "POST", "/api/repos/" + repo + "/issues/7/draft", ImportIssueDraftRequest{RequestID: "r", Agent: "claude", Model: "a\nb"}, ""},
		{"draft model too long", "POST", "/api/repos/" + repo + "/issues/7/draft", ImportIssueDraftRequest{RequestID: "r", Agent: "claude", Model: strings.Repeat("m", 101)}, ""},
		{"draft request id missing", "POST", "/api/repos/" + repo + "/issues/7/draft", ImportIssueDraftRequest{Agent: "claude"}, ""},
		{"start unknown mode", "POST", "/api/tasks/task_d1/start", StartDraftRequest{Mode: "root"}, ""},
		{"start empty body", "POST", "/api/tasks/task_d1/start", nil, ""},
		{"start code mode while write modes are off", "POST", "/api/tasks/task_d1/start", StartDraftRequest{Mode: core.ModeCode}, ""},
		// noise
		{"ignore no entries", "POST", "/api/repos/" + repo + "/noise/proposals", CreateIgnoreProposalRequest{RequestID: "r"}, ""},
		{"ignore too many", "POST", "/api/repos/" + repo + "/noise/proposals", CreateIgnoreProposalRequest{RequestID: "r", Entries: manyEntries(33)}, ""},
		{"ignore traversal", "POST", "/api/repos/" + repo + "/noise/proposals", CreateIgnoreProposalRequest{RequestID: "r", Entries: []string{"/../x/"}}, ""},
		{"ignore absolute", "POST", "/api/repos/" + repo + "/noise/proposals", CreateIgnoreProposalRequest{RequestID: "r", Entries: []string{"C:/x/"}}, ""},
		{"ignore newline", "POST", "/api/repos/" + repo + "/noise/proposals", CreateIgnoreProposalRequest{RequestID: "r", Entries: []string{"/a\nb/"}}, ""},
		{"ignore wildcard", "POST", "/api/repos/" + repo + "/noise/proposals", CreateIgnoreProposalRequest{RequestID: "r", Entries: []string{"/*/"}}, ""},
		{"ignore duplicate", "POST", "/api/repos/" + repo + "/noise/proposals", CreateIgnoreProposalRequest{RequestID: "r", Entries: []string{"/a/", "/a/"}}, ""},
		{"ignore too long together", "POST", "/api/repos/" + repo + "/noise/proposals", CreateIgnoreProposalRequest{RequestID: "r", Entries: longEntries()}, ""},
		// notices
		{"notices negative cursor", "GET", "/api/notifications?after=-1", nil, ""},
		{"notices text cursor", "GET", "/api/notifications?after=abc", nil, ""},
		{"notices huge cursor", "GET", "/api/notifications?after=" + strings.Repeat("9", 30), nil, ""},
		{"notices limit zero", "GET", "/api/notifications?limit=0", nil, ""},
		{"notices limit over 200", "GET", "/api/notifications?limit=201", nil, ""},
		{"notices limit text", "GET", "/api/notifications?limit=ten", nil, ""},
		// standup
		{"standup no range", "GET", "/api/projects/" + proj + "/standup", nil, ""},
		{"standup only from", "GET", "/api/projects/" + proj + "/standup?from=2026-03-01T00:00:00Z", nil, ""},
		{"standup not a time", "GET", "/api/projects/" + proj + "/standup?from=yesterday&to=today", nil, ""},
		{"standup reversed", "GET", "/api/projects/" + proj + "/standup?from=2026-03-02T00:00:00Z&to=2026-03-01T00:00:00Z", nil, ""},
		{"standup too long", "GET", "/api/projects/" + proj + "/standup?from=2026-01-01T00:00:00Z&to=2026-03-01T00:00:00Z", nil, ""},
		{"standup oversized value", "GET", "/api/projects/" + proj + "/standup?from=" + strings.Repeat("2", 200) + "&to=x", nil, ""},
		{"polish reversed", "POST", "/api/projects/" + proj + "/standup/polish", pol(func(r *StandupPolishRequest) { r.From, r.To = r.To, r.From }), ""},
		{"polish too long", "POST", "/api/projects/" + proj + "/standup/polish", pol(func(r *StandupPolishRequest) { r.To = r.From.AddDate(0, 0, 40) }), ""},
		{"polish no agent", "POST", "/api/projects/" + proj + "/standup/polish", pol(func(r *StandupPolishRequest) { r.Agent = "" }), ""},
		{"polish no request id", "POST", "/api/projects/" + proj + "/standup/polish", pol(func(r *StandupPolishRequest) { r.RequestID = "" }), ""},
		{"polish bad time", "POST", "/api/projects/" + proj + "/standup/polish", nil, `{"requestId":"r","from":"x","to":"y","agent":"a"}`},
		// new repos
		{"preview leaf with a separator", "POST", "/api/projects/" + proj + "/new-repos/preview", withNew(func(r *NewRepoPreviewRequest) { r.Leaf = "a/b" }), ""},
		{"preview leaf traversal", "POST", "/api/projects/" + proj + "/new-repos/preview", withNew(func(r *NewRepoPreviewRequest) { r.Leaf = ".." }), ""},
		{"preview leaf drive", "POST", "/api/projects/" + proj + "/new-repos/preview", withNew(func(r *NewRepoPreviewRequest) { r.Leaf = "C:evil" }), ""},
		{"preview leaf device name", "POST", "/api/projects/" + proj + "/new-repos/preview", withNew(func(r *NewRepoPreviewRequest) { r.Leaf = "CON" }), ""},
		{"preview leaf empty", "POST", "/api/projects/" + proj + "/new-repos/preview", withNew(func(r *NewRepoPreviewRequest) { r.Leaf = "" }), ""},
		{"preview readme too big", "POST", "/api/projects/" + proj + "/new-repos/preview", withNew(func(r *NewRepoPreviewRequest) { r.README = strings.Repeat("r", 65<<10) }), ""},
		{"preview NUL in ignore", "POST", "/api/projects/" + proj + "/new-repos/preview", withNew(func(r *NewRepoPreviewRequest) { r.Ignore = "a\x00b" }), ""},
		{"preview message too long", "POST", "/api/projects/" + proj + "/new-repos/preview", withNew(func(r *NewRepoPreviewRequest) { r.CommitMessage = strings.Repeat("m", 5<<10) }), ""},
		{"reserve bad hash", "POST", "/api/projects/" + proj + "/new-repos", ReserveNewRepoRequest{RequestID: "r", NewRepoPreviewRequest: okNew, Hash: "short"}, ""},
		{"reserve no request id", "POST", "/api/projects/" + proj + "/new-repos", ReserveNewRepoRequest{NewRepoPreviewRequest: okNew, Hash: hex64("c")}, ""},
		{"reserve leaf with a path", "POST", "/api/projects/" + proj + "/new-repos", ReserveNewRepoRequest{RequestID: "r", NewRepoPreviewRequest: NewRepoPreviewRequest{Leaf: `..\x`}, Hash: hex64("c")}, ""},
		{"reserve agent with a path", "POST", "/api/projects/" + proj + "/new-repos", ReserveNewRepoRequest{RequestID: "r", NewRepoPreviewRequest: okNew, Hash: hex64("c"), Agent: "/bin/sh"}, ""},
		{"publish preview host with a scheme", "POST", "/api/new-repos/op_1/publish/preview", withPub(func(r *PublishPreviewRequest) { r.HostInstance = "https://evil.test" }), ""},
		{"publish preview uppercase host", "POST", "/api/new-repos/op_1/publish/preview", withPub(func(r *PublishPreviewRequest) { r.HostInstance = "GitLab" }), ""},
		{"publish preview owner with a slash", "POST", "/api/new-repos/op_1/publish/preview", withPub(func(r *PublishPreviewRequest) { r.Owner = "a/b" }), ""},
		{"publish preview owner empty", "POST", "/api/new-repos/op_1/publish/preview", withPub(func(r *PublishPreviewRequest) { r.Owner = "" }), ""},
		{"publish preview name ends in .git", "POST", "/api/new-repos/op_1/publish/preview", withPub(func(r *PublishPreviewRequest) { r.Name = "x.git" }), ""},
		{"publish preview bad owner kind", "POST", "/api/new-repos/op_1/publish/preview", withPub(func(r *PublishPreviewRequest) { r.OwnerKind = "team" }), ""},
		{"publish preview bad visibility", "POST", "/api/new-repos/op_1/publish/preview", withPub(func(r *PublishPreviewRequest) { r.Visibility = "internal" }), ""},
		{"publish bad hash", "POST", "/api/new-repos/op_1/publish", PublishNewRepoRequest{RequestID: "r", PublishPreviewRequest: okPub, Hash: "zz"}, ""},
		{"publish no request id", "POST", "/api/new-repos/op_1/publish", PublishNewRepoRequest{PublishPreviewRequest: okPub, Hash: hex64("d")}, ""},
		{"retry bad hash", "POST", "/api/new-repos/op_1/retry", RetryNewRepoRequest{Hash: "nope"}, ""},
		{"retry empty body", "POST", "/api/new-repos/op_1/retry", nil, ""},
		{"retry unknown field", "POST", "/api/new-repos/op_1/retry", nil, `{"hash":"` + hex64("c") + `","force":true}`},
	}
	for _, c := range cases {
		before := f.count()
		var resp reply
		var data []byte
		if c.raw != "" {
			resp, data = e.doRaw(t, c.method, c.path, c.raw)
		} else {
			resp, data = e.do(t, c.method, c.path, c.body)
		}
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400: %s", c.name, resp.StatusCode, data)
		}
		var eb ErrorBody
		if json.Unmarshal(data, &eb) != nil || eb.Code != ErrBadRequest || eb.Message == "" {
			t.Errorf("%s: body %s is not a plain bad request", c.name, data)
		}
		if f.count() != before {
			t.Errorf("%s reached the service: %s", c.name, f.last())
		}
	}
}

func manyEntries(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("/dir%d/", i)
	}
	return out
}

func longEntries() []string {
	out := make([]string, 10)
	for i := range out {
		out[i] = "/" + strings.Repeat(string(rune('a'+i)), 500) + "/"
	}
	return out
}

// An accepted request reaches the service exactly as validated: a number or ID
// with surprising spellings never changes which object is read.
func TestPhase6AcceptedPathValuesReachTheService(t *testing.T) {
	f := &p6fakes{}
	e := p6env(t, f)
	repo := string(e.repo.ID)
	e.do(t, "GET", "/api/repos/"+repo+"/issues/1234567", nil)
	if f.last() != "issue "+repo+" 1234567" {
		t.Errorf("call = %q", f.last())
	}
	e.do(t, "GET", "/api/repos/"+repo+"/ci/runs/987654321012/failed-jobs", nil)
	if f.last() != "failedjobs "+repo+" 987654321012" {
		t.Errorf("call = %q", f.last())
	}
	// No limit: the default page. A limit of 200 is the largest accepted.
	e.do(t, "GET", "/api/notifications", nil)
	if f.last() != "notices 0 50" {
		t.Errorf("call = %q", f.last())
	}
	if resp, _ := e.do(t, "GET", "/api/notifications?limit=200", nil); resp.StatusCode != 200 || f.last() != "notices 0 200" {
		t.Errorf("limit 200: %d %q", resp.StatusCode, f.last())
	}
	// A time zone offset is converted to UTC before the service sees it.
	e.do(t, "GET", "/api/projects/"+string(e.project.ID)+"/standup?from=2026-03-01T00:00:00%2B07:00&to=2026-03-02T00:00:00%2B07:00", nil)
	if !strings.HasSuffix(f.last(), "2026-02-28T17:00:00Z 2026-03-01T17:00:00Z") {
		t.Errorf("call = %q", f.last())
	}
}

// StartDraft follows the same write-mode switch as POST /api/tasks.
func TestPhase6StartDraftHonoursWriteModes(t *testing.T) {
	f := &p6fakes{}
	e := p6env(t, f, func(d *Deps) { d.WriteModes = true })
	if resp, data := e.do(t, "POST", "/api/tasks/task_d1/start", StartDraftRequest{Mode: core.ModeCode}); resp.StatusCode != 200 {
		t.Errorf("code mode with write modes on: %d %s", resp.StatusCode, data)
	}
}

// ---- error mapping ----

type userMsgErr struct{ msg string }

func (e *userMsgErr) Error() string        { return "internal: " + e.msg }
func (e *userMsgErr) UserMessage() string  { return e.msg }
func (e *userMsgErr) NothingWritten() bool { return true }

func TestPhase6ErrorMapping(t *testing.T) {
	const secretCause = "dial tcp 10.9.8.7:5432: password=hunter2 /home/alice/.ygg/secret.db"
	cases := []struct {
		name    string
		err     error
		status  int
		code    ErrorCode
		message string // substring the body must carry ("" = none required)
	}{
		{"user error", &coretasks.UserError{Message: "Pick an agent."}, 400, ErrBadRequest, "Pick an agent."},
		{"wrapped user error", fmt.Errorf("ctx: %w", &coretasks.UserError{Message: "Pick an agent."}), 400, ErrBadRequest, "Pick an agent."},
		{"rejected", &tasks.RejectedError{Reason: "x", Message: "That draft can't be started."}, 409, ErrConflict, "That draft can't be started."},
		{"not found", fmt.Errorf("load: %w", db.ErrNotFound), 404, ErrNotFound, "doesn't exist"},
		{"conflict", fmt.Errorf("save: %w", db.ErrConflict), 409, ErrConflict, "changed"},
		{"digest mismatch", fmt.Errorf("save: %w", db.ErrDigestMismatch), 409, ErrConflict, "different request"},
		{"request changed", &proposals.RequestChangedError{Request: "req_1"}, 409, ErrConflict, "different proposals"},
		{"already decided", proposals.ErrAlreadyDecided, 409, ErrConflict, "already decided"},
		{"nothing written refusal", &userMsgErr{msg: "A folder with that name already exists."}, 409, ErrConflict, "already exists"},
		{"try again", TryAgain(errors.New("superseded")), 503, ErrUnavailable, "Try again"},
		{"shutting down", ShuttingDown(errors.New("closed")), 503, ErrUnavailable, "shutting down"},
		{"classed by an adapter", apperr.New(apperr.Invalid, "That folder is not tracked.", errors.New("x")), 400, ErrBadRequest, "not tracked"},
		{"unclassified", errors.New(secretCause), 500, ErrInternal, "Something went wrong"},
		{"wrapped unclassified", fmt.Errorf("a: %w", errors.New(secretCause)), 500, ErrInternal, "Something went wrong"},
	}
	for _, c := range cases {
		f := &p6fakes{err: c.err}
		e := p6env(t, f)
		for _, rt := range p6routes(e) {
			resp, data := e.do(t, rt.method, rt.path, rt.body)
			if resp.StatusCode != c.status {
				t.Errorf("%s on %s: status %d, want %d: %s", c.name, rt.name, resp.StatusCode, c.status, data)
				continue
			}
			var eb ErrorBody
			if json.Unmarshal(data, &eb) != nil || eb.Code != c.code {
				t.Errorf("%s on %s: body %s", c.name, rt.name, data)
			}
			if c.message != "" && !strings.Contains(string(data), c.message) {
				t.Errorf("%s on %s: body %s lacks %q", c.name, rt.name, data, c.message)
			}
			for _, leak := range []string{"hunter2", "10.9.8.7", "/home/alice", "internal:", "superseded", "tcp"} {
				if strings.Contains(string(data), leak) {
					t.Errorf("%s on %s: the body leaks %q: %s", c.name, rt.name, leak, data)
				}
			}
		}
	}
}

// A message that carries a token is redacted on the error path too.
func TestPhase6ErrorMessagesAreRedacted(t *testing.T) {
	f := &p6fakes{err: &coretasks.UserError{Message: "The host answered with token=" + fakeToken}}
	e := p6env(t, f)
	_, data := e.do(t, "GET", "/api/actions/req_1", nil)
	if strings.Contains(string(data), fakeToken) {
		t.Errorf("token in error body: %s", data)
	}
	f.err = apperr.New(apperr.Conflict, "failed at https://user:"+fakeToken+"@git.acme.test/x", nil)
	_, data = e.do(t, "GET", "/api/actions/req_1", nil)
	if strings.Contains(string(data), fakeToken) {
		t.Errorf("token in classed error body: %s", data)
	}
}

// ---- redaction and bounds ----

func TestPhase6ResponsesAreRedacted(t *testing.T) {
	leak := "log line: Authorization: Bearer " + fakeToken + " clone https://bot:" + fakeToken + "@git.acme.test/shop.git"
	f := &p6fakes{
		preview: &ActionPreviewDTO{RequestID: "r", Digest: hex64("a"), Kind: ActionDiagnoseRepo, Source: "s", ContextText: leak, Unavailable: []string{leak}, Context: ContextAvailable},
		failed:  &FailedJobsResponse{RepoID: "r", RunID: "1", Jobs: []FailedJobDTO{{JobID: "1", Name: "test", Log: leak, LogUnavailable: leak}}, Status: HostContextStatusDTO{Phase: HostContextReady, Message: leak}},
		issue:   &IssueResponse{Issue: &IssueDTO{Number: 1, Title: leak, Body: leak}, Status: HostContextStatusDTO{Phase: HostContextReady}},
		mrctx:   &MRContextResponse{Diff: leak, Status: HostContextStatusDTO{Phase: HostContextReady}},
		standup: &StandupResponse{Text: leak, Message: leak, Unavailable: []StandupUnavailableDTO{{Source: "commits", Reason: leak}}},
		noise:   &NoiseResponse{Available: true, Message: leak},
	}
	e := p6env(t, f)
	repo, proj := string(e.repo.ID), string(e.project.ID)
	for _, rt := range []p6route{
		{path: "/api/repos/" + repo + "/issues/1", method: "GET"},
		{path: "/api/repos/" + repo + "/mrs/1/context", method: "GET"},
		{path: "/api/repos/" + repo + "/ci/runs/1/failed-jobs", method: "GET"},
		{path: "/api/repos/" + repo + "/noise", method: "GET"},
		{path: "/api/projects/" + proj + "/standup?from=2026-03-01T00:00:00Z&to=2026-03-02T00:00:00Z", method: "GET"},
		{path: "/api/actions/preview", method: "POST", body: ActionRequestDTO{RequestID: "r", RepoID: e.repo.ID, Kind: ActionDiagnoseRepo, Agent: "claude"}},
	} {
		resp, data := e.do(t, rt.method, rt.path, rt.body)
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d %s", rt.path, resp.StatusCode, data)
		}
		if strings.Contains(string(data), fakeToken) {
			t.Errorf("%s: the response carries a token: %s", rt.path, data)
		}
		if !json.Valid(data) {
			t.Errorf("%s: invalid JSON after redaction", rt.path)
		}
	}
}

func TestPhase6ResponsesAreBounded(t *testing.T) {
	bigLog, bigBody := strings.Repeat("é", 200<<10), strings.Repeat("b", 100<<10) // 400 KiB of two-byte runes
	jobs := make([]FailedJobDTO, 0, 600)
	for i := range 600 {
		jobs = append(jobs, FailedJobDTO{JobID: fmt.Sprint(i + 1), Name: "j", Log: bigLog})
	}
	issues := make([]IssueDTO, 0, 600)
	for i := range 600 {
		issues = append(issues, IssueDTO{Number: i + 1, Title: "t", Body: bigBody})
	}
	notices := make([]NoticeDTO, 0, 300)
	for i := range 300 {
		notices = append(notices, NoticeDTO{ID: "n", TaskID: "t", TaskTitle: "x", Status: NoticeDone, Cursor: int64(i)})
	}
	ops := make([]NewRepoOperationDTO, 0, 300)
	for range 300 {
		ops = append(ops, op6("op", NewRepoReady))
	}
	f := &p6fakes{
		failed:  &FailedJobsResponse{RepoID: "r", RunID: "1", Jobs: jobs},
		issues:  &IssuesResponse{RepoID: "r", Issues: issues},
		mrctx:   &MRContextResponse{Diff: strings.Repeat("d", 1<<20)},
		notices: &NoticesResponse{Notices: notices},
		standup: &StandupResponse{Text: strings.Repeat("t", 1<<20), Commits: make([]StandupCommitDTO, 300)},
		preview: &ActionPreviewDTO{RequestID: "r", Digest: hex64("a"), Kind: ActionDiagnoseRepo, Source: "s", ContextText: strings.Repeat("c", 1<<20), Context: ContextAvailable},
		opList:  ops,
		issue:   &IssueResponse{Issue: &IssueDTO{Number: 1, Body: strings.Repeat("b", 1<<20)}},
	}
	e := p6env(t, f)
	repo, proj := string(e.repo.ID), string(e.project.ID)

	var fj FailedJobsResponse
	_, data := e.do(t, "GET", "/api/repos/"+repo+"/ci/runs/1/failed-jobs", nil)
	if err := json.Unmarshal(data, &fj); err != nil || len(fj.Jobs) != 500 || !fj.Truncated || !fj.Jobs[0].LogTruncated || len(fj.Jobs[0].Log) > 256<<10 {
		t.Errorf("failed jobs: %v jobs=%d truncated=%v", err, len(fj.Jobs), fj.Truncated)
	}
	total := 0
	for _, j := range fj.Jobs {
		total += len(j.Log)
	}
	if total > 1<<20 {
		t.Errorf("failed job logs total %d bytes, over the 1 MiB budget", total)
	}
	var is IssuesResponse
	_, data = e.do(t, "GET", "/api/repos/"+repo+"/issues", nil)
	if err := json.Unmarshal(data, &is); err != nil || len(is.Issues) != 500 || !is.Truncated || !is.Issues[0].BodyTruncated || len(is.Issues[0].Body) > 64<<10 {
		t.Errorf("issues: %v n=%d", err, len(is.Issues))
	}
	total = 0
	for _, i := range is.Issues {
		total += len(i.Body)
	}
	if total > 1<<20 {
		t.Errorf("issue bodies total %d bytes, over the 1 MiB budget", total)
	}
	var one IssueResponse
	_, data = e.do(t, "GET", "/api/repos/"+repo+"/issues/1", nil)
	if err := json.Unmarshal(data, &one); err != nil || one.Issue == nil || !one.Issue.BodyTruncated || len(one.Issue.Body) > 64<<10 {
		t.Errorf("issue: %v", err)
	}
	var mr MRContextResponse
	_, data = e.do(t, "GET", "/api/repos/"+repo+"/mrs/1/context", nil)
	if err := json.Unmarshal(data, &mr); err != nil || !mr.DiffTruncated || len(mr.Diff) > 64<<10 {
		t.Errorf("mr: %v", err)
	}
	var ns NoticesResponse
	_, data = e.do(t, "GET", "/api/notifications?limit=25", nil)
	if err := json.Unmarshal(data, &ns); err != nil || len(ns.Notices) != 25 {
		t.Errorf("notices: %v n=%d", err, len(ns.Notices))
	}
	var su StandupResponse
	_, data = e.do(t, "GET", "/api/projects/"+proj+"/standup?from=2026-03-01T00:00:00Z&to=2026-03-02T00:00:00Z", nil)
	if err := json.Unmarshal(data, &su); err != nil || len(su.Commits) != 200 || len(su.Text) > 64<<10 || len(su.Truncated) != 2 {
		t.Errorf("standup: %v commits=%d text=%d truncated=%v", err, len(su.Commits), len(su.Text), su.Truncated)
	}
	var pv ActionPreviewDTO
	_, data = e.do(t, "POST", "/api/actions/preview", ActionRequestDTO{RequestID: "r", RepoID: e.repo.ID, Kind: ActionDiagnoseRepo, Agent: "claude"})
	if err := json.Unmarshal(data, &pv); err != nil || !pv.Truncated || len(pv.ContextText) > 64<<10 {
		t.Errorf("preview: %v", err)
	}
	var list []NewRepoOperationDTO
	_, data = e.do(t, "GET", "/api/projects/"+proj+"/new-repos", nil)
	if err := json.Unmarshal(data, &list); err != nil || len(list) != 200 {
		t.Errorf("new repos: %v n=%d", err, len(list))
	}
}

// Cutting text never leaves half a character.
func TestCutTextKeepsCharacters(t *testing.T) {
	s := strings.Repeat("é", 10)
	for max := 0; max <= len(s); max++ {
		got, cut := cutText(s, max)
		if len(got) > max || (len(s) > max) != cut || !strings.HasPrefix(s, got) {
			t.Fatalf("max %d: %q cut=%v", max, got, cut)
		}
		if strings.ToValidUTF8(got, "?") != got {
			t.Fatalf("max %d: invalid UTF-8", max)
		}
	}
}

// ---- Host, Origin and session ----

func TestPhase6RoutesRefuseForeignHostOriginAndMissingSession(t *testing.T) {
	f := &p6fakes{}
	e := p6env(t, f)
	hostile := map[string]func(*http.Request){
		"foreign host":        func(r *http.Request) { r.Host = "evil.example" },
		"loopback look-alike": func(r *http.Request) { r.Host = "127.0.0.1.evil.example" },
		"foreign origin":      func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") },
		"opaque origin":       func(r *http.Request) { r.Header.Set("Origin", "null") },
		"origin other port":   func(r *http.Request) { r.Header.Set("Origin", "http://127.0.0.1:1") },
	}
	for _, rt := range p6routes(e) {
		for name, mod := range hostile {
			before := f.count()
			resp, _ := e.do(t, rt.method, rt.path, rt.body, mod)
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("%s with a %s: status %d, want 403", rt.name, name, resp.StatusCode)
			}
			if f.count() != before {
				t.Errorf("%s with a %s reached the service", rt.name, name)
			}
		}
		// No session cookie: 401, and the service is not reached.
		before := f.count()
		resp, _ := e.do(t, rt.method, rt.path, rt.body, func(r *http.Request) { r.Header.Del("Cookie") })
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s without a session: status %d, want 401", rt.name, resp.StatusCode)
		}
		// A made-up session is no better.
		resp, _ = e.do(t, rt.method, rt.path, rt.body, func(r *http.Request) {
			r.Header.Del("Cookie")
			r.AddCookie(&http.Cookie{Name: SessionCookie, Value: "forged"})
		})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s with a forged session: status %d, want 401", rt.name, resp.StatusCode)
		}
		if f.count() != before {
			t.Errorf("%s without a session reached the service", rt.name)
		}
		// The control: the plain request works.
		if resp, _ := e.do(t, rt.method, rt.path, rt.body); resp.StatusCode != http.StatusOK {
			t.Errorf("%s: the plain request got %d", rt.name, resp.StatusCode)
		}
	}
}

// A wrong method is refused by the mux, not routed to a mutation.
func TestPhase6MutationsAreNotReachableByGet(t *testing.T) {
	f := &p6fakes{}
	e := p6env(t, f)
	for _, rt := range p6routes(e) {
		if rt.method != "POST" {
			continue
		}
		before := f.count()
		resp, _ := e.do(t, "GET", rt.path, nil)
		// Two POST paths are also GET routes of their own (the action named
		// "preview" or "execute", the new repo list): those are reads.
		if f.count() != before {
			if last := f.last(); !strings.HasPrefix(last, "get ") && !strings.HasPrefix(last, "nr-list") {
				t.Errorf("GET %s ran a mutation: %q", rt.path, last)
			}
			continue
		}
		if resp.StatusCode == http.StatusOK {
			t.Errorf("GET %s: status %d", rt.path, resp.StatusCode)
		}
	}
}

// ---- no confirmation ----

type spyProposals struct {
	mu    sync.Mutex
	calls []string
}

func (s *spyProposals) rec(c string) { s.mu.Lock(); s.calls = append(s.calls, c); s.mu.Unlock() }
func (s *spyProposals) List(context.Context, proposals.ListFilter) ([]proposals.View, error) {
	s.rec("list")
	return nil, nil
}

func (s *spyProposals) Get(context.Context, core.ProposalID) (proposals.View, error) {
	s.rec("get")
	return proposals.View{}, db.ErrNotFound
}

func (s *spyProposals) Confirm(context.Context, core.ProposalID, string, string) (proposals.View, error) {
	s.rec("confirm")
	return proposals.View{}, nil
}

func (s *spyProposals) Reject(context.Context, core.ProposalID, string) (proposals.View, error) {
	s.rec("reject")
	return proposals.View{}, nil
}

func (s *spyProposals) Retry(context.Context, core.ProposalID, string) (proposals.View, error) {
	s.rec("retry")
	return proposals.View{}, nil
}

// Invariant 2: no Phase 6 route confirms, rejects or retries a proposal. A
// proposal is only ever decided through /api/proposals/{id}/confirm.
func TestInvariant_2_phase6_mutations_never_decide_a_proposal(t *testing.T) {
	spy := &spyProposals{}
	f := &p6fakes{}
	e := p6env(t, f, func(d *Deps) { d.Proposals = spy })
	n := 0
	for _, rt := range p6routes(e) {
		if resp, data := e.do(t, rt.method, rt.path, rt.body); resp.StatusCode != 200 {
			t.Fatalf("%s: %d %s", rt.name, resp.StatusCode, data)
		}
		n++
	}
	spy.mu.Lock()
	touched := append([]string(nil), spy.calls...)
	spy.mu.Unlock()
	if len(touched) != 0 {
		t.Errorf("the Phase 6 routes touched the proposal service: %v", touched)
	}
	if n == 0 {
		t.Fatal("no route ran")
	}
	// The control: the real decision route does reach the spy.
	e.do(t, "POST", "/api/proposals/prop_1/confirm", ConfirmProposalRequest{Hash: hex64("f")})
	spy.mu.Lock()
	defer spy.mu.Unlock()
	if len(spy.calls) == 0 {
		t.Error("the control request never reached the spy, so the test proves nothing")
	}
}

// The handlers register routes only through the Route constants: no handler
// answers a path outside them under the Phase 6 prefixes.
func TestPhase6NoUndeclaredRoutes(t *testing.T) {
	f := &p6fakes{}
	e := p6env(t, f)
	repo := string(e.repo.ID)
	for _, p := range []string{
		"/api/actions", "/api/actions/req_1/confirm", "/api/repos/" + repo + "/issues/7/start",
		"/api/new-repos/op_1/confirm", "/api/new-repos", "/api/notifications/read",
	} {
		for _, m := range []string{"GET", "POST"} {
			if resp, _ := e.do(t, m, p, map[string]string{}); resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotImplemented {
				t.Errorf("%s %s answered %d", m, p, resp.StatusCode)
			}
		}
	}
}
