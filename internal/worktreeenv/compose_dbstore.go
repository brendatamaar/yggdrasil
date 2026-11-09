package worktreeenv

import (
	"context"
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	env "github.com/brendatamaar/yggdrasil/internal/core/worktreeenv"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// ComposeSettings are a repo's compose opt-in (WE-03). Off by default.
type ComposeSettings struct {
	Enabled bool
	File    env.RelPath
	// ConfirmedHash is the hash of the compose file the user confirmed; empty
	// when none.
	ConfirmedHash string
}

// ComposeStore keeps the compose opt-in. It writes only the compose columns
// and keeps the profile settings as they are.
type ComposeStore interface {
	// ComposeSettings returns the settings (defaults for a repo without any;
	// an unknown repo is ErrNotFound).
	ComposeSettings(ctx context.Context, repo core.RepoID) (ComposeSettings, error)
	// PutComposeSettings stores the settings and the audit entry in one commit.
	PutComposeSettings(ctx context.Context, repo core.RepoID, c ComposeSettings, at time.Time, audit core.AuditEntry) error
}

var _ ComposeStore = DBStore{}

// ComposeSettings implements ComposeStore. A stored file name is parsed again.
func (d DBStore) ComposeSettings(ctx context.Context, repo core.RepoID) (ComposeSettings, error) {
	rec, err := d.S.GetRepoEnvSettings(ctx, repo)
	if err != nil {
		return ComposeSettings{}, mapErr(err)
	}
	out := ComposeSettings{Enabled: rec.ComposeEnabled, ConfirmedHash: rec.ComposeConfirmedHash}
	if rec.ComposeFile != "" {
		f, err := env.ParseComposeFile(rec.ComposeFile)
		if err != nil {
			return ComposeSettings{}, fmt.Errorf("stored compose file of repo %s: %w", repo, err)
		}
		out.File = f
	}
	return out, nil
}

// PutComposeSettings implements ComposeStore. The profile columns are read
// and written back unchanged in the same statement set; a concurrent profile
// edit within that window is the one thing this can lose (the settings row has
// no compose-only upsert yet).
func (d DBStore) PutComposeSettings(ctx context.Context, repo core.RepoID, c ComposeSettings, at time.Time, audit core.AuditEntry) error {
	audit, err := d.prep(audit)
	if err != nil {
		return err
	}
	rec, err := d.S.GetRepoEnvSettings(ctx, repo)
	if err != nil {
		return mapErr(err)
	}
	rec.ComposeEnabled, rec.ComposeFile, rec.ComposeConfirmedHash = c.Enabled, string(c.File), c.ConfirmedHash
	rec.UpdatedAt = at
	return mapErr(d.S.Commit(ctx, func(tx *db.Tx) error {
		if err := tx.PutRepoEnvSettings(ctx, rec); err != nil {
			return err
		}
		return tx.AppendAudit(ctx, audit)
	}))
}
