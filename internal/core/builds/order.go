package builds

import "github.com/brendatamaar/yggdrasil/internal/core"

// OrderModules orders repos so that a module comes after the modules it
// depends on (BL-04). Members of a dependency cycle are kept together in
// input order and reported once each in cycles; the order is deterministic.
func OrderModules(mods []Module) (order []core.RepoID, cycles [][]core.RepoID) {
	// Implemented in batch B1 (docs/phases/phase-2.md).
	return nil, nil
}
