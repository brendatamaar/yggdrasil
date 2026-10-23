package repos

import corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"

// Deliver feeds an input to the actions state like a git command reporting its
// result, so a test can deliver a result twice or out of turn.
func (s *ActionsService) Deliver(in corerepos.ActInput) error {
	_, err := s.apply(in)
	return err
}
