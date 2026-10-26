package claudecode

import (
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/guard"
	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// policyFor returns a valid hook-enforced policy over dirs: they are readable,
// and writable in every mode but Ask. It allows `go test ./...` and `go vet ./...`.
func policyFor(t *testing.T, mode core.TaskMode, dirs ...string) guard.Policy {
	t.Helper()
	p := guard.Policy{
		Mode:        mode,
		FoldCase:    platform.FoldCase(),
		Enforcement: guard.EnforcementHook,
		Allow: []guard.CommandRule{
			{Program: "go", Args: []string{"test", "./..."}, Tail: guard.TailNone},
			{Program: "go", Args: []string{"vet", "./..."}, Tail: guard.TailNone},
		},
	}
	for _, d := range dirs {
		canon, err := platform.ResolveRoot(d)
		if err != nil {
			t.Fatal(err)
		}
		p.ReadRoots = append(p.ReadRoots, canon)
		if mode != core.ModeAsk {
			p.WriteRoots = append(p.WriteRoots, canon)
		}
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	return p
}
