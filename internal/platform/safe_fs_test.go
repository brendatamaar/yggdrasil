package platform_test

import (
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/platform/platformtest"
)

// The real implementations pass the same contract as the fakes
// (internal/platform/platformtest/fakedisk_test.go).

func TestRealSafeInitFSMeetsContract(t *testing.T) {
	platformtest.RunSafeInitFSContract(t, func(t *testing.T) (platform.SafeInitFS, string) {
		return platformtest.OSSafeInitFS(), platformtest.CanonicalTempDir(t)
	})
}

func TestRealIgnoreWriterMeetsContract(t *testing.T) {
	platformtest.RunIgnoreWriterContract(t, func(t *testing.T) (platform.IgnoreWriter, string) {
		return platformtest.OSIgnoreWriter(), platformtest.CanonicalTempDir(t)
	})
}
