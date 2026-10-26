package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/audit"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/guard"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// TrustAgent is the adapter whose config paths the trust endpoints scan with:
// the agent that reads its config from the repo.
const TrustAgent = "claude-code"

// TrustStatus scans a repo's agent config and returns its trust state (SF-09).
// A repo that cannot be scanned completely is Untrusted. A repo that does not
// exist is db.ErrNotFound.
func (s *Service) TrustStatus(ctx context.Context, repo core.RepoID) (guard.Trust, error) {
	return s.trustStep(ctx, repo, nil)
}

// Trust records trust in hash, the config the user was shown. When that hash
// is no longer the repo's config, it returns the unchanged (not Trusted) state
// and a nil error.
func (s *Service) Trust(ctx context.Context, repo core.RepoID, hash string) (guard.Trust, error) {
	return s.trustStep(ctx, repo, func(now time.Time) guard.TrustInput {
		return guard.UserTrusts{Repo: repo, Hash: hash, Now: now}
	})
}

// Revoke withdraws trust from a repo.
func (s *Service) Revoke(ctx context.Context, repo core.RepoID) (guard.Trust, error) {
	return s.trustStep(ctx, repo, func(now time.Time) guard.TrustInput {
		return guard.UserRevokes{Repo: repo, Now: now}
	})
}

// trustStep scans the repo, folds the scan into the stored state, applies the
// user's input (nil for a plain status) and persists the resulting commands
// and audit entries in one Commit.
func (s *Service) trustStep(ctx context.Context, repoID core.RepoID, user func(time.Time) guard.TrustInput) (guard.Trust, error) {
	repo, err := s.c.Store.GetRepo(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("find repo %s: %w", repoID, err)
	}
	trusted, hash, err := s.c.Store.GetRepoTrust(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("read trust of repo %s: %w", repoID, err)
	}
	var paths, names []string
	if a, ok := s.c.Adapters.Get(TrustAgent); ok {
		if p, ok := a.(agents.RepoConfigProvider); ok {
			paths = p.ConfigPaths()
		}
		if n, ok := a.(agents.ConfigNameProvider); ok {
			names = n.ConfigNames()
		}
	}
	now := s.c.Now().UTC()
	var st guard.Trust = guard.Unknown{Repo: repoID}
	if trusted && len(hash) == 64 {
		st = guard.Trusted{Repo: repoID, Hash: hash}
	}
	var cmds []guard.Command
	scan, serr := s.c.Scanner.Scan(ctx, repo.Path, paths, names)
	if serr != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		s.c.Log.Warn("agent config scan incomplete", "repo", repoID, "err", serr)
		// The core clears nothing on a failed scan; the stored trust is
		// withdrawn here as the admission path does.
		st, _ = guard.StepTrust(st, guard.ScanFailed{Repo: repoID, Why: serr.Error()})
		if trusted {
			cmds = append(cmds, guard.ClearTrust{Repo: repoID},
				guard.Audit{Entry: guard.AuditEntry{Action: "guard.trust", Outcome: "scan_incomplete", Repo: repoID}})
		}
		// Nothing the user asked can be applied to a config we cannot see.
		return st, s.persistTrust(ctx, cmds)
	}
	// Trusted states keep the scan they were computed from, so a revoke can
	// show what was trusted.
	if t, ok := st.(guard.Trusted); ok && t.Hash == scan.Hash {
		t.Scan = scan
		st = t
	}
	var c []guard.Command
	st, c = guard.StepTrust(st, guard.Scanned{Repo: repoID, Scan: scan, Now: now})
	cmds = append(cmds, c...)
	if user != nil {
		st, c = guard.StepTrust(st, user(now))
		cmds = append(cmds, c...)
	}
	return st, s.persistTrust(ctx, cmds)
}

// persistTrust applies PersistTrust, ClearTrust and Audit commands in one
// Commit. Nothing to do is not an error.
func (s *Service) persistTrust(ctx context.Context, cmds []guard.Command) error {
	if len(cmds) == 0 {
		return nil
	}
	var entries []core.AuditEntry
	for _, c := range cmds {
		a, ok := c.(guard.Audit)
		if !ok {
			continue
		}
		detail := map[string]any{"outcome": a.Entry.Outcome}
		if len(a.Entry.Files) > 0 {
			detail["files"] = a.Entry.Files
		}
		raw, err := json.Marshal(detail)
		if err != nil {
			return fmt.Errorf("encode audit detail: %w", err)
		}
		e, err := audit.Prepare(core.AuditEntry{Actor: "user", Action: a.Entry.Action, Subject: "repo:" + string(a.Entry.Repo), Detail: raw},
			s.c.Redactor, core.AuditID(s.c.NewID("aud")), s.c.Now())
		if err != nil {
			return fmt.Errorf("prepare audit entry: %w", err)
		}
		entries = append(entries, e)
	}
	return s.c.Store.Commit(ctx, func(tx *db.Tx) error {
		for _, c := range cmds {
			switch c := c.(type) {
			case guard.PersistTrust:
				if err := tx.SetRepoTrust(ctx, c.Repo, true, c.Hash); err != nil {
					return err
				}
			case guard.ClearTrust:
				if err := tx.SetRepoTrust(ctx, c.Repo, false, ""); err != nil {
					return err
				}
			}
		}
		for _, e := range entries {
			if err := tx.AppendAudit(ctx, e); err != nil {
				return err
			}
		}
		return nil
	})
}
