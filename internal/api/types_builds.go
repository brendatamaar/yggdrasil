package api

import (
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// DTOs of the builds area (docs/design/builds.md). They are plain; the builds
// task may refine them in this file. The core types have no JSON tags, so
// every conversion happens in the handlers.

// BuildTargetKind says where a build runs.
type BuildTargetKind string

const (
	// BuildTargetMain: in the main checkouts of the repos.
	BuildTargetMain BuildTargetKind = "main"
	// BuildTargetWorktree: in one task worktree.
	BuildTargetWorktree BuildTargetKind = "worktree"
)

// BuildStepDTO is one step of a build.
type BuildStepDTO struct {
	Index  int             `json:"index"`
	RepoID core.RepoID     `json:"repoId"`
	Name   string          `json:"name"`
	Status core.StepStatus `json:"status"`
	// Outcome is empty until the step has ended.
	Outcome core.StepOutcome `json:"outcome,omitempty"`
	// StartFailure is not_found, bad_args, dir_missing or other when Outcome
	// is start_failed; StartMessage says what to do next.
	StartFailure string   `json:"startFailure,omitempty"`
	StartMessage string   `json:"startMessage,omitempty"`
	ExitCode     *int     `json:"exitCode,omitempty"`
	Argv         []string `json:"argv"`
	Attempt      int      `json:"attempt"`
	// LogTruncated: the output was cut after the process ended.
	LogTruncated bool       `json:"logTruncated"`
	StartedAt    *time.Time `json:"startedAt,omitempty"`
	FinishedAt   *time.Time `json:"finishedAt,omitempty"`
}

// BuildArtifactDTO is a file a successful build produced (BL-07).
type BuildArtifactDTO struct {
	RepoID core.RepoID `json:"repoId"`
	// Path is relative to the repo, with forward slashes.
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// BuildDTO is a build with its steps.
type BuildDTO struct {
	ID         core.BuildID     `json:"id"`
	Status     core.BuildStatus `json:"status"`
	Target     BuildTargetKind  `json:"target"`
	WorktreeID *core.WorktreeID `json:"worktreeId,omitempty"`
	TaskID     *core.TaskID     `json:"taskId,omitempty"`
	PresetID   string           `json:"presetId,omitempty"`
	// Actor is "user", "agent:<name>" or "system".
	Actor       string         `json:"actor"`
	QueuedAt    time.Time      `json:"queuedAt"`
	StartedAt   *time.Time     `json:"startedAt,omitempty"`
	CancelledAt *time.Time     `json:"cancelledAt,omitempty"`
	FinishedAt  *time.Time     `json:"finishedAt,omitempty"`
	Steps       []BuildStepDTO `json:"steps"`
	// ArtifactsCollected: Artifacts was looked up (it may be empty).
	ArtifactsCollected bool               `json:"artifactsCollected"`
	Artifacts          []BuildArtifactDTO `json:"artifacts"`
}

// BuildListResponse is the body of GET /api/builds, newest first.
type BuildListResponse struct {
	Builds []BuildDTO `json:"builds"`
}

// StepSpecDTO is one command to run: argv only, with no shell. The server
// refuses shell characters such as && and pipes; split them into steps.
type StepSpecDTO struct {
	RepoID core.RepoID `json:"repoId"`
	Name   string      `json:"name"`
	Argv   []string    `json:"argv"`
}

// EnqueueBuildRequest is the body of POST /api/builds. A WorktreeID builds in
// that task worktree instead of the main checkout.
type EnqueueBuildRequest struct {
	Steps      []StepSpecDTO    `json:"steps"`
	WorktreeID *core.WorktreeID `json:"worktreeId,omitempty"`
}

// EnqueueBuildResponse is the body of 202 Accepted for a new build.
type EnqueueBuildResponse struct {
	BuildID core.BuildID `json:"buildId"`
}

// BuildPresetDTO is a saved list of steps for a project (BL-05).
type BuildPresetDTO struct {
	ID        string         `json:"id"`
	ProjectID core.ProjectID `json:"projectId"`
	Name      string         `json:"name"`
	Steps     []StepSpecDTO  `json:"steps"`
}

// SaveBuildPresetRequest is the body of POST /api/build-presets.
type SaveBuildPresetRequest struct {
	ProjectID core.ProjectID `json:"projectId"`
	Name      string         `json:"name"`
	Steps     []StepSpecDTO  `json:"steps"`
}

// BuildPresetListResponse is the body of GET /api/build-presets?project=.
type BuildPresetListResponse struct {
	Presets []BuildPresetDTO `json:"presets"`
}

// StepLogResponse is the body of GET /api/builds/{id}/steps/{i}/log?after=.
// Next is the offset to ask for next.
type StepLogResponse struct {
	Data string `json:"data"`
	Next int64  `json:"next"`
}

// BuildChangedMessage is the data of a change message on the "builds" and
// "build:<id>" topics. The client refetches the build by ID.
type BuildChangedMessage struct {
	BuildID core.BuildID     `json:"buildId"`
	Status  core.BuildStatus `json:"status"`
}

// BuildLogMessage is one line of live output on a "build:<id>" topic. Lines
// are redacted before they are sent.
type BuildLogMessage struct {
	BuildID core.BuildID `json:"buildId"`
	Step    int          `json:"step"`
	Attempt int          `json:"attempt"`
	Line    string       `json:"line"`
}
