package tasks

import (
	"context"
	"fmt"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/guard"
)

// touchedRepos are the repos a mode touches: none for Ask (Admit lets Ask
// through whatever the trust), the task's repo for Code and CurrentBranch,
// every repo of the project for Project mode.
func touchedRepos(mode core.TaskMode, repos []core.Repo) []core.Repo {
	switch mode {
	case core.ModeCode, core.ModeCurrentBranch:
		return repos[:min(1, len(repos))]
	case core.ModeProject:
		return repos
	}
	return nil
}

// admit asks guard.Admit whether a turn may run in mode. It builds one trust
// view per repo the mode touches from a fresh scan of the repo's agent config
// (SF-09). Whatever the scan says, a failure makes the view untrusted.
func (s *Service) admit(ctx context.Context, adapter agents.Adapter, mode core.TaskMode, prev *core.TaskMode, optIn bool, repos []core.Repo) (guard.Admission, error) {
	if err := mode.Validate(); err != nil {
		return guard.Admission{Refusal: guard.ReasonModeUnavailable}, nil
	}
	var views []guard.TrustView
	for _, r := range touchedRepos(mode, repos) {
		v, err := s.trustView(ctx, adapter, r)
		if err != nil {
			return guard.Admission{}, err
		}
		views = append(views, v)
	}
	return guard.Admit(guard.Request{
		Mode: mode, Previous: prev, OptIn: optIn, Trust: views,
		Enforcement: guard.Enforcement(adapter.Capabilities().Enforcement),
	}), nil
}

// trustView scans the repo's agent config and runs guard.StepTrust from the
// stored trust. A scan that could not read everything gives an untrusted view
// and clears the stored trust (ScanFailed carry-in); a changed config clears
// it too, as the core asks.
func (s *Service) trustView(ctx context.Context, adapter agents.Adapter, repo core.Repo) (guard.TrustView, error) {
	var paths, names []string
	if p, ok := adapter.(agents.RepoConfigProvider); ok {
		paths = p.ConfigPaths()
	}
	if n, ok := adapter.(agents.ConfigNameProvider); ok {
		names = n.ConfigNames()
	}
	trusted, hash, err := s.c.Store.GetRepoTrust(ctx, repo.ID)
	if err != nil {
		return guard.TrustUntrusted, fmt.Errorf("read trust of repo %s: %w", repo.ID, err)
	}
	var st guard.Trust = guard.Unknown{Repo: repo.ID}
	storedTrust := trusted && len(hash) == 64
	if storedTrust {
		st = guard.Trusted{Repo: repo.ID, Hash: hash}
	}
	scan, serr := s.c.Scanner.Scan(ctx, repo.Path, paths, names)
	if serr != nil {
		if ctx.Err() != nil {
			return guard.TrustUntrusted, ctx.Err()
		}
		s.c.Log.Warn("agent config scan incomplete", "repo", repo.ID, "err", serr)
		next, _ := guard.StepTrust(st, guard.ScanFailed{Repo: repo.ID, Why: serr.Error()})
		if storedTrust {
			if err := s.clearTrust(ctx, repo.ID, "scan_incomplete"); err != nil {
				return guard.TrustUntrusted, fmt.Errorf("clear trust of repo %s: %w", repo.ID, err)
			}
		}
		return guard.View(next), nil
	}
	next, cmds := guard.StepTrust(st, guard.Scanned{Repo: repo.ID, Scan: scan, Now: s.c.Now().UTC()})
	for _, c := range cmds {
		if _, ok := c.(guard.ClearTrust); ok {
			if err := s.clearTrust(ctx, repo.ID, "config_changed"); err != nil {
				return guard.TrustUntrusted, fmt.Errorf("clear trust of repo %s: %w", repo.ID, err)
			}
		}
	}
	return guard.View(next), nil
}
