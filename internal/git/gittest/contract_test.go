package gittest_test

import (
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/git/gittest"
)

func TestFakeGitWriterContract(t *testing.T) { gittest.GitWriterContract(t, gittest.NewFakeWorld) }

func TestRealGitWriterContract(t *testing.T) { gittest.GitWriterContract(t, gittest.NewRealWorld) }

func TestFakeLandingGitContract(t *testing.T) {
	gittest.LandingGitContract(t, gittest.NewFakeLandingWorld)
}

func TestRealLandingGitContract(t *testing.T) {
	gittest.LandingGitContract(t, gittest.NewRealLandingWorld)
}
