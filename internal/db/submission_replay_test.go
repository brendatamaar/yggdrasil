package db

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/actions"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
)

func TestSubmissionContendersRollbackAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "restart.db")
	d, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	s := NewStore(d, nil)
	p, r := seed(t, s)
	digest := strings.Repeat("a", 64)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, id := range []core.TaskID{"task_a", "task_b"} {
		wg.Go(func() {
			<-start
			results <- s.Commit(ctx, func(tx *Tx) error {
				task := newTask(p, r, nil)
				task.ID, task.CreatedAt, task.UpdatedAt = id, at0, at0
				if _, err := tx.CreateTask(ctx, task); err != nil {
					return err
				}
				if err := tx.SaveTurn(ctx, id, coretasks.TurnRow{ID: "turn_" + coretasks.TurnID(id), Seq: 1, Mode: core.ModeCode, Prompt: "do it", State: coretasks.TurnWaiting}, true); err != nil {
					return err
				}
				if err := tx.AppendAudit(ctx, core.AuditEntry{ID: core.AuditID("audit_" + id), At: at0, Actor: "user", Action: "submit", Subject: string(id)}); err != nil {
					return err
				}
				return tx.LinkTaskSubmission(ctx, "req_submit", digest, id)
			})
		})
	}
	close(start)
	wg.Wait()
	close(results)
	wins, conflicts := 0, 0
	for err := range results {
		if err == nil {
			wins++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("wins %d conflicts %d", wins, conflicts)
	}
	for _, table := range []string{"tasks", "task_turns", "audit_log", "task_submissions"} {
		if n := count(t, d, "SELECT count(*) FROM "+table); n != 1 {
			t.Fatalf("%s rows %d", table, n)
		}
	}
	winner, gotDigest, err := s.GetTaskSubmission(ctx, "req_submit")
	if err != nil || gotDigest != digest {
		t.Fatalf("lookup %s %s %v", winner, gotDigest, err)
	}
	if err := s.Commit(ctx, func(tx *Tx) error {
		id, dg, err := tx.GetTaskSubmission(ctx, "req_submit")
		if err != nil || id != winner || dg != digest {
			t.Fatalf("Tx lookup %s %s %v", id, dg, err)
		}
		return tx.LinkTaskSubmission(ctx, "req_submit", digest, winner)
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(ctx, func(tx *Tx) error { return tx.LinkTaskSubmission(ctx, "req_submit", strings.Repeat("b", 64), winner) }); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("changed input: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	s = NewStore(d, nil)
	id, dg, err := s.GetTaskSubmission(ctx, "req_submit")
	if err != nil || id != winner || dg != digest {
		t.Fatalf("restart %s %s %v", id, dg, err)
	}
	if _, _, err := s.GetTaskSubmission(ctx, "absent"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id     core.RequestID
		digest string
		task   core.TaskID
	}{
		{"", digest, winner}, {"bad id", digest, winner}, {"req", strings.Repeat("A", 64), winner}, {"req", "bad", winner}, {"req", digest, ""}, {"req", digest, "absent"},
	} {
		if err := s.Commit(ctx, func(tx *Tx) error { return tx.LinkTaskSubmission(ctx, tc.id, tc.digest, tc.task) }); err == nil {
			t.Fatalf("accepted %+v", tc)
		}
	}
}

func TestTrackedReadyRepoTransactionScope(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	s := NewStore(d, nil)
	p, r := seed(t, s)
	check := func(project core.ProjectID, want bool) {
		t.Helper()
		err := s.Commit(ctx, func(tx *Tx) error {
			got, err := tx.GetTrackedReadyRepo(ctx, project, r.ID)
			if err == nil && got.ID != r.ID {
				t.Fatal(got)
			}
			return err
		})
		if want && err != nil || !want && !errors.Is(err, ErrNotFound) {
			t.Fatalf("scope want %v: %v", want, err)
		}
	}
	check(p.ID, true)
	check("foreign", false)
	for _, q := range []string{"UPDATE repos SET readiness='reserved'", "UPDATE repos SET readiness='ready',untracked_at=" + ts, "UPDATE repos SET untracked_at=NULL", "UPDATE projects SET untracked_at=" + ts} {
		if _, err := d.write.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
		check(p.ID, strings.Contains(q, "untracked_at=NULL"))
	}
}

func replayFixture(t *testing.T, s *Store, repo core.RepoID) ActionRecord {
	t.Helper()
	req := actions.Request{RequestID: "req_replay", RepoID: repo, Kind: actions.DiagnoseBuild, SourceID: "build_1", Agent: "mock", Model: "model-a"}
	rec := ActionRecord{Request: req, Digest: req.Digest(), State: ActionSubmitting, Attempt: 1, Key: "action:req_replay:1", CreatedAt: at0}
	if err := s.Commit(context.Background(), func(tx *Tx) error { _, _, err := tx.BeginActionRequest(context.Background(), rec); return err }); err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestActionFullReplayAndImmutableUpdates(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "replay.db")
	d, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewStore(d, nil)
	_, r := seed(t, s)
	rec := replayFixture(t, s, r.ID)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s = NewStore(d, nil)
	got, err := s.GetActionRequest(ctx, rec.Request.RequestID)
	if err != nil || got.Request != rec.Request || got.Digest != rec.Digest {
		t.Fatalf("roundtrip %+v %v", got, err)
	}
	for _, change := range []func(*actions.Request){func(r *actions.Request) { r.Agent = "other" }, func(r *actions.Request) { r.Model = "other" }, func(r *actions.Request) { r.RepoID = "other" }, func(r *actions.Request) { r.Kind = actions.ReviewMR }, func(r *actions.Request) { r.SourceID = "build_2" }} {
		changed := rec
		change(&changed.Request)
		changed.Digest = changed.Request.Digest()
		if err := s.Commit(ctx, func(tx *Tx) error { _, _, err := tx.BeginActionRequest(ctx, changed); return err }); !errors.Is(err, ErrDigestMismatch) {
			t.Fatalf("begin changed %v", err)
		}
		if err := s.Commit(ctx, func(tx *Tx) error { _, err := tx.SaveActionRequest(ctx, changed, 0, at0); return err }); !errors.Is(err, ErrDigestMismatch) {
			t.Fatalf("save changed %v", err)
		}
	}
	got, err = s.GetActionRequest(ctx, rec.Request.RequestID)
	if err != nil || got.Version != 0 || got.Request != rec.Request {
		t.Fatalf("mutated %+v %v", got, err)
	}
	for _, q := range []string{"UPDATE workspace_action_requests SET agent=NULL", "UPDATE workspace_action_requests SET agent='bad/agent'", "UPDATE workspace_action_requests SET model=char(10)", "UPDATE workspace_action_requests SET digest='" + strings.Repeat("0", 64) + "'"} {
		t.Run(q, func(t *testing.T) {
			if _, err := d.write.ExecContext(ctx, q); err != nil {
				t.Fatal(err)
			}
			if _, err := s.GetActionRequest(ctx, rec.Request.RequestID); err == nil {
				t.Fatal("corruption loaded")
			}
			if _, err := s.ListUnsettledActionRequests(ctx); err == nil {
				t.Fatal("corruption listed")
			}
			if err := s.Commit(ctx, func(tx *Tx) error { _, err := tx.SaveActionRequest(ctx, rec, 0, at0); return err }); err == nil {
				t.Fatal("corruption updated")
			}
			if _, err := d.write.ExecContext(ctx, "UPDATE workspace_action_requests SET agent=?,model=?,digest=?", rec.Request.Agent, rec.Request.Model, rec.Digest); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMigration00008LegacyFailsClosedAndPreservesData(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	s := NewStore(d, nil)
	_, r, task := seedTask(t, s)
	rec := replayFixture(t, s, r.ID)
	prop, err := s.CreateProposal(ctx, core.Proposal{TaskID: &task.ID, RepoID: &r.ID, Kind: core.ProposalCommit, Summary: "preserve"})
	if err != nil {
		t.Fatal(err)
	}
	beforeTask, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeRepo, err := s.GetRepo(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeProp, err := s.GetProposal(ctx, prop.ID)
	if err != nil {
		t.Fatal(err)
	}
	indexes := func() string {
		var value string
		if err := d.write.QueryRowContext(ctx, `SELECT coalesce(group_concat(sql, ';'),'') FROM (SELECT sql FROM sqlite_master WHERE type='index' AND tbl_name IN ('repos','tasks','proposals') ORDER BY name)`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	beforeIndexes := indexes()
	m, err := NewMigrator(d.write)
	if err != nil {
		t.Fatal(err)
	}
	before := schemaOf(t, d, "repos", "tasks", "proposals")
	if _, err := m.DownTo(ctx, 7); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, schemaOf(t, d, "repos", "tasks", "proposals")) {
		t.Fatal("unrelated schema changed")
	}
	if got, err := s.GetTask(ctx, task.ID); err != nil || got.ID != task.ID {
		t.Fatal("task lost", err)
	}
	if _, err := s.GetActionRequest(ctx, rec.Request.RequestID); !errors.Is(err, ErrLegacyActionRequest) {
		t.Fatalf("legacy lookup %v", err)
	}
	if err := s.Commit(ctx, func(tx *Tx) error { _, _, err := tx.BeginActionRequest(ctx, rec); return err }); !errors.Is(err, ErrLegacyActionRequest) {
		t.Fatalf("legacy begin %v", err)
	}
	if err := s.Commit(ctx, func(tx *Tx) error { _, err := tx.SaveActionRequest(ctx, rec, 0, at0); return err }); !errors.Is(err, ErrLegacyActionRequest) {
		t.Fatalf("legacy update %v", err)
	}
	afterTask, err := s.GetTask(ctx, task.ID)
	if err != nil || !reflect.DeepEqual(beforeTask, afterTask) {
		t.Fatal("task changed", err)
	}
	afterRepo, err := s.GetRepo(ctx, r.ID)
	if err != nil || !reflect.DeepEqual(beforeRepo, afterRepo) {
		t.Fatal("repo changed", err)
	}
	afterProp, err := s.GetProposal(ctx, prop.ID)
	if err != nil || !reflect.DeepEqual(beforeProp, afterProp) {
		t.Fatal("proposal changed", err)
	}
	if indexes() != beforeIndexes {
		t.Fatal("indexes changed")
	}
	if n := count(t, d, "SELECT count(*) FROM pragma_foreign_key_check"); n != 0 {
		t.Fatalf("broken FKs %d", n)
	}
}
