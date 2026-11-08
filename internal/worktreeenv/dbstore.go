package worktreeenv

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/audit"
	"github.com/brendatamaar/yggdrasil/internal/core"
	env "github.com/brendatamaar/yggdrasil/internal/core/worktreeenv"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// DBStore implements PortStore, SettingsStore and AuditSink on the SQLite
// store. Every write is one Store.Commit with its audit entry.
type DBStore struct {
	S *db.Store
	// Redactor masks secrets in audit entries (invariant 4); the default
	// removes token-shaped text.
	Redactor safety.Redactor
}

// prep makes the entry ready for the log: checked and redacted, keeping the
// ID and time the caller set.
func (d DBStore) prep(e core.AuditEntry) (core.AuditEntry, error) {
	r := d.Redactor
	if r == nil {
		r = safety.NewRedactor()
	}
	p, err := audit.Prepare(e, r, e.ID, e.At)
	if err != nil {
		return core.AuditEntry{}, fmt.Errorf("prepare audit entry: %w", err)
	}
	return p, nil
}

func mapErr(err error) error {
	switch {
	case errors.Is(err, db.ErrConflict):
		return fmt.Errorf("%w: %w", ErrConflict, err)
	case errors.Is(err, db.ErrNotFound):
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	}
	return err
}

// LoadBlocks implements PortStore.
func (d DBStore) LoadBlocks(ctx context.Context) (map[core.WorktreeID]core.PortBlock, error) {
	rows, err := d.S.ListLiveWorktreePorts(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[core.WorktreeID]core.PortBlock, len(rows))
	for _, r := range rows {
		b, err := env.BlockFromRow(r.Block.Base, r.Block.Size)
		if err != nil {
			return nil, fmt.Errorf("stored block of %s: %w", r.Worktree, err)
		}
		out[r.Worktree] = b
	}
	return out, nil
}

// SaveBlock implements PortStore.
func (d DBStore) SaveBlock(ctx context.Context, w core.WorktreeID, b core.PortBlock, at time.Time, audit core.AuditEntry) error {
	audit, err := d.prep(audit)
	if err != nil {
		return err
	}
	base, size := env.RowOf(b)
	err = d.S.Commit(ctx, func(tx *db.Tx) error {
		if err := tx.InsertWorktreePort(ctx, w, core.PortBlock{Base: base, Size: size}, at); err != nil {
			return err
		}
		return tx.AppendAudit(ctx, audit)
	})
	return mapErr(err)
}

// ReleaseBlock implements PortStore.
func (d DBStore) ReleaseBlock(ctx context.Context, w core.WorktreeID, at time.Time, audit core.AuditEntry) (bool, error) {
	audit, err := d.prep(audit)
	if err != nil {
		return false, err
	}
	var released bool
	err = d.S.Commit(ctx, func(tx *db.Tx) error {
		var err error
		released, err = tx.ReleaseWorktreePort(ctx, w, at)
		if err != nil || !released {
			return err
		}
		return tx.AppendAudit(ctx, audit)
	})
	return released && err == nil, mapErr(err)
}

// RepoEnv implements SettingsStore. Stored paths are parsed again here, so a
// bad row cannot reach the file copy.
func (d DBStore) RepoEnv(ctx context.Context, repo core.RepoID) (RepoEnv, error) {
	rec, err := d.S.GetRepoEnvSettings(ctx, repo)
	if err != nil {
		return RepoEnv{}, mapErr(err)
	}
	paths, err := env.ParseProfile(rec.ProfilePaths)
	if err != nil {
		return RepoEnv{}, fmt.Errorf("stored profile of repo %s: %w", repo, err)
	}
	return RepoEnv{Profile: paths, AgentCopy: rec.AgentCopy}, nil
}

// PutRepoEnv implements SettingsStore. The compose fields of the stored row
// are kept as they are.
func (d DBStore) PutRepoEnv(ctx context.Context, repo core.RepoID, e RepoEnv, at time.Time, audit core.AuditEntry) error {
	audit, err := d.prep(audit)
	if err != nil {
		return err
	}
	rec, err := d.S.GetRepoEnvSettings(ctx, repo)
	if err != nil {
		return mapErr(err)
	}
	rec.ProfilePaths = make([]string, len(e.Profile))
	for i, p := range e.Profile {
		rec.ProfilePaths[i] = string(p)
	}
	rec.AgentCopy, rec.UpdatedAt = e.AgentCopy, at
	return mapErr(d.S.Commit(ctx, func(tx *db.Tx) error {
		if err := tx.PutRepoEnvSettings(ctx, rec); err != nil {
			return err
		}
		return tx.AppendAudit(ctx, audit)
	}))
}

// PortRange implements SettingsStore.
func (d DBStore) PortRange(ctx context.Context) (env.Range, error) {
	s, err := d.S.GetSetting(ctx, db.PortRangeSetting)
	if errors.Is(err, db.ErrNotFound) {
		return env.DefaultRange, nil
	}
	if err != nil {
		return env.Range{}, err
	}
	return env.ParseRange(s.Value)
}

// SetPortRange implements SettingsStore.
func (d DBStore) SetPortRange(ctx context.Context, r env.Range, audit core.AuditEntry) error {
	audit, err := d.prep(audit)
	if err != nil {
		return err
	}
	version := db.SettingNew
	if s, err := d.S.GetSetting(ctx, db.PortRangeSetting); err == nil {
		version = s.Version
	} else if !errors.Is(err, db.ErrNotFound) {
		return err
	}
	return mapErr(d.S.Commit(ctx, func(tx *db.Tx) error {
		if _, err := tx.SetSetting(ctx, db.PortRangeSetting, r.String(), version); err != nil {
			return err
		}
		return tx.AppendAudit(ctx, audit)
	}))
}

// Append implements AuditSink.
func (d DBStore) Append(ctx context.Context, e core.AuditEntry) error {
	e, err := d.prep(e)
	if err != nil {
		return err
	}
	return d.S.Commit(ctx, func(tx *db.Tx) error { return tx.AppendAudit(ctx, e) })
}
