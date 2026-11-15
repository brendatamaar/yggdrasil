package repos

import (
	"context"
	"fmt"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
)

var _ NoiseSource = (*StatusService)(nil)

// NoiseFacts reads only the existing status snapshot. Scope is checked against
// the active source first, so an untrack cannot expose a retained cached view.
// No git invocation, refresh, directory walk or file write happens here.
func (s *StatusService) NoiseFacts(ctx context.Context, id core.RepoID) (corerepos.NoiseFacts, error) {
	if err := ctx.Err(); err != nil {
		return corerepos.NoiseFacts{}, fmt.Errorf("%w: %w", ErrNoiseUnavailable, err)
	}
	if err := id.Validate(); err != nil {
		return corerepos.NoiseFacts{}, fmt.Errorf("%w: %w", ErrNoiseUnavailable, err)
	}
	active, err := s.loadActive(ctx)
	if err != nil {
		return corerepos.NoiseFacts{}, fmt.Errorf("%w: %w", ErrNoiseUnavailable, err)
	}
	var path corerepos.CleanPath
	for _, r := range active {
		if r.ID == id {
			path = r.Path
			break
		}
	}
	if path == "" {
		return corerepos.NoiseFacts{}, ErrNoiseUnavailable
	}
	state, err := s.Snapshot(ctx)
	if err != nil {
		return corerepos.NoiseFacts{}, fmt.Errorf("%w: %w", ErrNoiseUnavailable, err)
	}
	entry, ok := state.Entries[id]
	if !ok || entry.Path != path {
		return corerepos.NoiseFacts{}, ErrNoiseUnavailable
	}
	fresh, ok := entry.View.(corerepos.Fresh)
	if !ok {
		return corerepos.NoiseFacts{}, ErrNoiseUnavailable
	}
	return ParseNoiseSnapshot(id, fresh.Snap)
}

// ParseNoiseSnapshot validates the sampled paths without normalizing or losing
// evidence. Unsupported file names make noise unavailable, leaving ordinary
// repo status intact. Incomplete samples always remain labelled incomplete.
func ParseNoiseSnapshot(id core.RepoID, snap corerepos.Snapshot) (corerepos.NoiseFacts, error) {
	if len(snap.UntrackedPaths) > 5000*4097 || strings.Count(snap.UntrackedPaths, "\x00") >= 5000 {
		return corerepos.NoiseFacts{}, ErrNoiseUnavailable
	}
	f := corerepos.NoiseFacts{Repo: id, UntrackedCount: snap.Untracked, Truncated: snap.UntrackedTruncated}
	if snap.UntrackedPaths != "" {
		f.Sample = strings.Split(snap.UntrackedPaths, "\x00")
	}
	f.Truncated = f.Truncated || len(f.Sample) < f.UntrackedCount
	if err := f.Validate(); err != nil {
		return corerepos.NoiseFacts{}, fmt.Errorf("%w: %w", ErrNoiseUnavailable, err)
	}
	return f, nil
}
