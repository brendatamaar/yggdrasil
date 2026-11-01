package custom

import "testing"

func TestBootstrapCommands(t *testing.T) {
	tc, err := New(Config{Bootstrap: []Command{{Name: "deps", Argv: []string{"make", "deps"}}}})
	if err != nil {
		t.Fatal(err)
	}
	c := tc.Commands(tc.Detection())
	if len(c.Bootstrap) != 1 || c.Bootstrap[0].Name != "deps" || c.Bootstrap[0].Argv[1] != "deps" {
		t.Fatalf("%+v", c.Bootstrap)
	}
	c.Bootstrap[0].Argv[0] = "x"
	if tc.Commands(tc.Detection()).Bootstrap[0].Argv[0] != "make" {
		t.Fatal("Commands shares memory with the config")
	}
}

func TestBootstrapRejects(t *testing.T) {
	for name, cmd := range map[string]Command{
		"no name":   {Argv: []string{"x"}},
		"empty":     {Name: "b"},
		"empty exe": {Name: "b", Argv: []string{""}},
		"metachar":  {Name: "b", Argv: []string{"make", "a&&b"}},
	} {
		if _, err := New(Config{Bootstrap: []Command{cmd}}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
