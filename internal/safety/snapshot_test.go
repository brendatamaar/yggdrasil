package safety_test

import (
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/safety/safetytest"
)

func TestFakeSnapshotterContract(t *testing.T) {
	safetytest.SnapshotterContract(t, safetytest.FakeHarness())
}
