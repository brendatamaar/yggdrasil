package tasks

import (
	"math/rand"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

func TestPreviousMode(t *testing.T) {
	for name, tc := range map[string]struct {
		recorded core.TaskMode
		turns    []TurnRow
		want     core.TaskMode
	}{
		"first turn wins":          {core.ModeAsk, []TurnRow{{Mode: core.ModeCode}, {Mode: core.ModeAsk}}, core.ModeCode},
		"turns counted, no rows":   {core.ModeCurrentBranch, nil, core.ModeCurrentBranch},
		"empty rows":               {core.ModeCode, []TurnRow{}, core.ModeCode},
		"recorded ask stays ask":   {core.ModeAsk, nil, core.ModeAsk},
		"first turn overrides row": {core.ModeCode, []TurnRow{{Mode: core.ModeProject}}, core.ModeProject},
	} {
		if got := PreviousMode(tc.recorded, tc.turns); got != tc.want {
			t.Errorf("%s: got %s, want %s", name, got, tc.want)
		}
	}
}

// Whatever the rows are, the result is the first turn's mode or the recorded
// one. It is never a mode the task does not have.
func TestPropPreviousModeIsNeverInvented(t *testing.T) {
	modes := []core.TaskMode{core.ModeAsk, core.ModeCode, core.ModeCurrentBranch, core.ModeProject}
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		for i := 0; i < 100; i++ {
			recorded := modes[r.Intn(len(modes))]
			var turns []TurnRow
			for n := r.Intn(3); n > 0; n-- {
				turns = append(turns, TurnRow{Mode: modes[r.Intn(len(modes))]})
			}
			got := PreviousMode(recorded, turns)
			want := recorded
			if len(turns) > 0 {
				want = turns[0].Mode
			}
			if got != want {
				t.Fatalf("recorded %s turns %v: got %s", recorded, turns, got)
			}
		}
	})
}
