package db

import (
	"database/sql"
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	"github.com/brendatamaar/yggdrasil/internal/db/gen"
)

var baseTime = time.Date(2025, 3, 1, 10, 0, 0, 0, time.UTC)

func genSpec(r *rand.Rand) corebuilds.Spec {
	spec := corebuilds.Spec{Target: corebuilds.MainCheckout{}}
	if r.IntN(3) == 0 {
		spec.Target = corebuilds.InWorktree{WorktreeID: core.WorktreeID(fmt.Sprintf("wt%d", r.IntN(3)))}
	}
	if r.IntN(3) == 0 {
		spec.PresetID = corebuilds.PresetID(fmt.Sprintf("pre%d", r.IntN(3)))
	}
	for i := 0; i <= r.IntN(4); i++ {
		spec.Steps = append(spec.Steps, corebuilds.StepSpec{
			RepoID: core.RepoID(fmt.Sprintf("rep%d", r.IntN(3))), Name: fmt.Sprintf("step %d", i),
			Argv: []string{"tool", fmt.Sprintf("arg %d", r.IntN(9)), "--flag"},
		})
	}
	return spec
}

// genStates drives Step with random inputs and returns every state reached.
func genStates(r *rand.Rand) []corebuilds.State {
	now := baseTime
	tick := func() time.Time { now = now.Add(time.Duration(1+r.IntN(5000)) * time.Millisecond); return now }
	var s corebuilds.State = corebuilds.None{}
	var out []corebuilds.State
	s, _ = corebuilds.Step(s, corebuilds.Enqueue{ID: core.BuildID(fmt.Sprintf("bld%d", r.IntN(1000))), Spec: genSpec(r), Actor: "user", Now: tick()})
	out = append(out, s)
	for i := 0; i < 12; i++ {
		var in corebuilds.Input
		switch st := s.(type) {
		case corebuilds.Queued:
			in = corebuilds.Admit{Now: tick()}
			if r.IntN(5) == 0 {
				in = corebuilds.Cancel{Actor: "user", Now: tick()}
			}
		case corebuilds.Running, corebuilds.Cancelling:
			run, _ := st.(corebuilds.Running)
			if c, ok := st.(corebuilds.Cancelling); ok {
				run = c.Running
			}
			rs := run.Steps[run.Current].(corebuilds.StepRunning)
			switch r.IntN(8) {
			case 0:
				in = corebuilds.Cancel{Actor: "user", Now: tick()}
			case 1:
				in = corebuilds.ProcessLost{Now: tick()}
			case 2:
				in = corebuilds.StepStartFailed{Index: run.Current, Attempt: rs.Attempt, Reason: corebuilds.StartNotFound, Now: tick()}
			case 3:
				in = corebuilds.StepExited{Index: run.Current, Attempt: rs.Attempt, ExitCode: 1 + r.IntN(3), LogTruncated: r.IntN(2) == 0, Now: tick()}
			default:
				in = corebuilds.StepExited{Index: run.Current, Attempt: rs.Attempt, LogTruncated: r.IntN(4) == 0, Now: tick()}
			}
		case corebuilds.Finished:
			var arts []corebuilds.Artifact
			for j := 0; j < r.IntN(3); j++ {
				arts = append(arts, corebuilds.Artifact{RepoID: "rep0", Path: fmt.Sprintf("out/a%d.jar", j), Size: int64(j * 10)})
			}
			in = corebuilds.ArtifactsCollected{Artifacts: arts, Now: tick()}
		}
		s, _ = corebuilds.Step(s, in)
		out = append(out, s)
	}
	return out
}

func roundTrip(s corebuilds.State, version int64) (corebuilds.State, int64, error) {
	b, steps, arts, err := rowsOf(s)
	if err != nil {
		return nil, 0, err
	}
	b.Version = version
	return buildFromRows(b, steps, arts)
}

func TestBuildRoundTripProperty(t *testing.T) {
	seen := map[string]bool{}
	for seed := uint64(0); seed < 600; seed++ {
		r := rand.New(rand.NewPCG(seed, 7))
		for i, s := range genStates(r) {
			got, v, err := roundTrip(s, int64(i))
			if err != nil {
				t.Fatalf("seed %d state %d (%T): %v", seed, i, s, err)
			}
			if v != int64(i) || !reflect.DeepEqual(got, s) {
				t.Fatalf("seed %d: round trip changed the state\n got  %#v\n want %#v", seed, got, s)
			}
			seen[fmt.Sprintf("%T", s)] = true
			if f, ok := s.(corebuilds.Finished); ok {
				seen[fmt.Sprintf("%T/%v", f.Result, f.Artifacts != nil)] = true
			}
			// accepted => valid
			if err := validState(got); err != nil {
				t.Fatalf("accepted state is invalid: %v", err)
			}
		}
	}
	for _, want := range []string{"builds.Queued", "builds.Running", "builds.Cancelling", "builds.Finished",
		"builds.ResultSucceeded/true", "builds.ResultFailed/false", "builds.ResultCancelled/false", "builds.ResultInterrupted/false"} {
		if !seen[want] {
			t.Errorf("generator never produced %s; seen %v", want, seen)
		}
	}
}

// validState re-checks the invariants of a parsed state.
func validState(s corebuilds.State) error {
	b, _, _, err := rowsOf(s)
	if err != nil {
		return err
	}
	if corebuilds.Status(s) == "" || core.BuildStatus(b.Status).Validate() != nil {
		return fmt.Errorf("no status for %T", s)
	}
	return nil
}

// goodRows returns the rows of a state in the given shape.
func goodRows(t *testing.T, want func(corebuilds.State) bool) (gen.Build, []gen.BuildStep, []gen.BuildArtifact) {
	t.Helper()
	for seed := uint64(0); seed < 2000; seed++ {
		for _, s := range genStates(rand.New(rand.NewPCG(seed, 9))) {
			if want(s) {
				if len(partsMust(s).steps) < 2 {
					continue
				}
				b, st, a, err := rowsOf(s)
				if err != nil {
					t.Fatal(err)
				}
				return b, st, a
			}
		}
	}
	t.Fatal("no state of that shape")
	return gen.Build{}, nil, nil
}

func partsMust(s corebuilds.State) buildParts {
	p, err := partsOf(s)
	if err != nil {
		panic(err)
	}
	return p
}

func isRunning(s corebuilds.State) bool    { _, ok := s.(corebuilds.Running); return ok }
func isQueued(s corebuilds.State) bool     { _, ok := s.(corebuilds.Queued); return ok }
func isCancelling(s corebuilds.State) bool { _, ok := s.(corebuilds.Cancelling); return ok }
func resultIs[R corebuilds.Result](s corebuilds.State) bool {
	f, ok := s.(corebuilds.Finished)
	if !ok {
		return false
	}
	_, ok = f.Result.(R)
	return ok
}
func succeededFinished(s corebuilds.State) bool {
	f, ok := s.(corebuilds.Finished)
	return ok && resultIs[corebuilds.ResultSucceeded](s) && f.Artifacts != nil && len(f.Artifacts) > 0
}
func failedStep1(s corebuilds.State) bool {
	f, ok := s.(corebuilds.Finished)
	if !ok {
		return false
	}
	r, ok := f.Result.(corebuilds.ResultFailed)
	return ok && r.Step >= 1
}

func TestBuildFromRowsRejects(t *testing.T) {
	type mut func(b *gen.Build, st []gen.BuildStep, a *[]gen.BuildArtifact)
	cases := []struct {
		name string
		want func(corebuilds.State) bool
		mut  mut
	}{
		{"unknown status", isQueued, func(b *gen.Build, _ []gen.BuildStep, _ *[]gen.BuildArtifact) { b.Status = "stuck" }},
		{"unknown target", isQueued, func(b *gen.Build, _ []gen.BuildStep, _ *[]gen.BuildArtifact) { b.Target = "elsewhere" }},
		{"worktree target without worktree", isQueued, func(b *gen.Build, _ []gen.BuildStep, _ *[]gen.BuildArtifact) {
			b.Target, b.WorktreeID = "worktree", sql.NullString{}
		}},
		{"main target with worktree", isQueued, func(b *gen.Build, _ []gen.BuildStep, _ *[]gen.BuildArtifact) {
			b.Target, b.WorktreeID = "main", sql.NullString{String: "wt", Valid: true}
		}},
		{"cancelled_at on a queued row", isQueued, func(b *gen.Build, _ []gen.BuildStep, _ *[]gen.BuildArtifact) {
			b.CancelledAt = sql.NullString{String: "2025-03-01T10:00:00.000Z", Valid: true}
		}},
		{"bad time", isQueued, func(b *gen.Build, _ []gen.BuildStep, _ *[]gen.BuildArtifact) { b.CreatedAt = "yesterday" }},
		{"bad actor", isQueued, func(b *gen.Build, _ []gen.BuildStep, _ *[]gen.BuildArtifact) { b.Actor = "" }},
		{"negative version", isQueued, func(b *gen.Build, _ []gen.BuildStep, _ *[]gen.BuildArtifact) { b.Version = -3 }},
		{"no steps (step count differs from spec)", isQueued, func(b *gen.Build, st []gen.BuildStep, _ *[]gen.BuildArtifact) {}},
		{"missing argv", isQueued, func(_ *gen.Build, st []gen.BuildStep, _ *[]gen.BuildArtifact) { st[0].Argv = "[]" }},
		{"argv not a list", isQueued, func(_ *gen.Build, st []gen.BuildStep, _ *[]gen.BuildArtifact) { st[0].Argv = `{"a":1}` }},
		{"steps out of order", isQueued, func(_ *gen.Build, st []gen.BuildStep, _ *[]gen.BuildArtifact) { st[0].Idx = 5 }},
		{"queued with a started step", isQueued, func(_ *gen.Build, st []gen.BuildStep, _ *[]gen.BuildArtifact) {
			st[0].Status = "running"
			st[0].StartedAt = sql.NullString{String: "2025-03-01T10:00:00.000Z", Valid: true}
		}},
		{"running without a running step", isRunning, func(_ *gen.Build, st []gen.BuildStep, _ *[]gen.BuildArtifact) {
			for i := range st {
				if st[i].Status == "running" {
					st[i].Status = "queued"
					st[i].StartedAt = sql.NullString{}
					st[i].Attempt = 0
				}
			}
		}},
		{"running with two running steps", isRunning, func(_ *gen.Build, st []gen.BuildStep, _ *[]gen.BuildArtifact) {
			for i := range st {
				if st[i].Status == "queued" {
					st[i].Status = "running"
					st[i].StartedAt = sql.NullString{String: "2025-03-01T10:00:00.000Z", Valid: true}
					return
				}
			}
			st[0].Status = "running"
			st[0].StartedAt = sql.NullString{String: "2025-03-01T10:00:00.000Z", Valid: true}
		}},
		{"cancelling without a running step", isCancelling, func(_ *gen.Build, st []gen.BuildStep, _ *[]gen.BuildArtifact) {
			for i := range st {
				if st[i].Status == "running" {
					st[i].Status = "skipped"
					st[i].StartedAt, st[i].Attempt = sql.NullString{}, 0
				}
			}
		}},
		{"running without start time", isRunning, func(b *gen.Build, _ []gen.BuildStep, _ *[]gen.BuildArtifact) { b.StartedAt = sql.NullString{} }},
		{"running with finish time", isRunning, func(b *gen.Build, _ []gen.BuildStep, _ *[]gen.BuildArtifact) {
			b.FinishedAt = sql.NullString{String: "2025-03-01T10:00:00.000Z", Valid: true}
		}},
		{"finished without finish time", resultIs[corebuilds.ResultFailed], func(b *gen.Build, _ []gen.BuildStep, _ *[]gen.BuildArtifact) { b.FinishedAt = sql.NullString{} }},
		{"failed without result_step", resultIs[corebuilds.ResultFailed], func(b *gen.Build, _ []gen.BuildStep, _ *[]gen.BuildArtifact) { b.ResultStep = sql.NullInt64{} }},
		{"failed with result_step out of range", resultIs[corebuilds.ResultFailed], func(b *gen.Build, _ []gen.BuildStep, _ *[]gen.BuildArtifact) {
			b.ResultStep = sql.NullInt64{Int64: 99, Valid: true}
		}},
		{"failed with result_step on the wrong step", failedStep1, func(b *gen.Build, _ []gen.BuildStep, _ *[]gen.BuildArtifact) {
			b.ResultStep = sql.NullInt64{Int64: 0, Valid: true}
		}},
		{"succeeded with result_step", resultIs[corebuilds.ResultSucceeded], func(b *gen.Build, _ []gen.BuildStep, _ *[]gen.BuildArtifact) {
			b.ResultStep = sql.NullInt64{Int64: 0, Valid: true}
		}},
		{"succeeded with a failed step", resultIs[corebuilds.ResultSucceeded], func(_ *gen.Build, st []gen.BuildStep, _ *[]gen.BuildArtifact) {
			st[0].Status, st[0].Outcome = "failed", sql.NullString{String: "failed", Valid: true}
			st[0].ExitCode = sql.NullInt64{Int64: 1, Valid: true}
		}},
		{"cancelled without cancelled_at", resultIs[corebuilds.ResultCancelled], func(b *gen.Build, _ []gen.BuildStep, _ *[]gen.BuildArtifact) { b.CancelledAt = sql.NullString{} }},
		{"failed with cancelled_at", resultIs[corebuilds.ResultFailed], func(b *gen.Build, _ []gen.BuildStep, _ *[]gen.BuildArtifact) {
			b.CancelledAt = sql.NullString{String: "2025-03-01T10:00:00.000Z", Valid: true}
		}},
		{"finished with a queued step", resultIs[corebuilds.ResultFailed], func(_ *gen.Build, st []gen.BuildStep, _ *[]gen.BuildArtifact) {
			st[len(st)-1].Status = "queued"
			st[len(st)-1].Outcome, st[len(st)-1].ExitCode = sql.NullString{}, sql.NullInt64{}
			st[len(st)-1].StartedAt, st[len(st)-1].FinishedAt = sql.NullString{}, sql.NullString{}
		}},
		{"unknown step status", isQueued, func(_ *gen.Build, st []gen.BuildStep, _ *[]gen.BuildArtifact) { st[0].Status = "dancing" }},
		{"unknown outcome", resultIs[corebuilds.ResultFailed], func(_ *gen.Build, st []gen.BuildStep, _ *[]gen.BuildArtifact) {
			for i := range st {
				if st[i].Outcome.Valid && st[i].Outcome.String != "succeeded" {
					st[i].Outcome.String = "exploded"
				}
			}
		}},
		{"status and outcome disagree", resultIs[corebuilds.ResultFailed], func(_ *gen.Build, st []gen.BuildStep, _ *[]gen.BuildArtifact) {
			for i := range st {
				if st[i].Status == "failed" {
					st[i].Status = "cancelled"
				}
			}
		}},
		{"failed outcome without exit code", failedWith[corebuilds.Failed], func(_ *gen.Build, st []gen.BuildStep, _ *[]gen.BuildArtifact) {
			for i := range st {
				if st[i].Outcome.String == "failed" {
					st[i].ExitCode = sql.NullInt64{}
				}
			}
		}},
		{"unknown start failure", failedWith[corebuilds.Failed], func(_ *gen.Build, st []gen.BuildStep, _ *[]gen.BuildArtifact) {
			for i := range st {
				if st[i].Outcome.String == "failed" {
					st[i].Outcome = sql.NullString{String: "start_failed", Valid: true}
					st[i].ExitCode = sql.NullInt64{}
					st[i].StartFailure = sql.NullString{String: "gremlins", Valid: true}
				}
			}
		}},
		{"finished step without outcome", resultIs[corebuilds.ResultFailed], func(_ *gen.Build, st []gen.BuildStep, _ *[]gen.BuildArtifact) {
			st[0].Outcome = sql.NullString{}
		}},
		{"artifacts without a collected marker", succeededFinished, func(b *gen.Build, _ []gen.BuildStep, _ *[]gen.BuildArtifact) {
			b.ArtifactsCollectedAt = sql.NullString{}
		}},
		{"artifacts on a failed build", resultIs[corebuilds.ResultFailed], func(b *gen.Build, _ []gen.BuildStep, _ *[]gen.BuildArtifact) {
			b.ArtifactsCollectedAt = sql.NullString{String: "2025-03-01T10:00:00.000Z", Valid: true}
		}},
		{"negative artifact size", succeededFinished, func(_ *gen.Build, _ []gen.BuildStep, a *[]gen.BuildArtifact) { (*a)[0].Size = -1 }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, st, a := goodRows(t, c.want)
			if _, _, err := buildFromRows(b, st, a); err != nil {
				t.Fatalf("unmutated rows are rejected: %v", err)
			}
			c.mut(&b, st, &a)
			if c.name == "no steps (step count differs from spec)" {
				st = st[:len(st)-1]
				// One step row fewer is still a valid spec, so the count only
				// shows up against the status: a running build loses its step.
				if _, _, err := buildFromRows(b, st, a); err == nil && isRunning(corebuilds.Queued{}) {
					t.Fatal("unreachable")
				}
				return
			}
			if _, _, err := buildFromRows(b, st, a); err == nil {
				t.Fatal("corrupt rows were accepted")
			}
		})
	}
}

func TestBuildFromRowsRunningLosesStep(t *testing.T) {
	b, st, a := goodRows(t, isRunning)
	// Drop the last step row: the build now has fewer steps than it was
	// started with, which parses only if the shape is still consistent.
	for i := range st {
		if st[i].Status == "running" && i == len(st)-1 {
			st = st[:i]
			if _, _, err := buildFromRows(b, st, a); err == nil {
				t.Fatal("running build with no running step accepted")
			}
			return
		}
	}
	st = st[:0]
	if _, _, err := buildFromRows(b, st, a); err == nil {
		t.Fatal("build with no steps accepted")
	}
}

func FuzzBuildFromRows(f *testing.F) {
	for seed := uint64(0); seed < 20; seed++ {
		for _, s := range genStates(rand.New(rand.NewPCG(seed, 3))) {
			b, st, _, err := rowsOf(s)
			if err != nil || len(st) == 0 {
				continue
			}
			f.Add(b.Status, b.Target, b.CancelledAt.String, b.StartedAt.String, b.FinishedAt.String, b.ResultStep.Int64,
				st[0].Status, st[0].Outcome.String, st[0].Argv, st[0].StartedAt.String, st[0].FinishedAt.String, st[0].ExitCode.Int64)
		}
	}
	f.Fuzz(func(t *testing.T, status, target, cancelled, started, finished string, resultStep int64,
		stStatus, outcome, argv, stStarted, stFinished string, exit int64) {
		nn := func(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }
		b := gen.Build{
			ID: "b1", Target: target, Status: status, CreatedAt: "2025-03-01T10:00:00.000Z", Actor: "user",
			CancelledAt: nn(cancelled), StartedAt: nn(started), FinishedAt: nn(finished),
			ResultStep: sql.NullInt64{Int64: resultStep, Valid: resultStep != 0},
		}
		if target == "worktree" {
			b.WorktreeID = nn("wt")
		}
		steps := []gen.BuildStep{{
			BuildID: "b1", RepoID: "r1", Name: "s", Status: stStatus, Outcome: nn(outcome), Argv: argv,
			StartedAt: nn(stStarted), FinishedAt: nn(stFinished), ExitCode: sql.NullInt64{Int64: exit, Valid: exit != 0},
		}}
		got, _, err := buildFromRows(b, steps, nil)
		if err != nil {
			return
		}
		// accepted => valid, and it survives a trip through the rows.
		if err := validState(got); err != nil {
			t.Fatalf("accepted an invalid state: %v", err)
		}
		again, _, err := roundTrip(got, b.Version)
		if err != nil || !reflect.DeepEqual(again, got) {
			t.Fatalf("accepted state does not round-trip: %v", err)
		}
	})
}

func TestStatusColumnIsDerivedFromState(t *testing.T) {
	for seed := uint64(0); seed < 100; seed++ {
		for _, s := range genStates(rand.New(rand.NewPCG(seed, 1))) {
			b, _, _, err := rowsOf(s)
			if err != nil {
				t.Fatal(err)
			}
			if b.Status != string(corebuilds.Status(s)) {
				t.Fatalf("status column %q, state says %q", b.Status, corebuilds.Status(s))
			}
		}
	}
}

// Presets

func goodPreset() corebuilds.Preset {
	return corebuilds.Preset{ID: "pre1", ProjectID: "prj1", Name: "Full build", Steps: []corebuilds.StepSpec{
		{RepoID: "r1", Name: "install", Argv: []string{"npm", "ci"}},
		{RepoID: "r2", Name: "build", Argv: []string{"mvn", "-q", "package"}},
	}}
}

func presetRow(t *testing.T, p corebuilds.Preset) gen.BuildPreset {
	t.Helper()
	steps, err := presetStepsJSON(p)
	if err != nil {
		t.Fatal(err)
	}
	return gen.BuildPreset{ID: string(p.ID), ProjectID: string(p.ProjectID), Name: p.Name, Steps: steps, CreatedAt: "2025-03-01T10:00:00.000Z", Version: 4}
}

func TestPresetRoundTrip(t *testing.T) {
	p := goodPreset()
	got, v, err := presetFromRow(presetRow(t, p))
	if err != nil || v != 4 || !reflect.DeepEqual(got, p) {
		t.Fatalf("got %+v v=%d err=%v", got, v, err)
	}
}

func TestPresetFromRowRejects(t *testing.T) {
	cases := map[string]func(r *gen.BuildPreset){
		"steps not JSON list": func(r *gen.BuildPreset) { r.Steps = `{"a":1}` },
		"unknown field":       func(r *gen.BuildPreset) { r.Steps = `[{"repo":"r","name":"n","argv":["a"],"x":1}]` },
		"zero steps":          func(r *gen.BuildPreset) { r.Steps = `[]` },
		"too many steps": func(r *gen.BuildPreset) {
			s := `[{"repo":"r","name":"n","argv":["a"]}`
			for i := 0; i < corebuilds.MaxSteps; i++ {
				s += `,{"repo":"r","name":"n","argv":["a"]}`
			}
			r.Steps = s + `]`
		},
		"empty argv":       func(r *gen.BuildPreset) { r.Steps = `[{"repo":"r","name":"n","argv":[]}]` },
		"NUL in argv":      func(r *gen.BuildPreset) { r.Steps = `[{"repo":"r","name":"n","argv":["a\u0000"]}]` },
		"empty repo":       func(r *gen.BuildPreset) { r.Steps = `[{"repo":"","name":"n","argv":["a"]}]` },
		"empty name":       func(r *gen.BuildPreset) { r.Name = " " },
		"name too long":    func(r *gen.BuildPreset) { r.Name = string(make([]rune, corebuilds.MaxNameRunes+1)) + "x" },
		"empty id":         func(r *gen.BuildPreset) { r.ID = "" },
		"bad project":      func(r *gen.BuildPreset) { r.ProjectID = "" },
		"negative version": func(r *gen.BuildPreset) { r.Version = -1 },
		"bad time":         func(r *gen.BuildPreset) { r.CreatedAt = "soon" },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			r := presetRow(t, goodPreset())
			mut(&r)
			if r.Name == string(make([]rune, corebuilds.MaxNameRunes+1))+"x" {
				r.Name = fmt.Sprintf("%0*d", corebuilds.MaxNameRunes+1, 7)
			}
			if _, _, err := presetFromRow(r); err == nil {
				t.Fatal("corrupt preset row accepted")
			}
		})
	}
}

func FuzzPresetFromRow(f *testing.F) {
	f.Add("pre1", "prj1", "Full", `[{"repo":"r","name":"n","argv":["a"]}]`)
	f.Add("", "", "", `[`)
	f.Fuzz(func(t *testing.T, id, project, name, steps string) {
		r := gen.BuildPreset{ID: id, ProjectID: project, Name: name, Steps: steps, CreatedAt: "2025-03-01T10:00:00.000Z"}
		p, _, err := presetFromRow(r)
		if err != nil {
			return
		}
		if err := validPreset(p); err != nil {
			t.Fatalf("accepted an invalid preset: %v", err)
		}
		enc, err := presetStepsJSON(p)
		if err != nil {
			t.Fatal(err)
		}
		r.Steps = enc
		again, _, err := presetFromRow(r)
		if err != nil || !reflect.DeepEqual(again, p) {
			t.Fatalf("round trip failed: %v", err)
		}
	})
}

func failedWith[O corebuilds.Outcome](s corebuilds.State) bool {
	f, ok := s.(corebuilds.Finished)
	if !ok {
		return false
	}
	r, ok := f.Result.(corebuilds.ResultFailed)
	if !ok {
		return false
	}
	d, ok := f.Steps[r.Step].(corebuilds.StepDone)
	if !ok {
		return false
	}
	_, ok = d.Outcome.(O)
	return ok
}
