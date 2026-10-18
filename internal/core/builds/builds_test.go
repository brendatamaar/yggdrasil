package builds

import (
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Every variant of every closed set implements its sealed interface.
var (
	_ = []State{None{}, Queued{}, Running{}, Cancelling{}, Finished{}}
	_ = []StepState{StepPending{}, StepRunning{}, StepDone{}, StepSkipped{}}
	_ = []Outcome{Succeeded{}, Failed{}, StartFailed{}, Cancelled{}, Interrupted{}}
	_ = []Result{ResultSucceeded{}, ResultFailed{}, ResultCancelled{}, ResultInterrupted{}}
	_ = []Input{Enqueue{}, Admit{}, StepExited{}, StepStartFailed{}, Cancel{}, ProcessLost{}, ArtifactsCollected{}}
	_ = []Command{WriteAudit{}, Publish{}, Wake{}, StartStep{}, KillStep{}, CollectArtifacts{}}
	_ = []Target{MainCheckout{}, InWorktree{}}
)

func TestStartFailureValidate(t *testing.T) {
	for _, f := range []StartFailure{StartNotFound, StartBadArgs, StartDirMissing, StartOther} {
		if err := f.Validate(); err != nil {
			t.Errorf("%q rejected: %v", f, err)
		}
	}
	for _, f := range []StartFailure{"", "Not_Found", "nope"} {
		if f.Validate() == nil {
			t.Errorf("%q accepted", f)
		}
	}
}

func okStep() StepSpec { return StepSpec{RepoID: "r1", Name: "verify", Argv: []string{"go", "test"}} }

func TestSpecValidate(t *testing.T) {
	good := Spec{Target: MainCheckout{}, Steps: []StepSpec{okStep()}}
	if err := good.Validate(); err != nil {
		t.Fatalf("good spec rejected: %v", err)
	}
	if err := (Spec{Target: InWorktree{WorktreeID: "w1"}, Steps: good.Steps, PresetID: "p1"}).Validate(); err != nil {
		t.Fatalf("worktree spec rejected: %v", err)
	}
	many := make([]StepSpec, MaxSteps+1)
	for i := range many {
		many[i] = okStep()
	}
	bad := map[string]Spec{
		"no target":      {Steps: good.Steps},
		"worktree no id": {Target: InWorktree{}, Steps: good.Steps},
		"no steps":       {Target: MainCheckout{}},
		"too many steps": {Target: MainCheckout{}, Steps: many},
		"empty argv":     {Target: MainCheckout{}, Steps: []StepSpec{{RepoID: "r1", Name: "x"}}},
		"empty argv0":    {Target: MainCheckout{}, Steps: []StepSpec{{RepoID: "r1", Name: "x", Argv: []string{""}}}},
		"NUL in argv":    {Target: MainCheckout{}, Steps: []StepSpec{{RepoID: "r1", Name: "x", Argv: []string{"a", "b\x00"}}}},
		"empty name":     {Target: MainCheckout{}, Steps: []StepSpec{{RepoID: "r1", Name: " ", Argv: []string{"a"}}}},
		"long name":      {Target: MainCheckout{}, Steps: []StepSpec{{RepoID: "r1", Name: strings.Repeat("é", MaxNameRunes+1), Argv: []string{"a"}}}},
		"bad repo id":    {Target: MainCheckout{}, Steps: []StepSpec{{RepoID: "", Name: "x", Argv: []string{"a"}}}},
		"bad preset id":  {Target: MainCheckout{}, Steps: good.Steps, PresetID: "a b"},
	}
	for name, s := range bad {
		if s.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := (Spec{Target: MainCheckout{}, Steps: []StepSpec{{RepoID: "r1", Name: strings.Repeat("é", MaxNameRunes), Argv: []string{"a"}}}}).Validate(); err != nil {
		t.Errorf("name of exactly %d runes rejected: %v", MaxNameRunes, err)
	}
}

func TestPresetAndActorValidate(t *testing.T) {
	p := Preset{ID: "pre1", ProjectID: "p1", Name: "verify all", Steps: []StepSpec{okStep()}}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*Preset){
		"id":      func(p *Preset) { p.ID = "" },
		"project": func(p *Preset) { p.ProjectID = "" },
		"name":    func(p *Preset) { p.Name = "" },
		"steps":   func(p *Preset) { p.Steps = nil },
	} {
		q := p
		mut(&q)
		if q.Validate() == nil {
			t.Errorf("preset with bad %s accepted", name)
		}
	}
	for _, a := range []Actor{"user", "system", "agent:claude"} {
		if err := a.Validate(); err != nil {
			t.Errorf("%q rejected", a)
		}
	}
	for _, a := range []Actor{"", "a\nb"} {
		if a.Validate() == nil {
			t.Errorf("%q accepted", a)
		}
	}
}

func TestStatus(t *testing.T) {
	cases := []struct {
		s    State
		want core.BuildStatus
	}{
		{None{}, ""},
		{Queued{}, core.BuildQueued},
		{Running{}, core.BuildRunning},
		{Cancelling{}, core.BuildRunning},
		{Finished{Result: ResultSucceeded{}}, core.BuildSucceeded},
		{Finished{Result: ResultFailed{Step: 1}}, core.BuildFailed},
		{Finished{Result: ResultCancelled{}}, core.BuildCancelled},
		{Finished{Result: ResultInterrupted{Step: 0}}, core.BuildInterrupted},
	}
	for _, c := range cases {
		if got := Status(c.s); got != c.want {
			t.Errorf("Status(%T) = %q, want %q", c.s, got, c.want)
		}
		if got := Status(c.s); got != "" {
			if err := got.Validate(); err != nil {
				t.Errorf("Status(%T) = %q is not a valid build status", c.s, got)
			}
		}
	}
}

// The placeholders change nothing until batch B1 lands the logic.
func TestPlaceholdersAreInert(t *testing.T) {
	now := time.Unix(0, 0).UTC()
	s := State(Queued{ID: "b1", QueuedAt: now})
	got, cmds := Run(s, []Input{Admit{Now: now}, Cancel{Actor: "user", Now: now}})
	if _, ok := got.(Queued); !ok || len(cmds) != 0 {
		t.Fatalf("Run = %#v, %v", got, cmds)
	}
	if id, ok := Next([]QueuedRef{{ID: "b1", QueuedAt: now}}, 0, 1); ok || id != "" {
		t.Fatalf("Next placeholder = %q, %v", id, ok)
	}
	if order, cycles := OrderModules(nil); order != nil || cycles != nil {
		t.Fatal("OrderModules placeholder returned data")
	}
}
