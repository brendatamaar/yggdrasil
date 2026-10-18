package api

import (
	"context"
	"net/http"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
)

// This file is owned by the builds task (BL-03, BL-05 to BL-07, BL-09). The
// DTOs are in types_builds.go. The file is excluded from tygo (tygo.yaml): it
// holds Go-only wiring.

// BuildService is implemented by internal/builds (docs/design/builds.md).
// The handler parses the request with builds.ParseRequest and passes the
// resulting Spec; every method returns errors classified by internal/apperr.
type BuildService interface {
	// Enqueue stores a new queued build and wakes the queue worker. It
	// returns once the build is committed (the handler answers 202).
	Enqueue(ctx context.Context, spec corebuilds.Spec, actor corebuilds.Actor) (core.BuildID, error)
	// Cancel asks a build to stop. It is idempotent.
	Cancel(ctx context.Context, id core.BuildID, actor corebuilds.Actor) error
	// Get returns one build with its steps.
	Get(ctx context.Context, id core.BuildID) (core.Build, error)
	// Artifacts returns the files a successful build produced, and whether
	// they were collected yet.
	Artifacts(ctx context.Context, id core.BuildID) (artifacts []corebuilds.Artifact, collected bool, err error)
	// List returns the newest builds first, at most limit.
	List(ctx context.Context, limit int) ([]core.Build, error)
	// StepLog returns the log of one step from byte offset after, and the
	// offset to ask for next.
	StepLog(ctx context.Context, id core.BuildID, step int, after int64) (data []byte, next int64, err error)
	// ListPresets returns a project's presets.
	ListPresets(ctx context.Context, project core.ProjectID) ([]corebuilds.Preset, error)
	// SavePreset stores a preset. A duplicate name in the project is a conflict.
	SavePreset(ctx context.Context, p corebuilds.Preset) error
	// RunPreset enqueues a build from a preset.
	RunPreset(ctx context.Context, id corebuilds.PresetID, actor corebuilds.Actor) (core.BuildID, error)
}

// routesBuilds registers the build endpoints. It registers nothing when the
// service is not wired.
func (s *Server) routesBuilds(mux *http.ServeMux) {
	if s.builds == nil {
		return
	}
	_ = mux // the builds task adds its routes here
}
