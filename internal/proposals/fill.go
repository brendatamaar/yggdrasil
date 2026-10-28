package proposals

import (
	"context"
	"errors"

	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

const cannotReadMsg = "Yggdrasil could not read the current state of the branch to pin this proposal to it. Check that the repo is available, then try again."

// dirOf is the folder a target resolves to, or "" when it does not exist.
func (s *Service) dirOf(ctx context.Context, where coreprop.Target) (string, error) {
	switch w := where.(type) {
	case coreprop.InWorktree:
		wt, err := s.cfg.Resolver.Worktree(ctx, w.ID)
		if err != nil {
			if errors.Is(err, db.ErrNotFound) {
				return "", nil
			}
			return "", err
		}
		return wt.Path, nil
	case coreprop.MainCheckout:
		info, err := s.cfg.Resolver.Repo(ctx, w.Repo)
		if err != nil {
			if errors.Is(err, db.ErrNotFound) {
				return "", nil
			}
			return "", err
		}
		return info.Path, nil
	}
	return "", nil
}

// fillExpected completes the checksums a caller may leave out: the branch tip
// (expect_head) of a Commit, Push or MergeRequest and the diff checksum of a
// Commit. It runs before the action is hashed, so what the user confirms
// includes them. A value the caller gave is kept; whether it is right is
// judged at execution. The result is validated with the strict rules, so an
// action that cannot be pinned is refused.
func (s *Service) fillExpected(ctx context.Context, a coreprop.Action) (coreprop.Action, error) {
	var where coreprop.Target
	switch v := a.(type) {
	case coreprop.Commit:
		where = v.Where
	case coreprop.Push:
		where = v.Where
	case coreprop.MergeRequest:
		where = v.Where
	default:
		return a, validated(a)
	}
	dir, err := s.dirOf(ctx, where)
	if err != nil {
		return nil, err
	}
	if dir == "" {
		return nil, refusal(coreprop.RefuseBadFacts, cannotReadMsg)
	}
	head := func() (string, error) {
		h, err := s.cfg.Git.Head(ctx, dir)
		if err != nil {
			return "", refusal(coreprop.RefuseBadFacts, cannotReadMsg)
		}
		return h, nil
	}
	switch v := a.(type) {
	case coreprop.Commit:
		if v.ExpectHead == "" {
			if v.ExpectHead, err = head(); err != nil {
				return nil, err
			}
		}
		if v.ExpectDiffHash == "" {
			dh, derr := s.cfg.Git.DiffHash(ctx, dir, pathStrings(v.Paths))
			if derr != nil {
				return nil, refusal(coreprop.RefuseBadFacts, cannotReadMsg)
			}
			v.ExpectDiffHash = dh
		}
		a = v
	case coreprop.Push:
		if v.ExpectHead == "" {
			if v.ExpectHead, err = head(); err != nil {
				return nil, err
			}
		}
		a = v
	case coreprop.MergeRequest:
		if v.ExpectHead == "" {
			if v.ExpectHead, err = head(); err != nil {
				return nil, err
			}
		}
		a = v
	}
	return a, validated(a)
}

func validated(a coreprop.Action) error {
	if err := a.Validate(); err != nil {
		return refusal(coreprop.RefuseBadFacts, "The proposal is not complete or not valid: "+err.Error())
	}
	return nil
}
