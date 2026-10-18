package repos_test

import (
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/repos"
	"github.com/brendatamaar/yggdrasil/internal/repos/repostest"
)

func TestStatusReaderContractFake(t *testing.T) {
	repostest.StatusReaderContract(t, func(t *testing.T) (repos.StatusReader, repostest.StatusSetup) {
		f := repostest.NewFakeReader()
		return f, repostest.FakeSetup{Reader: f}
	})
}

func TestStatusReaderContractGit(t *testing.T) {
	fx := newGitFx(t)
	repostest.StatusReaderContract(t, func(t *testing.T) (repos.StatusReader, repostest.StatusSetup) {
		return repos.NewGitReader(fx.g), fx
	})
}
