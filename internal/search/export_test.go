package search

import coresearch "github.com/brendatamaar/yggdrasil/internal/core/search"

// Argv exposes the command line for the injection tests.
func (g *GitGrepper) Argv(q coresearch.Query) []string { return g.argv(q) }
