package gittest_test

import (
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/git/gittest"
)

func TestFakeRepoInitializerContract(t *testing.T) {
	gittest.RunRepoInitializerContract(t, gittest.NewFakeInitWorld)
}

func TestRealRepoInitializerContract(t *testing.T) {
	gittest.RunRepoInitializerContract(t, gittest.NewRealInitWorld)
}

func TestFakeOriginManagerContract(t *testing.T) {
	gittest.RunOriginManagerContract(t, gittest.NewFakeInitWorld)
}

func TestRealOriginManagerContract(t *testing.T) {
	gittest.RunOriginManagerContract(t, gittest.NewRealInitWorld)
}

func TestFakePublishTransportContract(t *testing.T) {
	gittest.RunPublishTransportContract(t, gittest.NewFakePublishWorld)
}

func TestRealPublishTransportContract(t *testing.T) {
	gittest.RunPublishTransportContract(t, gittest.NewRealPublishWorld)
}
