package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db/gen"
)

// BuildSettingsRecord is a repo's stored build settings (BL-08, WE-05). The
// store keeps the verify override as JSON text and does not interpret it:
// internal/builds parses and validates it. This package cannot import
// internal/builds.
type BuildSettingsRecord struct {
	Repo core.RepoID
	// Verify is the verify override as JSON; empty means "use the default".
	// It must be valid JSON when set.
	Verify    json.RawMessage
	FullTests bool
	Bootstrap bool
	// Version starts at 0 for the first Put and grows by one with each Put.
	Version   int64
	UpdatedAt time.Time
}

// DefaultBuildSettingsRecord is what a repo without a stored row gets.
func DefaultBuildSettingsRecord(repo core.RepoID) BuildSettingsRecord {
	return BuildSettingsRecord{Repo: repo, Bootstrap: true}
}

func buildSettingsFrom(r gen.RepoBuildSetting) (BuildSettingsRecord, error) {
	at, err := parseTime(r.UpdatedAt)
	if err != nil {
		return BuildSettingsRecord{}, fmt.Errorf("build settings of %s: %w", r.RepoID, err)
	}
	rec := BuildSettingsRecord{Repo: core.RepoID(r.RepoID), FullTests: r.FullTests != 0,
		Bootstrap: r.Bootstrap != 0, Version: r.Version, UpdatedAt: at}
	if r.Verify.Valid {
		rec.Verify = json.RawMessage(r.Verify.String)
	}
	return rec, nil
}

// GetBuildSettings returns the settings of a repo. A repo without a row gets
// the defaults (Version 0, zero UpdatedAt); an unknown repo is ErrNotFound.
func (s *Store) GetBuildSettings(ctx context.Context, repo core.RepoID) (BuildSettingsRecord, error) {
	row, err := s.db.reader().GetRepoBuildSettings(ctx, string(repo))
	if err == nil {
		return buildSettingsFrom(row)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return BuildSettingsRecord{}, fmt.Errorf("get build settings %s: %w", repo, err)
	}
	if _, err := s.GetRepoIncludingUntracked(ctx, repo); err != nil {
		return BuildSettingsRecord{}, err
	}
	return DefaultBuildSettingsRecord(repo), nil
}

// PutBuildSettings writes the settings inside a transaction (an upsert that
// bumps the version). Call it with AppendAudit in the same Store.Commit
// (invariant 8); an unknown repo is ErrNotFound and Commit writes nothing.
// rec.UpdatedAt is the write time and must not be zero.
func (tx *Tx) PutBuildSettings(ctx context.Context, rec BuildSettingsRecord) error {
	if err := rec.Repo.Validate(); err != nil {
		return err
	}
	if rec.UpdatedAt.IsZero() {
		return errors.New("build settings need a write time")
	}
	if len(rec.Verify) > 0 && !json.Valid(rec.Verify) {
		return errors.New("the verify override is not valid JSON")
	}
	err := tx.q.UpsertRepoBuildSettings(ctx, gen.UpsertRepoBuildSettingsParams{
		RepoID: string(rec.Repo), Verify: nullText(string(rec.Verify)),
		FullTests: b2i(rec.FullTests), Bootstrap: b2i(rec.Bootstrap), UpdatedAt: fmtTime(rec.UpdatedAt),
	})
	if err != nil {
		return fmt.Errorf("put build settings %s: %w", rec.Repo, err)
	}
	return nil
}
