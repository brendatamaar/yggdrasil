package db

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

func openTest(t *testing.T) *DB {
	t.Helper()
	// A space in the path is part of the contract (Windows user folders).
	d, err := Open(context.Background(), filepath.Join(t.TempDir(), "with space", "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func seed(t *testing.T, s *Store) (core.Project, core.Repo) {
	t.Helper()
	ctx := context.Background()
	p, err := s.CreateProject(ctx, core.Project{Name: "demo", Path: `C:\work\demo`})
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.CreateRepo(ctx, core.Repo{ProjectID: p.ID, Name: "api", Path: `C:\work\demo\api`, DefaultBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	return p, r
}

func newTask(p core.Project, r core.Repo, parent *core.TaskID) core.Task {
	return core.Task{
		ProjectID: p.ID, ParentTaskID: parent, Title: "t", Prompt: "do it", Scope: core.ScopeRepo,
		RepoIDs: []core.RepoID{r.ID}, Mode: core.ModeCode, Agent: "claude",
	}
}

func TestOpenPaths(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	d, err := Open(ctx, filepath.Join(dir, "my data", "my data.db"))
	if err != nil {
		t.Fatalf("path with space: %v", err)
	}
	_ = d.Close()
	if _, err := Open(ctx, filepath.Join(dir, "x?y.db")); err == nil || !strings.Contains(err.Error(), "'?'") {
		t.Fatalf("path with '?': got %v", err)
	}
	if _, err := Open(ctx, ""); err == nil {
		t.Fatal("empty path accepted")
	}
}

func TestPragmasAndReadOnlyPool(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()
	var fk int
	var mode string
	if err := d.Read.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys = %d, %v", fk, err)
	}
	if err := d.Read.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q, %v", mode, err)
	}
	var bt int
	if err := d.Write.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&bt); err != nil || bt != 5000 {
		t.Fatalf("busy_timeout = %d, %v", bt, err)
	}
	if _, err := d.Read.ExecContext(ctx, "INSERT INTO projects (id, name, path, created_at) VALUES ('p','p','p','t')"); err == nil {
		t.Fatal("read pool accepted a write")
	}
}

func TestMigrationsUpDownUp(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()
	p, err := NewMigrator(d.Write)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 0); err != nil {
		t.Fatalf("down: %v", err)
	}
	var n int
	if err := d.Write.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name = 'projects'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("projects table after down: %d, %v", n, err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if err := d.Write.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name = 'projects'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("projects table after up: %d, %v", n, err)
	}
}

func TestForeignKeyCascade(t *testing.T) {
	d := openTest(t)
	s := NewStore(d)
	ctx := context.Background()
	p, r := seed(t, s)
	task, err := s.CreateTask(ctx, newTask(p, r, nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.ExecContext(ctx, "DELETE FROM projects WHERE id = ?", string(p.ID)); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"SELECT count(*) FROM repos", "SELECT count(*) FROM tasks", "SELECT count(*) FROM task_repos",
	} {
		var n int
		if err := d.Read.QueryRowContext(ctx, q).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s = %d after cascade, %v (task %s)", q, n, err, task.ID)
		}
	}
	// A task of a missing project is refused: foreign keys are on.
	if _, err := s.CreateTask(ctx, newTask(p, r, nil)); err == nil {
		t.Fatal("task of a deleted project accepted")
	}
}

func TestSubTaskOneLevel(t *testing.T) {
	d := openTest(t)
	s := NewStore(d)
	ctx := context.Background()
	p, r := seed(t, s)
	parent, err := s.CreateTask(ctx, newTask(p, r, nil))
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.CreateTask(ctx, newTask(p, r, &parent.ID))
	if err != nil {
		t.Fatalf("one level: %v", err)
	}
	// insert: a sub-task of a sub-task
	if _, err := s.CreateTask(ctx, newTask(p, r, &child.ID)); !errors.Is(err, ErrSubTaskDepth) {
		t.Fatalf("insert at depth 2: got %v, want ErrSubTaskDepth", err)
	}
	// update: give a task with sub-tasks a parent, and point a task at a sub-task
	other, err := s.CreateTask(ctx, newTask(p, r, nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ id, parent core.TaskID }{{parent.ID, other.ID}, {other.ID, child.ID}} {
		_, err := d.Write.ExecContext(ctx, "UPDATE tasks SET parent_task_id = ? WHERE id = ?", string(c.parent), string(c.id))
		if err == nil || !strings.Contains(err.Error(), "sub-tasks are one level only") {
			t.Fatalf("update %s -> parent %s: got %v", c.id, c.parent, err)
		}
	}
	subs, err := s.ListSubTasks(ctx, parent.ID)
	if err != nil || len(subs) != 1 || subs[0].ID != child.ID || *subs[0].ParentTaskID != parent.ID {
		t.Fatalf("sub-tasks = %+v, %v", subs, err)
	}
}

func TestAuditLogAppendOnly(t *testing.T) {
	d := openTest(t)
	s := NewStore(d)
	ctx := context.Background()
	e := core.AuditEntry{ID: "a1", At: time.Now(), Actor: "system", Action: "test", Subject: "task:1"}
	if err := s.InsertAudit(ctx, e); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"UPDATE audit_log SET actor = 'x'", "DELETE FROM audit_log"} {
		if _, err := d.Write.ExecContext(ctx, q); err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Fatalf("%s: got %v", q, err)
		}
	}
	got, err := s.ListAudit(ctx, "task:1", 10)
	if err != nil || len(got) != 1 || string(got[0].Detail) != "{}" {
		t.Fatalf("audit = %+v, %v", got, err)
	}
}

func TestConcurrentWriters(t *testing.T) {
	d := openTest(t)
	s := NewStore(d)
	ctx := context.Background()
	const n = 40
	var wg sync.WaitGroup
	errs := make(chan error, 2*n)
	for i := range n {
		wg.Go(func() {
			_, err := s.CreateProject(ctx, core.Project{Name: "p" + string(rune('A'+i/26)) + string(rune('a'+i%26)), Path: "x"})
			errs <- err
			// readers run next to the writer
			_, err = s.ListProjects(ctx)
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	ps, err := s.ListProjects(ctx)
	if err != nil || len(ps) != n {
		t.Fatalf("projects = %d, %v", len(ps), err)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	d := openTest(t)
	s := NewStore(d)
	ctx := context.Background()
	p, r := seed(t, s)

	if err := s.SetRepoTrusted(ctx, r.ID, true); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetRepo(ctx, r.ID); err != nil || !got.Trusted {
		t.Fatalf("repo = %+v, %v", got, err)
	}

	task, err := s.CreateTask(ctx, newTask(p, r, nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateTaskStatus(ctx, task.ID, core.TaskDone); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetTask(ctx, task.ID)
	if err != nil || got.Status != core.TaskDone || got.FinishedAt == nil || len(got.RepoIDs) != 1 || !got.CreatedAt.Equal(task.CreatedAt) {
		t.Fatalf("task = %+v, %v", got, err)
	}
	if _, err := s.GetTask(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing task: %v", err)
	}
	if err := s.UpdateTaskStatus(ctx, "nope", core.TaskDone); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing task: %v", err)
	}

	dep, err := s.CreateTask(ctx, newTask(p, r, nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddTaskDependency(ctx, task.ID, dep.ID); err != nil {
		t.Fatal(err)
	}
	if ids, err := s.TaskDependencies(ctx, task.ID); err != nil || len(ids) != 1 || ids[0] != dep.ID {
		t.Fatalf("deps = %v, %v", ids, err)
	}

	wt, err := s.CreateWorktree(ctx, core.Worktree{RepoID: r.ID, TaskID: task.ID, Name: "w1", Path: `C:\wt`, Branch: "ygg/w1"})
	if err != nil || wt.Status != core.WorktreeActive {
		t.Fatalf("worktree = %+v, %v", wt, err)
	}
	if err := s.UpdateWorktreeStatus(ctx, wt.ID, core.WorktreeMerged); err != nil {
		t.Fatal(err)
	}

	if err := s.AppendTaskEvent(ctx, TaskEvent{TaskID: task.ID, Seq: 1, Kind: "status", At: time.Now(), Data: json.RawMessage(`{"seq":1}`)}); err != nil {
		t.Fatal(err)
	}
	if evs, err := s.ListTaskEvents(ctx, task.ID, 0); err != nil || len(evs) != 1 || string(evs[0].Data) != `{"seq":1}` {
		t.Fatalf("events = %+v, %v", evs, err)
	}

	cs, err := s.CreateChangeSet(ctx, core.ChangeSet{TaskID: task.ID, MergeOrder: []core.RepoID{r.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if gotCS, err := s.GetChangeSet(ctx, cs.ID); err != nil || len(gotCS.MergeOrder) != 1 || gotCS.Status != core.ChangeSetOpen {
		t.Fatalf("change set = %+v, %v", gotCS, err)
	}

	prop, err := s.CreateProposal(ctx, core.Proposal{ChangeSetID: &cs.ID, TaskID: &task.ID, RepoID: &r.ID, Kind: core.ProposalCommit, Summary: "commit"})
	if err != nil || prop.Status != core.ProposalPending {
		t.Fatalf("proposal = %+v, %v", prop, err)
	}
	if err := s.UpdateProposalStatus(ctx, prop.ID, core.ProposalConfirmed); err != nil {
		t.Fatal(err)
	}
	if pending, err := s.ListProposals(ctx, core.ProposalPending); err != nil || len(pending) != 0 {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
	if gotP, err := s.GetProposal(ctx, prop.ID); err != nil || gotP.DecidedAt == nil || string(gotP.Payload) != "{}" {
		t.Fatalf("proposal = %+v, %v", gotP, err)
	}

	b, err := s.CreateBuild(ctx, core.Build{TaskID: &task.ID, WorktreeID: &wt.ID, Steps: []core.BuildStep{
		{RepoID: r.ID, Name: "build", Status: core.BuildQueued},
		{RepoID: r.ID, Name: "test", Status: core.BuildQueued},
	}})
	if err != nil {
		t.Fatal(err)
	}
	code := 0
	if err := s.UpdateBuildStep(ctx, b.ID, core.BuildStep{Index: 1, Status: core.BuildSucceeded, ExitCode: &code}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateBuildStatus(ctx, b.ID, core.BuildSucceeded); err != nil {
		t.Fatal(err)
	}
	gotB, err := s.GetBuild(ctx, b.ID)
	if err != nil || len(gotB.Steps) != 2 || gotB.Steps[1].ExitCode == nil || gotB.FinishedAt == nil || gotB.Steps[0].ExitCode != nil {
		t.Fatalf("build = %+v, %v", gotB, err)
	}
}
