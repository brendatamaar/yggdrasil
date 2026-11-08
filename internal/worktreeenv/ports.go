package worktreeenv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	env "github.com/brendatamaar/yggdrasil/internal/core/worktreeenv"
)

// maxAllocTries bounds the reload-and-retry when two allocations race
// (docs/design/worktree-env.md, Failures).
const maxAllocTries = 3

// firstProbeBlocks is how many free blocks past the live ones the first probe
// covers; the whole range is probed only when that was not enough.
const firstProbeBlocks = 8

// BlockFor returns the worktree's port block, allocating one when it has none
// (idempotent). Dev servers call it; on ErrRangeFull they start without port
// injection and show a warning.
func (s *Service) BlockFor(ctx context.Context, w core.WorktreeID) (core.PortBlock, error) {
	return s.Allocate(ctx, w)
}

// Allocate gives the worktree the lowest free aligned block of the configured
// range that has no listening port.
func (s *Service) Allocate(ctx context.Context, w core.WorktreeID) (core.PortBlock, error) {
	if err := w.Validate(); err != nil {
		return core.PortBlock{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var last error
	for try := 0; try < maxAllocTries; try++ {
		b, retry, err := s.allocateOnce(ctx, w, try == 0)
		if err == nil {
			return b, nil
		}
		last = err
		if !retry {
			return core.PortBlock{}, err
		}
	}
	return core.PortBlock{}, fmt.Errorf("allocate port block for %s: %w", w, last)
}

func (s *Service) loadTable(ctx context.Context, auditConflicts bool) (env.Table, error) {
	rng, err := s.c.Settings.PortRange(ctx)
	if err != nil {
		return env.Table{}, &Error{Msg: "The port range in settings is not valid. Use the form 20000-29999 and try again.", Err: err}
	}
	blocks, err := s.c.Ports.LoadBlocks(ctx)
	if err != nil {
		return env.Table{}, fmt.Errorf("load port blocks: %w", err)
	}
	t, cmds := env.Allocate(env.Table{Range: rng}, env.Loaded{Blocks: blocks})
	if auditConflicts {
		for _, c := range cmds {
			if a, ok := c.(env.AllocAudit); ok {
				if err := s.appendAlloc(ctx, a); err != nil {
					return env.Table{}, err
				}
			}
		}
	}
	return t, nil
}

func (s *Service) allocAudit(a env.AllocAudit) core.AuditEntry {
	detail, _ := json.Marshal(map[string]int{"base": a.Block.Base, "size": a.Block.Size, "lo": a.Range.Lo, "hi": a.Range.Hi})
	subject := "settings:" + string(a.Action)
	if a.Worktree != "" {
		subject = wtSubject(a.Worktree)
	}
	return auditEntry(s.c.NewID("aud"), "system", string(a.Action), subject, s.c.Now().UTC(), detail)
}

func (s *Service) appendAlloc(ctx context.Context, a env.AllocAudit) error {
	if err := s.c.Audit.Append(ctx, s.allocAudit(a)); err != nil {
		return fmt.Errorf("write audit entry: %w", err)
	}
	return nil
}

// allocateOnce runs the decision once. retry says that reloading may help.
func (s *Service) allocateOnce(ctx context.Context, w core.WorktreeID, auditConflicts bool) (core.PortBlock, bool, error) {
	t, err := s.loadTable(ctx, auditConflicts)
	if err != nil {
		return core.PortBlock{}, false, err
	}
	if b, ok := t.Live[w]; ok {
		return b, false, nil
	}
	hi := min(t.Range.Hi, t.Range.Lo+(len(t.Live)+firstProbeBlocks)*core.PortBlockSize-1)
	for {
		busy, perr := s.c.Prober.Busy(ctx, t.Range.Lo, hi)
		if perr != nil {
			if ctx.Err() != nil {
				return core.PortBlock{}, false, ctx.Err()
			}
			busy = nil // a failed probe counts as free
		}
		persist, audit, refused := decide(env.Table{Range: env.Range{Lo: t.Range.Lo, Hi: hi}, Live: t.Live}, w, busy, s.c.Now())
		if refused {
			if hi < t.Range.Hi {
				hi = t.Range.Hi // widen the probe to the whole range, then decide again
				continue
			}
			return core.PortBlock{}, false, &Error{
				Msg: "No free port block is left. Widen the port range in settings; the worktree still works, but dev servers start without reserved ports.",
				Err: ErrRangeFull,
			}
		}
		if persist == nil || audit == nil {
			return core.PortBlock{}, false, errors.New("worktreeenv: internal error: allocation without a block")
		}
		if err := s.c.Ports.SaveBlock(ctx, persist.Worktree, persist.Block, s.c.Now().UTC(), s.allocAudit(*audit)); err != nil {
			return core.PortBlock{}, errors.Is(err, ErrConflict), fmt.Errorf("save port block: %w", err)
		}
		return persist.Block, false, nil
	}
}

// decide runs the core's allocation and picks out its commands.
func decide(t env.Table, w core.WorktreeID, busy []int, now time.Time) (persist *env.PersistBlock, audit *env.AllocAudit, refused bool) {
	_, cmds := env.Allocate(t, env.AllocateRequested{Worktree: w, Busy: busy, Now: now})
	for _, c := range cmds {
		switch v := c.(type) {
		case env.PersistBlock:
			persist = &v
		case env.AllocAudit:
			audit = &v
		case env.AllocReply:
			refused = v.Refusal == env.RefusedRangeFull
		}
	}
	return persist, audit, refused
}

// Release frees the worktree's block for reuse. The row stays as history.
func (s *Service) Release(ctx context.Context, w core.WorktreeID) error {
	if err := w.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.loadTable(ctx, false)
	if err != nil {
		return err
	}
	_, cmds := env.Allocate(t, env.Released{Worktree: w, Now: s.c.Now()})
	for _, c := range cmds {
		if a, ok := c.(env.AllocAudit); ok {
			if _, err := s.c.Ports.ReleaseBlock(ctx, w, s.c.Now().UTC(), s.allocAudit(a)); err != nil {
				return fmt.Errorf("release port block: %w", err)
			}
		}
	}
	return nil
}
