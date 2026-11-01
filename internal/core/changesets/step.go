package changesets

import "github.com/brendatamaar/yggdrasil/internal/core"

// StepSet is (set, input) -> (set, commands): the change-set table of
// docs/design/changesets.md. An input it does not handle in the current state
// changes nothing and returns no commands.
//
// TODO(CS1): decision logic. Foundations ship the signature only.
func StepSet(s State, in Input) (State, []Command) {
	_ = in
	return s, nil
}

// Preflight returns the advisory warnings of running scope with the current
// order and dependency edges. It never blocks. Pure.
//
// TODO(CS1): warning rule.
func Preflight(s State, scope Scope, deps []Edge) []Warning {
	_, _, _ = s, scope, deps
	return nil
}

// SuggestOrder returns a merge order of repos with every dependency before
// its dependents. When the edges form a cycle it returns repos unchanged and
// the repos in the cycle. Pure.
//
// TODO(CS1): ordering.
func SuggestOrder(repos []core.RepoID, deps []Edge) (order []core.RepoID, cycle []core.RepoID) {
	_ = deps
	return append([]core.RepoID(nil), repos...), nil
}

// RenderLinks replaces the LinkMarker line of an MR description template with
// the link block: the set's repos in merge order, as repo (branch) and, for
// MRs already created, their URL (only URLs on host that pass the web-URL
// check). A template without the marker is returned unchanged. Pure.
//
// TODO(CS1): renderer.
func RenderLinks(template string, order []core.RepoID, branches map[core.RepoID]string, links map[core.RepoID]string, host string) string {
	_, _, _, _, _ = order, branches, links, host, core.RepoID("")
	return template
}

// ValidateLinkMarker rejects a description with more than one LinkMarker or a
// marker that is not on its own line. Pure.
//
// TODO(CS1): validation.
func ValidateLinkMarker(description string) error {
	_ = description
	return nil
}
