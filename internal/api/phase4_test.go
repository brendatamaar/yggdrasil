package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/changesets"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/review"
	cv "github.com/brendatamaar/yggdrasil/internal/core/verify"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/tasks"
	vsvc "github.com/brendatamaar/yggdrasil/internal/verify"
)

// The server publishes to the hub for both services.
var (
	_ vsvc.Publisher       = (*Server)(nil)
	_ changesets.Publisher = (*Server)(nil)
)

// fakeP4 implements every Phase 4 service interface and counts calls.
type fakeP4 struct {
	mu     sync.Mutex
	calls  []string
	err    error  // returned by every method when set
	optIns []bool // the optIn of each send-failure and send-notes call
}

func (f *fakeP4) rec(s string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
	return f.err
}

func (f *fakeP4) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

func (f *fakeP4) List(_ context.Context, task core.TaskID) (VerificationsResponse, error) {
	return VerificationsResponse{}, f.rec("verify.list")
}
func (f *fakeP4) Retry(_ context.Context, task core.TaskID) (VerificationsResponse, error) {
	return VerificationsResponse{Verifications: []VerificationDTO{{ID: "v1", TaskID: task, State: VerifyVerifying}}}, f.rec("verify.retry")
}
func (f *fakeP4) Cancel(_ context.Context, id cv.VerificationID) (VerificationDTO, error) {
	return VerificationDTO{ID: string(id), State: VerifyCancelled}, f.rec("verify.cancel")
}
func (f *fakeP4) SendFailure(_ context.Context, id cv.VerificationID, optIn bool) (SendFailureResponse, error) {
	f.mu.Lock()
	f.optIns = append(f.optIns, optIn)
	f.mu.Unlock()
	return SendFailureResponse{TaskID: "tsk_1", TurnID: "t2"}, f.rec("verify.send")
}

type fakeReview struct{ *fakeP4 }

func (f fakeReview) List(context.Context, core.TaskID) (ReviewNotesResponse, error) {
	return ReviewNotesResponse{}, f.rec("notes.list")
}
func (f fakeReview) Create(_ context.Context, task core.TaskID, req CreateNoteRequest) (ReviewNoteDTO, error) {
	return ReviewNoteDTO{ID: "n1", TaskID: task, Text: req.Text, State: NoteOpen}, f.rec("notes.create")
}
func (f fakeReview) Edit(_ context.Context, id review.NoteID, req EditNoteRequest) (ReviewNoteDTO, error) {
	return ReviewNoteDTO{ID: string(id), Text: req.Text}, f.rec("notes.edit")
}
func (f fakeReview) Delete(context.Context, review.NoteID) error { return f.rec("notes.delete") }
func (f fakeReview) Send(_ context.Context, _ core.TaskID, req SendNotesRequest) (SendNotesResponse, error) {
	f.mu.Lock()
	f.optIns = append(f.optIns, req.OptIn)
	f.mu.Unlock()
	return SendNotesResponse{TurnID: "t3"}, f.rec("notes.send")
}

type fakeCS struct{ *fakeP4 }

func (f fakeCS) Get(_ context.Context, id core.ChangeSetID) (ChangeSetDTO, error) {
	return ChangeSetDTO{ID: id, State: ChangeSetStateOpen}, f.rec("cs.get")
}
func (f fakeCS) ForTask(_ context.Context, task core.TaskID) (ChangeSetDTO, error) {
	return ChangeSetDTO{ID: "cs1", TaskID: task}, f.rec("cs.fortask")
}
func (f fakeCS) SetOrder(_ context.Context, id core.ChangeSetID, _ SetOrderRequest) (ChangeSetDTO, error) {
	return ChangeSetDTO{ID: id}, f.rec("cs.order")
}
func (f fakeCS) Preflight(context.Context, core.ChangeSetID, PreflightRequest) (PreflightResponse, error) {
	return PreflightResponse{}, f.rec("cs.preflight")
}
func (f fakeCS) ConfirmRun(_ context.Context, id core.ChangeSetID, _ ConfirmRunRequest) (ChangeSetDTO, error) {
	return ChangeSetDTO{ID: id, State: ChangeSetStateConfirming}, f.rec("cs.confirm")
}
func (f fakeCS) StopRun(_ context.Context, id core.ChangeSetID) (ChangeSetDTO, error) {
	return ChangeSetDTO{ID: id}, f.rec("cs.stop")
}
func (f fakeCS) Close(_ context.Context, id core.ChangeSetID) (ChangeSetDTO, error) {
	return ChangeSetDTO{ID: id, State: ChangeSetStateClosed}, f.rec("cs.close")
}

type fakeLanding struct{ *fakeP4 }

func (f fakeLanding) ProposeMergeLocal(_ context.Context, wt core.WorktreeID, req MergeLocalRequest) (ProposalDTO, error) {
	return ProposalDTO{ID: "prop_1", Kind: core.ProposalMergeLocal, Status: core.ProposalPending, Summary: req.Title}, f.rec("land.merge")
}
func (f fakeLanding) ProposeRemoveWorktree(context.Context, core.WorktreeID) (ProposalDTO, error) {
	return ProposalDTO{ID: "prop_2", Kind: core.ProposalRemoveWorktree, Status: core.ProposalPending}, f.rec("land.remove")
}

type fakeDiffer struct {
	*fakeP4
	diff git.BaseDiff
	got  []string
}

func (f *fakeDiffer) DiffWorktree(_ context.Context, task core.TaskID, repo core.RepoID) (git.BaseDiff, error) {
	f.got = append(f.got, string(task)+"/"+string(repo))
	return f.diff, f.rec("diff")
}

func p4Env(t *testing.T, f *fakeP4, d *fakeDiffer) *env {
	t.Helper()
	return newEnv(t, func(deps *Deps) {
		deps.Verification = f
		deps.Review = fakeReview{f}
		deps.ChangeSets = fakeCS{f}
		deps.Landing = fakeLanding{f}
		if d != nil {
			deps.WorktreeDiff = d
		}
	})
}

// Compile-time: the fake implements the verification interface.
var _ VerificationService = (*fakeP4)(nil)

type route struct {
	method, path string
	body         any
	status       int // expected on success
}

func p4Routes() []route {
	return []route{
		{"GET", "/api/tasks/tsk_1/verifications", nil, 200},
		{"POST", "/api/tasks/tsk_1/verify/retry", nil, 200},
		{"POST", "/api/verifications/v1/cancel", nil, 200},
		{"POST", "/api/verifications/v1/send-failure", nil, 200},
		{"GET", "/api/tasks/tsk_1/notes", nil, 200},
		{"POST", "/api/tasks/tsk_1/notes", CreateNoteRequest{Text: "x"}, 201},
		{"POST", "/api/tasks/tsk_1/notes/send", SendNotesRequest{IDs: []string{"n1"}}, 200},
		{"PUT", "/api/notes/n1", EditNoteRequest{Text: "y"}, 200},
		{"DELETE", "/api/notes/n1", nil, 204},
		{"GET", "/api/changesets/cs1", nil, 200},
		{"GET", "/api/tasks/tsk_1/changeset", nil, 200},
		{"PUT", "/api/changesets/cs1/order", SetOrderRequest{Order: []core.RepoID{"r1"}}, 200},
		{"POST", "/api/changesets/cs1/preflight", PreflightRequest{Scope: ScopeDTO{Kind: "all"}}, 200},
		{"POST", "/api/changesets/cs1/confirm", ConfirmRunRequest{Scope: ScopeDTO{Kind: "all"}}, 200},
		{"POST", "/api/changesets/cs1/stop", nil, 200},
		{"POST", "/api/changesets/cs1/close", nil, 200},
		{"POST", "/api/worktrees/w1/merge-local", MergeLocalRequest{Title: "Ship it"}, 201},
		{"POST", "/api/worktrees/w1/remove", nil, 201},
		{"GET", "/api/tasks/tsk_1/repos/r1/diff", nil, 200},
	}
}

func TestPhase4RoutesSucceed(t *testing.T) {
	f := &fakeP4{}
	e := p4Env(t, f, &fakeDiffer{fakeP4: f})
	for _, c := range p4Routes() {
		resp, data := e.do(t, c.method, c.path, c.body)
		if resp.StatusCode != c.status {
			t.Errorf("%s %s = %d %s, want %d", c.method, c.path, resp.StatusCode, data, c.status)
		}
	}
	if f.count() != len(p4Routes()) {
		t.Errorf("the services were called %d times for %d routes", f.count(), len(p4Routes()))
	}
}

func TestPhase4NilServicesAnswer501(t *testing.T) {
	e := newEnv(t)
	for _, c := range p4Routes() {
		resp, data := e.do(t, c.method, c.path, c.body)
		if resp.StatusCode != 501 {
			t.Errorf("%s %s = %d %s, want 501", c.method, c.path, resp.StatusCode, data)
		}
	}
}

// A request from another site (Origin) or for another host (DNS rebinding)
// reaches no service (invariant 7).
func TestPhase4RoutesRefuseForeignOriginAndHost(t *testing.T) {
	f := &fakeP4{}
	e := p4Env(t, f, &fakeDiffer{fakeP4: f})
	for _, c := range p4Routes() {
		resp, _ := e.do(t, c.method, c.path, c.body, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example.com") })
		if resp.StatusCode != 403 {
			t.Errorf("%s %s with a foreign Origin = %d, want 403", c.method, c.path, resp.StatusCode)
		}
		resp, _ = e.do(t, c.method, c.path, c.body, func(r *http.Request) { r.Host = "evil.example.com" })
		if resp.StatusCode != 403 {
			t.Errorf("%s %s with a foreign Host = %d, want 403", c.method, c.path, resp.StatusCode)
		}
	}
	if f.count() != 0 {
		t.Errorf("a refused request reached a service %d times", f.count())
	}
}

func TestPhase4RoutesNeedTheSession(t *testing.T) {
	f := &fakeP4{}
	e := p4Env(t, f, &fakeDiffer{fakeP4: f})
	for _, c := range p4Routes() {
		resp, _ := e.do(t, c.method, c.path, c.body, func(r *http.Request) { r.Header.Del("Cookie") })
		if resp.StatusCode != 401 && resp.StatusCode != 403 {
			t.Errorf("%s %s without a session = %d, want 401/403", c.method, c.path, resp.StatusCode)
		}
	}
	if f.count() != 0 {
		t.Errorf("a request without a session reached a service %d times", f.count())
	}
}

func TestPhase4ErrorClassesMapToStatuses(t *testing.T) {
	cases := []struct {
		err    error
		status int
		msg    string
	}{
		{apperr.New(apperr.NotFound, "That verification doesn't exist.", errors.New("x")), 404, "That verification doesn't exist."},
		{apperr.New(apperr.Conflict, "Yggdrasil couldn't tell whether the agent received the failure.", errors.New("send result unknown")), 409, "couldn't tell whether the agent received"},
		{apperr.New(apperr.Unavailable, "Try again soon.", errors.New("x")), 503, "Try again soon."},
		{apperr.New(apperr.Invalid, "The note is empty.", errors.New("x")), 400, "The note is empty."},
	}
	for _, c := range cases {
		f := &fakeP4{err: c.err}
		e := p4Env(t, f, &fakeDiffer{fakeP4: f})
		for _, r := range p4Routes() {
			resp, data := e.do(t, r.method, r.path, r.body)
			if resp.StatusCode != c.status || !strings.Contains(string(data), c.msg) {
				t.Errorf("%s %s with %v = %d %s, want %d", r.method, r.path, c.err, resp.StatusCode, data, c.status)
			}
		}
	}
	// An error without a class is a generic 500 that leaks nothing.
	f := &fakeP4{err: errors.New(`open C:\Users\someone\secret.txt: access denied`)}
	e := p4Env(t, f, &fakeDiffer{fakeP4: f})
	for _, r := range p4Routes() {
		resp, data := e.do(t, r.method, r.path, r.body)
		if resp.StatusCode != 500 || strings.Contains(string(data), "secret") {
			t.Errorf("%s %s with a plain error = %d %s", r.method, r.path, resp.StatusCode, data)
		}
	}
}

func TestPhase4BadBodiesAndIDs(t *testing.T) {
	f := &fakeP4{}
	e := p4Env(t, f, &fakeDiffer{fakeP4: f})
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/tasks/tsk_1/notes"},
		{"POST", "/api/tasks/tsk_1/notes/send"},
		{"PUT", "/api/notes/n1"},
		{"PUT", "/api/changesets/cs1/order"},
		{"POST", "/api/changesets/cs1/preflight"},
		{"POST", "/api/changesets/cs1/confirm"},
	} {
		// An unknown field and a second JSON value are refused.
		for _, body := range []any{map[string]any{"bogus": 1}, "not an object"} {
			resp, data := e.do(t, c.method, c.path, body)
			if resp.StatusCode != 400 {
				t.Errorf("%s %s with body %v = %d %s, want 400", c.method, c.path, body, resp.StatusCode, data)
			}
		}
	}
	for _, p := range []string{
		"/api/verifications/bad id/cancel", "/api/verifications/a%2Fb/send-failure",
	} {
		if resp, _ := e.do(t, "POST", p, nil); resp.StatusCode != 404 {
			t.Errorf("POST %s = %d, want 404", p, resp.StatusCode)
		}
	}
	if resp, _ := e.do(t, "GET", "/api/tasks/bad%20id/verifications", nil); resp.StatusCode != 404 {
		t.Errorf("bad task id = %d, want 404", resp.StatusCode)
	}
	if resp, _ := e.do(t, "POST", "/api/worktrees/w1/merge-local", MergeLocalRequest{Title: strings.Repeat("a", 201)}); resp.StatusCode != 400 {
		t.Errorf("a long merge title = %d, want 400", resp.StatusCode)
	}
	if resp, _ := e.do(t, "POST", "/api/worktrees/w1/merge-local", MergeLocalRequest{Title: "two\nlines"}); resp.StatusCode != 400 {
		t.Errorf("a multi-line merge title = %d, want 400", resp.StatusCode)
	}
	if f.count() != 0 {
		t.Errorf("invalid requests reached a service %d times", f.count())
	}
}

// Empty lists are JSON arrays, never null.
func TestPhase4EmptyListsAreArrays(t *testing.T) {
	f := &fakeP4{}
	e := p4Env(t, f, &fakeDiffer{fakeP4: f})
	for _, p := range []string{"/api/tasks/tsk_1/verifications", "/api/tasks/tsk_1/notes", "/api/changesets/cs1"} {
		_, data := e.do(t, "GET", p, nil)
		if strings.Contains(string(data), "null") {
			t.Errorf("GET %s has a null: %s", p, data)
		}
	}
}

// The landing routes create a pending proposal and nothing else: the only
// calls the service sees are the two propose methods, and the proposal comes
// back pending.
func TestLandingRoutesOnlyCreatePendingProposals(t *testing.T) {
	f := &fakeP4{}
	e := p4Env(t, f, nil)
	_, data := e.do(t, "POST", "/api/worktrees/w1/merge-local", MergeLocalRequest{Title: "Ship it"})
	var p ProposalDTO
	if err := json.Unmarshal(data, &p); err != nil || p.Status != core.ProposalPending || p.Kind != core.ProposalMergeLocal {
		t.Fatalf("merge-local = %s", data)
	}
	_, data = e.do(t, "POST", "/api/worktrees/w1/remove", nil)
	if err := json.Unmarshal(data, &p); err != nil || p.Status != core.ProposalPending || p.Kind != core.ProposalRemoveWorktree {
		t.Fatalf("remove = %s", data)
	}
	if got := strings.Join(f.calls, ","); got != "land.merge,land.remove" {
		t.Errorf("calls = %s", got)
	}
	// An empty body is fine for merge-local (the title defaults).
	if resp, data := e.do(t, "POST", "/api/worktrees/w1/merge-local", nil); resp.StatusCode != 201 {
		t.Errorf("merge-local without a body = %d %s", resp.StatusCode, data)
	}
}

func TestWorktreeDiffEndpoint(t *testing.T) {
	f := &fakeP4{}
	d := &fakeDiffer{fakeP4: f, diff: git.BaseDiff{HasChanges: true, Files: []git.BaseFile{
		{Path: "a.go", Status: "M", Added: 1, Hash: strings.Repeat("a", 64), Patch: "diff --git a/a.go b/a.go\n+x\n"},
		{Path: "b.bin", Status: "A", Binary: true, Hash: strings.Repeat("b", 64)},
		{Path: "c.go", Status: "M", EOLOnly: true, Hash: strings.Repeat("c", 64), Patch: "diff --git a/c.go b/c.go\n", PatchTruncated: true},
	}, Truncated: true}}
	e := p4Env(t, f, d)
	_, data := e.do(t, "GET", "/api/tasks/tsk_1/repos/r1/diff", nil)
	var out WorktreeDiffDTO
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Text != "diff --git a/a.go b/a.go\n+x\ndiff --git a/c.go b/c.go\n" || !out.Binary || !out.Truncated || len(out.Files) != 3 {
		t.Fatalf("diff = %+v", out)
	}
	if !out.Files[2].EOLOnly || out.Files[0].Hash != strings.Repeat("a", 64) || !out.Files[2].PatchTruncated {
		t.Errorf("files = %+v", out.Files)
	}
	if len(d.got) != 1 || d.got[0] != "tsk_1/r1" {
		t.Errorf("differ was asked for %v", d.got)
	}
	// A repo or task id that cannot exist never reaches the differ.
	for _, p := range []string{"/api/tasks/tsk_1/repos/bad%20repo/diff", "/api/tasks/bad%20task/repos/r1/diff"} {
		if resp, _ := e.do(t, "GET", p, nil); resp.StatusCode != 404 {
			t.Errorf("GET %s = %d, want 404", p, resp.StatusCode)
		}
	}
	if len(d.got) != 1 {
		t.Errorf("an invalid id reached the differ: %v", d.got)
	}
}

func TestWorktreeDiffSizeCap(t *testing.T) {
	big := strings.Repeat("x", maxDiffTextBytes/2+1)
	out := worktreeDiffDTO(git.BaseDiff{Files: []git.BaseFile{
		{Path: "a", Patch: big, Hash: "h1"}, {Path: "b", Patch: big, Hash: "h2"},
	}})
	if len(out.Text) > maxDiffTextBytes || !out.Truncated || out.Files[0].Omitted || !out.Files[1].Omitted || out.Files[1].Hash != "h2" {
		t.Errorf("cap not applied: len=%d truncated=%v files=%+v", len(out.Text), out.Truncated, out.Files)
	}
}

// ---- verification mapping -------------------------------------------------

func rec(state cv.State) vsvc.Record {
	return vsvc.Record{Verification: cv.Verification{ID: "v1", Task: "tsk_1", Repo: "r1", Worktree: "w1", State: state}}
}

func TestVerificationFromRecordFailed(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	r := rec(cv.Failed{Turn: "t1", Attempt: 1, Stage: cv.StageVerify, Build: "b1", Step: 1, Reason: cv.ExitCode{Code: 2}, At: at})
	d := VerificationFromRecord(r, "shop-api", "test", "FAIL order total")
	if d.State != VerifyFailed || d.FailedStep != "test" || d.Message != "FAIL order total" || d.ExitCode == nil || *d.ExitCode != 2 ||
		!d.CanSendFailure || d.Stage != "verify" || d.BuildID != "b1" || d.RepoName != "shop-api" || d.At == nil {
		t.Errorf("failed = %+v", d)
	}
	// The default step name when the service gave none.
	if d := VerificationFromRecord(r, "", "", ""); d.FailedStep != "step 2" || d.Message != "" {
		t.Errorf("default = %+v", d)
	}
	// Already sent, or a send whose outcome is unknown: no button.
	for _, mod := range []func(*vsvc.Record){func(r *vsvc.Record) { r.Sent = true }, func(r *vsvc.Record) { r.Sending = true }} {
		x := r
		mod(&x)
		if VerificationFromRecord(x, "", "", "").CanSendFailure {
			t.Error("canSendFailure must be false once sent or sending")
		}
	}
	// A step that could not start carries its plain failure text.
	s := rec(cv.Failed{Turn: "t1", Attempt: 1, Stage: cv.StageBootstrap, Build: "b1", Reason: cv.StartFailed{Failure: "npm was not found"}, At: at})
	if d := VerificationFromRecord(s, "", "install", ""); d.Message != "npm was not found" || d.ExitCode != nil || d.Stage != "bootstrap" {
		t.Errorf("start failure = %+v", d)
	}
}

func TestVerificationFromRecordCancelCause(t *testing.T) {
	for cause, want := range map[cv.CancelCause]string{
		cv.CancelNotTrusted: "no longer trusted", cv.CancelUser: "Stopped by you", cv.CancelExternal: "Stopped",
	} {
		d := VerificationFromRecord(rec(cv.Cancelled{Turn: "t1", Attempt: 1, Cause: cause, At: time.Now()}), "", "", "")
		if d.State != VerifyCancelled || d.CancelCause != string(cause) || !strings.Contains(d.Reason, want) || d.CanSendFailure {
			t.Errorf("%s: %+v", cause, d)
		}
	}
}

func TestVerificationFromRecordOtherStates(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		s    cv.State
		want string
	}{
		{cv.None{}, VerifyNone},
		{cv.Skipped{Turn: "t1", Reason: cv.SkipNoChanges}, VerifySkipped},
		{cv.Unverifiable{Turn: "t1", Attempt: 1, Reason: cv.UnverifiableNotTrusted}, VerifyUnverifiable},
		{cv.Bootstrapping{Turn: "t1", Attempt: 1, Build: "b1", Since: now}, VerifyBootstrapping},
		{cv.Verifying{Turn: "t1", Attempt: 1, Build: "b1", Since: now}, VerifyVerifying},
		{cv.Passed{Turn: "t1", Attempt: 1, Build: "b1", At: now}, VerifyPassed},
		{cv.Interrupted{Turn: "t1", Attempt: 1, Stage: cv.StageVerify, At: now}, VerifyInterrupted},
	} {
		d := VerificationFromRecord(rec(c.s), "", "", "")
		if d.State != c.want || d.CanSendFailure || d.Message != "" {
			t.Errorf("%T: %+v", c.s, d)
		}
	}
	d := VerificationFromRecord(rec(cv.Unverifiable{Turn: "t1", Attempt: 1, Reason: cv.UnverifiableNoCommand}), "", "", "")
	if d.Reason == "" {
		t.Error("an unverifiable result must say why")
	}
}

func TestVerificationSummaryIsTheWorstState(t *testing.T) {
	now := time.Now()
	sum := VerificationSummary([]cv.State{
		cv.Passed{Turn: "t", Attempt: 1, Build: "b", At: now},
		cv.Failed{Turn: "t", Attempt: 1, Stage: cv.StageVerify, Build: "b", Reason: cv.ExitCode{Code: 1}, At: now},
		cv.Verifying{Turn: "t", Attempt: 1, Build: "b", Since: now},
	})
	if sum.State != VerifyFailed || sum.Total != 3 || sum.Passed != 1 || sum.Failed != 1 || sum.Running != 1 {
		t.Errorf("summary = %+v", sum)
	}
	if s := VerificationSummary(nil); s.State != VerifyNone || s.Total != 0 {
		t.Errorf("empty = %+v", s)
	}
	if s := VerificationSummary([]cv.State{cv.Verifying{Turn: "t", Attempt: 1, Build: "b", Since: now}}); s.State != VerifyVerifying {
		t.Errorf("running = %+v", s)
	}
}

// ---- adapter ----------------------------------------------------------------

type fakeVBackend struct {
	recs []vsvc.Record
	sent []cv.VerificationID
	err  error
}

func (f *fakeVBackend) List(context.Context, core.TaskID) ([]vsvc.Record, error) {
	return f.recs, f.err
}
func (f *fakeVBackend) Retry(context.Context, core.TaskID) ([]vsvc.Record, error) {
	return f.recs, f.err
}
func (f *fakeVBackend) Get(context.Context, cv.VerificationID) (vsvc.Record, error) {
	return f.recs[0], f.err
}
func (f *fakeVBackend) Cancel(context.Context, cv.VerificationID) (vsvc.Record, error) {
	return f.recs[0], f.err
}
func (f *fakeVBackend) SendFailure(_ context.Context, id cv.VerificationID, optIn bool) error {
	f.sent = append(f.sent, id)
	return f.err
}
func (f *fakeVBackend) FailureDetail(context.Context, vsvc.Record) (string, string) {
	return "test", "redacted tail"
}

func TestVerificationAdapter(t *testing.T) {
	failed := rec(cv.Failed{Turn: "t1", Attempt: 1, Stage: cv.StageVerify, Build: "b1", Step: 0, Reason: cv.ExitCode{Code: 1}, At: time.Now()})
	b := &fakeVBackend{recs: []vsvc.Record{failed}}
	svc := NewVerificationService(b, func(context.Context, core.RepoID) string { return "shop-api" })
	out, err := svc.List(context.Background(), "tsk_1")
	if err != nil || len(out.Verifications) != 1 {
		t.Fatalf("list = %+v %v", out, err)
	}
	v := out.Verifications[0]
	if v.Message != "redacted tail" || v.FailedStep != "test" || v.RepoName != "shop-api" || !v.CanSendFailure || out.Summary.State != VerifyFailed {
		t.Errorf("dto = %+v summary = %+v", v, out.Summary)
	}
	resp, err := svc.SendFailure(context.Background(), "v1", false)
	if err != nil || resp.TaskID != "tsk_1" || len(b.sent) != 1 {
		t.Errorf("send = %+v %v", resp, err)
	}
	// A failed send passes the service's error through untouched.
	b.err = apperr.New(apperr.Conflict, "unknown outcome", errors.New("x"))
	if _, err := svc.SendFailure(context.Background(), "v1", false); !errors.Is(err, b.err) {
		t.Errorf("send error = %v", err)
	}
	// The board summary.
	b.err = nil
	sm := svc.(VerificationSummarizer)
	if s, ok, err := sm.Summary(context.Background(), "tsk_1"); err != nil || !ok || s.State != VerifyFailed {
		t.Errorf("summary = %+v %v %v", s, ok, err)
	}
	b.recs = nil
	if _, ok, _ := sm.Summary(context.Background(), "tsk_1"); ok {
		t.Error("no rows must give no summary")
	}
}

// The message of the unknown-outcome conflict reaches the UI as a 409 with the
// service's own text (the status and the words read sensibly).
func TestSendFailureUnknownOutcomeReadsAsConflict(t *testing.T) {
	b := &fakeVBackend{recs: []vsvc.Record{rec(cv.None{})}, err: apperr.New(apperr.Conflict,
		"Yggdrasil couldn't tell whether the agent received the failure. Check the agent before sending it again, or verify again to start a new attempt.", errors.New("send result unknown"))}
	e := newEnv(t, func(d *Deps) { d.Verification = NewVerificationService(b, nil) })
	// Get fails first in the stub backend (returns err), which is the same class.
	resp, data := e.do(t, "POST", "/api/verifications/v1/send-failure", nil)
	var eb ErrorBody
	_ = json.Unmarshal(data, &eb)
	if resp.StatusCode != 409 || eb.Code != ErrConflict || !strings.Contains(eb.Message, "couldn't tell whether the agent received") {
		t.Errorf("= %d %s", resp.StatusCode, data)
	}
}

// ---- board ------------------------------------------------------------------

func TestBoardAndTaskCarryTheVerificationSummary(t *testing.T) {
	failed := rec(cv.Failed{Turn: "t1", Attempt: 1, Stage: cv.StageVerify, Build: "b1", Reason: cv.ExitCode{Code: 1}, At: time.Now()})
	ft := &fakeTasks{board: tasks.BoardView{Max: 2, Cards: []tasks.BoardCard{
		{Task: core.Task{ID: "tsk_1", Title: "A", Status: core.TaskReview}, Column: "review"},
		{Task: core.Task{ID: "tsk_2", Title: "B", Status: core.TaskReview}, Column: "review"},
	}}}
	b := &fakeVBackend{recs: []vsvc.Record{failed}}
	e := newEnv(t, func(d *Deps) { d.Tasks = ft; d.Verification = NewVerificationService(b, nil) })
	_, data := e.do(t, "GET", "/api/board", nil)
	var out BoardResponse
	if err := json.Unmarshal(data, &out); err != nil || len(out.Cards) != 2 {
		t.Fatalf("board = %s", data)
	}
	for _, c := range out.Cards {
		if c.Verification == nil || c.Verification.State != VerifyFailed || c.Verification.Failed != 1 {
			t.Errorf("card %s verification = %+v", c.TaskID, c.Verification)
		}
	}
	// Without a verification service the field is absent, and the board works.
	e2 := newEnv(t, func(d *Deps) { d.Tasks = ft })
	_, data = e2.do(t, "GET", "/api/board", nil)
	if strings.Contains(string(data), `"verification"`) {
		t.Errorf("board without verification has the field: %s", data)
	}
	// A failing summary never fails the board.
	b.err = errors.New("db down")
	if resp, data := e.do(t, "GET", "/api/board", nil); resp.StatusCode != 200 || strings.Contains(string(data), `"verification"`) {
		t.Errorf("board with a failing summary = %d %s", resp.StatusCode, data)
	}
}

// ---- hub --------------------------------------------------------------------

func TestServerPublishesToTheNewTopics(t *testing.T) {
	e := newEnv(t)
	for _, topic := range []string{TopicVerifications, TopicChangeSets} {
		if _, err := parseTopic(topic); err != nil {
			t.Errorf("topic %s: %v", topic, err)
		}
	}
	// Publishing with no client connected must not block or panic.
	e.srv.PublishVerification("v1", "tsk_1", "r1", VerifyPassed)
	e.srv.PublishChangeSet("cs1", "tsk_1", "open")
	e.srv.PublishNotes("tsk_1")
}

func TestSetPhase4ServicesBindsLate(t *testing.T) {
	e := newEnv(t)
	if resp, _ := e.do(t, "GET", "/api/tasks/tsk_1/verifications", nil); resp.StatusCode != 501 {
		t.Fatalf("before = %d", resp.StatusCode)
	}
	f := &fakeP4{}
	e.srv.SetPhase4Services(Phase4Services{Verification: f})
	if resp, _ := e.do(t, "GET", "/api/tasks/tsk_1/verifications", nil); resp.StatusCode != 200 {
		t.Errorf("after = %d", resp.StatusCode)
	}
}
