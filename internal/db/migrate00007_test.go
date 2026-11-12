package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/newrepos"
)

// latestMigration is the highest migration version. Tests that assert the
// final version use it, so adding a migration changes one line.
const latestMigration = 7

func TestMigration00007FreshDatabase(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()
	m, err := NewMigrator(d.write)
	if err != nil {
		t.Fatal(err)
	}
	if v, err := m.GetDBVersion(ctx); err != nil || v != latestMigration {
		t.Fatalf("version = %d, %v; want %d", v, err, latestMigration)
	}
	for _, tbl := range []string{"repo_reservations", "proposal_effects", "workspace_action_requests",
		"workspace_action_proposals", "outbox", "notices", "notice_high_water"} {
		if n := count(t, d, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, tbl); n != 1 {
			t.Errorf("table %s is missing", tbl)
		}
	}
	if n := count(t, d, `SELECT count(*) FROM pragma_foreign_key_check`); n != 0 {
		t.Errorf("%d broken foreign keys", n)
	}
}

// Upgrade from version 6 with existing rows: every row survives, existing repos
// are ready, existing tasks have no source, proposals keep every column and
// index, and the new kinds are accepted afterwards.
func TestMigration00007UpgradeFromSix(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()
	s := NewStore(d, nil)
	p, r := seed(t, s)
	task, err := s.CreateTask(ctx, newTask(p, r, nil))
	if err != nil {
		t.Fatal(err)
	}
	tid, rid := string(task.ID), string(r.ID)
	exec := func(q string, args ...any) error { _, err := d.write.ExecContext(ctx, q, args...); return err }
	must := func(q string, args ...any) {
		t.Helper()
		if err := exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	must(`INSERT INTO change_sets (id, task_id, status, merge_order, created_at) VALUES ('cs1', ?, 'open', '[]', `+ts+`)`, tid)
	kinds := []string{"commit", "push", "merge_request", "install", "publish", "agent_file", "delete_files", "subtasks", "merge_local", "remove_worktree"}
	for i, k := range kinds {
		must(`INSERT INTO proposals (id, change_set_id, task_id, repo_id, kind, status, summary, created_at, action_hash, attempt, source, message, retryable, version)
		      VALUES (?, 'cs1', ?, ?, ?, 'pending', 'sum', `+ts+`, ?, 2, 'agent', 'm', 1, 3)`,
			"p"+string(rune('a'+i)), tid, rid, k, strings.Repeat("c", 64))
	}
	m, err := NewMigrator(d.write)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.DownTo(ctx, 6); err != nil {
		t.Fatalf("down to 00006: %v", err)
	}
	before := schemaOf(t, d, "proposals", "repos", "tasks")
	if n := count(t, d, `SELECT count(*) FROM pragma_table_info('repos') WHERE name = 'readiness'`); n != 0 {
		t.Fatal("readiness survived the down migration")
	}
	if _, err := m.Up(ctx); err != nil {
		t.Fatalf("up to 00007: %v", err)
	}
	after := schemaOf(t, d, "proposals", "repos", "tasks")
	if got, want := after["proposals"], before["proposals"]; got != want {
		t.Errorf("proposals columns changed:\n got %s\nwant %s", got, want)
	}
	if got, want := after["repos"], before["repos"]+",readiness"; got != want {
		t.Errorf("repos columns:\n got %s\nwant %s", got, want)
	}
	for _, c := range []string{"source_kind", "source_instance", "source_project", "source_number", "dedup_key"} {
		if !strings.Contains(after["tasks"], c) {
			t.Errorf("tasks.%s is missing", c)
		}
	}
	if n := count(t, d, `SELECT count(*) FROM proposals WHERE attempt = 2 AND source = 'agent' AND message = 'm' AND retryable = 1 AND version = 3`); n != len(kinds) {
		t.Fatalf("%d proposals kept their data, want %d", n, len(kinds))
	}
	if n := count(t, d, `SELECT count(*) FROM repos WHERE readiness = 'ready'`); n != 1 {
		t.Errorf("existing repos must be ready, %d are", n)
	}
	if n := count(t, d, `SELECT count(*) FROM tasks WHERE source_kind = '' AND dedup_key = ''`); n != 1 {
		t.Error("an existing task has a source")
	}
	for _, ix := range []string{"proposals_status", "proposals_change_set_id", "proposals_task_status", "repos_project_readiness", "tasks_project_dedup"} {
		if n := count(t, d, `SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, ix); n != 1 {
			t.Errorf("index %s missing", ix)
		}
	}
	for _, c := range []struct{ child, parent string }{{"proposals", "change_sets"}, {"proposals", "tasks"}, {"proposals", "repos"}, {"proposal_effects", "proposals"}, {"repo_reservations", "repos"}, {"workspace_action_requests", "repos"}} {
		if n := count(t, d, `SELECT count(*) FROM pragma_foreign_key_list('`+c.child+`') WHERE "table" = ?`, c.parent); n < 1 {
			t.Errorf("%s no longer points at %s", c.child, c.parent)
		}
	}
	if n := count(t, d, `SELECT count(*) FROM pragma_foreign_key_check`); n != 0 {
		t.Errorf("%d broken foreign keys", n)
	}
	if n := count(t, d, `PRAGMA foreign_keys`); n != 1 {
		t.Error("foreign keys left off")
	}
	// The new kinds are accepted, the old CHECK still holds.
	for i, k := range []string{"ignore_entries", "review_comment", "init_repo", "pull_ff"} {
		must(`INSERT INTO proposals (id, task_id, repo_id, kind, status, summary, created_at) VALUES (?, ?, ?, ?, 'pending', 's', `+ts+`)`, "n"+string(rune('a'+i)), tid, rid, k)
	}
	if err := exec(`INSERT INTO proposals (id, kind, status, summary, created_at) VALUES ('bad', 'rm_rf', 'pending', 's', ` + ts + `)`); err == nil {
		t.Error("an unknown proposal kind was accepted")
	}
}

// The migration is safe to run again after a crash between its commit and the
// goose version row, and the down migration removes only what 00007 added.
func TestMigration00007RerunAndDown(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()
	m, err := NewMigrator(d.write)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.DownTo(ctx, 6); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	// Forget the version row, as after a crash, and run again.
	if _, err := d.write.ExecContext(ctx, `DELETE FROM goose_db_version WHERE version_id = 7`); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Up(ctx); err != nil {
		t.Fatalf("rerun of 00007: %v", err)
	}
	if v, err := m.GetDBVersion(ctx); err != nil || v != latestMigration {
		t.Fatalf("version = %d, %v; want %d", v, err, latestMigration)
	}
	if _, err := m.DownTo(ctx, 6); err != nil {
		t.Fatalf("down: %v", err)
	}
	for _, tbl := range []string{"repo_reservations", "outbox", "notices"} {
		if n := count(t, d, `SELECT count(*) FROM sqlite_master WHERE name = ?`, tbl); n != 0 {
			t.Errorf("%s survived the down migration", tbl)
		}
	}
}

// The sqlc schema file and the migration describe the same new tables.
func TestSqlcSchema00007MatchesMigration(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	// A file database: migration 00003 pins a second connection, which an
	// in-memory database would give its own empty copy.
	ref, err := openPool(ctx, filepath.Join(t.TempDir(), "ref.db"), false, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer ref.Close()
	p, err := NewMigrator(ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 6); err != nil {
		t.Fatal(err)
	}
	if _, err := ref.ExecContext(ctx, schema00007); err != nil {
		t.Fatal(err)
	}
	cols := func(db *sql.DB, table string) [][4]string {
		rows, err := db.QueryContext(ctx, `SELECT name, type, "notnull", coalesce(dflt_value,'') FROM pragma_table_info('`+table+`') ORDER BY name`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out [][4]string
		for rows.Next() {
			var c [4]string
			if err := rows.Scan(&c[0], &c[1], &c[2], &c[3]); err != nil {
				t.Fatal(err)
			}
			out = append(out, c)
		}
		return out
	}
	for _, tbl := range []string{"repos", "tasks", "repo_reservations", "proposal_effects", "workspace_action_requests",
		"workspace_action_proposals", "outbox", "notices", "notice_high_water"} {
		if got, want := cols(d.write, tbl), cols(ref, tbl); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: migration %v, schema file %v", tbl, got, want)
		}
	}
}

// Reserved repos are invisible to ordinary reads and cannot join an ordinary
// task; the narrow setup path works; a reserved repo cannot be promoted twice.
func TestReservedRepoInvisibleAndNarrowSetupPath(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()
	s := NewStore(d, nil)
	p, _ := seed(t, s)
	now := at0
	spec := newReposSpec(p.ID, "rep_new", "fresh-repo")
	err := s.Commit(ctx, func(tx *Tx) error {
		return tx.ReserveRepo(ctx, ReserveRepo{
			Operation: "op_1", Project: p.ID, Repo: "rep_new", SetupTask: "tsk_setup",
			Path: "/work/acme/fresh-repo", Spec: spec, Agent: "mock", Title: "Set up fresh-repo", Now: now,
		})
	})
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if _, err := s.GetRepo(ctx, "rep_new"); err == nil {
		t.Error("GetRepo returned a reserved repo")
	}
	if _, err := s.GetRepoIncludingUntracked(ctx, "rep_new"); err == nil {
		t.Error("GetRepoIncludingUntracked returned a reserved repo")
	}
	repos, err := s.ListRepos(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range repos {
		if r.ID == "rep_new" {
			t.Error("ListRepos lists a reserved repo")
		}
	}
	if ws, err := s.GetWorkspace(ctx); err != nil {
		t.Fatal(err)
	} else {
		for _, pr := range ws.Projects {
			for _, r := range pr.Repos {
				if r.ID == "rep_new" {
					t.Error("the workspace lists a reserved repo")
				}
			}
		}
	}
	if r, err := s.GetReservedRepo(ctx, "rep_new"); err != nil || r.Readiness != core.RepoReserved {
		t.Errorf("GetReservedRepo = %+v, %v", r, err)
	}
	// An ordinary task cannot take a reserved repo.
	err = s.Commit(ctx, func(tx *Tx) error {
		t := newTask(p, core.Repo{ID: "rep_new"}, nil)
		_, err := tx.CreateTask(ctx, t)
		return err
	})
	if err == nil {
		t.Error("an ordinary task was created over a reserved repo")
	}
	// The setup draft exists, is a draft, links the reserved repo and is not on the board list.
	setup, err := s.GetSetupTask(ctx, "rep_new")
	if err != nil || setup.Status != core.TaskDraft || setup.Source == nil || setup.Source.Kind != core.SourceRepoSetup {
		t.Fatalf("setup task = %+v, %v", setup, err)
	}
	if tasks, err := s.ListTasks(ctx, p.ID); err != nil {
		t.Fatal(err)
	} else {
		for _, tk := range tasks {
			if tk.ID == "tsk_setup" {
				t.Error("the setup draft is listed with the ordinary tasks")
			}
		}
	}
	// The setup draft can never start, even by a direct write.
	if _, err := d.write.ExecContext(ctx, `UPDATE tasks SET status = 'queued' WHERE id = 'tsk_setup'`); err == nil {
		t.Error("a repo-setup draft was moved to queued")
	}
	if _, err := d.write.ExecContext(ctx, `INSERT INTO task_turns (id, task_id, seq, mode, prompt, state) VALUES ('trn_x', 'tsk_setup', 1, 'ask', 'p', 'waiting')`); err == nil {
		t.Error("a repo-setup draft got a turn")
	}
	// The same leaf cannot be reserved twice.
	err = s.Commit(ctx, func(tx *Tx) error {
		return tx.ReserveRepo(ctx, ReserveRepo{
			Operation: "op_2", Project: p.ID, Repo: "rep_dup", SetupTask: "tsk_setup2",
			Path: "/work/acme/fresh-repo", Spec: newReposSpec(p.ID, "rep_dup", "fresh-repo"), Agent: "mock", Title: "Set up", Now: now,
		})
	})
	if err == nil {
		t.Error("a leaf was reserved twice")
	}
	rec, err := s.GetReservation(ctx, "op_1")
	if err != nil || rec.Operation.Repo != "rep_new" || rec.Leaf != "fresh-repo" {
		t.Fatalf("reservation = %+v, %v", rec, err)
	}
	// Promotion happens once.
	if err := s.Commit(ctx, func(tx *Tx) error { return tx.MarkRepoReady(ctx, "rep_new", "main") }); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	if _, err := s.GetRepo(ctx, "rep_new"); err != nil {
		t.Errorf("a ready repo is not visible: %v", err)
	}
	if err := s.Commit(ctx, func(tx *Tx) error { return tx.MarkRepoReady(ctx, "rep_new", "main") }); err == nil {
		t.Error("a repo became ready twice")
	}
}

func newReposSpec(project core.ProjectID, repo core.RepoID, leaf string) newrepos.InitSpec {
	return newrepos.InitSpec{
		Project: project, Repo: repo, Leaf: leaf, Branch: newrepos.DefaultBranch,
		README: "# " + leaf + "\n", Ignore: "/node_modules/\n", CommitMessage: "Initial commit",
	}
}
