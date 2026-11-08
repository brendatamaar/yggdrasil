package verify_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	cv "github.com/brendatamaar/yggdrasil/internal/core/verify"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/verify"
)

// rowAllowed says which columns a state may fill; every other column must be
// empty (no stray data of another state). rowRequired are the columns it must
// fill. Both are written down here, not taken from the code under test.
var (
	rowAllowed = map[string][]string{
		"none":          {},
		"skipped":       {"turn", "reason"},
		"unverifiable":  {"turn", "attempt", "reason"},
		"bootstrapping": {"turn", "attempt", "build", "next", "deps", "since"},
		"verifying":     {"turn", "attempt", "build", "deps", "since"},
		"passed":        {"turn", "attempt", "build", "at"},
		"failed":        {"turn", "attempt", "stage", "build", "failstep", "kind", "exit", "text", "at"},
		"interrupted":   {"turn", "attempt", "stage", "at"},
		"cancelled":     {"turn", "attempt", "reason", "at"},
	}
	rowRequired = map[string][]string{
		"none":          {},
		"skipped":       {"turn", "reason"},
		"unverifiable":  {"turn", "attempt", "reason"},
		"bootstrapping": {"turn", "attempt", "build", "next", "since"},
		"verifying":     {"turn", "attempt", "build", "since"},
		"passed":        {"turn", "attempt", "build", "at"},
		"failed":        {"turn", "attempt", "stage", "build", "kind", "at"},
		"interrupted":   {"turn", "attempt", "stage", "at"},
		"cancelled":     {"turn", "attempt", "reason", "at"},
	}
	rowType = map[string]reflect.Type{
		"none": reflect.TypeOf(cv.None{}), "skipped": reflect.TypeOf(cv.Skipped{}), "unverifiable": reflect.TypeOf(cv.Unverifiable{}),
		"bootstrapping": reflect.TypeOf(cv.Bootstrapping{}), "verifying": reflect.TypeOf(cv.Verifying{}), "passed": reflect.TypeOf(cv.Passed{}),
		"failed": reflect.TypeOf(cv.Failed{}), "interrupted": reflect.TypeOf(cv.Interrupted{}), "cancelled": reflect.TypeOf(cv.Cancelled{}),
	}
)

func filledColumns(row db.VerificationRow) map[string]bool {
	return map[string]bool{
		"turn": row.Turn != "", "attempt": row.Attempt != 0, "reason": row.Reason != "", "stage": row.Stage != "",
		"build": row.Build != "", "next": row.NextBuild != "", "deps": row.DepsHash != "", "failstep": row.FailStep != 0,
		"kind": row.FailKind != "", "exit": row.ExitCode != 0, "text": row.FailText != "", "since": row.Since != nil, "at": row.At != nil,
	}
}

// rowProblem checks an accepted row without Validate or the formatter: the
// state is the one the row code names, the required columns are there, no
// column of another state is, and the stored chain has real steps only when
// something runs.
func rowProblem(row db.VerificationRow, rec verify.Record) error {
	want, known := rowType[row.State]
	if !known {
		return fmt.Errorf("accepted unknown state %q", row.State)
	}
	if reflect.TypeOf(rec.State) != want {
		return fmt.Errorf("state %T for code %q", rec.State, row.State)
	}
	filled := filledColumns(row)
	allowed := map[string]bool{}
	for _, c := range rowAllowed[row.State] {
		allowed[c] = true
	}
	for c, on := range filled {
		if on && !allowed[c] {
			return fmt.Errorf("column %s filled in state %s", c, row.State)
		}
	}
	for _, c := range rowRequired[row.State] {
		if !filled[c] {
			return fmt.Errorf("column %s missing in state %s", c, row.State)
		}
	}
	if row.State == "bootstrapping" && row.Build == row.NextBuild {
		return errors.New("a bootstrapping row with one build for both stages")
	}
	if row.Attempt < 0 {
		return errors.New("negative attempt")
	}
	if rec.ID != cv.VerificationID(row.ID) || rec.Task != row.Task || rec.Repo != row.Repo || rec.Worktree != row.Worktree {
		return errors.New("identity changed")
	}
	type step struct {
		Name string
		Argv []string
	}
	var pj struct{ Bootstrap, Verify []step }
	if err := json.Unmarshal(row.Plan, &pj); err != nil {
		return fmt.Errorf("accepted an unreadable plan: %w", err)
	}
	live := row.State == "bootstrapping" || row.State == "verifying"
	if !live && (len(pj.Bootstrap) > 0 || len(pj.Verify) > 0) {
		return errors.New("a chain is kept although nothing runs")
	}
	if live && len(pj.Verify) == 0 {
		return errors.New("a live row without a verify chain")
	}
	for _, st := range append(pj.Bootstrap, pj.Verify...) {
		if strings.TrimSpace(st.Name) == "" || len(st.Argv) == 0 || st.Argv[0] == "" {
			return errors.New("a chain step without a name or a program")
		}
	}
	return nil
}

// The row predicate must be able to fail, or the fuzz target proves nothing.
func TestRowPredicateCanFail(t *testing.T) {
	for name, s := range allStates() {
		row, err := verify.RowFromVerification(verification(s), "")
		if err != nil {
			t.Fatal(err)
		}
		rec, err := verify.VerificationFromRow(row)
		if err != nil {
			t.Fatal(err)
		}
		if err := rowProblem(row, rec); err != nil {
			t.Errorf("%s: good row rejected: %v", name, err)
		}
		if name != "passed" {
			continue
		}
		stray := row
		stray.FailKind = "exit_code"
		if rowProblem(stray, rec) == nil {
			t.Error("stray failure data on a passed row is not caught")
		}
		missing := row
		missing.Build = ""
		if rowProblem(missing, rec) == nil {
			t.Error("a passed row without a build is not caught")
		}
		other := row
		other.State = "failed"
		if rowProblem(other, rec) == nil {
			t.Error("a state of the wrong type is not caught")
		}
	}
}
