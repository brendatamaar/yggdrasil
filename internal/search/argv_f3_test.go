package search_test

import (
	"slices"
	"testing"

	coresearch "github.com/brendatamaar/yggdrasil/internal/core/search"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/search"
	"github.com/brendatamaar/yggdrasil/internal/search/searchtest"
)

func TestArgvBlocksRepoConfiguredHelpers(t *testing.T) {
	g, err := search.NewGitGrepper(searchtest.GitEnv(t), platform.Start)
	if err != nil {
		t.Fatal(err)
	}
	argv := g.Argv(coresearch.Query{Pattern: "x", Mode: coresearch.ModeLiteral, Case: true})
	end := slices.Index(argv, "grep")
	for _, want := range []string{"core.fsmonitor=false", "core.hooksPath=", "protocol.allow=never"} {
		i := slices.Index(argv, want)
		if i < 1 || i > end || argv[i-1] != "-c" {
			t.Errorf("missing -c %s before the subcommand: %q", want, argv)
		}
	}
}
