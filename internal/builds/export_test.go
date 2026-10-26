package builds

import (
	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
)

// Post feeds an interpreter-style input without waiting, like a step process
// reporting its exit.
func (s *Service) Post(id core.BuildID, in corebuilds.Input) { s.post(id, in) }

// SetPanicHook makes the owner call fn before it handles each input; fn may
// panic. Call it before Start.
func (s *Service) SetPanicHook(fn func(corebuilds.Input)) { s.panicHook = fn }

// MatchSegments exposes the glob matcher to the tests.
var MatchSegments = matchSegments
