package tasks

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/newrepos"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
)

// A model-based property test of issue import and explicit start (AC-05,
// NR-02). It generates operation sequences against the real Service on a real
// SQLite store and the scripted adapter, predicts every result with a small
// independent model, and after EVERY operation (accepted or refused, restarts
// included) checks the invariants:
//
//   - import never runs work: no turn, session, worktree or adapter Start for a
//     draft that was not started, and no other effect;
//   - scope precedes dedup: an out-of-scope repo refuses (plain-language, never
//     a raw database error) even when the issue is already imported;
//   - issue identity is permanent: one task per (project, issue) across request
//     IDs, restarts, concurrent contenders and after the draft was started;
//   - request identity is immutable: the same request and payload replays the
//     first task, any changed immutable field refuses;
//   - explicit start admits exactly one first turn, and a refused start leaves
//     the draft inert;
//   - persistence is ordered: every committed link has its audit entry, refused
//     operations write nothing.
//
// A failure names the seed and prints the operation log, which is the small
// reproducing sequence (the run stops at the first violated invariant).

type repoKind int

const (
	repoA0 repoKind = iota
	repoA1
	repoOwnB
	repoForeign // a repo of project B named in project A
	repoUnknown
	repoReserved
)

var allRepoKinds = []repoKind{repoA0, repoA0, repoA1, repoOwnB, repoForeign, repoUnknown, repoReserved}

type impOp struct {
	req             core.RequestID
	repo            repoKind
	issue           int
	title, brief    string
	model           string
	freshRequestKey bool
}

type reqRec struct {
	digest string
	task   core.TaskID
	op     impOp // the committed request, so it can be replayed later
}

type draftModel struct {
	h          *harness
	projA      core.ProjectID
	projB      core.ProjectID
	repoB      core.Repo
	reserved   core.RepoID
	a1Untacked bool

	requests map[core.RequestID]reqRec
	issues   map[string]core.TaskID // projectID|dedupKey -> task
	tasks    map[core.TaskID]*modelDraft
	links    map[core.TaskID]int // request links per task = import audit entries
	starts   int
	log      []string
	nextReq  int
}

type modelDraft struct {
	project core.ProjectID
	repo    core.RepoID
	issue   core.TaskSource
	title   string
	brief   string
	model   string
	started bool
}

func newDraftModel(t *testing.T) *draftModel {
	h := newHarness(t, []string{"shop-api", "shop-web"})
	ctx := context.Background()
	other := dbtest.Project(t, h.store, core.Project{Name: "acme-admin", Path: filepath.Join(t.TempDir(), "acme-admin")})
	rb := dbtest.Repo(t, h.store, core.Repo{ProjectID: other.ID, Name: "shop-admin", Path: filepath.Join(other.Path, "shop-admin"), DefaultBranch: "main"})
	if err := os.MkdirAll(rb.Path, 0o750); err != nil {
		t.Fatal(err)
	}
	spec := newrepos.InitSpec{Project: h.project.ID, Repo: "rep_shopnew", Leaf: "shop-new", Branch: newrepos.DefaultBranch,
		README: "# shop-new\n", Ignore: "/node_modules/\n", CommitMessage: "Initial commit"}
	if err := h.store.Commit(ctx, func(tx *db.Tx) error {
		return tx.ReserveRepo(ctx, db.ReserveRepo{Operation: "op_shop", Project: h.project.ID, Repo: spec.Repo, SetupTask: "tsk_setup",
			Path: filepath.Join(h.project.Path, "shop-new"), Spec: spec, Agent: "mock", Title: "Set up", Now: time.Now().UTC()})
	}); err != nil {
		t.Fatal(err)
	}
	return &draftModel{h: h, projA: h.project.ID, projB: other.ID, repoB: rb, reserved: spec.Repo,
		requests: map[core.RequestID]reqRec{}, issues: map[string]core.TaskID{}, tasks: map[core.TaskID]*modelDraft{}, links: map[core.TaskID]int{}}
}

func (m *draftModel) specOf(op impOp) coretasks.DraftSpec {
	s := coretasks.DraftSpec{RequestID: op.req, Project: m.projA,
		Issue: coretasks.IssueRef{Instance: "git.acme.dev", Project: "acme/shop-api", Number: op.issue},
		Title: op.title, Brief: op.brief, Agent: "mock", Model: op.model}
	switch op.repo {
	case repoA0:
		s.Repo = m.h.repos[0].ID
	case repoA1:
		s.Repo = m.h.repos[1].ID
	case repoOwnB:
		s.Project, s.Repo = m.projB, m.repoB.ID
	case repoForeign:
		s.Repo = m.repoB.ID
	case repoUnknown:
		s.Repo = "rep_nothere"
	case repoReserved:
		s.Repo = m.reserved
	}
	return s
}

// digestOf is the model's own notion of "the same request": every immutable
// field of the import.
func digestOf(s coretasks.DraftSpec) string {
	return fmt.Sprintf("%s|%s|%s|%s|%d|%s|%s|%s", s.Project, s.Repo, s.Issue.Instance, s.Issue.Project, s.Issue.Number, s.Title, s.Brief, s.Model)
}

// scope predicts the refusal reason of an out-of-scope repo, or "".
func (m *draftModel) scope(op impOp) string {
	switch op.repo {
	case repoA0, repoOwnB:
		return ""
	case repoA1:
		if m.a1Untacked {
			return "repo_untracked"
		}
		return ""
	case repoReserved:
		return "repo_not_ready"
	}
	return "repo_untracked"
}

func (m *draftModel) doImport(t *testing.T, op impOp) {
	ctx := context.Background()
	// An out-of-scope repo keeps its request key on purpose: scope must refuse
	// before the digest check and before a replay, so a committed request
	// replayed after the repo left scope (same or changed payload) is predicted
	// as a scope refusal, never as the original task or a digest mismatch.
	if op.freshRequestKey {
		m.nextReq++
		op.req = core.RequestID(fmt.Sprintf("req_fresh%d", m.nextReq))
	}
	spec := m.specOf(op)
	m.log = append(m.log, fmt.Sprintf("import req=%s repo=%d issue=%d title=%q brief=%q model=%q", op.req, op.repo, op.issue, op.title, op.brief, op.model))
	res, err := m.h.svc.ImportIssueDraft(ctx, spec)
	d := digestOf(spec)
	key := string(spec.Project) + "|" + spec.Issue.DedupKey()
	// Predicted order: scope refusal, then digest mismatch, then replay returns
	// the original, then issue dedup, then create.
	switch {
	case m.scope(op) != "":
		if !rejectedWith(err, m.scope(op)) || res.Task.ID != "" || errors.Is(err, db.ErrNotFound) {
			t.Fatalf("out of scope (%s) must refuse with a plain reason, got %+v %v", m.scope(op), res, err)
		}
		return
	case m.requests[op.req].digest != "" && m.requests[op.req].digest != d:
		if !errors.Is(err, db.ErrDigestMismatch) || res.Task.ID != "" {
			t.Fatalf("changed immutable payload must refuse, got %+v %v", res, err)
		}
		return
	case m.requests[op.req].digest == d:
		if err != nil || res.Created || res.Task.ID != m.requests[op.req].task {
			t.Fatalf("replay must return the first task %s, got %+v %v", m.requests[op.req].task, res, err)
		}
		return
	case m.issues[key] != "":
		if err != nil || res.Created || res.Task.ID != m.issues[key] {
			t.Fatalf("issue dedup must return %s, got %+v %v", m.issues[key], res, err)
		}
		m.requests[op.req] = reqRec{digest: d, task: res.Task.ID, op: op}
		m.links[res.Task.ID]++
		return
	}
	if err != nil || !res.Created {
		t.Fatalf("first import must create a draft, got %+v %v", res, err)
	}
	m.requests[op.req] = reqRec{digest: d, task: res.Task.ID, op: op}
	m.issues[key] = res.Task.ID
	m.links[res.Task.ID]++
	m.tasks[res.Task.ID] = &modelDraft{project: spec.Project, repo: spec.Repo, issue: spec.Issue.Source(), title: spec.Title, brief: spec.Brief, model: spec.Model}
}

// burst imports one issue from several goroutines with distinct request IDs.
func (m *draftModel) burst(t *testing.T, issue int) {
	m.log = append(m.log, fmt.Sprintf("burst issue=%d", issue))
	const n = 4
	specs := make([]coretasks.DraftSpec, n)
	ops := make([]impOp, n)
	results := make([]DraftResult, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range specs {
		m.nextReq++
		op := impOp{req: core.RequestID(fmt.Sprintf("req_fresh%d", m.nextReq)), repo: repoA0, issue: issue, title: "Burst", brief: "burst"}
		specs[i], ops[i] = m.specOf(op), op
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = m.h.svc.ImportIssueDraft(context.Background(), specs[i])
		}()
	}
	wg.Wait()
	key := string(specs[0].Project) + "|" + specs[0].Issue.DedupKey()
	known := m.issues[key]
	created := 0
	winner := known
	for i := range results {
		if errs[i] != nil {
			t.Fatalf("burst contender %d: %v", i, errs[i])
		}
		if winner == "" {
			winner = results[i].Task.ID
		}
		if results[i].Task.ID != winner {
			t.Fatalf("contenders disagree: %s vs %s", results[i].Task.ID, winner)
		}
		if results[i].Created {
			created++
		}
		m.requests[specs[i].RequestID] = reqRec{digest: digestOf(specs[i]), task: winner, op: ops[i]}
		m.links[winner]++
	}
	if (known == "" && created != 1) || (known != "" && created != 0) {
		t.Fatalf("burst created %d drafts (known=%q)", created, known)
	}
	if known == "" {
		s := specs[0]
		m.issues[key] = winner
		m.tasks[winner] = &modelDraft{project: s.Project, repo: s.Repo, issue: s.Issue.Source(), title: s.Title, brief: s.Brief, model: s.Model}
	}
}

// replayCommitted replays an already-committed request ID, with the same
// payload or with a changed one. When the repo has since left scope (untracked)
// the model predicts a scope refusal in both cases; otherwise the original
// task (same payload) or a digest mismatch (changed payload).
func (m *draftModel) replayCommitted(t *testing.T, r *rand.Rand, changed, preferOutOfScope bool) {
	var ids []core.RequestID
	for id := range m.requests {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var pick []core.RequestID
	for _, id := range ids {
		if !preferOutOfScope || m.scope(m.requests[id].op) != "" {
			pick = append(pick, id)
		}
	}
	if len(pick) == 0 {
		return
	}
	rec := m.requests[pick[r.Intn(len(pick))]]
	op := rec.op
	op.freshRequestKey = false
	if changed {
		op.title += " (edited)"
	}
	m.doImport(t, op)
}

func (m *draftModel) pickDraft(r *rand.Rand, started bool) core.TaskID {
	var ids []core.TaskID
	for id, d := range m.tasks {
		if d.started == started {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return ""
	}
	// Map order is random: sort for a reproducible choice.
	for i := range ids {
		for j := i + 1; j < len(ids); j++ {
			if ids[j] < ids[i] {
				ids[i], ids[j] = ids[j], ids[i]
			}
		}
	}
	return ids[r.Intn(len(ids))]
}

func (m *draftModel) start(t *testing.T, id core.TaskID, twice bool) {
	ctx := context.Background()
	d := m.tasks[id]
	m.log = append(m.log, fmt.Sprintf("start %s twice=%v started=%v", id, twice, d.started))
	n := 1
	if twice {
		n = 2
	}
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = m.h.svc.StartDraft(ctx, coretasks.DraftStart{Task: id, Mode: core.ModeAsk}, "")
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		}
	}
	tracked := d.repo != m.h.repos[1].ID || !m.a1Untacked
	switch {
	case d.started || !tracked:
		if ok != 0 {
			t.Fatalf("a started draft or one outside scope was started again: %v", errs)
		}
	default:
		if ok != 1 {
			t.Fatalf("exactly one start must win, got %d (%v)", ok, errs)
		}
		d.started = true
		m.starts++
		m.h.waitStatus(id, core.TaskDone)
	}
	for _, err := range errs {
		if err == nil {
			continue
		}
		var user *coretasks.UserError
		var rej *RejectedError
		if !errors.As(err, &user) && !errors.As(err, &rej) {
			t.Fatalf("start refusal is not a supported plain refusal: %v", err)
		}
	}
}

// check evaluates every invariant against the stored state.
func (m *draftModel) check(t *testing.T) {
	t.Helper()
	h, ctx := m.h, context.Background()
	for _, proj := range []core.ProjectID{m.projA, m.projB} {
		list, err := h.store.ListTasks(ctx, proj)
		if err != nil {
			t.Fatal(err)
		}
		perKey := map[string]int{}
		issueTasks := 0
		for _, task := range list {
			if task.Source == nil || task.Source.Kind != core.SourceIssue {
				continue // the reserved repo's internal setup draft
			}
			issueTasks++
			perKey[task.Source.DedupKey]++
			md := m.tasks[task.ID]
			if md == nil {
				t.Fatalf("unknown task %s was created", task.ID)
			}
			if task.ProjectID != md.project || *task.Source != md.issue || task.Title != md.title || task.Prompt != md.brief ||
				task.Agent != "mock" || task.Model != md.model || task.Mode != core.ModeAsk || task.Scope != core.ScopeRepo ||
				len(task.RepoIDs) != 1 || task.RepoIDs[0] != md.repo {
				t.Fatalf("task %s lost its identity or provenance: %+v want %+v", task.ID, task, md)
			}
			turns, err := h.store.ListTaskTurns(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if md.started {
				if len(turns) != 1 || task.Status != core.TaskDone {
					t.Fatalf("started draft %s: %d turns, status %s", task.ID, len(turns), task.Status)
				}
			} else if len(turns) != 0 || task.Status != core.TaskDraft || task.SessionID != "" {
				t.Fatalf("an imported draft ran: %d turns, status %s, session %q", len(turns), task.Status, task.SessionID)
			}
			if wts, err := h.store.ListWorktrees(ctx, task.ID); err != nil || len(wts) != 0 {
				t.Fatalf("Ask drafts have no worktree: %v %v", wts, err)
			}
			if got := auditCount(h, task.ID, "task.import"); got != m.links[task.ID] {
				t.Fatalf("task %s has %d import audit entries, want one per committed link (%d)", task.ID, got, m.links[task.ID])
			}
		}
		for k, n := range perKey {
			if n != 1 {
				t.Fatalf("issue %s has %d tasks", k, n)
			}
		}
		want := 0
		for _, md := range m.tasks {
			if md.project == proj {
				want++
			}
		}
		if issueTasks != want {
			t.Fatalf("project %s has %d issue tasks, model %d", proj, issueTasks, want)
		}
	}
	if got := len(h.adapter.Starts()); got != m.starts {
		t.Fatalf("adapter started %d runs, only %d drafts were started", got, m.starts)
	}
	for id, rec := range m.requests {
		got, digest, err := h.store.GetTaskSubmission(ctx, id)
		if err != nil || got != rec.task || digest == "" {
			t.Fatalf("request %s link: %s %v, want task %s", id, got, err, rec.task)
		}
	}
}

func auditCount(h *harness, id core.TaskID, action string) int {
	n := 0
	for _, a := range h.audits(id) {
		if a == action {
			n++
		}
	}
	return n
}

func TestPropIssueImportAndStartSequences(t *testing.T) {
	// Each seed builds a fresh SQLite store (about 6 s under -race), so the
	// default is a handful of long sequences; raise TASKS_PROP_SEEDS to dig.
	seeds := 5
	if v, err := strconv.Atoi(os.Getenv("TASKS_PROP_SEEDS")); err == nil && v > 0 {
		seeds = v
	} else if testing.Short() {
		seeds = 2
	}
	pool := []core.RequestID{"req_a", "req_b", "req_c"}
	for seed := int64(1); seed <= int64(seeds); seed++ {
		ok := t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			r := rand.New(rand.NewSource(seed))
			m := newDraftModel(t)
			m.h.start()
			defer func() {
				if t.Failed() {
					t.Logf("seed %d operation log (stops at the failing operation):", seed)
					for i, l := range m.log {
						t.Logf("  %02d %s", i, l)
					}
				}
			}()
			for i := 0; i < 40; i++ {
				switch p := r.Intn(20); {
				case p < 11:
					m.doImport(t, impOp{req: pool[r.Intn(len(pool))], repo: allRepoKinds[r.Intn(len(allRepoKinds))], issue: 1 + r.Intn(3),
						title: []string{"Fix login", "Add search"}[r.Intn(2)], brief: []string{"Users cannot log in.", "crlf\r\ntext\r\n"}[r.Intn(2)],
						model: []string{"", "model_one"}[r.Intn(2)], freshRequestKey: r.Intn(3) == 0})
				case p < 12:
					m.burst(t, 1+r.Intn(3))
				case p < 14:
					if id := m.pickDraft(r, false); id != "" {
						m.start(t, id, r.Intn(2) == 0)
					}
				case p < 15:
					if id := m.pickDraft(r, true); id != "" {
						m.start(t, id, false) // restarting a started draft must refuse
					}
				case p < 17:
					m.log = append(m.log, "restart")
					m.h.restart()
				case p < 18:
					if !m.a1Untacked {
						// Commit one shop-web request first, so the replays below
						// have an out-of-scope repo to hit.
						m.doImport(t, impOp{repo: repoA1, issue: 1 + r.Intn(3), title: "Fix login", brief: "Users cannot log in.", freshRequestKey: true})
						m.log = append(m.log, "untrack shop-web")
						dbtest.UntrackRepo(t, m.h.store, m.h.repos[1])
						m.a1Untacked = true
						m.replayCommitted(t, r, false, true) // scope precedes replay
						m.replayCommitted(t, r, true, true)  // scope precedes the digest check
					}
				case p < 19:
					m.replayCommitted(t, r, r.Intn(2) == 0, r.Intn(2) == 0)
				default:
					m.doImport(t, impOp{req: pool[r.Intn(len(pool))], repo: repoA0, issue: 1 + r.Intn(3), title: "Fix login", brief: "Users cannot log in."})
				}
				m.check(t)
			}
		})
		if !ok {
			return
		}
	}
}
