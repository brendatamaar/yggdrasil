package builds

import (
	"context"

	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
)

// maxRunning is the number of builds that run at once (BL-03).
const maxRunning = 1

// worker admits queued builds. It runs until ctx is done and re-runs Next each
// time it is woken, so a finished build is followed by the next one (S3).
func (s *Service) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
			s.admitAll(ctx)
		}
	}
}

func (s *Service) admitAll(ctx context.Context) {
	for ctx.Err() == nil {
		refs, err := s.d.Store.ListQueued(ctx)
		if err != nil {
			s.d.Log.Error("builds: list queued", "err", err)
			return
		}
		running, err := s.d.Store.CountRunning(ctx)
		if err != nil {
			s.d.Log.Error("builds: count running", "err", err)
			return
		}
		id, ok := corebuilds.Next(refs, running, maxRunning)
		if !ok {
			return
		}
		res, err := s.send(ctx, id, corebuilds.Admit{Now: s.d.Clock()})
		if err != nil || !res.changed {
			return // an Admit that changes nothing would loop forever
		}
	}
}
