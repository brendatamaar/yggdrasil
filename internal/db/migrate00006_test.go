package db

import (
	"context"
	"strconv"
	"testing"
)

// Migration 00006 adds dev_servers, worktree_ports and repo_env_settings. The
// CHECKs and partial unique indexes hold, and the migration goes down and up.
func TestMigration00006(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()
	s := NewStore(d, nil)
	p, r := seed(t, s)
	task, err := s.CreateTask(ctx, newTask(p, r, nil))
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string) error { _, err := d.write.ExecContext(ctx, q); return err }
	for _, w := range []string{"w1", "w2"} {
		if err := exec(`INSERT INTO worktrees (id, repo_id, task_id, name, path, branch, status, created_at)
			VALUES ('` + w + `', '` + string(r.ID) + `', '` + string(task.ID) + `', '` + w + `', 'C:\wt', 'ygg/` + w + `', 'active', ` + ts + `)`); err != nil {
			t.Fatal(err)
		}
	}
	srv := func(id, status, extra string) string {
		return `INSERT INTO dev_servers (id, repo_id, command_key, status, attempt` + extraCols(extra) + `, updated_at)
		        VALUES ('` + id + `', '` + string(r.ID) + `', 'dev', '` + status + `', 1` + extraVals(extra) + `, ` + ts + `)`
	}
	mustOK := func(q string) {
		t.Helper()
		if err := exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustFail := func(what, q string) {
		t.Helper()
		if err := exec(q); err == nil {
			t.Errorf("%s was accepted", what)
		}
	}
	mustOK(srv("s1", "running", "pid|4242"))
	mustFail("running without a pid", srv("s2", "running", ""))
	mustFail("failed without a fail kind", srv("s3", "failed", ""))
	mustFail("stopping without a cause", srv("s4", "stopping", "pid|1"))
	mustOK(srv("s5", "failed", "fail_kind|'crashed'"))

	port := func(w string, base int, released string) string {
		q := `INSERT INTO worktree_ports (worktree_id, base, size, allocated_at` + extraCols(released) + `)
		      VALUES ('` + w + `', ` + strconv.Itoa(base) + `, 20, ` + ts + extraVals(released) + `)`
		return q
	}
	mustOK(port("w1", 20000, ""))
	mustFail("a second live block for a worktree", port("w1", 20020, ""))
	mustFail("a live block with a taken base", port("w2", 20000, ""))
	mustOK(port("w2", 20020, ""))
	if err := exec(`UPDATE worktree_ports SET released_at = ` + ts + ` WHERE worktree_id = 'w1'`); err != nil {
		t.Fatal(err)
	}
	mustOK(port("w1", 20000, "")) // the released base can be reused
	mustFail("a block past 65535", port("w2", 65530, "released_at|"+ts))

	mustOK(`INSERT INTO repo_env_settings (repo_id, updated_at) VALUES ('` + string(r.ID) + `', ` + ts + `)`)
	mustFail("compose enabled without a file", `UPDATE repo_env_settings SET compose_enabled = 1`)

	m, err := NewMigrator(d.write)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.DownTo(ctx, 5); err != nil {
		t.Fatalf("down to 00005: %v", err)
	}
	if _, err := m.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
}
