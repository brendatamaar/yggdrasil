package golang

import (
	"os"
	"reflect"
	"testing"
	"testing/fstest"

	"github.com/brendatamaar/yggdrasil/internal/builds"
)

func argvs(ss []builds.Step) [][]string {
	var out [][]string
	for _, s := range ss {
		out = append(out, s.Argv)
	}
	return out
}

func TestFixtureWithMain(t *testing.T) {
	tc := New()
	d, ok := tc.Detect(os.DirFS("testdata/go-cmd"))
	if !ok || d.Dir != "." || d.Toolchain != "golang" || d.Facts["main"] != "true" {
		t.Fatalf("%+v %v", d, ok)
	}
	c := tc.Commands(d)
	want := map[string][][]string{
		"install":   {{"go", "mod", "download"}},
		"bootstrap": {{"go", "mod", "download"}},
		"verify":    {{"go", "vet", "./..."}, {"go", "build", "./..."}},
		"full":      {{"go", "test", "./..."}},
		"build":     {{"go", "build", "./..."}},
		"dev":       {{"go", "run", "."}},
	}
	got := map[string][][]string{
		"install": argvs(c.Install), "bootstrap": argvs(c.Bootstrap), "verify": argvs(c.Verify),
		"full": argvs(c.FullTests), "build": argvs(c.Build), "dev": argvs(c.DevServer),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
	if tc.Artifacts(d)[0] != "bin/**" {
		t.Fatal(tc.Artifacts(d))
	}
}

func TestLibraryHasNoDevServer(t *testing.T) {
	repo := fstest.MapFS{"go.mod": {Data: []byte("module x\n")}, "x.go": {Data: []byte("package x\n")}, "x_test.go": {Data: []byte("package main\n")}}
	d, ok := New().Detect(repo)
	if !ok || d.Facts["main"] != "" {
		t.Fatalf("%+v", d)
	}
	if len(New().Commands(d).DevServer) != 0 {
		t.Fatal("dev server for a library")
	}
}

func TestSubFolderAndNoMatch(t *testing.T) {
	d, ok := New().Detect(fstest.MapFS{"svc/go.mod": {}, "svc/main.go": {Data: []byte("// c\npackage main\n")}})
	if !ok || d.Dir != "svc" || d.Facts["main"] != "true" {
		t.Fatalf("%+v %v", d, ok)
	}
	if got := New().DepsFiles(d); !reflect.DeepEqual(got, []string{"svc/go.mod", "svc/go.sum"}) {
		t.Fatal(got)
	}
	if _, ok := New().Detect(fstest.MapFS{"package.json": {}}); ok {
		t.Fatal("matched")
	}
}
