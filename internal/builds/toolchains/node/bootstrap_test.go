package node

import (
	"reflect"
	"testing"
)

func TestBootstrapByLockfile(t *testing.T) {
	for lock, want := range map[string][]string{
		"package-lock.json": {"npm", "ci"},
		"pnpm-lock.yaml":    {"pnpm", "install", "--frozen-lockfile"},
		"yarn.lock":         {"yarn", "install", "--frozen-lockfile"},
		"":                  {"npm", "install"},
	} {
		d, _ := New().Detect(repo(lock, ""))
		c := New().Commands(d)
		if len(c.Bootstrap) != 1 || !reflect.DeepEqual(c.Bootstrap[0].Argv, want) || c.Bootstrap[0].Name == "" {
			t.Errorf("lock %q: bootstrap = %+v, want %v", lock, c.Bootstrap, want)
		}
		// Install stays the same command, apart from Bootstrap.
		if !reflect.DeepEqual(c.Install[0].Argv, want) {
			t.Errorf("lock %q: install = %v", lock, c.Install[0].Argv)
		}
	}
}

func TestBootstrapDoesNotAliasInstall(t *testing.T) {
	d, _ := New().Detect(repo("package-lock.json", ""))
	c := New().Commands(d)
	c.Bootstrap[0].Argv[0] = "changed"
	if c.Install[0].Argv[0] != "npm" {
		t.Fatal("Bootstrap and Install share an argv slice")
	}
}

func TestVerifyChainHasCIEnv(t *testing.T) {
	d, _ := New().Detect(repo("pnpm-lock.yaml", `"typecheck":"tsc","lint":"eslint .","test":"vitest","build":"vite build"`))
	c := New().Commands(d)
	var names []string
	for _, s := range c.Verify {
		names = append(names, s.Name)
		if !reflect.DeepEqual(s.Env, []string{"CI=true"}) {
			t.Errorf("verify step %q env = %v", s.Name, s.Env)
		}
	}
	if !reflect.DeepEqual(names, []string{"typecheck", "lint", "test"}) {
		t.Fatalf("verify = %v", names)
	}
	if len(c.FullTests) != 0 {
		t.Fatalf("node has no extra full tests, got %v", c.FullTests)
	}
	for _, s := range c.Build {
		if len(s.Env) != 0 {
			t.Errorf("build step %q has env %v", s.Name, s.Env)
		}
	}
}

func TestVerifyFallsBackToBuildWithEnv(t *testing.T) {
	d, _ := New().Detect(repo("", `"build":"vite build"`))
	c := New().Commands(d)
	if len(c.Verify) != 1 || c.Verify[0].Name != "build" || !reflect.DeepEqual(c.Verify[0].Env, []string{"CI=true"}) {
		t.Fatalf("verify = %+v", c.Verify)
	}
}

func TestDepsFiles(t *testing.T) {
	for lock, want := range map[string][]string{
		"pnpm-lock.yaml":    {"package.json", "pnpm-lock.yaml"},
		"yarn.lock":         {"package.json", "yarn.lock"},
		"package-lock.json": {"package.json", "package-lock.json"},
		"":                  {"package.json", "package-lock.json"},
	} {
		d, _ := New().Detect(repo(lock, ""))
		if got := New().DepsFiles(d); !reflect.DeepEqual(got, want) {
			t.Errorf("lock %q: %v, want %v", lock, got, want)
		}
	}
}

func TestDepsFilesInSubfolder(t *testing.T) {
	m := repo("", "")
	m["frontend/package.json"] = m["package.json"]
	delete(m, "package.json")
	m["frontend/yarn.lock"] = m["frontend/package.json"]
	d, _ := New().Detect(m)
	want := []string{"frontend/package.json", "frontend/yarn.lock"}
	if got := New().DepsFiles(d); !reflect.DeepEqual(got, want) {
		t.Fatalf("%v, want %v", got, want)
	}
}
