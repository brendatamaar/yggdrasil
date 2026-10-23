package builds

import (
	"context"

	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
)

// StartStepAgain hands a StartStep command straight to the interpreter, as a
// replayed or duplicated command would. It returns how many follow-up inputs
// the interpreter produced. Call it after Start.
func (s *Service) StartStepAgain(ctx context.Context, c corebuilds.StartStep) int {
	return len(s.startStep(ctx, c))
}
