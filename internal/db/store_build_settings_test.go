package db

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

var bsNow = time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)

func bsAudit(id string, repo core.RepoID) core.AuditEntry {
	return core.AuditEntry{ID: core.AuditID(id), At: bsNow, Actor: "user", Action: "build_settings.update",
		Subject: "repo:" + string(repo), Detail: json.RawMessage(`{"fullTests":true}`)}
}

// put is the write path callers use: settings and audit in one Commit.
func put(ctx context.Context, s *Store, rec BuildSettingsRecord, audit core.AuditEntry) error {
	return s.Commit(ctx, func(tx *Tx) error {
		if err := tx.PutBuildSettings(ctx, rec); err != nil {
			return err
		}
		return tx.AppendAudit(ctx, audit)
	})
}

func TestBuildSettingsDefaultsWithoutRow(t *testing.T) {
	ctx := context.Background()
	s := NewStore(openTest(t), nil)
	_, r := seed(t, s)
	got, err := s.GetBuildSettings(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := DefaultBuildSettingsRecord(r.ID)
	if got.Repo != want.Repo || len(got.Verify) != 0 || got.FullTests || !got.Bootstrap || got.Version != 0 {
		t.Fatalf("got %+v, want defaults", got)
	}
}

func TestBuildSettingsUnknownRepoIsNotFound(t *testing.T) {
	ctx := context.Background()
	s := NewStore(openTest(t), nil)
	if _, err := s.GetBuildSettings(ctx, "rep_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get: %v", err)
	}
	rec := BuildSettingsRecord{Repo: "rep_missing", Bootstrap: true, UpdatedAt: bsNow}
	if err := put(ctx, s, rec, bsAudit("aud_1", "rep_missing")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("put: %v", err)
	}
	if entries, _ := s.ListAudit(ctx, "repo:rep_missing", 10); len(entries) != 0 {
		t.Fatalf("an audit entry was written for an unknown repo: %+v", entries)
	}
}

func TestBuildSettingsRoundTripAndVersion(t *testing.T) {
	ctx := context.Background()
	s := NewStore(openTest(t), nil)
	_, r := seed(t, s)
	rec := BuildSettingsRecord{Repo: r.ID, Verify: json.RawMessage(`[{"name":"a","argv":["x"]}]`), FullTests: true, Bootstrap: false, UpdatedAt: bsNow}
	if err := put(ctx, s, rec, bsAudit("aud_1", r.ID)); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetBuildSettings(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Verify) != string(rec.Verify) || !got.FullTests || got.Bootstrap || got.Version != 0 || !got.UpdatedAt.Equal(bsNow) {
		t.Fatalf("got %+v", got)
	}
	rec.Verify, rec.FullTests, rec.Bootstrap, rec.UpdatedAt = nil, false, true, bsNow.Add(time.Hour)
	if err := put(ctx, s, rec, bsAudit("aud_2", r.ID)); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetBuildSettings(ctx, r.ID)
	if len(got.Verify) != 0 || got.FullTests || !got.Bootstrap || got.Version != 1 || !got.UpdatedAt.Equal(bsNow.Add(time.Hour)) {
		t.Fatalf("after second put: %+v", got)
	}
}

func TestBuildSettingsAuditInSameTransaction(t *testing.T) {
	ctx := context.Background()
	s := NewStore(openTest(t), nil)
	_, r := seed(t, s)
	rec := BuildSettingsRecord{Repo: r.ID, FullTests: true, Bootstrap: true, UpdatedAt: bsNow}
	if err := put(ctx, s, rec, bsAudit("aud_1", r.ID)); err != nil {
		t.Fatal(err)
	}
	entries, err := s.ListAudit(ctx, "repo:"+string(r.ID), 10)
	if err != nil || len(entries) != 1 || entries[0].Action != "build_settings.update" {
		t.Fatalf("audit = %+v, %v", entries, err)
	}
	// A bad audit entry rolls the settings back.
	rec.FullTests = false
	bad := bsAudit("aud_2", r.ID)
	bad.Actor = ""
	if err := put(ctx, s, rec, bad); err == nil {
		t.Fatal("accepted an audit entry without an actor")
	}
	got, _ := s.GetBuildSettings(ctx, r.ID)
	if !got.FullTests || got.Version != 0 {
		t.Fatalf("settings changed although the audit write failed: %+v", got)
	}
}

func TestBuildSettingsRejectsBadInput(t *testing.T) {
	ctx := context.Background()
	s := NewStore(openTest(t), nil)
	_, r := seed(t, s)
	for name, rec := range map[string]BuildSettingsRecord{
		"zero time":   {Repo: r.ID, Bootstrap: true},
		"bad json":    {Repo: r.ID, Verify: json.RawMessage(`{`), UpdatedAt: bsNow},
		"bad repo id": {Repo: "", UpdatedAt: bsNow},
	} {
		if err := put(ctx, s, rec, bsAudit("aud_x", r.ID)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestBuildSettingsDeletedWithRepo(t *testing.T) {
	// The row cascades with the repo, so Get on a removed repo is ErrNotFound.
	ctx := context.Background()
	d := openTest(t)
	s := NewStore(d, nil)
	_, r := seed(t, s)
	rec := BuildSettingsRecord{Repo: r.ID, Bootstrap: true, UpdatedAt: bsNow}
	if err := put(ctx, s, rec, bsAudit("aud_1", r.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.write.ExecContext(ctx, `DELETE FROM repos WHERE id = ?`, string(r.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetBuildSettings(ctx, r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}
