package changesets

import (
	"context"
	"io/fs"
	"log/slog"

	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	cs "github.com/brendatamaar/yggdrasil/internal/core/changesets"
)

// EdgesFromModules turns module coordinates into repo dependency edges: repo A
// depends on repo B when a module of A lists the coordinates (group and
// artifact, version ignored) of a module of B. Edges inside one repo are left
// out. The result is deterministic and has no duplicates.
func EdgesFromModules(mods []corebuilds.Module) []cs.Edge {
	type coord struct{ g, a string }
	owners := map[coord][]core.RepoID{}
	for _, m := range mods {
		k := coord{m.Coord.Group, m.Coord.Artifact}
		owners[k] = append(owners[k], m.RepoID)
	}
	var out []cs.Edge
	seen := map[cs.Edge]bool{}
	for _, m := range mods {
		for _, d := range m.Deps {
			for _, to := range owners[coord{d.Group, d.Artifact}] {
				e := cs.Edge{Dependent: m.RepoID, Dependency: to}
				if to != m.RepoID && !seen[e] {
					seen[e] = true
					out = append(out, e)
				}
			}
		}
	}
	return out
}

// ToolchainDeps is a DepSource over the toolchains that implement
// builds.Orderer (Maven at v1). It reads manifest files only, through OpenRepo.
type ToolchainDeps struct {
	Toolchains []builds.Toolchain
	// OpenRepo returns the file system of a tracked repo's main checkout.
	OpenRepo func(ctx context.Context, repo core.RepoID) (fs.FS, error)
	Log      *slog.Logger
}

// Edges implements DepSource. A repo that can't be opened, or whose manifest
// can't be read, has no edges.
func (d ToolchainDeps) Edges(ctx context.Context, repos []core.RepoID) ([]cs.Edge, error) {
	var mods []corebuilds.Module
	for _, r := range repos {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		repoFS, err := d.OpenRepo(ctx, r)
		if err != nil {
			d.warn("could not open a repo to read its dependencies", r, err)
			continue
		}
		for _, tc := range d.Toolchains {
			o, ok := tc.(builds.Orderer)
			if !ok {
				continue
			}
			det, found := tc.Detect(repoFS)
			if !found {
				continue
			}
			ms, err := o.Modules(repoFS, det)
			if err != nil {
				d.warn("could not read a repo's dependencies", r, err)
				continue
			}
			for _, m := range ms {
				m.RepoID = r
				mods = append(mods, m)
			}
		}
	}
	return EdgesFromModules(mods), nil
}

func (d ToolchainDeps) warn(msg string, r core.RepoID, err error) {
	if d.Log != nil {
		d.Log.Warn(msg, "repo", r, "err", err)
	}
}
