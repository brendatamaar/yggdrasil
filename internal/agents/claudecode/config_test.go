package claudecode

import (
	"path/filepath"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/agents"
)

var _ agents.RepoConfigProvider = (*Adapter)(nil)

// ConfigPaths and ownAgentConfig must name the same files, or the trust scan
// would miss a file the adapter treats as config.
func TestConfigPathsMatchOwnAgentConfig(t *testing.T) {
	paths := New(Options{}).ConfigPaths()
	if len(paths) == 0 {
		t.Fatal("no config paths")
	}
	for _, p := range paths {
		want := filepath.Join("/r", p)
		exists := func(got string) bool { return got == want }
		if got := ownAgentConfig("/r", exists); got != p {
			t.Errorf("ownAgentConfig finds %q for a repo with only %q", got, p)
		}
	}
}
