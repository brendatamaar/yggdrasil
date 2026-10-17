package db

import (
	"database/sql"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db/gen"
)

// A row that the schema's CHECKs would refuse but a damaged file could hold
// is an error at the boundary, not a value of the wrong type in the core.
func TestRowMappersValidate(t *testing.T) {
	good := gen.BuildStep{BuildID: "b", Idx: 0, RepoID: "r", Name: "n", Status: "succeeded", Argv: "[]",
		Outcome: sql.NullString{String: "start_failed", Valid: true}}
	st, err := buildStepFrom(good)
	if err != nil || st.Status != core.StepSucceeded || st.Outcome != core.OutcomeStartFailed {
		t.Fatalf("good row = %+v, %v", st, err)
	}
	unfinished := good
	unfinished.Outcome = sql.NullString{}
	if st, err := buildStepFrom(unfinished); err != nil || st.Outcome != "" {
		t.Fatalf("step without an outcome = %+v, %v", st, err)
	}
	badStatus := good
	badStatus.Status = "stuck"
	if _, err := buildStepFrom(badStatus); err == nil {
		t.Error("unknown step status accepted")
	}
	badOutcome := good
	badOutcome.Outcome = sql.NullString{String: "bogus", Valid: true}
	if _, err := buildStepFrom(badOutcome); err == nil {
		t.Error("unknown step outcome accepted")
	}
	// "skipped" is a step status, not a build status.
	s := NewStore(nil, nil)
	if _, err := s.buildFrom(t.Context(), nil, gen.Build{ID: "b", Status: "skipped", CreatedAt: "2026-01-01T00:00:00.000Z"}); err == nil {
		t.Error("build status 'skipped' accepted")
	}
}
