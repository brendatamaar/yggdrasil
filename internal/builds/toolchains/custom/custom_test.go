package custom

import (
	"errors"
	"testing"
	"testing/fstest"

	"github.com/brendatamaar/yggdrasil/internal/platform"
)

func TestCommandsAndNeverDetects(t *testing.T) {
	tc, err := New(Config{Build: []Command{{Name: "make", Argv: []string{"make", "all"}}}, Artifacts: []string{"out/*.bin"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := tc.Detect(fstest.MapFS{"Makefile": {}, "package.json": {}}); ok {
		t.Fatal("custom must not detect")
	}
	d := tc.Detection()
	if got := tc.Commands(d).Build[0].Argv; len(got) != 2 || got[0] != "make" {
		t.Fatal(got)
	}
	if tc.Artifacts(d)[0] != "out/*.bin" {
		t.Fatal("artifacts")
	}
}

func TestRejects(t *testing.T) {
	bad := map[string]Config{
		"no name":   {Verify: []Command{{Argv: []string{"x"}}}},
		"empty":     {Verify: []Command{{Name: "v"}}},
		"empty exe": {Install: []Command{{Name: "i", Argv: []string{""}}}},
		"metachar":  {Build: []Command{{Name: "b", Argv: []string{"make", "a&&b"}}}},
		"artifact":  {Artifacts: []string{""}},
	}
	for name, cfg := range bad {
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	_, err := New(Config{Build: []Command{{Name: "b", Argv: []string{"make", "|x"}}}})
	if !errors.Is(err, platform.ErrBatchArg) {
		t.Fatalf("want ErrBatchArg, got %v", err)
	}
}
