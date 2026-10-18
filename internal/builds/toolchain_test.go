package builds

import (
	"io/fs"
	"testing"
	"testing/fstest"

	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
)

// fakeToolchain shows the minimum a module implements; fakeOrdered adds the
// optional Orderer.
type fakeToolchain struct{}

func (fakeToolchain) Name() string { return "fake" }
func (fakeToolchain) Detect(repo fs.FS) (Detection, bool) {
	if _, err := fs.Stat(repo, "fake.json"); err != nil {
		return Detection{}, false
	}
	return Detection{Toolchain: "fake", Dir: "."}, true
}
func (fakeToolchain) Commands(Detection) Commands {
	return Commands{Verify: []Step{{Name: "test", Argv: []string{"fake", "test"}}}}
}
func (fakeToolchain) Artifacts(Detection) []string { return []string{"dist/*.zip"} }

type fakeOrdered struct{ fakeToolchain }

func (fakeOrdered) Modules(fs.FS, Detection) ([]corebuilds.Module, error) { return nil, nil }

var (
	_ Toolchain = fakeToolchain{}
	_ Toolchain = fakeOrdered{}
	_ Orderer   = fakeOrdered{}
)

func TestOrdererIsOptional(t *testing.T) {
	var plain Toolchain = fakeToolchain{}
	if _, ok := plain.(Orderer); ok {
		t.Fatal("a plain toolchain must not need to implement Orderer")
	}
	var ordered Toolchain = fakeOrdered{}
	if _, ok := ordered.(Orderer); !ok {
		t.Fatal("Orderer is found by type assertion")
	}
}

func TestDetectReadsFilesOnly(t *testing.T) {
	d, ok := fakeToolchain{}.Detect(fstest.MapFS{"fake.json": {Data: []byte("{}")}})
	if !ok || d.Toolchain != "fake" {
		t.Fatalf("Detect = %+v, %v", d, ok)
	}
	if _, ok := (fakeToolchain{}).Detect(fstest.MapFS{}); ok {
		t.Fatal("detected an empty repo")
	}
}

func TestStepSpecCopiesArgv(t *testing.T) {
	s := Step{Name: "test", Argv: []string{"go", "test"}}
	spec := s.Spec("r1")
	spec.Argv[0] = "changed"
	if s.Argv[0] != "go" || spec.RepoID != "r1" || spec.Name != "test" {
		t.Fatalf("Spec shares or loses data: %+v %+v", s, spec)
	}
	if err := s.Spec("r1").Validate(); err != nil {
		t.Fatal(err)
	}
}
