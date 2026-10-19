package toolchains

import (
	"testing"
	"testing/fstest"

	"github.com/brendatamaar/yggdrasil/internal/builds"
)

func TestDetectAllMultiple(t *testing.T) {
	repo := fstest.MapFS{
		"pom.xml":            {Data: []byte("<project/>")},
		"mvnw.cmd":           {},
		"web/package.json":   {Data: []byte(`{"scripts":{"build":"x"}}`)},
		"web/pnpm-lock.yaml": {},
	}
	got := DetectAll(repo)
	if len(got) != 2 || got[0].Toolchain.Name() != "node" || got[1].Toolchain.Name() != "maven" {
		t.Fatalf("%+v", got)
	}
	if got[0].Detection.Dir != "web" || got[1].Detection.Dir != "." {
		t.Fatalf("%+v", got)
	}
}

func TestDetectAllNone(t *testing.T) {
	if got := DetectAll(fstest.MapFS{"main.c": {}}); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestMavenIsOrderer(t *testing.T) {
	for _, tc := range All() {
		_, isOrd := tc.(builds.Orderer)
		if isOrd != (tc.Name() == "maven") {
			t.Errorf("%s orderer=%v", tc.Name(), isOrd)
		}
	}
}
