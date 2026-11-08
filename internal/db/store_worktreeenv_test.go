package db

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

func TestWorktreePortsReleaseKeepsHistory(t *testing.T) {
	ctx := context.Background()
	s := NewStore(openTest(t), nil)
	_, r := seed(t, s)
	mk := func(name string) core.WorktreeID {
		task := core.Task{ID: core.TaskID(NewID("tsk")), ProjectID: r.ProjectID, Title: "t", Scope: core.ScopeRepo,
			RepoIDs: []core.RepoID{r.ID}, Mode: core.ModeCode, Agent: "mock", CreatedAt: bsNow, UpdatedAt: bsNow}
		var id core.WorktreeID
		err := s.Commit(ctx, func(tx *Tx) error {
			if _, err := tx.CreateTask(ctx, task); err != nil {
				return err
			}
			w, err := tx.CreateWorktree(ctx, core.Worktree{ID: core.WorktreeID(NewID("wt")), RepoID: r.ID, TaskID: task.ID,
				Name: name, Path: `C:\wt\` + name, Branch: "ygg/" + name, CreatedAt: bsNow})
			id = w.ID
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	a, b := mk("a"), mk("b")
	blk := core.PortBlock{Base: 20000, Size: 20}
	save := func(w core.WorktreeID, bl core.PortBlock) error {
		return s.Commit(ctx, func(tx *Tx) error {
			if err := tx.InsertWorktreePort(ctx, w, bl, bsNow); err != nil {
				return err
			}
			return tx.AppendAudit(ctx, bsAudit(NewID("aud"), r.ID))
		})
	}
	if err := save(a, blk); err != nil {
		t.Fatal(err)
	}
	if err := save(b, blk); !errors.Is(err, ErrConflict) {
		t.Fatalf("same base: %v", err)
	}
	if err := save(b, core.PortBlock{Base: 0, Size: 20}); err == nil {
		t.Fatal("base 0 accepted")
	}
	if err := save(b, core.PortBlock{Base: 65530, Size: 20}); err == nil {
		t.Fatal("block past 65535 accepted")
	}
	var released bool
	if err := s.Commit(ctx, func(tx *Tx) error {
		var err error
		released, err = tx.ReleaseWorktreePort(ctx, a, bsNow)
		return err
	}); err != nil || !released {
		t.Fatalf("release: %v %v", released, err)
	}
	live, err := s.ListLiveWorktreePorts(ctx)
	if err != nil || len(live) != 0 {
		t.Fatalf("live = %v %v", live, err)
	}
	var rows int
	if err := s.db.read.QueryRowContext(ctx, `SELECT count(*) FROM worktree_ports`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("history rows = %d %v", rows, err)
	}
	if err := save(b, blk); err != nil {
		t.Fatalf("reuse after release: %v", err)
	}
	if err := save(a, core.PortBlock{Base: 20020, Size: 20}); err != nil {
		t.Fatalf("new block for a released worktree: %v", err)
	}
}

func TestRepoEnvSettings(t *testing.T) {
	ctx := context.Background()
	s := NewStore(openTest(t), nil)
	_, r := seed(t, s)
	got, err := s.GetRepoEnvSettings(ctx, r.ID)
	if err != nil || got.AgentCopy || got.ComposeEnabled || len(got.ProfilePaths) != 0 {
		t.Fatalf("defaults: %+v %v", got, err)
	}
	if _, err := s.GetRepoEnvSettings(ctx, "rep_unknown"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown repo: %v", err)
	}
	rec := RepoEnvRecord{Repo: r.ID, ProfilePaths: []string{".env"}, AgentCopy: true, UpdatedAt: bsNow}
	put := func(rec RepoEnvRecord) error {
		return s.Commit(ctx, func(tx *Tx) error {
			if err := tx.PutRepoEnvSettings(ctx, rec); err != nil {
				return err
			}
			return tx.AppendAudit(ctx, bsAudit(NewID("aud"), r.ID))
		})
	}
	if err := put(rec); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetRepoEnvSettings(ctx, r.ID)
	if err != nil || !got.AgentCopy || len(got.ProfilePaths) != 1 || got.ProfilePaths[0] != ".env" {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	rec.ComposeEnabled = true // compose without a file is refused
	if err := put(rec); err == nil {
		t.Fatal("compose without file accepted")
	}
	rec.UpdatedAt = bsNow.Add(0)
	rec.ComposeEnabled, rec.ProfilePaths = false, nil
	if err := put(rec); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetRepoEnvSettings(ctx, r.ID); len(got.ProfilePaths) != 0 {
		t.Fatalf("empty profile not stored: %+v", got)
	}
}

func TestPutRepoEnvProfileKeepsComposeColumns(t *testing.T) {
	ctx := context.Background()
	s := NewStore(openTest(t), nil)
	_, r := seed(t, s)
	commit := func(fn func(tx *Tx) error) error {
		return s.Commit(ctx, func(tx *Tx) error {
			if err := fn(tx); err != nil {
				return err
			}
			return tx.AppendAudit(ctx, bsAudit(NewID("aud"), r.ID))
		})
	}
	// no row yet: the upsert creates it with the defaults for the rest
	if err := commit(func(tx *Tx) error { return tx.PutRepoEnvProfile(ctx, r.ID, []string{".env"}, true, bsNow) }); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetRepoEnvSettings(ctx, r.ID)
	if !got.AgentCopy || got.ComposeEnabled || got.ComposeFile != "" || len(got.ProfilePaths) != 1 {
		t.Fatalf("created row: %+v", got)
	}
	full := RepoEnvRecord{Repo: r.ID, ProfilePaths: []string{"a"}, ComposeEnabled: true, ComposeFile: "compose.yml",
		ComposeConfirmedHash: "abc", UpdatedAt: bsNow}
	if err := commit(func(tx *Tx) error { return tx.PutRepoEnvSettings(ctx, full) }); err != nil {
		t.Fatal(err)
	}
	if err := commit(func(tx *Tx) error { return tx.PutRepoEnvProfile(ctx, r.ID, []string{"b", "c"}, false, bsNow) }); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetRepoEnvSettings(ctx, r.ID)
	if !got.ComposeEnabled || got.ComposeFile != "compose.yml" || got.ComposeConfirmedHash != "abc" ||
		got.AgentCopy || len(got.ProfilePaths) != 2 {
		t.Fatalf("profile write changed compose columns: %+v", got)
	}
	err := commit(func(tx *Tx) error { return tx.PutRepoEnvProfile(ctx, "rep_unknown", nil, false, bsNow) })
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown repo: %v", err)
	}
}

// Profile writes racing with compose writes never lose the compose columns.
func TestPutRepoEnvProfileDoesNotLoseConcurrentComposeChange(t *testing.T) {
	ctx := context.Background()
	s := NewStore(openTest(t), nil)
	_, r := seed(t, s)
	for i := range 30 {
		hash := fmt.Sprintf("h%d", i)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			err := s.Commit(ctx, func(tx *Tx) error {
				if err := tx.PutRepoEnvProfile(ctx, r.ID, []string{".env"}, i%2 == 0, bsNow); err != nil {
					return err
				}
				return tx.AppendAudit(ctx, bsAudit(NewID("aud"), r.ID))
			})
			if err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer wg.Done()
			rec := RepoEnvRecord{Repo: r.ID, ComposeEnabled: true, ComposeFile: "compose.yml", ComposeConfirmedHash: hash, UpdatedAt: bsNow}
			err := s.Commit(ctx, func(tx *Tx) error {
				if err := tx.PutRepoEnvSettings(ctx, rec); err != nil {
					return err
				}
				return tx.AppendAudit(ctx, bsAudit(NewID("aud"), r.ID))
			})
			if err != nil {
				t.Error(err)
			}
		}()
		wg.Wait()
		got, _ := s.GetRepoEnvSettings(ctx, r.ID)
		if got.ComposeConfirmedHash != hash || got.ComposeFile != "compose.yml" {
			t.Fatalf("round %d: compose columns changed by a profile write: %+v", i, got)
		}
	}
}
