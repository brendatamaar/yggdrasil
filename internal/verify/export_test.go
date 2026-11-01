package verify

import (
	"context"

	"github.com/brendatamaar/yggdrasil/internal/core"
	cv "github.com/brendatamaar/yggdrasil/internal/core/verify"
)

// FeedResultForTest delivers a build result as if the poll had mapped it, so
// a test can hand the shell a result the core refuses (the watchdog case).
func (s *Service) FeedResultForTest(ctx context.Context, task core.TaskID, repo core.RepoID, wt core.WorktreeID, build core.BuildID, res cv.BuildResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.feedResult(ctx, key{task: task, repo: repo, wt: wt}, build, res)
}
