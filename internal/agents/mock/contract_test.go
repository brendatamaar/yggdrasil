package mock

import (
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/agents/agentstest"
)

// TestAdapterContract runs the shared adapter contract against the mock
// (AG-01, AG-02, AG-21). The mock has no CLI, so KindMissingPrereq is
// unsupported; claudecode covers that clause.
func TestAdapterContract(t *testing.T) {
	agentstest.AdapterContract(t, func(k agentstest.Kind) (agentstest.Subject, error) {
		switch k {
		case agentstest.KindPlain, agentstest.KindEdits, agentstest.KindError:
			return agentstest.Subject{Adapter: New(Options{Fixture: string(k)})}, nil
		case agentstest.KindNested:
			return agentstest.Subject{Adapter: New(Options{Fixture: "subagents"})}, nil
		case agentstest.KindCancel:
			return agentstest.Subject{Adapter: New(Options{Fixture: "plain", Delay: time.Hour})}, nil
		}
		return agentstest.Subject{}, agentstest.ErrUnsupported
	})
}
