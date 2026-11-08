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

// PortRangeSetting is the settings key that holds the port range for worktree
// port blocks, written as "lo-hi" (WE-02). Absent means 20000-29999.
const PortRangeSetting = "worktreeenv.port_range"

// WorktreePortRecord is a live port block of a worktree.
type WorktreePortRecord struct {
	Worktree    core.WorktreeID
	Block       core.PortBlock
	AllocatedAt time.Time
}

// ListLiveWorktreePorts returns the blocks that are not released, lowest base
// first.
func (s *Store) ListLiveWorktreePorts(ctx context.Context) ([]WorktreePortRecord, error) {
	rows, err := s.db.reader().ListLiveWorktreePorts(ctx)
	if err != nil {
		return nil, fmt.Errorf("list worktree ports: %w", err)
	}
	out := make([]WorktreePortRecord, 0, len(rows))
	for _, r := range rows {
		at, err := parseTime(r.AllocatedAt)
		if err != nil {
			return nil, fmt.Errorf("worktree port of %s: %w", r.WorktreeID, err)
		}
		out = append(out, WorktreePortRecord{
			Worktree: core.WorktreeID(r.WorktreeID), Block: core.PortBlock{Base: int(r.Base), Size: int(r.Size)}, AllocatedAt: at,
		})
	}
	return out, nil
}

// InsertWorktreePort stores a live block for a worktree. A worktree that
// already has a live block, or a base that a live block already uses, is
// ErrConflict; an unknown worktree is ErrNotFound. Call it with AppendAudit in
// the same Store.Commit (invariant 8).
func (tx *Tx) InsertWorktreePort(ctx context.Context, w core.WorktreeID, b core.PortBlock, at time.Time) error {
	if err := w.Validate(); err != nil {
		return err
	}
	if err := b.Validate(); err != nil {
		return err
	}
	if err := needTime("insert worktree port", at); err != nil {
		return err
	}
	err := tx.q.InsertWorktreePort(ctx, gen.InsertWorktreePortParams{
		WorktreeID: string(w), Base: int64(b.Base), Size: int64(b.Size), AllocatedAt: fmtTime(at),
	})
	if err != nil {
		return fmt.Errorf("insert worktree port %s: %w", w, err)
	}
	return nil
}

// ReleaseWorktreePort marks the worktree's live block released. It reports
// whether there was one; a second call returns false and changes nothing. No
// row is deleted. Append the audit entry only when it returns true.
func (tx *Tx) ReleaseWorktreePort(ctx context.Context, w core.WorktreeID, at time.Time) (bool, error) {
	if err := needTime("release worktree port", at); err != nil {
		return false, err
	}
	n, err := tx.q.ReleaseWorktreePort(ctx, gen.ReleaseWorktreePortParams{
		ReleasedAt: sql.NullString{String: fmtTime(at), Valid: true}, WorktreeID: string(w),
	})
	if err != nil {
		return false, fmt.Errorf("release worktree port %s: %w", w, err)
	}
	return n > 0, nil
}

// RepoEnvRecord is a repo's worktree environment settings (WE-01, WE-03).
// ProfilePaths is stored as given; internal/worktreeenv validates it with
// ParseProfile at the boundary.
type RepoEnvRecord struct {
	Repo                 core.RepoID
	ProfilePaths         []string
	AgentCopy            bool
	ComposeEnabled       bool
	ComposeFile          string
	ComposeConfirmedHash string
	UpdatedAt            time.Time
}

// GetRepoEnvSettings returns a repo's settings. A repo without a row gets the
// defaults (no profile, agent copy off, compose off); an unknown repo is
// ErrNotFound.
func (s *Store) GetRepoEnvSettings(ctx context.Context, repo core.RepoID) (RepoEnvRecord, error) {
	row, err := s.db.reader().GetRepoEnvSettings(ctx, string(repo))
	if err == nil {
		var paths []string
		if err := json.Unmarshal([]byte(row.ProfilePaths), &paths); err != nil {
			return RepoEnvRecord{}, fmt.Errorf("profile paths of %s: %w", repo, err)
		}
		at, err := parseTime(row.UpdatedAt)
		if err != nil {
			return RepoEnvRecord{}, fmt.Errorf("repo env settings of %s: %w", repo, err)
		}
		return RepoEnvRecord{
			Repo: repo, ProfilePaths: paths, AgentCopy: row.AgentCopy != 0,
			ComposeEnabled: row.ComposeEnabled != 0, ComposeFile: row.ComposeFile,
			ComposeConfirmedHash: row.ComposeConfirmedHash, UpdatedAt: at,
		}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return RepoEnvRecord{}, fmt.Errorf("get repo env settings %s: %w", repo, err)
	}
	if _, err := s.GetRepoIncludingUntracked(ctx, repo); err != nil {
		return RepoEnvRecord{}, err
	}
	return RepoEnvRecord{Repo: repo}, nil
}

// PutRepoEnvProfile writes only the profile paths and the agent-copy toggle
// of a repo (an upsert). The compose columns of an existing row are left as
// they are, in the same statement, so a concurrent compose change is never
// lost. Call it with AppendAudit in the same Store.Commit; an unknown repo is
// ErrNotFound.
func (tx *Tx) PutRepoEnvProfile(ctx context.Context, repo core.RepoID, paths []string, agentCopy bool, at time.Time) error {
	if err := repo.Validate(); err != nil {
		return err
	}
	if err := needTime("repo env profile", at); err != nil {
		return err
	}
	if paths == nil {
		paths = []string{}
	}
	raw, err := json.Marshal(paths)
	if err != nil {
		return fmt.Errorf("encode profile paths: %w", err)
	}
	err = tx.q.UpsertRepoEnvProfile(ctx, gen.UpsertRepoEnvProfileParams{
		RepoID: string(repo), ProfilePaths: string(raw), AgentCopy: b2i(agentCopy), UpdatedAt: fmtTime(at),
	})
	if err != nil {
		return fmt.Errorf("put repo env profile %s: %w", repo, err)
	}
	return nil
}

// PutRepoEnvSettings writes the settings (an upsert). Call it with AppendAudit
// in the same Store.Commit (invariant 8); an unknown repo is ErrNotFound.
func (tx *Tx) PutRepoEnvSettings(ctx context.Context, rec RepoEnvRecord) error {
	if err := rec.Repo.Validate(); err != nil {
		return err
	}
	if err := needTime("repo env settings", rec.UpdatedAt); err != nil {
		return err
	}
	if rec.ComposeEnabled && rec.ComposeFile == "" {
		return errors.New("compose needs a compose file")
	}
	paths := rec.ProfilePaths
	if paths == nil {
		paths = []string{}
	}
	raw, err := json.Marshal(paths)
	if err != nil {
		return fmt.Errorf("encode profile paths: %w", err)
	}
	err = tx.q.UpsertRepoEnvSettings(ctx, gen.UpsertRepoEnvSettingsParams{
		RepoID: string(rec.Repo), ProfilePaths: string(raw), AgentCopy: b2i(rec.AgentCopy),
		ComposeEnabled: b2i(rec.ComposeEnabled), ComposeFile: rec.ComposeFile,
		ComposeConfirmedHash: rec.ComposeConfirmedHash, UpdatedAt: fmtTime(rec.UpdatedAt),
	})
	if err != nil {
		return fmt.Errorf("put repo env settings %s: %w", rec.Repo, err)
	}
	return nil
}
