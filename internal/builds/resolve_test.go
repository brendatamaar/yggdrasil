package builds

import (
	"reflect"
	"testing"
)

func defaultCmds() Commands {
	return Commands{
		Install:   []Step{{Name: "install", Argv: []string{"npm", "ci"}}},
		Bootstrap: []Step{{Name: "Install dependencies", Argv: []string{"npm", "ci"}}},
		Verify:    []Step{{Name: "lint", Argv: []string{"npm", "run", "lint"}, Env: []string{"CI=true"}}},
		FullTests: []Step{{Name: "test", Argv: []string{"npm", "test"}}},
		Build:     []Step{{Name: "build", Argv: []string{"npm", "run", "build"}}},
		DevServer: []Step{{Name: "dev", Argv: []string{"npm", "run", "dev"}}},
	}
}

func TestResolveCommands(t *testing.T) {
	override := []Command{{Name: "mine", Argv: []string{"make", "check"}}}
	cases := []struct {
		name       string
		s          RepoBuildSettings
		verify     []string // step names
		bootstrap  int
		fullTests  int
		verifyArgv []string // first verify step argv
	}{
		{"defaults", DefaultBuildSettings(), []string{"lint"}, 1, 1, []string{"npm", "run", "lint"}},
		{"override replaces", RepoBuildSettings{VerifyOverride: override, Bootstrap: true}, []string{"mine"}, 1, 1, []string{"make", "check"}},
		{"full tests appended", RepoBuildSettings{FullTests: true, Bootstrap: true}, []string{"lint", "test"}, 1, 1, []string{"npm", "run", "lint"}},
		{"override then full tests", RepoBuildSettings{VerifyOverride: override, FullTests: true, Bootstrap: true}, []string{"mine", "test"}, 1, 1, []string{"make", "check"}},
		{"bootstrap off", RepoBuildSettings{}, []string{"lint"}, 0, 1, []string{"npm", "run", "lint"}},
	}
	for _, tc := range cases {
		got := ResolveCommands(Detection{}, defaultCmds(), tc.s)
		var names []string
		for _, s := range got.Verify {
			names = append(names, s.Name)
		}
		if !reflect.DeepEqual(names, tc.verify) {
			t.Errorf("%s: verify = %v, want %v", tc.name, names, tc.verify)
		}
		if len(got.Bootstrap) != tc.bootstrap {
			t.Errorf("%s: bootstrap steps = %d, want %d", tc.name, len(got.Bootstrap), tc.bootstrap)
		}
		if !reflect.DeepEqual(got.Verify[0].Argv, tc.verifyArgv) {
			t.Errorf("%s: first verify argv = %v", tc.name, got.Verify[0].Argv)
		}
		// Everything else passes through.
		if !reflect.DeepEqual(got.Install, defaultCmds().Install) || !reflect.DeepEqual(got.Build, defaultCmds().Build) ||
			!reflect.DeepEqual(got.DevServer, defaultCmds().DevServer) || len(got.FullTests) != tc.fullTests {
			t.Errorf("%s: other lists changed: %+v", tc.name, got)
		}
	}
}

func TestResolveCommandsEmptyOverrideKeepsDefault(t *testing.T) {
	got := ResolveCommands(Detection{}, defaultCmds(), RepoBuildSettings{VerifyOverride: []Command{}, Bootstrap: true})
	if !reflect.DeepEqual(got.Verify, defaultCmds().Verify) {
		t.Fatalf("%+v", got.Verify)
	}
}

func TestResolveCommandsNoFullTestsLeavesVerifyAlone(t *testing.T) {
	c := defaultCmds()
	c.FullTests = nil // Node: the tests are already in the chain
	got := ResolveCommands(Detection{}, c, RepoBuildSettings{FullTests: true, Bootstrap: true})
	if !reflect.DeepEqual(got.Verify, c.Verify) {
		t.Fatalf("%+v", got.Verify)
	}
}

func TestResolveCommandsSharesNoMemory(t *testing.T) {
	in := defaultCmds()
	s := RepoBuildSettings{VerifyOverride: []Command{{Name: "mine", Argv: []string{"make"}}}, FullTests: true, Bootstrap: true}
	got := ResolveCommands(Detection{}, in, s)
	got.Install[0].Argv[0] = "X"
	got.Bootstrap[0].Argv[0] = "X"
	got.Verify[0].Argv[0] = "X"
	got.Verify[1].Argv[0] = "X" // the appended full-test step
	got.FullTests[0].Argv[0] = "X"
	got.Build[0].Argv[0] = "X"
	got.DevServer[0].Argv[0] = "X"
	if !reflect.DeepEqual(in, defaultCmds()) {
		t.Fatalf("input changed: %+v", in)
	}
	if s.VerifyOverride[0].Argv[0] != "make" {
		t.Fatal("settings changed")
	}
	plain := ResolveCommands(Detection{}, in, DefaultBuildSettings())
	plain.Verify[0].Env[0] = "X=1"
	if in.Verify[0].Env[0] != "CI=true" {
		t.Fatal("env shared with the input")
	}
}
