package dotnet

import (
	"os"
	"reflect"
	"testing"
	"testing/fstest"
)

func TestSolutionPreferred(t *testing.T) {
	tc := New()
	d, ok := tc.Detect(os.DirFS("testdata/web"))
	if !ok || d.Dir != "." || d.Facts["kind"] != "sln" || d.Facts["target"] != "shop.sln" {
		t.Fatalf("%+v %v", d, ok)
	}
	c := tc.Commands(d)
	check := func(name string, got, want []string) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v", name, got)
		}
	}
	check("bootstrap", c.Bootstrap[0].Argv, []string{"dotnet", "restore", "shop.sln"})
	check("install", c.Install[0].Argv, []string{"dotnet", "restore", "shop.sln"})
	check("verify", c.Verify[0].Argv, []string{"dotnet", "build", "shop.sln", "--no-restore"})
	check("full", c.FullTests[0].Argv, []string{"dotnet", "test", "shop.sln"})
	check("build", c.Build[0].Argv, []string{"dotnet", "build", "shop.sln"})
	if len(c.DevServer) != 0 {
		t.Fatal("a solution has no single project to run")
	}
}

func TestWebProjectDevServer(t *testing.T) {
	d, ok := New().Detect(fstest.MapFS{"Api.csproj": {Data: []byte(`<Project Sdk="Microsoft.NET.Sdk.Web"/>`)}})
	if !ok || d.Facts["kind"] != "csproj" || d.Facts["web"] != "true" {
		t.Fatalf("%+v", d)
	}
	s := New().Commands(d).DevServer[0]
	if !reflect.DeepEqual(s.Argv, []string{"dotnet", "run", "--project", "Api.csproj"}) ||
		!reflect.DeepEqual(s.Env, []string{"ASPNETCORE_URLS=http://127.0.0.1:{port}"}) {
		t.Fatalf("%+v", s)
	}
}

func TestConsoleProjectHasNoURLs(t *testing.T) {
	d, _ := New().Detect(fstest.MapFS{"Tool.csproj": {Data: []byte(`<Project Sdk="Microsoft.NET.Sdk"/>`)}})
	if s := New().Commands(d).DevServer[0]; len(s.Env) != 0 {
		t.Fatalf("%+v", s)
	}
}

func TestGlobalJsonOnlyAndSubFolder(t *testing.T) {
	d, ok := New().Detect(fstest.MapFS{"global.json": {}})
	if !ok || d.Facts["kind"] != "global" {
		t.Fatalf("%+v", d)
	}
	if got := New().Commands(d).Verify[0].Argv; !reflect.DeepEqual(got, []string{"dotnet", "build", "--no-restore"}) {
		t.Fatal(got)
	}
	d, ok = New().Detect(fstest.MapFS{"src/App.csproj": {}, "obj/X.csproj": {}})
	if !ok || d.Dir != "src" {
		t.Fatalf("%+v", d)
	}
}

func TestSolutionBeatsProjectAndNoMatch(t *testing.T) {
	d, _ := New().Detect(fstest.MapFS{"A.csproj": {}, "Z.slnx": {}})
	if d.Facts["target"] != "Z.slnx" {
		t.Fatalf("%+v", d)
	}
	if _, ok := New().Detect(fstest.MapFS{"go.mod": {}}); ok {
		t.Fatal("matched")
	}
}
