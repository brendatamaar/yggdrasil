package composer

import (
	"context"
	"testing"

	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
)

func TestEveryListedModelPassesTheModelNameRule(t *testing.T) {
	for agent, list := range knownModels {
		if len(list) == 0 {
			t.Errorf("%s has no models", agent)
		}
		for _, m := range list {
			if !coretasks.ValidModelName(m.ID) {
				t.Errorf("%s: %q is not a valid model name", agent, m.ID)
			}
			if m.Label == "" {
				t.Errorf("%s: %q has no label", agent, m.ID)
			}
		}
	}
}

func TestModelsPerRegisteredAgent(t *testing.T) {
	s := newService(t, newStore(t), func(c *Config) { c.Agents = []string{"claude-code", "mock", "other"} })
	got, err := s.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Agent != "claude-code" || got[2].Agent != "other" {
		t.Fatalf("got %+v", got)
	}
	ids := map[string]bool{}
	for _, m := range got[0].Models {
		ids[m.ID] = true
	}
	if !ids["haiku"] || !ids["sonnet"] || !ids["opus"] {
		t.Fatalf("aliases missing: %+v", got[0].Models)
	}
	for _, r := range got {
		if !r.AllowCustom || r.Models == nil {
			t.Fatalf("%s: allowCustom/non-nil list: %+v", r.Agent, r)
		}
	}
}
