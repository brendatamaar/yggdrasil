package node

import (
	"reflect"
	"testing"
	"testing/fstest"
)

func repo(lock string, scripts string) fstest.MapFS {
	m := fstest.MapFS{"package.json": {Data: []byte(`{"name":"acme-shop-web","scripts":{` + scripts + `}}`)}}
	if lock != "" {
		m[lock] = &fstest.MapFile{Data: []byte("x")}
	}
	return m
}

func TestDetectPackageManager(t *testing.T) {
	for lock, want := range map[string]string{"pnpm-lock.yaml": "pnpm", "yarn.lock": "yarn", "package-lock.json": "npm", "": "npm"} {
		d, ok := New().Detect(repo(lock, ""))
		if !ok || d.Runner != want {
			t.Errorf("lock %q: got %q ok=%v, want %q", lock, d.Runner, ok, want)
		}
	}
}

func TestDetectNoMatch(t *testing.T) {
	if _, ok := New().Detect(fstest.MapFS{"README.md": {}}); ok {
		t.Fatal("matched a repo without package.json")
	}
	if _, ok := New().Detect(fstest.MapFS{"package.json": {Data: []byte("{")}}); ok {
		t.Fatal("matched broken package.json")
	}
}

func TestDetectSubfolder(t *testing.T) {
	m := fstest.MapFS{"pom.xml": {}, "frontend/package.json": {Data: []byte(`{}`)}, "frontend/yarn.lock": {}}
	d, ok := New().Detect(m)
	if !ok || d.Dir != "frontend" || d.Runner != "yarn" {
		t.Fatalf("got %+v ok=%v", d, ok)
	}
	if got := New().Artifacts(d)[0]; got != "frontend/dist/**" {
		t.Fatalf("artifact %q", got)
	}
}

func TestVerifyChain(t *testing.T) {
	d, _ := New().Detect(repo("pnpm-lock.yaml", `"lint":"x","test":"y","build":"z","build:staging":"s","dev":"d"`))
	c := New().Commands(d)
	var names []string
	for _, s := range c.Verify {
		names = append(names, s.Name)
	}
	if !reflect.DeepEqual(names, []string{"lint", "test"}) {
		t.Fatalf("verify %v", names)
	}
	if !reflect.DeepEqual(c.Verify[0].Argv, []string{"pnpm", "run", "lint"}) {
		t.Fatalf("argv %v", c.Verify[0].Argv)
	}
	if !reflect.DeepEqual(c.Install[0].Argv, []string{"pnpm", "install", "--frozen-lockfile"}) {
		t.Fatalf("install %v", c.Install[0].Argv)
	}
	if len(c.Build) != 1 || len(c.DevServer) != 1 || c.DevServer[0].Name != "dev" {
		t.Fatalf("build/dev %+v", c)
	}
	if eb := EnvBuilds(d); len(eb) != 1 || eb[0].Name != "build:staging" {
		t.Fatalf("env builds %+v", eb)
	}
}

func TestVerifyFallsBackToBuild(t *testing.T) {
	d, _ := New().Detect(repo("package-lock.json", `"build":"b","start":"s"`))
	c := New().Commands(d)
	if len(c.Verify) != 1 || c.Verify[0].Name != "build" {
		t.Fatalf("verify %+v", c.Verify)
	}
	if c.Install[0].Argv[1] != "ci" || c.DevServer[0].Name != "start" {
		t.Fatalf("%+v", c)
	}
}

func TestNoScripts(t *testing.T) {
	d, _ := New().Detect(repo("", ""))
	c := New().Commands(d)
	if len(c.Verify) != 0 || len(c.Build) != 0 || len(c.DevServer) != 0 {
		t.Fatalf("%+v", c)
	}
}
