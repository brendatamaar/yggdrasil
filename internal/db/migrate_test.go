package db

import (
	"context"
	"errors"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

func count(t *testing.T, d *DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := d.Write.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// Migration 00002 rebuilds builds and build_steps. Rows written under the 00001
// schema must survive it, in both directions.
func TestMigration00002KeepsData(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()
	m, err := NewMigrator(d.Write)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.DownTo(ctx, 1); err != nil {
		t.Fatalf("down to 00001: %v", err)
	}
	for _, q := range []string{
		`INSERT INTO projects (id, name, path, created_at) VALUES ('p1', 'acme-shop', '/work', '2026-01-01T00:00:00.000Z')`,
		`INSERT INTO repos (id, project_id, name, path, created_at) VALUES ('r1', 'p1', 'shop-api', '/work/shop-api', '2026-01-01T00:00:00.000Z')`,
		`INSERT INTO tasks (id, project_id, title, prompt, scope, mode, agent, status, created_at, updated_at)
		 VALUES ('t1', 'p1', 't', 'p', 'repo', 'ask', 'mock', 'running', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`,
		`INSERT INTO builds (id, task_id, status, created_at) VALUES ('b1', 't1', 'running', '2026-01-02T00:00:00.000Z')`,
		`INSERT INTO build_steps (build_id, idx, repo_id, name, status, exit_code, log_path, started_at)
		 VALUES ('b1', 0, 'r1', 'verify', 'succeeded', 0, 'logs/b1-0.log', '2026-01-02T00:00:01.000Z')`,
		`INSERT INTO build_steps (build_id, idx, repo_id, name, status) VALUES ('b1', 1, 'r1', 'build', 'queued')`,
		`INSERT INTO proposals (id, task_id, kind, status, summary, created_at)
		 VALUES ('pr1', 't1', 'commit', 'pending', 'commit', '2026-01-02T00:00:00.000Z')`,
	} {
		if _, err := d.Write.ExecContext(ctx, q); err != nil {
			t.Fatalf("seed 00001 data: %v\n%s", err, q)
		}
	}

	if _, err := m.Up(ctx); err != nil {
		t.Fatalf("up to 00002: %v", err)
	}
	if n := count(t, d, `SELECT count(*) FROM build_steps`); n != 2 {
		t.Fatalf("build steps after the rebuild = %d, want 2 (a cascade deleted them?)", n)
	}
	s := NewStore(d)
	b, err := s.GetBuild(ctx, "b1")
	if err != nil {
		t.Fatal(err)
	}
	if b.Status != core.BuildRunning || b.Version != 0 || b.TaskID == nil || *b.TaskID != "t1" ||
		b.CreatedAt.IsZero() || len(b.Steps) != 2 {
		t.Fatalf("build after up = %+v", b)
	}
	st := b.Steps[0]
	if st.Name != "verify" || st.Status != core.BuildSucceeded || st.ExitCode == nil || *st.ExitCode != 0 ||
		st.LogPath != "logs/b1-0.log" || st.StartedAt == nil || len(st.Argv) != 0 || st.Attempt != 0 || st.Outcome != "" {
		t.Fatalf("step after up = %+v", st)
	}
	if task, err := s.GetTask(ctx, "t1"); err != nil || task.Version != 0 {
		t.Fatalf("task after up = %+v, %v", task, err)
	}
	if p, err := s.GetProposal(ctx, "pr1"); err != nil || p.Version != 0 {
		t.Fatalf("proposal after up = %+v, %v", p, err)
	}
	// The rebuilt tables keep their foreign keys: deleting the build removes its steps.
	if _, err := d.Write.ExecContext(ctx, `INSERT INTO builds (id, status, created_at) VALUES ('b2', 'interrupted', '2026-01-03T00:00:00.000Z')`); err != nil {
		t.Fatalf("new status 'interrupted' refused: %v", err)
	}
	if _, err := d.Write.ExecContext(ctx, `INSERT INTO build_steps (build_id, idx, repo_id, name, status, outcome, argv) VALUES ('b2', 0, 'r1', 'x', 'skipped', 'start_failed', '["go","test"]')`); err != nil {
		t.Fatalf("new step columns refused: %v", err)
	}
	if _, err := d.Write.ExecContext(ctx, `INSERT INTO build_steps (build_id, idx, repo_id, name, status) VALUES ('nope', 0, 'r1', 'x', 'queued')`); err == nil {
		t.Fatal("build step of a missing build accepted: the foreign key was lost in the rebuild")
	}
	if _, err := d.Write.ExecContext(ctx, `INSERT INTO build_steps (build_id, idx, repo_id, name, status, outcome) VALUES ('b1', 5, 'r1', 'x', 'queued', 'bogus')`); err == nil {
		t.Fatal("unknown outcome accepted")
	}
	if _, err := d.Write.ExecContext(ctx, `DELETE FROM builds WHERE id = 'b2'`); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT count(*) FROM build_steps WHERE build_id = 'b2'`); n != 0 {
		t.Fatalf("steps of a deleted build remain: %d", n)
	}

	// Down with data: the statuses 00001 does not know are mapped, nothing is lost.
	if _, err := d.Write.ExecContext(ctx, `UPDATE builds SET status = 'interrupted' WHERE id = 'b1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.ExecContext(ctx, `UPDATE build_steps SET status = 'skipped' WHERE build_id = 'b1' AND idx = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := m.DownTo(ctx, 1); err != nil {
		t.Fatalf("down again with data: %v", err)
	}
	if n := count(t, d, `SELECT count(*) FROM builds WHERE id = 'b1' AND status = 'failed'`); n != 1 {
		t.Fatal("interrupted build not mapped to failed")
	}
	if n := count(t, d, `SELECT count(*) FROM build_steps WHERE build_id = 'b1' AND idx = 1 AND status = 'cancelled'`); n != 1 {
		t.Fatal("skipped step not mapped to cancelled")
	}
	if n := count(t, d, `SELECT count(*) FROM pragma_table_info('tasks') WHERE name = 'version'`); n != 0 {
		t.Fatal("tasks.version survived the down migration")
	}
	if n := count(t, d, `SELECT count(*) FROM sqlite_master WHERE name IN ('build_presets', 'build_artifacts')`); n != 0 {
		t.Fatal("new tables survived the down migration")
	}
	if _, err := m.Up(ctx); err != nil {
		t.Fatalf("up after down: %v", err)
	}
	if n := count(t, d, `SELECT count(*) FROM build_steps`); n != 2 {
		t.Fatalf("steps after down and up = %d, want 2", n)
	}
}

func TestStaleVersionIsAConflict(t *testing.T) {
	d := openTest(t)
	s := NewStore(d)
	ctx := context.Background()
	p, r := seed(t, s)

	task, err := s.CreateTask(ctx, newTask(p, r, nil))
	if err != nil {
		t.Fatal(err)
	}
	// A cancel that raced a step finish: both read version 0, the second loses.
	if v, err := s.UpdateTaskStatus(ctx, task.ID, core.TaskCancelled, task.Version); err != nil || v != 1 {
		t.Fatalf("first update = %d, %v", v, err)
	}
	if _, err := s.UpdateTaskStatus(ctx, task.ID, core.TaskDone, task.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale task status update = %v, want ErrConflict", err)
	}
	if _, err := s.SetTaskSession(ctx, task.ID, "sess", task.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale task session update = %v, want ErrConflict", err)
	}
	got, err := s.GetTask(ctx, task.ID)
	if err != nil || got.Status != core.TaskCancelled || got.Version != 1 {
		t.Fatalf("task = %+v, %v; the stale write must not change it", got, err)
	}
	if v, err := s.SetTaskSession(ctx, task.ID, "sess", got.Version); err != nil || v != 2 {
		t.Fatalf("session with the current version = %d, %v", v, err)
	}

	prop, err := s.CreateProposal(ctx, core.Proposal{TaskID: &task.ID, Kind: core.ProposalCommit, Summary: "c"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateProposalStatus(ctx, prop.ID, core.ProposalRejected, prop.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateProposalStatus(ctx, prop.ID, core.ProposalConfirmed, prop.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale proposal update = %v, want ErrConflict", err)
	}

	b, err := s.CreateBuild(ctx, core.Build{TaskID: &task.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateBuildStatus(ctx, b.ID, core.BuildCancelled, b.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateBuildStatus(ctx, b.ID, core.BuildSucceeded, b.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale build update = %v, want ErrConflict", err)
	}
	if gb, err := s.GetBuild(ctx, b.ID); err != nil || gb.Status != core.BuildCancelled {
		t.Fatalf("build = %+v, %v; cancelled must not be overwritten", gb, err)
	}

	// A missing row is NotFound, not a conflict.
	if _, err := s.UpdateTaskStatus(ctx, "nope", core.TaskDone, 0); !errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) {
		t.Fatalf("missing task = %v, want ErrNotFound", err)
	}
	if _, err := s.UpdateProposalStatus(ctx, "nope", core.ProposalFailed, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing proposal = %v, want ErrNotFound", err)
	}
	if _, err := s.UpdateBuildStatus(ctx, "nope", core.BuildFailed, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing build = %v, want ErrNotFound", err)
	}
}

func TestBuildStepFieldsRoundTrip(t *testing.T) {
	d := openTest(t)
	s := NewStore(d)
	ctx := context.Background()
	p, r := seed(t, s)
	task, err := s.CreateTask(ctx, newTask(p, r, nil))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBuild(ctx, core.Build{TaskID: &task.ID, Steps: []core.BuildStep{
		{RepoID: r.ID, Name: "verify", Status: core.BuildQueued, Argv: []string{"go", "test", "./..."}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	st := b.Steps[0]
	st.Status, st.Attempt, st.Outcome = core.BuildInterrupted, 2, "interrupted"
	if err := s.UpdateBuildStep(ctx, b.ID, st); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetBuild(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	g := got.Steps[0]
	if len(g.Argv) != 3 || g.Argv[2] != "./..." || g.Attempt != 2 || g.Outcome != "interrupted" || g.Status != core.BuildInterrupted {
		t.Fatalf("step = %+v", g)
	}
}
