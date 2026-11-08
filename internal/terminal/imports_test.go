package terminal_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNotReachableFromAgents fails if this package (or its test support)
// imports internal/mcp or internal/agents: the terminal is for the human
// only (docs/phases/phase-5.md decision 4).
func TestNotReachableFromAgents(t *testing.T) {
	const prefix = "github.com/brendatamaar/yggdrasil/internal/"
	banned := []string{"mcp", "agents"}
	var checked int
	err := filepath.WalkDir(".", func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		checked++
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			rel, ok := strings.CutPrefix(path, prefix)
			if !ok {
				continue
			}
			for _, b := range banned {
				if rel == b || strings.HasPrefix(rel, b+"/") {
					t.Errorf("%s imports internal/%s", p, rel)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 5 {
		t.Fatalf("only %d files checked", checked)
	}
}
