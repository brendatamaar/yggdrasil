package composer

import (
	"context"

	"github.com/brendatamaar/yggdrasil/internal/api"
)

// knownModels is the documented model list per adapter name. It is static on
// purpose: listing models would need a network call or the agent's account.
// Any other valid model name is still accepted (allowCustom), so a new model
// works before this list learns about it.
//
//   - claude-code: the Claude Code aliases (they follow the newest model of a
//     family) and the pinned id that the recorded fixtures use.
//   - mock: the demo agent has one fake model.
var knownModels = map[string][]api.ModelDTO{
	"claude-code": {
		{ID: "haiku", Label: "Haiku (fast, cheap)"},
		{ID: "sonnet", Label: "Sonnet (balanced)"},
		{ID: "opus", Label: "Opus (most capable)"},
		{ID: "claude-haiku-5-5", Label: "Haiku 5.5 (pinned)"},
	},
	"mock": {
		{ID: "demo", Label: "Demo"},
	},
}

// Models lists the models of every registered agent, in the order the agents
// were registered. An agent without a documented list gets an empty list and
// allowCustom.
func (s *Service) Models(_ context.Context) ([]api.ModelsResponse, error) {
	out := make([]api.ModelsResponse, 0, len(s.c.Agents))
	for _, name := range s.c.Agents {
		models := append([]api.ModelDTO{}, knownModels[name]...)
		out = append(out, api.ModelsResponse{Agent: name, Models: models, AllowCustom: true})
	}
	return out, nil
}
