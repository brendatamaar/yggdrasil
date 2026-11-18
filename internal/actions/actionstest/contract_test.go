package actionstest

import "testing"

// The fakes run the same suites as the real adapters (TestScopeReaderContract*
// and TestContextReaderContract* in cmd/ygg).
func TestScopeReaderContractFake(t *testing.T) {
	ScopeReaderContract(t, func(*testing.T) ScopeRig { return NewFakeScope().Rig() })
}

func TestContextReaderContractFake(t *testing.T) {
	ContextReaderContract(t, func(*testing.T) ContextRig { return NewFakeContext().Rig() })
}
