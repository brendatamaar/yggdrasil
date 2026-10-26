package tasks

import (
	"context"
	"errors"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core/guard"
	"github.com/brendatamaar/yggdrasil/internal/git"
)

// errSnapshotLost: the snapshot did not answer in time. The core has no
// timers, so the shell turns it into TurnAborted.
var errSnapshotLost = errors.New("the snapshot did not answer in time")

// checker runs guard.StepIntegrity for one folder of an Ask turn.
type checker struct {
	dir   string
	state guard.Integrity
}

// verdict is what the integrity commands asked for.
type verdict struct {
	stop    bool // StopTask
	flag    bool // FlagTask, Violated or Unverifiable
	aborted bool // a snapshot was lost or cancelled
	audits  []auditSpec
}

func (v verdict) violated() bool { return v.stop || v.flag }

func (v *verdict) merge(o verdict) {
	v.stop = v.stop || o.stop
	v.flag = v.flag || o.flag
	v.aborted = v.aborted || o.aborted
	v.audits = append(v.audits, o.audits...)
}

const maxAuditedChanges = 50

// integrityStep feeds one input to the checker and interprets the commands:
// TakeSnapshot (with one retry, and a timeout that becomes TurnAborted),
// StopTask, FlagTask and Audit. Nothing here writes to a working tree; there is
// no revert (invariant 9).
func (s *Service) integrityStep(ctx context.Context, c *checker, in guard.IntegrityInput) verdict {
	var v verdict
	for in != nil {
		next, cmds := guard.StepIntegrity(c.state, in)
		c.state = next
		in = nil
		for _, cmd := range cmds {
			switch x := cmd.(type) {
			case guard.TakeSnapshot:
				snap, err := s.takeSnapshot(ctx, c.dir)
				switch {
				case err == nil:
					in = guard.SnapshotTaken{Turn: x.Turn, Phase: x.Phase, Snap: snap}
				case errors.Is(err, errSnapshotLost), ctx.Err() != nil:
					v.aborted = true
					in = guard.TurnAborted{Turn: x.Turn}
				default:
					s.c.Log.Warn("integrity snapshot failed", "phase", x.Phase, "err", err)
					reason := "the snapshot failed"
					if errors.Is(err, git.ErrSnapshotTooLarge) {
						reason = "the working tree is too large to check"
					}
					in = guard.SnapshotFailed{Turn: x.Turn, Phase: x.Phase, Reason: reason}
				}
			case guard.StopTask:
				v.stop = true
			case guard.FlagTask:
				v.flag = true
				paths := make([]string, 0, len(x.Changes))
				for _, ch := range x.Changes {
					if len(paths) == maxAuditedChanges {
						break
					}
					paths = append(paths, ch.Path)
				}
				v.audits = append(v.audits, auditSpec{Action: "guard.integrity", Detail: map[string]any{
					"outcome": "flagged", "turn": string(x.Turn), "changes": paths, "changeCount": len(x.Changes),
				}})
			case guard.Audit:
				d := map[string]any{"outcome": x.Entry.Outcome, "turn": string(x.Entry.Turn)}
				if len(x.Entry.Files) > 0 {
					d["files"] = x.Entry.Files[:min(len(x.Entry.Files), maxAuditedChanges)]
				}
				v.audits = append(v.audits, auditSpec{Action: x.Entry.Action, Detail: d})
			}
		}
	}
	switch c.state.(type) {
	case guard.Violated, guard.Unverifiable:
		v.flag = true
	}
	return v
}

// takeSnapshot reads the snapshot with a timeout, and one retry after a short
// wait for an ordinary failure. A too-large tree is never retried. A snapshot
// that does not answer in time is errSnapshotLost.
func (s *Service) takeSnapshot(ctx context.Context, dir string) (guard.Snapshot, error) {
	for try := 0; ; try++ {
		cctx, cancel := context.WithTimeout(ctx, s.c.SnapshotTimeout)
		snap, err := s.c.Snapshotter.Snapshot(cctx, dir)
		lost := cctx.Err() == context.DeadlineExceeded && ctx.Err() == nil
		cancel()
		switch {
		case err == nil:
			return snap, nil
		case ctx.Err() != nil:
			return guard.Snapshot{}, ctx.Err()
		case lost:
			return guard.Snapshot{}, errSnapshotLost
		case errors.Is(err, git.ErrSnapshotTooLarge), try > 0:
			return guard.Snapshot{}, err
		}
		select {
		case <-time.After(s.c.SnapshotRetry):
		case <-ctx.Done():
			return guard.Snapshot{}, ctx.Err()
		}
	}
}
