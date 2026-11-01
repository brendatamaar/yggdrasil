package db

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	"github.com/brendatamaar/yggdrasil/internal/db/gen"
)

// NewBuild is the expected version SaveBuild takes to insert a build that
// does not exist yet (the None to Queued step). Stored versions start at 0.
const NewBuild int64 = -1

// BuildFilter narrows ListBuildStates. Zero fields do not filter.
type BuildFilter struct {
	Project core.ProjectID
	Repo    core.RepoID
	Status  core.BuildStatus
	// Limit is the most builds returned, newest first; 0 means 200, and the
	// most allowed is 1000.
	Limit int
}

const (
	defaultBuildLimit = 200
	maxBuildLimit     = 1000
)

// Writes

// LoadBuild returns the state and version of a build, or ErrNotFound.
func (tx *Tx) LoadBuild(ctx context.Context, id core.BuildID) (corebuilds.State, int64, error) {
	return loadBuild(ctx, tx.q, id)
}

// SaveBuild stores s. With expected == NewBuild it inserts (ErrConflict when
// the ID exists); otherwise it updates the build if it is still at expected
// (ErrConflict if the version moved, ErrNotFound if the build is gone). The
// steps and artifacts are rewritten with the build. It returns the new
// version (0 for an insert). The status column comes from corebuilds.Status.
func (tx *Tx) SaveBuild(ctx context.Context, s corebuilds.State, expected int64) (int64, error) {
	b, steps, arts, err := rowsOf(s)
	if err != nil {
		return 0, fmt.Errorf("save build: %w", err)
	}
	what := "save build " + b.ID
	next := int64(0)
	if expected == NewBuild {
		err := tx.q.InsertBuildRow(ctx, gen.InsertBuildRowParams{
			ID: b.ID, WorktreeID: b.WorktreeID, Target: b.Target, Status: b.Status, CreatedAt: b.CreatedAt,
			FinishedAt: b.FinishedAt, Actor: b.Actor, StartedAt: b.StartedAt, ResultStep: b.ResultStep,
			PresetID: b.PresetID, CancelledAt: b.CancelledAt, ArtifactsCollectedAt: b.ArtifactsCollectedAt,
		})
		if err != nil {
			return 0, fmt.Errorf("%s: %w", what, err)
		}
	} else {
		n, err := tx.q.UpdateBuildRow(ctx, gen.UpdateBuildRowParams{
			Status: b.Status, FinishedAt: b.FinishedAt, StartedAt: b.StartedAt, ResultStep: b.ResultStep,
			CancelledAt: b.CancelledAt, ArtifactsCollectedAt: b.ArtifactsCollectedAt, ID: b.ID, Version: expected,
		})
		if next, err = bumped(what, n, err, expected, func() error { _, err := tx.q.GetBuild(ctx, b.ID); return err }); err != nil {
			return 0, err
		}
		if err := tx.q.DeleteBuildStepRows(ctx, b.ID); err != nil {
			return 0, fmt.Errorf("%s: %w", what, err)
		}
		if err := tx.q.DeleteBuildArtifactRows(ctx, b.ID); err != nil {
			return 0, fmt.Errorf("%s: %w", what, err)
		}
	}
	for _, st := range steps {
		if err := tx.q.InsertBuildStepRow(ctx, gen.InsertBuildStepRowParams{
			BuildID: st.BuildID, Idx: st.Idx, RepoID: st.RepoID, Name: st.Name, Status: st.Status, ExitCode: st.ExitCode,
			StartedAt: st.StartedAt, FinishedAt: st.FinishedAt, Argv: st.Argv, Attempt: st.Attempt, Outcome: st.Outcome,
			StartFailure: st.StartFailure, LogTruncated: st.LogTruncated, Env: st.Env,
		}); err != nil {
			return 0, fmt.Errorf("%s: %w", what, err)
		}
	}
	for _, a := range arts {
		if err := tx.q.InsertBuildArtifactRow(ctx, gen.InsertBuildArtifactRowParams(a)); err != nil {
			return 0, fmt.Errorf("%s: %w", what, err)
		}
	}
	return next, nil
}

// InsertPreset stores p; now is its creation time. A name already used in the
// project returns ErrConflict.
func (tx *Tx) InsertPreset(ctx context.Context, p corebuilds.Preset, now time.Time) error {
	if err := needTime("build preset", now); err != nil {
		return err
	}
	steps, err := presetStepsJSON(p)
	if err != nil {
		return err
	}
	err = tx.q.InsertBuildPreset(ctx, gen.InsertBuildPresetParams{
		ID: string(p.ID), ProjectID: string(p.ProjectID), Name: p.Name, Steps: steps, CreatedAt: fmtTime(now),
	})
	if err != nil {
		return fmt.Errorf("insert build preset %s: %w", p.ID, err)
	}
	return nil
}

// UpdatePreset replaces the name and steps of a preset that is still at
// expected, and returns the new version. A stale version or a name already
// used in the project returns ErrConflict.
func (tx *Tx) UpdatePreset(ctx context.Context, p corebuilds.Preset, expected int64) (int64, error) {
	steps, err := presetStepsJSON(p)
	if err != nil {
		return 0, err
	}
	n, err := tx.q.UpdateBuildPreset(ctx, gen.UpdateBuildPresetParams{Name: p.Name, Steps: steps, ID: string(p.ID), Version: expected})
	return bumped("update build preset "+string(p.ID), n, err, expected, func() error {
		_, err := tx.q.GetBuildPreset(ctx, string(p.ID))
		return err
	})
}

// Reads

// GetBuildState returns the state and version of a build, or ErrNotFound.
func (s *Store) GetBuildState(ctx context.Context, id core.BuildID) (corebuilds.State, int64, error) {
	return loadBuild(ctx, s.db.reader(), id)
}

// ListBuildStates returns builds newest first, narrowed by f.
func (s *Store) ListBuildStates(ctx context.Context, f BuildFilter) ([]corebuilds.State, error) {
	if f.Status != "" {
		if err := f.Status.Validate(); err != nil {
			return nil, err
		}
	}
	limit := f.Limit
	if limit <= 0 {
		limit = defaultBuildLimit
	}
	limit = min(limit, maxBuildLimit)
	arg := gen.ListBuildIDsFilteredParams{MaxRows: int64(limit)}
	if f.Status != "" {
		arg.Status = string(f.Status)
	}
	if f.Repo != "" {
		arg.RepoID = string(f.Repo)
	}
	if f.Project != "" {
		arg.ProjectID = string(f.Project)
	}
	q := s.db.reader()
	ids, err := q.ListBuildIDsFiltered(ctx, arg)
	if err != nil {
		return nil, fmt.Errorf("list builds: %w", err)
	}
	return loadAll(ctx, q, ids)
}

// ListQueued returns the queued builds, oldest first (the order Next uses).
func (s *Store) ListQueued(ctx context.Context) ([]corebuilds.QueuedRef, error) {
	rows, err := s.db.reader().ListQueuedBuildRefs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list queued builds: %w", err)
	}
	out := make([]corebuilds.QueuedRef, 0, len(rows))
	for _, r := range rows {
		at, err := parseTime(r.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("queued build %s: %w", r.ID, err)
		}
		out = append(out, corebuilds.QueuedRef{ID: core.BuildID(r.ID), QueuedAt: at})
	}
	return out, nil
}

// GetRunningCount returns how many builds are Running or Cancelling.
func (s *Store) GetRunningCount(ctx context.Context) (int, error) {
	n, err := s.db.reader().CountRunningBuilds(ctx)
	if err != nil {
		return 0, fmt.Errorf("count running builds: %w", err)
	}
	return int(n), nil
}

// ListActive returns the Running and Cancelling builds, oldest first.
func (s *Store) ListActive(ctx context.Context) ([]corebuilds.State, error) {
	q := s.db.reader()
	ids, err := q.ListActiveBuildIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list active builds: %w", err)
	}
	return loadAll(ctx, q, ids)
}

// GetPreset returns a preset and its version, or ErrNotFound.
func (s *Store) GetPreset(ctx context.Context, id corebuilds.PresetID) (corebuilds.Preset, int64, error) {
	r, err := s.db.reader().GetBuildPreset(ctx, string(id))
	if err := found("get build preset "+string(id), err); err != nil {
		return corebuilds.Preset{}, 0, err
	}
	return presetFromRow(r)
}

// ListPresets returns the presets of a project ordered by name.
func (s *Store) ListPresets(ctx context.Context, project core.ProjectID) ([]corebuilds.Preset, error) {
	rows, err := s.db.reader().ListBuildPresets(ctx, string(project))
	if err != nil {
		return nil, fmt.Errorf("list build presets: %w", err)
	}
	out := make([]corebuilds.Preset, 0, len(rows))
	for _, r := range rows {
		p, _, err := presetFromRow(r)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func loadAll(ctx context.Context, q *gen.Queries, ids []string) ([]corebuilds.State, error) {
	out := make([]corebuilds.State, 0, len(ids))
	for _, id := range ids {
		st, _, err := loadBuild(ctx, q, core.BuildID(id))
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, nil
}

func loadBuild(ctx context.Context, q *gen.Queries, id core.BuildID) (corebuilds.State, int64, error) {
	what := "load build " + string(id)
	b, err := q.GetBuild(ctx, string(id))
	if err := found(what, err); err != nil {
		return nil, 0, err
	}
	steps, err := q.ListBuildSteps(ctx, b.ID)
	if err != nil {
		return nil, 0, fmt.Errorf("%s: list steps: %w", what, err)
	}
	arts, err := q.ListBuildArtifactRows(ctx, b.ID)
	if err != nil {
		return nil, 0, fmt.Errorf("%s: list artifacts: %w", what, err)
	}
	st, v, err := buildFromRows(b, steps, arts)
	if err != nil {
		return nil, 0, fmt.Errorf("%s: %w", what, err)
	}
	return st, v, nil
}

// State to rows

// buildParts is the data every non-None state shares, flattened.
type buildParts struct {
	id          core.BuildID
	spec        corebuilds.Spec
	queuedAt    time.Time
	actor       corebuilds.Actor
	startedAt   *time.Time
	cancelledAt *time.Time
	finishedAt  *time.Time
	steps       []corebuilds.StepState
	result      corebuilds.Result
	artifacts   []corebuilds.Artifact
}

func partsOf(s corebuilds.State) (buildParts, error) {
	switch st := s.(type) {
	case corebuilds.Queued:
		steps := make([]corebuilds.StepState, len(st.Spec.Steps))
		for i := range steps {
			steps[i] = corebuilds.StepPending{}
		}
		return buildParts{id: st.ID, spec: st.Spec, queuedAt: st.QueuedAt, actor: st.Actor, steps: steps}, nil
	case corebuilds.Running:
		return runningParts(st, nil), nil
	case corebuilds.Cancelling:
		at := st.CancelledAt
		return runningParts(st.Running, &at), nil
	case corebuilds.Finished:
		return buildParts{
			id: st.ID, spec: st.Spec, queuedAt: st.QueuedAt, actor: st.Actor, startedAt: st.StartedAt,
			cancelledAt: st.CancelledAt, finishedAt: &st.FinishedAt, steps: st.Steps, result: st.Result, artifacts: st.Artifacts,
		}, nil
	}
	return buildParts{}, fmt.Errorf("a %T has no stored form", s)
}

func runningParts(r corebuilds.Running, cancelledAt *time.Time) buildParts {
	started := r.StartedAt
	return buildParts{
		id: r.ID, spec: r.Spec, queuedAt: r.QueuedAt, actor: r.Actor, startedAt: &started,
		cancelledAt: cancelledAt, steps: r.Steps,
	}
}

// rowsOf flattens a state into the rows that store it. Times are cut to
// milliseconds, like every other stored time.
func rowsOf(s corebuilds.State) (gen.Build, []gen.BuildStep, []gen.BuildArtifact, error) {
	p, err := partsOf(s)
	if err != nil {
		return gen.Build{}, nil, nil, err
	}
	if err := p.id.Validate(); err != nil {
		return gen.Build{}, nil, nil, err
	}
	if err := p.spec.Validate(); err != nil {
		return gen.Build{}, nil, nil, err
	}
	if len(p.steps) != len(p.spec.Steps) {
		return gen.Build{}, nil, nil, fmt.Errorf("build has %d step states for %d steps", len(p.steps), len(p.spec.Steps))
	}
	if err := needTime("build", p.queuedAt); err != nil {
		return gen.Build{}, nil, nil, err
	}
	b := gen.Build{
		ID: string(p.id), Target: "main", Status: string(corebuilds.Status(s)), CreatedAt: fmtTime(p.queuedAt),
		Actor: string(p.actor), StartedAt: fmtNullTime(p.startedAt), CancelledAt: fmtNullTime(p.cancelledAt),
		FinishedAt: fmtNullTime(p.finishedAt), PresetID: sql.NullString{String: string(p.spec.PresetID), Valid: p.spec.PresetID != ""},
	}
	if b.Status == "" {
		return gen.Build{}, nil, nil, errors.New("build state has no status")
	}
	if w, ok := p.spec.Target.(corebuilds.InWorktree); ok {
		b.Target = "worktree"
		b.WorktreeID = sql.NullString{String: string(w.WorktreeID), Valid: true}
	}
	switch r := p.result.(type) {
	case corebuilds.ResultFailed:
		b.ResultStep = sql.NullInt64{Int64: int64(r.Step), Valid: true}
	case corebuilds.ResultInterrupted:
		b.ResultStep = sql.NullInt64{Int64: int64(r.Step), Valid: true}
	}
	var arts []gen.BuildArtifact
	if p.artifacts != nil {
		// The marker only has to be non-null; the finish time is the closest
		// fact the state holds.
		b.ArtifactsCollectedAt = fmtNullTime(p.finishedAt)
		for _, a := range p.artifacts {
			arts = append(arts, gen.BuildArtifact{BuildID: b.ID, RepoID: string(a.RepoID), Path: a.Path, Size: a.Size})
		}
	}
	steps := make([]gen.BuildStep, len(p.steps))
	for i, ss := range p.steps {
		argv, err := json.Marshal(p.spec.Steps[i].Argv)
		if err != nil {
			return gen.Build{}, nil, nil, fmt.Errorf("encode step %d: %w", i, err)
		}
		env := p.spec.Steps[i].Env
		if env == nil {
			env = []string{}
		}
		envJSON, err := json.Marshal(env)
		if err != nil {
			return gen.Build{}, nil, nil, fmt.Errorf("encode step %d environment: %w", i, err)
		}
		row := gen.BuildStep{
			BuildID: b.ID, Idx: int64(i), RepoID: string(p.spec.Steps[i].RepoID), Name: p.spec.Steps[i].Name, Argv: string(argv),
			Env: string(envJSON),
		}
		if err := stepRow(&row, ss); err != nil {
			return gen.Build{}, nil, nil, fmt.Errorf("step %d: %w", i, err)
		}
		steps[i] = row
	}
	return b, steps, arts, nil
}

func stepRow(row *gen.BuildStep, ss corebuilds.StepState) error {
	switch st := ss.(type) {
	case corebuilds.StepPending:
		row.Status = "queued"
	case corebuilds.StepSkipped:
		row.Status = "skipped"
	case corebuilds.StepRunning:
		row.Status = "running"
		row.Attempt = int64(st.Attempt)
		row.StartedAt = fmtNullTime(&st.StartedAt)
	case corebuilds.StepDone:
		row.Attempt = int64(st.Attempt)
		row.StartedAt = fmtNullTime(&st.StartedAt)
		row.FinishedAt = fmtNullTime(&st.FinishedAt)
		row.LogTruncated = b2i(st.LogTruncated)
		switch o := st.Outcome.(type) {
		case corebuilds.Succeeded:
			row.Status, row.Outcome = "succeeded", nstr("succeeded")
		case corebuilds.Failed:
			row.Status, row.Outcome = "failed", nstr("failed")
			row.ExitCode = sql.NullInt64{Int64: int64(o.ExitCode), Valid: true}
		case corebuilds.StartFailed:
			row.Status, row.Outcome = "failed", nstr("start_failed")
			row.StartFailure = nstr(string(o.Reason))
		case corebuilds.Cancelled:
			row.Status, row.Outcome = "cancelled", nstr("cancelled")
		case corebuilds.Interrupted:
			row.Status, row.Outcome = "interrupted", nstr("interrupted")
		default:
			return fmt.Errorf("unknown outcome %T", st.Outcome)
		}
	default:
		return fmt.Errorf("unknown step state %T", ss)
	}
	return nil
}

func nstr(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }

// Rows to state

func corrupt(format string, a ...any) error {
	return fmt.Errorf("stored build is inconsistent: "+format, a...)
}

// buildFromRows parses the rows of one build into a state and its version. It
// is the boundary for stored builds: a row that no state can produce is an
// error (a corrupt database), not a guess.
func buildFromRows(b gen.Build, steps []gen.BuildStep, arts []gen.BuildArtifact) (corebuilds.State, int64, error) {
	id := core.BuildID(b.ID)
	if err := id.Validate(); err != nil {
		return nil, 0, err
	}
	status := core.BuildStatus(b.Status)
	if err := status.Validate(); err != nil {
		return nil, 0, corrupt("%v", err)
	}
	if b.Version < 0 {
		return nil, 0, corrupt("negative version")
	}
	spec, err := specFromRows(b, steps)
	if err != nil {
		return nil, 0, err
	}
	queuedAt, err := parseTime(b.CreatedAt)
	if err != nil {
		return nil, 0, err
	}
	actor := corebuilds.Actor(b.Actor)
	if err := actor.Validate(); err != nil {
		return nil, 0, corrupt("%v", err)
	}
	started, err := parseNullTime(b.StartedAt)
	if err != nil {
		return nil, 0, err
	}
	cancelled, err := parseNullTime(b.CancelledAt)
	if err != nil {
		return nil, 0, err
	}
	finished, err := parseNullTime(b.FinishedAt)
	if err != nil {
		return nil, 0, err
	}
	states := make([]corebuilds.StepState, len(steps))
	for i, r := range steps {
		if states[i], err = stepFromRow(r); err != nil {
			return nil, 0, corrupt("step %d: %v", i, err)
		}
	}
	collected := b.ArtifactsCollectedAt.Valid
	if !collected && len(arts) > 0 {
		return nil, 0, corrupt("artifacts stored for a build that never collected them")
	}
	d := rowData{b: b, id: id, spec: spec, queuedAt: queuedAt, actor: actor, started: started,
		cancelled: cancelled, finished: finished, steps: states, collected: collected}
	var st corebuilds.State
	switch status {
	case core.BuildQueued:
		st, err = d.queued()
	case core.BuildRunning:
		st, err = d.running()
	default:
		st, err = d.terminal(status, arts)
	}
	if err != nil {
		return nil, 0, err
	}
	return st, b.Version, nil
}

// rowData is a build row with its columns parsed, before the status decides
// which state it is.
type rowData struct {
	b         gen.Build
	id        core.BuildID
	spec      corebuilds.Spec
	queuedAt  time.Time
	actor     corebuilds.Actor
	started   *time.Time
	cancelled *time.Time
	finished  *time.Time
	steps     []corebuilds.StepState
	collected bool
}

func specFromRows(b gen.Build, steps []gen.BuildStep) (corebuilds.Spec, error) {
	spec := corebuilds.Spec{PresetID: corebuilds.PresetID(b.PresetID.String)}
	if b.PresetID.Valid && b.PresetID.String == "" {
		return spec, corrupt("empty preset id")
	}
	switch b.Target {
	case "main":
		if b.WorktreeID.Valid {
			return spec, corrupt("a main-checkout build names a worktree")
		}
		spec.Target = corebuilds.MainCheckout{}
	case "worktree":
		if !b.WorktreeID.Valid {
			return spec, corrupt("a worktree build has no worktree")
		}
		spec.Target = corebuilds.InWorktree{WorktreeID: core.WorktreeID(b.WorktreeID.String)}
	default:
		return spec, corrupt("unknown target %q", b.Target)
	}
	for i, r := range steps {
		if r.BuildID != b.ID || r.Idx != int64(i) {
			return spec, corrupt("step rows are not numbered 0 to %d in order", len(steps)-1)
		}
		var argv []string
		dec := json.NewDecoder(bytes.NewReader([]byte(r.Argv)))
		if err := dec.Decode(&argv); err != nil || dec.More() {
			return spec, corrupt("step %d: argv is not a JSON list of strings", i)
		}
		var env []string
		envDec := json.NewDecoder(bytes.NewReader([]byte(r.Env)))
		if err := envDec.Decode(&env); err != nil || envDec.More() {
			return spec, corrupt("step %d: env is not a JSON list of strings", i)
		}
		if len(env) == 0 {
			env = nil
		}
		spec.Steps = append(spec.Steps, corebuilds.StepSpec{RepoID: core.RepoID(r.RepoID), Name: r.Name, Argv: argv, Env: env})
	}
	if err := spec.Validate(); err != nil {
		return spec, corrupt("%v", err)
	}
	return spec, nil
}

func stepFromRow(r gen.BuildStep) (corebuilds.StepState, error) {
	if r.Attempt < 0 {
		return nil, errors.New("negative attempt")
	}
	started, err := parseNullTime(r.StartedAt)
	if err != nil {
		return nil, err
	}
	finished, err := parseNullTime(r.FinishedAt)
	if err != nil {
		return nil, err
	}
	switch r.Status {
	case "queued", "skipped":
		if started != nil || finished != nil || r.Outcome.Valid || r.ExitCode.Valid || r.StartFailure.Valid || r.Attempt != 0 || r.LogTruncated != 0 {
			return nil, fmt.Errorf("a %s step has run data", r.Status)
		}
		if r.Status == "queued" {
			return corebuilds.StepPending{}, nil
		}
		return corebuilds.StepSkipped{}, nil
	case "running":
		if started == nil || finished != nil || r.Outcome.Valid || r.ExitCode.Valid || r.StartFailure.Valid || r.LogTruncated != 0 {
			return nil, errors.New("a running step needs a start time and nothing else")
		}
		return corebuilds.StepRunning{Attempt: int(r.Attempt), StartedAt: *started}, nil
	case "succeeded", "failed", "cancelled", "interrupted":
	default:
		return nil, fmt.Errorf("unknown step status %q", r.Status)
	}
	if started == nil || finished == nil || !r.Outcome.Valid {
		return nil, errors.New("a finished step needs start time, finish time and outcome")
	}
	if r.StartFailure.Valid != (r.Outcome.String == "start_failed") {
		return nil, errors.New("start failure and outcome disagree")
	}
	if r.ExitCode.Valid != (r.Outcome.String == "failed") {
		return nil, errors.New("exit code and outcome disagree")
	}
	var o corebuilds.Outcome
	wantStatus := r.Outcome.String
	switch r.Outcome.String {
	case "succeeded":
		o = corebuilds.Succeeded{}
	case "failed":
		o = corebuilds.Failed{ExitCode: int(r.ExitCode.Int64)}
	case "start_failed":
		f := corebuilds.StartFailure(r.StartFailure.String)
		if err := f.Validate(); err != nil {
			return nil, err
		}
		o = corebuilds.StartFailed{Reason: f}
		wantStatus = "failed"
	case "cancelled":
		o = corebuilds.Cancelled{}
	case "interrupted":
		o = corebuilds.Interrupted{}
	default:
		return nil, fmt.Errorf("unknown outcome %q", r.Outcome.String)
	}
	if r.Status != wantStatus {
		return nil, fmt.Errorf("status %q does not match outcome %q", r.Status, r.Outcome.String)
	}
	return corebuilds.StepDone{Attempt: int(r.Attempt), StartedAt: *started, FinishedAt: *finished, Outcome: o, LogTruncated: r.LogTruncated == 1}, nil
}

func (d rowData) queued() (corebuilds.State, error) {
	if d.started != nil || d.cancelled != nil || d.finished != nil || d.b.ResultStep.Valid || d.collected {
		return nil, corrupt("a queued build has run data (cancelled_at on a queued row is not allowed)")
	}
	for _, s := range d.steps {
		if _, ok := s.(corebuilds.StepPending); !ok {
			return nil, corrupt("a queued build has a step that is not queued")
		}
	}
	return corebuilds.Queued{ID: d.id, Spec: d.spec, QueuedAt: d.queuedAt, Actor: d.actor}, nil
}

func (d rowData) running() (corebuilds.State, error) {
	if d.started == nil || d.finished != nil || d.b.ResultStep.Valid || d.collected {
		return nil, corrupt("a running build needs a start time and no finish data")
	}
	cur := -1
	for i, s := range d.steps {
		if _, ok := s.(corebuilds.StepRunning); ok {
			if cur >= 0 {
				return nil, corrupt("a running build has more than one running step")
			}
			cur = i
		}
	}
	if cur < 0 {
		return nil, corrupt("a running build has no running step")
	}
	for i, s := range d.steps {
		switch {
		case i < cur:
			if dn, ok := s.(corebuilds.StepDone); !ok || !isSucceeded(dn) {
				return nil, corrupt("step %d of a running build should have succeeded", i)
			}
		case i > cur:
			if _, ok := s.(corebuilds.StepPending); !ok {
				return nil, corrupt("step %d of a running build should be queued", i)
			}
		}
	}
	r := corebuilds.Running{ID: d.id, Spec: d.spec, QueuedAt: d.queuedAt, StartedAt: *d.started, Actor: d.actor, Steps: d.steps, Current: cur}
	if d.cancelled != nil {
		return corebuilds.Cancelling{Running: r, CancelledAt: *d.cancelled}, nil
	}
	return r, nil
}

func isSucceeded(d corebuilds.StepDone) bool {
	_, ok := d.Outcome.(corebuilds.Succeeded)
	return ok
}

// terminalShape checks steps = Done(Succeeded)* [one Done with an outcome
// that want accepts] Skipped*, and returns the index of the middle step, or
// -1 when every step is skipped.
func terminalShape(steps []corebuilds.StepState, want func(corebuilds.Outcome) bool) (int, error) {
	at := -1
	ended := false
	for i, s := range steps {
		switch st := s.(type) {
		case corebuilds.StepDone:
			if ended {
				return 0, corrupt("step %d follows the step that ended the build", i)
			}
			if !isSucceeded(st) {
				if !want(st.Outcome) {
					return 0, corrupt("step %d ended in a way that does not match the build result", i)
				}
				at, ended = i, true
			}
		case corebuilds.StepSkipped:
			if !ended {
				if i > 0 {
					return 0, corrupt("step %d is skipped but no step ended the build", i)
				}
				ended = true
			}
		default:
			return 0, corrupt("a finished build has a step that is queued or running")
		}
	}
	return at, nil
}

func (d rowData) terminal(status core.BuildStatus, arts []gen.BuildArtifact) (corebuilds.State, error) {
	if d.finished == nil {
		return nil, corrupt("a finished build has no finish time")
	}
	f := corebuilds.Finished{
		ID: d.id, Spec: d.spec, QueuedAt: d.queuedAt, Actor: d.actor, StartedAt: d.started, CancelledAt: d.cancelled,
		FinishedAt: *d.finished, Steps: d.steps,
	}
	if (status == core.BuildCancelled) != (d.cancelled != nil) {
		return nil, corrupt("cancelled_at is set exactly when the build was cancelled")
	}
	if status != core.BuildSucceeded && d.collected {
		return nil, corrupt("only a succeeded build collects artifacts")
	}
	if status != core.BuildFailed && status != core.BuildInterrupted && d.b.ResultStep.Valid {
		return nil, corrupt("result_step set on a build that is neither failed nor interrupted")
	}
	if status != core.BuildCancelled && d.started == nil {
		return nil, corrupt("a build that ran has no start time")
	}
	resultStep := func(want func(corebuilds.Outcome) bool) (int, error) {
		rs := d.b.ResultStep
		if !rs.Valid || rs.Int64 < 0 || rs.Int64 >= int64(len(d.steps)) {
			return 0, corrupt("result_step is missing or out of range")
		}
		at, err := terminalShape(d.steps, want)
		if err != nil {
			return 0, err
		}
		if at != int(rs.Int64) {
			return 0, corrupt("result_step does not point at the step that ended the build")
		}
		return at, nil
	}
	switch status {
	case core.BuildSucceeded:
		for i, s := range d.steps {
			if dn, ok := s.(corebuilds.StepDone); !ok || !isSucceeded(dn) {
				return nil, corrupt("step %d of a succeeded build did not succeed", i)
			}
		}
		f.Result = corebuilds.ResultSucceeded{}
	case core.BuildFailed:
		i, err := resultStep(func(o corebuilds.Outcome) bool {
			switch o.(type) {
			case corebuilds.Failed, corebuilds.StartFailed:
				return true
			}
			return false
		})
		if err != nil {
			return nil, err
		}
		f.Result = corebuilds.ResultFailed{Step: i}
	case core.BuildInterrupted:
		i, err := resultStep(func(o corebuilds.Outcome) bool { _, ok := o.(corebuilds.Interrupted); return ok })
		if err != nil {
			return nil, err
		}
		f.Result = corebuilds.ResultInterrupted{Step: i}
	default: // cancelled
		at, err := terminalShape(d.steps, func(o corebuilds.Outcome) bool { _, ok := o.(corebuilds.Cancelled); return ok })
		if err != nil {
			return nil, err
		}
		if (at == -1) != (d.started == nil) {
			return nil, corrupt("a build that never ran should have every step skipped, and the reverse")
		}
		f.Result = corebuilds.ResultCancelled{}
	}
	if d.collected {
		f.Artifacts = []corebuilds.Artifact{}
		for _, a := range arts {
			art := corebuilds.Artifact{RepoID: core.RepoID(a.RepoID), Path: a.Path, Size: a.Size}
			if art.RepoID.Validate() != nil || art.Path == "" || art.Size < 0 {
				return nil, corrupt("bad artifact %q", a.Path)
			}
			f.Artifacts = append(f.Artifacts, art)
		}
	}
	return f, nil
}

// Presets

type presetStepJSON struct {
	Repo string   `json:"repo"`
	Name string   `json:"name"`
	Argv []string `json:"argv"`
}

func presetStepsJSON(p corebuilds.Preset) (string, error) {
	if err := validPreset(p); err != nil {
		return "", err
	}
	out := make([]presetStepJSON, len(p.Steps))
	for i, s := range p.Steps {
		out[i] = presetStepJSON{Repo: string(s.RepoID), Name: s.Name, Argv: s.Argv}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("encode preset steps: %w", err)
	}
	return string(b), nil
}

// validPreset is Preset.Validate. The rule against arguments a batch file could
// misread (platform.BatchArgsOK) is applied by builds.CheckPreset, because the
// db package may not import platform.
func validPreset(p corebuilds.Preset) error {
	if err := p.Validate(); err != nil {
		return err
	}
	return nil
}

// presetFromRow parses a stored preset and its version. A row that
// ParsePreset would have refused is an error.
func presetFromRow(r gen.BuildPreset) (corebuilds.Preset, int64, error) {
	p := corebuilds.Preset{ID: corebuilds.PresetID(r.ID), ProjectID: core.ProjectID(r.ProjectID), Name: r.Name}
	var steps []presetStepJSON
	dec := json.NewDecoder(bytes.NewReader([]byte(r.Steps)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&steps); err != nil || dec.More() {
		return corebuilds.Preset{}, 0, fmt.Errorf("stored preset %s: steps are not a JSON list of steps", r.ID)
	}
	for _, s := range steps {
		p.Steps = append(p.Steps, corebuilds.StepSpec{RepoID: core.RepoID(s.Repo), Name: s.Name, Argv: s.Argv})
	}
	if r.Version < 0 {
		return corebuilds.Preset{}, 0, fmt.Errorf("stored preset %s: negative version", r.ID)
	}
	if _, err := parseTime(r.CreatedAt); err != nil {
		return corebuilds.Preset{}, 0, fmt.Errorf("stored preset %s: %w", r.ID, err)
	}
	if err := validPreset(p); err != nil {
		return corebuilds.Preset{}, 0, fmt.Errorf("stored preset %s: %w", r.ID, err)
	}
	return p, r.Version, nil
}
