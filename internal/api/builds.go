package api

import (
	"context"

	"fmt"
	"io"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// This file is owned by the builds task (BL-03, BL-05 to BL-07, BL-09). The
// DTOs are in types_builds.go. The file is excluded from tygo (tygo.yaml): it
// holds Go-only wiring.

// BuildService is implemented by *builds.Service (docs/design/builds.md).
// Bodies arrive as raw JSON: the service parses them with its boundary
// parsers (builds.ParseRequest, builds.ParsePreset), which check them against
// the tracked repos. Every method returns errors classified by
// internal/apperr.
type BuildService interface {
	// EnqueueRequest parses a POST /api/builds body, stores a new queued
	// build and wakes the queue. It returns once the build is committed (the
	// handler answers 202).
	EnqueueRequest(ctx context.Context, raw []byte, actor corebuilds.Actor) (core.BuildID, error)
	// Cancel asks a build to stop. It is idempotent.
	Cancel(ctx context.Context, id core.BuildID, actor corebuilds.Actor) error
	// Get returns the state of one build.
	Get(ctx context.Context, id core.BuildID) (corebuilds.State, error)
	// List returns builds, newest first.
	List(ctx context.Context, f db.BuildFilter) ([]corebuilds.State, error)
	// StepLog returns the log of one step from byte offset after, and the
	// offset to ask for next.
	StepLog(ctx context.Context, id core.BuildID, step int, after int64) (data []byte, next int64, err error)
	// ListPresets returns a project's presets.
	ListPresets(ctx context.Context, project core.ProjectID) ([]corebuilds.Preset, error)
	// CreatePreset parses and stores a preset. A duplicate name in the
	// project is a conflict.
	CreatePreset(ctx context.Context, raw []byte, actor corebuilds.Actor) (corebuilds.Preset, error)
	// UpdatePreset changes the name and/or steps of a preset.
	UpdatePreset(ctx context.Context, id corebuilds.PresetID, patch []byte, actor corebuilds.Actor) (corebuilds.Preset, error)
	// RunPreset enqueues a build from a preset.
	RunPreset(ctx context.Context, id corebuilds.PresetID, actor corebuilds.Actor) (core.BuildID, error)
}

// buildActor is who asks: every build request comes from the person at the
// browser.
const buildActor corebuilds.Actor = "user"

// routesBuilds registers the build endpoints. It registers nothing when the
// service is not wired.
func (s *Server) routesBuilds(mux *http.ServeMux) {
	if s.builds == nil {
		return
	}
	mux.HandleFunc("POST /api/builds", s.handleEnqueueBuild)
	mux.HandleFunc("GET /api/builds", s.handleListBuilds)
	mux.HandleFunc("GET /api/builds/{id}", s.handleGetBuild)
	mux.HandleFunc("POST /api/builds/{id}/cancel", s.handleCancelBuild)
	mux.HandleFunc("GET /api/builds/{id}/steps/{step}/log", s.handleStepLog)
	mux.HandleFunc("GET /api/build-presets", s.handleListPresets)
	mux.HandleFunc("POST /api/build-presets", s.handleCreatePreset)
	mux.HandleFunc("PATCH /api/build-presets/{id}", s.handleUpdatePreset)
	mux.HandleFunc("POST /api/build-presets/{id}/run", s.handleRunPreset)
}

// PublishBuildChanged tells subscribers that a build changed, on "builds" and
// on "build:<id>". The builds service calls it after a commit (builds.Publisher).
func (s *Server) PublishBuildChanged(id core.BuildID, status core.BuildStatus) {
	msg := BuildChangedMessage{BuildID: id, Status: status}
	s.hub.publishNext(buildsTopic{}, msg)
	s.hub.publishNext(buildTopic{ID: id}, msg)
}

// PublishBuildLog sends one redacted line of live output on "build:<id>".
func (s *Server) PublishBuildLog(id core.BuildID, step, attempt int, line string) {
	s.hub.publishNext(buildTopic{ID: id}, BuildLogMessage{BuildID: id, Step: step, Attempt: attempt, Line: line})
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		badRequest(w, "The request body is too large or could not be read.", "Send at most 1 MiB of JSON.")
		return nil, false
	}
	return raw, true
}

func buildID(w http.ResponseWriter, r *http.Request) (core.BuildID, bool) {
	id := core.BuildID(r.PathValue("id"))
	if err := id.Validate(); err != nil {
		badRequest(w, "That isn't a build ID.", "Reload the page and try again.")
		return "", false
	}
	return id, true
}

func (s *Server) handleEnqueueBuild(w http.ResponseWriter, r *http.Request) {
	raw, ok := readBody(w, r)
	if !ok {
		return
	}
	id, err := s.builds.EnqueueRequest(r.Context(), raw, buildActor)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, EnqueueBuildResponse{BuildID: id})
}

func (s *Server) handleListBuilds(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := db.BuildFilter{Project: core.ProjectID(q.Get("project")), Repo: core.RepoID(q.Get("repo")), Status: core.BuildStatus(q.Get("status"))}
	if f.Project != "" && f.Project.Validate() != nil {
		badRequest(w, "That isn't a project ID.", "Check the project parameter.")
		return
	}
	if f.Repo != "" && f.Repo.Validate() != nil {
		badRequest(w, "That isn't a repo ID.", "Check the repo parameter.")
		return
	}
	if f.Status != "" && f.Status.Validate() != nil {
		badRequest(w, "That isn't a build status.", "Use queued, running, succeeded, failed, cancelled or interrupted.")
		return
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			badRequest(w, "The limit must be a number from 1 to 1000.", "Check the limit parameter.")
			return
		}
		f.Limit = n
	}
	states, err := s.builds.List(r.Context(), f)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := BuildListResponse{Builds: make([]BuildDTO, 0, len(states))}
	for _, st := range states {
		if dto, ok := buildDTO(st); ok {
			out.Builds = append(out.Builds, dto)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetBuild(w http.ResponseWriter, r *http.Request) {
	id, ok := buildID(w, r)
	if !ok {
		return
	}
	s.writeBuild(w, r, http.StatusOK, id)
}

func (s *Server) writeBuild(w http.ResponseWriter, r *http.Request, status int, id core.BuildID) {
	st, err := s.builds.Get(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	dto, ok := buildDTO(st)
	if !ok {
		writeError(w, http.StatusNotFound, ErrNotFound, "That build doesn't exist.", "Refresh the page and try again.")
		return
	}
	writeJSON(w, status, dto)
}

func (s *Server) handleCancelBuild(w http.ResponseWriter, r *http.Request) {
	id, ok := buildID(w, r)
	if !ok {
		return
	}
	if err := s.builds.Cancel(r.Context(), id, buildActor); err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeBuild(w, r, http.StatusAccepted, id)
}

// parseOffset parses the ?after= value of a log request: a decimal byte
// offset, at least 0. Empty means 0. The service checks it against the size of
// the log.
func parseOffset(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("log offset %q is not a whole number of bytes", raw)
		}
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("log offset %q is too large: %w", raw, err)
	}
	return n, nil
}

// trimPartialRune cuts an incomplete UTF-8 sequence off the end of data, so a
// chunk boundary never shows a replacement character. The cut bytes are read
// again from the next offset.
func trimPartialRune(data []byte) []byte {
	for i := 1; i < utf8.UTFMax && i <= len(data); i++ {
		b := data[len(data)-i]
		if utf8.RuneStart(b) {
			if !utf8.FullRune(data[len(data)-i:]) {
				return data[:len(data)-i]
			}
			break
		}
	}
	return data
}

func (s *Server) handleStepLog(w http.ResponseWriter, r *http.Request) {
	id, ok := buildID(w, r)
	if !ok {
		return
	}
	step, err := strconv.Atoi(r.PathValue("step"))
	if err != nil || step < 0 {
		badRequest(w, "That isn't a step number.", "Reload the page and try again.")
		return
	}
	after, err := parseOffset(r.URL.Query().Get("after"))
	if err != nil {
		badRequest(w, "The log offset must be a whole number of bytes, 0 or more.", "Ask again from 0.")
		return
	}
	data, next, err := s.builds.StepLog(r.Context(), id, step, after)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if cut := trimPartialRune(data); len(cut) != len(data) {
		next -= int64(len(data) - len(cut))
		data = cut
	}
	writeJSON(w, http.StatusOK, StepLogResponse{Data: string(data), Next: next})
}

func (s *Server) handleListPresets(w http.ResponseWriter, r *http.Request) {
	project := core.ProjectID(r.URL.Query().Get("project"))
	if err := project.Validate(); err != nil {
		badRequest(w, "Name the project with ?project=<id>.", "Check the project parameter.")
		return
	}
	ps, err := s.builds.ListPresets(r.Context(), project)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := BuildPresetListResponse{Presets: make([]BuildPresetDTO, 0, len(ps))}
	for _, p := range ps {
		out.Presets = append(out.Presets, presetDTO(p))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreatePreset(w http.ResponseWriter, r *http.Request) {
	raw, ok := readBody(w, r)
	if !ok {
		return
	}
	p, err := s.builds.CreatePreset(r.Context(), raw, buildActor)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, presetDTO(p))
}

func presetID(w http.ResponseWriter, r *http.Request) (corebuilds.PresetID, bool) {
	id := corebuilds.PresetID(r.PathValue("id"))
	if err := id.Validate(); err != nil {
		badRequest(w, "That isn't a preset ID.", "Reload the page and try again.")
		return "", false
	}
	return id, true
}

func (s *Server) handleUpdatePreset(w http.ResponseWriter, r *http.Request) {
	id, ok := presetID(w, r)
	if !ok {
		return
	}
	raw, ok := readBody(w, r)
	if !ok {
		return
	}
	p, err := s.builds.UpdatePreset(r.Context(), id, raw, buildActor)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, presetDTO(p))
}

func (s *Server) handleRunPreset(w http.ResponseWriter, r *http.Request) {
	id, ok := presetID(w, r)
	if !ok {
		return
	}
	bid, err := s.builds.RunPreset(r.Context(), id, buildActor)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, EnqueueBuildResponse{BuildID: bid})
}

func presetDTO(p corebuilds.Preset) BuildPresetDTO {
	return BuildPresetDTO{ID: string(p.ID), ProjectID: p.ProjectID, Name: p.Name, Steps: stepSpecDTOs(p.Steps)}
}

func stepSpecDTOs(in []corebuilds.StepSpec) []StepSpecDTO {
	out := make([]StepSpecDTO, len(in))
	for i, s := range in {
		out[i] = StepSpecDTO{RepoID: s.RepoID, Name: s.Name, Argv: append([]string{}, s.Argv...)}
	}
	return out
}

// buildDTO converts a build state. None has no DTO.
func buildDTO(st corebuilds.State) (BuildDTO, bool) {
	var (
		d       BuildDTO
		spec    corebuilds.Spec
		steps   []corebuilds.StepState
		actor   corebuilds.Actor
		hasStep bool
	)
	switch x := st.(type) {
	case corebuilds.Queued:
		d.ID, spec, actor, d.QueuedAt = x.ID, x.Spec, x.Actor, x.QueuedAt
	case corebuilds.Running:
		d.ID, spec, actor, d.QueuedAt, steps, hasStep = x.ID, x.Spec, x.Actor, x.QueuedAt, x.Steps, true
		d.StartedAt = ptr(x.StartedAt)
	case corebuilds.Cancelling:
		d.ID, spec, actor, d.QueuedAt, steps, hasStep = x.ID, x.Spec, x.Actor, x.QueuedAt, x.Steps, true
		d.StartedAt, d.CancelledAt = ptr(x.StartedAt), ptr(x.CancelledAt)
	case corebuilds.Finished:
		d.ID, spec, actor, d.QueuedAt, steps, hasStep = x.ID, x.Spec, x.Actor, x.QueuedAt, x.Steps, true
		d.StartedAt, d.CancelledAt, d.FinishedAt = x.StartedAt, x.CancelledAt, ptr(x.FinishedAt)
		d.ArtifactsCollected = x.Artifacts != nil
	default:
		return BuildDTO{}, false
	}
	d.Status = corebuilds.Status(st)
	d.Actor = string(actor)
	d.PresetID = string(spec.PresetID)
	d.Target = BuildTargetMain
	if wt, ok := spec.Target.(corebuilds.InWorktree); ok {
		d.Target = BuildTargetWorktree
		id := wt.WorktreeID
		d.WorktreeID = &id
	}
	d.Steps = make([]BuildStepDTO, len(spec.Steps))
	for i, ss := range spec.Steps {
		d.Steps[i] = BuildStepDTO{
			Index: i, RepoID: ss.RepoID, Name: ss.Name, Status: core.StepQueued, Argv: append([]string{}, ss.Argv...),
		}
		if hasStep && i < len(steps) {
			fillStep(&d.Steps[i], steps[i], ss.Argv[0])
		}
	}
	d.Artifacts = []BuildArtifactDTO{}
	if fin, ok := st.(corebuilds.Finished); ok {
		for _, a := range fin.Artifacts {
			d.Artifacts = append(d.Artifacts, BuildArtifactDTO{RepoID: a.RepoID, Path: a.Path, Size: a.Size})
		}
	}
	return d, true
}

func ptr[T any](v T) *T { return &v }

func fillStep(out *BuildStepDTO, st corebuilds.StepState, command string) {
	switch x := st.(type) {
	case corebuilds.StepPending:
		out.Status = core.StepQueued
	case corebuilds.StepSkipped:
		out.Status = core.StepSkipped
	case corebuilds.StepRunning:
		out.Status, out.Attempt, out.StartedAt = core.StepRunning, x.Attempt, ptr(x.StartedAt)
	case corebuilds.StepDone:
		out.Attempt, out.StartedAt, out.FinishedAt, out.LogTruncated = x.Attempt, ptr(x.StartedAt), ptr(x.FinishedAt), x.LogTruncated
		switch o := x.Outcome.(type) {
		case corebuilds.Succeeded:
			out.Status, out.Outcome = core.StepSucceeded, core.OutcomeSucceeded
		case corebuilds.Failed:
			out.Status, out.Outcome, out.ExitCode = core.StepFailed, core.OutcomeFailed, ptr(o.ExitCode)
		case corebuilds.StartFailed:
			out.Status, out.Outcome = core.StepFailed, core.OutcomeStartFailed
			out.StartFailure, out.StartMessage = string(o.Reason), startMessage(o.Reason, command)
		case corebuilds.Cancelled:
			out.Status, out.Outcome = core.StepCancelled, core.OutcomeCancelled
		case corebuilds.Interrupted:
			out.Status, out.Outcome = core.StepInterrupted, core.OutcomeInterrupted
		}
	}
}

// startMessage says in plain language what a start failure means and what to
// do next.
func startMessage(r corebuilds.StartFailure, command string) string {
	switch r {
	case corebuilds.StartNotFound:
		return fmt.Sprintf("%q was not found. Install it, or set the command in the repo's build settings.", command)
	case corebuilds.StartBadArgs:
		return "An argument has a character that a Windows batch file cannot take safely. Remove it or split the step."
	case corebuilds.StartDirMissing:
		return "The folder to build in no longer exists. Restore it, or build in another worktree."
	}
	return fmt.Sprintf("%q could not be started. Check the Yggdrasil log for details.", command)
}
