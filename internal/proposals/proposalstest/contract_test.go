package proposalstest_test

import (
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/proposals/proposalstest"
)

func TestFakeHostsContract(t *testing.T) {
	proposalstest.HostsContract(t, proposalstest.FakeHostsFactory)
}

func TestFakeBuildsContract(t *testing.T) { proposalstest.BuildsContract(t, proposalstest.FakeBuilds) }
