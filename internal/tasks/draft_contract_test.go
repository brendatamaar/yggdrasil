package tasks

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/worktrees"
)

// reopenDatabase closes the service and the SQLite file, opens the file again
// and starts a new service on it: the strongest restart the harness offers.
func (h *harness) reopenDatabase() *Service {
	h.t.Helper()
	_ = h.svc.Close()
	if err := h.dbh.Close(); err != nil {
		h.t.Fatal(err)
	}
	d, err := db.Open(context.Background(), h.dbPath)
	if err != nil {
		h.t.Fatal(err)
	}
	h.dbh = d
	h.t.Cleanup(func() { _ = d.Close() })
	h.store = db.NewStore(d, nil)
	h.wtCfg.Store = worktrees.DBStore{S: h.store}
	ens, err := worktrees.New(h.wtCfg)
	if err != nil {
		h.t.Fatal(err)
	}
	h.wts.inner = ens
	h.afterCommit, h.beforeCommit = nil, nil
	return h.start()
}

// The contract of the two ports the actions package consumes
// (tasks.DraftService and tasks.RequestSubmitter, ports_draft.go), run through
// the real Service on a real SQLite store and the scripted adapter. The same
// clauses run with no restart, after a service restart and after the database
// file was closed and reopened between every step, so the durable link, not
// process memory, carries the identity. There is no fake of these ports in
// this package (the actions package owns its consumer-side fake), so the
// variants are the subjects of the suite.
func TestContractDraftAndRequestPorts(t *testing.T) {
	for _, subject := range []struct {
		name    string
		between func(h *harness)
	}{
		{"same service", func(*harness) {}},
		{"restarted service", func(h *harness) { h.restart() }},
		{"reopened database", func(h *harness) { h.reopenDatabase() }},
	} {
		t.Run(subject.name, func(t *testing.T) {
			h := newHarness(t, []string{"shop-api", "shop-web"})
			h.start()
			runImportContract(t, h, subject.between)
			runSubmissionContract(t, h, subject.between)
			runStartContract(t, h, subject.between)
		})
	}
}

func importSpec(h *harness, req core.RequestID, number int) coretasks.DraftSpec {
	s := issueSpec(h)
	s.RequestID, s.Issue.Number = req, number
	s.Model = "model_one"
	return s
}

func mustTaskCount(t *testing.T, h *harness, want int, why string) {
	t.Helper()
	list, err := h.store.ListTasks(context.Background(), h.project.ID)
	if err != nil || len(list) != want {
		t.Fatalf("%s: %d tasks (%v), want %d", why, len(list), err, want)
	}
}

func runImportContract(t *testing.T, h *harness, between func(*harness)) {
	t.Helper()
	ctx := context.Background()
	if _, err := h.svc.LookupRequest(ctx, "req_c1"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("lookup of an unknown request: %v", err)
	}
	spec := importSpec(h, "req_c1", 101)
	first, err := h.svc.ImportIssueDraft(ctx, spec)
	if err != nil || !first.Created {
		t.Fatalf("import: %+v %v", first, err)
	}
	between(h)
	// Identity round-trips the store.
	stored, err := h.store.GetTask(ctx, first.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Title != spec.Title || stored.Prompt != spec.Brief || stored.Agent != "mock" || stored.Model != "model_one" ||
		stored.Mode != core.ModeAsk || stored.Status != core.TaskDraft || stored.Source == nil || *stored.Source != spec.Issue.Source() {
		t.Fatalf("stored draft lost identity: %+v", stored)
	}
	if id, err := h.svc.LookupRequest(ctx, "req_c1"); err != nil || id != first.Task.ID {
		t.Fatalf("lookup: %s %v", id, err)
	}
	// Equal replay returns the original.
	again, err := h.svc.ImportIssueDraft(ctx, spec)
	if err != nil || again.Created || again.Task.ID != first.Task.ID {
		t.Fatalf("replay: %+v %v", again, err)
	}
	// Any changed immutable field, or a request of another kind, refuses.
	for name, change := range map[string]func(*coretasks.DraftSpec){
		"title":        func(s *coretasks.DraftSpec) { s.Title += " v2" },
		"brief":        func(s *coretasks.DraftSpec) { s.Brief += " v2" },
		"model":        func(s *coretasks.DraftSpec) { s.Model = "model_two" },
		"agent":        func(s *coretasks.DraftSpec) { s.Agent = "mock2" },
		"issue number": func(s *coretasks.DraftSpec) { s.Issue.Number++ },
		"issue host":   func(s *coretasks.DraftSpec) { s.Issue.Instance = "git.acme.test" },
		"issue path":   func(s *coretasks.DraftSpec) { s.Issue.Project = "acme/shop-web" },
		"repo":         func(s *coretasks.DraftSpec) { s.Repo = h.repos[1].ID },
	} {
		changed := spec
		change(&changed)
		if _, err := h.svc.ImportIssueDraft(ctx, changed); !errors.Is(err, db.ErrDigestMismatch) {
			t.Fatalf("changed %s: %v, want a digest mismatch", name, err)
		}
	}
	if _, err := h.svc.SubmitWithRequest(ctx, "req_c1", h.msg("ask", "hi", 0)); !errors.Is(err, db.ErrDigestMismatch) {
		t.Fatalf("an import request id reused as a submission: %v", err)
	}
	between(h)
	mustTaskCount(t, h, 1, "refusals created tasks")
	if id, err := h.svc.LookupRequest(ctx, "req_c1"); err != nil || id != first.Task.ID {
		t.Fatalf("a refusal replaced the link: %s %v", id, err)
	}
	// Issue dedup: another request and changed content get the first task and
	// never replace its content.
	other := spec
	other.RequestID, other.Title, other.Brief = "req_c2", "Another title", "Another brief"
	dup, err := h.svc.ImportIssueDraft(ctx, other)
	if err != nil || dup.Created || dup.Task.ID != first.Task.ID || dup.Task.Title != spec.Title || dup.Task.Prompt != spec.Brief {
		t.Fatalf("issue dedup must answer with the first draft: %+v %v", dup, err)
	}
	between(h)
	if id, err := h.svc.LookupRequest(ctx, "req_c2"); err != nil || id != first.Task.ID {
		t.Fatalf("the deduplicated request is not linked: %s %v", id, err)
	}
	if stored, _ := h.store.GetTask(ctx, first.Task.ID); stored.Title != spec.Title || stored.Prompt != spec.Brief {
		t.Fatalf("a later import replaced the first content: %+v", stored)
	}
	// A different issue is a different draft; the first issue number is not a
	// prefix collision with 1010.
	next, err := h.svc.ImportIssueDraft(ctx, importSpec(h, "req_c3", 1010))
	if err != nil || !next.Created || next.Task.ID == first.Task.ID {
		t.Fatalf("different issue: %+v %v", next, err)
	}
	mustTaskCount(t, h, 2, "after a second issue")
	if turns, _ := h.store.ListTaskTurns(ctx, first.Task.ID); len(turns) != 0 || len(h.adapter.Starts()) != 0 {
		t.Fatal("imports have execution effects")
	}
	// A submission key cannot be reused as an import.
	sub, err := h.svc.SubmitWithRequest(ctx, "req_c4", h.msg("ask", "explain", 0))
	if err != nil {
		t.Fatal(err)
	}
	h.waitStatus(sub.Task.ID, core.TaskDone)
	if _, err := h.svc.ImportIssueDraft(ctx, importSpec(h, "req_c4", 102)); !errors.Is(err, db.ErrDigestMismatch) {
		t.Fatalf("a submission request id reused as an import: %v", err)
	}
	mustTaskCount(t, h, 3, "after the kind confusion")
}

func runSubmissionContract(t *testing.T, h *harness, between func(*harness)) {
	t.Helper()
	ctx := context.Background()
	startsBefore := len(h.adapter.Starts())
	req := h.msg("ask", "explain the cart", 0)
	req.Title, req.Model = "Cart", "model_one"
	first, err := h.svc.SubmitWithRequest(ctx, "req_s1", req)
	if err != nil || first.Task.ID == "" || first.Turn == "" {
		t.Fatalf("submit: %+v %v", first, err)
	}
	h.waitStatus(first.Task.ID, core.TaskDone)
	between(h)
	again, err := h.svc.SubmitWithRequest(ctx, "req_s1", req)
	if err != nil || again.Task.ID != first.Task.ID || again.Turn != first.Turn {
		t.Fatalf("equal replay: %+v %v", again, err)
	}
	if id, err := h.svc.LookupRequest(ctx, "req_s1"); err != nil || id != first.Task.ID {
		t.Fatalf("lookup: %s %v", id, err)
	}
	turns, err := h.store.ListTaskTurns(ctx, first.Task.ID)
	if err != nil || len(turns) != 1 || turns[0].ID != first.Turn {
		t.Fatalf("first turn: %v %v", turns, err)
	}
	for name, change := range map[string]func(*MessageRequest){
		"prompt": func(r *MessageRequest) { r.Prompt += "!" },
		"mode":   func(r *MessageRequest) { r.Mode = "code" },
		"model":  func(r *MessageRequest) { r.Model = "model_two" },
		"agent":  func(r *MessageRequest) { r.Agent = "mock2" },
		"title":  func(r *MessageRequest) { r.Title = "Other" },
		"repo":   func(r *MessageRequest) { r.Repos = []string{string(h.repos[1].ID)} },
		"scope":  func(r *MessageRequest) { r.Scope = "project" },
		"opt-in": func(r *MessageRequest) { r.OptIn = !r.OptIn },
	} {
		changed := req
		change(&changed)
		if _, err := h.svc.SubmitWithRequest(ctx, "req_s1", changed); !errors.Is(err, db.ErrDigestMismatch) {
			t.Fatalf("changed %s: %v, want a digest mismatch", name, err)
		}
	}
	// A different key with the same payload is a different task (there is no
	// issue-style dedup for plain submissions).
	second, err := h.svc.SubmitWithRequest(ctx, "req_s2", req)
	if err != nil || second.Task.ID == first.Task.ID {
		t.Fatalf("second key: %+v %v", second, err)
	}
	h.waitStatus(second.Task.ID, core.TaskDone)
	if got := len(h.adapter.Starts()) - startsBefore; got != 2 {
		t.Fatalf("%d runs for two submissions", got)
	}
	// New submissions only: an existing task or a bad request ID is a plain
	// refusal and creates nothing.
	existing := req
	existing.TaskID = first.Task.ID
	for name, c := range map[string]struct {
		id  core.RequestID
		req MessageRequest
	}{
		"existing task": {"req_s3", existing}, "empty id": {"", req}, "space in id": {"a b", req},
		"slash in id": {"a/b", req}, "long id": {core.RequestID(strings.Repeat("r", 101)), req},
	} {
		_, err := h.svc.SubmitWithRequest(ctx, c.id, c.req)
		var user *coretasks.UserError
		if !errors.As(err, &user) || user.Message == "" {
			t.Fatalf("%s: %v, want a plain refusal", name, err)
		}
	}
	if _, err := h.svc.LookupRequest(ctx, "req_s3"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("a refused request left a link: %v", err)
	}
	if got := len(h.adapter.Starts()) - startsBefore; got != 2 {
		t.Fatalf("a refusal started a run (%d)", got)
	}
}

func runStartContract(t *testing.T, h *harness, between func(*harness)) {
	t.Helper()
	ctx := context.Background()
	spec := importSpec(h, "req_t1", 301)
	draft, err := h.svc.ImportIssueDraft(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	setup := core.Task{ID: "tsk_setup_contract", ProjectID: h.project.ID, RepoIDs: []core.RepoID{h.repos[0].ID}, Title: "Setup", Prompt: "setup",
		Scope: core.ScopeRepo, Mode: core.ModeAsk, Agent: "mock", Status: core.TaskDraft, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		Source: &core.TaskSource{Kind: core.SourceRepoSetup}}
	if err := h.store.Commit(ctx, func(tx *db.Tx) error { _, err := tx.CreateTask(ctx, setup); return err }); err != nil {
		t.Fatal(err)
	}
	between(h)
	startsBefore := len(h.adapter.Starts())
	user := func(err error) bool { var u *coretasks.UserError; return errors.As(err, &u) && u.Message != "" }
	for name, c := range map[string]struct {
		start coretasks.DraftStart
		agent string
		ok    func(error) bool
	}{
		"empty task id":   {coretasks.DraftStart{Mode: core.ModeAsk}, "", user},
		"invalid task id": {coretasks.DraftStart{Task: "a b", Mode: core.ModeAsk}, "", user},
		"unknown task":    {coretasks.DraftStart{Task: "tsk_nothere", Mode: core.ModeAsk}, "", func(e error) bool { return errors.Is(e, db.ErrNotFound) }},
		"setup draft":     {coretasks.DraftStart{Task: setup.ID, Mode: core.ModeAsk}, "", user},
		"changed agent":   {coretasks.DraftStart{Task: draft.Task.ID, Mode: core.ModeAsk}, "other", user},
		"changed model":   {coretasks.DraftStart{Task: draft.Task.ID, Mode: core.ModeAsk, Model: "model_two"}, "", user},
		"invalid mode":    {coretasks.DraftStart{Task: draft.Task.ID, Mode: "root"}, "", user},
		"no opt-in":       {coretasks.DraftStart{Task: draft.Task.ID, Mode: core.ModeCurrentBranch}, "", func(e error) bool { return rejectedWith(e, "needs_opt_in") }},
		"project mode":    {coretasks.DraftStart{Task: draft.Task.ID, Mode: core.ModeProject, OptIn: true}, "", user},
	} {
		res, err := h.svc.StartDraft(ctx, c.start, c.agent)
		if err == nil || res.Task.ID != "" || !c.ok(err) {
			t.Fatalf("%s: %+v %v", name, res, err)
		}
		// A refusal leaves the draft exactly as it was.
		st, serr := h.store.GetTask(ctx, draft.Task.ID)
		turns, _ := h.store.ListTaskTurns(ctx, draft.Task.ID)
		if serr != nil || st.Status != core.TaskDraft || len(turns) != 0 || len(h.adapter.Starts()) != startsBefore {
			t.Fatalf("%s: the draft is not inert (%s, %d turns, %d starts)", name, st.Status, len(turns), len(h.adapter.Starts())-startsBefore)
		}
	}
	// The setup draft stays a draft and can not be started through Message either.
	req := h.msg("ask", "bypass", 0)
	req.TaskID = setup.ID
	if _, err := h.svc.Message(ctx, req); !user(err) {
		t.Fatalf("setup bypass through Message: %v", err)
	}
	res, err := h.svc.StartDraft(ctx, coretasks.DraftStart{Task: draft.Task.ID, Mode: core.ModeAsk, Model: "model_one"}, "mock")
	if err != nil || res.Task.ID != draft.Task.ID || res.Turn == "" {
		t.Fatalf("start: %+v %v", res, err)
	}
	h.waitStatus(draft.Task.ID, core.TaskDone)
	between(h)
	if _, err := h.svc.StartDraft(ctx, coretasks.DraftStart{Task: draft.Task.ID, Mode: core.ModeAsk}, ""); !user(err) {
		t.Fatalf("a started draft started again: %v", err)
	}
	turns, err := h.store.ListTaskTurns(ctx, draft.Task.ID)
	if err != nil || len(turns) != 1 || turns[0].ID != res.Turn || len(h.adapter.Starts()) != startsBefore+1 {
		t.Fatalf("first turn must be immutable: %v %v (%d starts)", turns, err, len(h.adapter.Starts())-startsBefore)
	}
	if st, _ := h.store.GetTask(ctx, setup.ID); st.Status != core.TaskDraft {
		t.Fatalf("the setup draft left draft: %s", st.Status)
	}
}
