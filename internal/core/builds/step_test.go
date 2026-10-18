package builds

import (
	"reflect"
	"testing"
	"time"
)

func spec(n int) Spec {
	st := make([]StepSpec, n)
	for i := range st {
		st[i] = StepSpec{RepoID: "r1", Name: "s", Argv: []string{"go", "test"}}
	}
	return Spec{Target: MainCheckout{}, Steps: st}
}

func at(s int) time.Time { return epoch.Add(timeSec(s)) }

// kinds names the command types, in order.
func kinds(cmds []Command) []string {
	var out []string
	for _, c := range cmds {
		out = append(out, reflect.TypeOf(c).Name())
	}
	return out
}

func apply(t *testing.T, s State, ins ...Input) (State, []Command) {
	t.Helper()
	return Run(s, ins)
}

func running(t *testing.T, n int) State {
	t.Helper()
	s, _ := apply(t, None{}, Enqueue{ID: "b1", Spec: spec(n), Actor: "user", Now: at(0)}, Admit{Now: at(1)})
	if _, ok := s.(Running); !ok {
		t.Fatalf("setup: %T", s)
	}
	return s
}

func TestTransitionTable(t *testing.T) {
	audit, pub, wake := "WriteAudit", "Publish", "Wake"
	queued, _ := apply(t, None{}, Enqueue{ID: "b1", Spec: spec(2), Actor: "user", Now: at(0)})
	cancelling, _ := apply(t, running(t, 2), Cancel{Actor: "user", Now: at(3)})
	last, _ := apply(t, running(t, 2), StepExited{Index: 0, Attempt: 1, Now: at(2)})
	done, _ := apply(t, running(t, 1), StepExited{Index: 0, Attempt: 1, Now: at(2)})

	cases := []struct {
		name  string
		from  State
		in    Input
		check func(t *testing.T, s State)
		cmds  []string
	}{
		{"None/Enqueue", None{}, Enqueue{ID: "b1", Spec: spec(1), Actor: "user", Now: at(0)},
			func(t *testing.T, s State) { _ = s.(Queued) }, []string{audit, pub, wake}},
		{"None/Enqueue no steps", None{}, Enqueue{ID: "b1", Spec: Spec{Target: MainCheckout{}}, Actor: "user", Now: at(0)},
			func(t *testing.T, s State) { _ = s.(None) }, nil},
		{"None/Admit", None{}, Admit{Now: at(0)}, func(t *testing.T, s State) { _ = s.(None) }, nil},
		{"Queued/Admit", queued, Admit{Now: at(1)}, func(t *testing.T, s State) {
			r := s.(Running)
			if r.Current != 0 || !r.StartedAt.Equal(at(1)) || !reflect.DeepEqual(r.Steps, []StepState{StepRunning{1, at(1)}, StepPending{}}) {
				t.Fatalf("%#v", r)
			}
		}, []string{audit, "StartStep", pub}},
		{"Queued/Cancel", queued, Cancel{Actor: "user", Now: at(1)}, func(t *testing.T, s State) {
			f := s.(Finished)
			if f.StartedAt != nil || f.CancelledAt == nil || !reflect.DeepEqual(f.Steps, []StepState{StepSkipped{}, StepSkipped{}}) {
				t.Fatalf("%#v", f)
			}
		}, []string{audit, pub, wake}},
		{"Queued/StepExited", queued, StepExited{Index: 0, Attempt: 1, Now: at(1)}, func(t *testing.T, s State) { _ = s.(Queued) }, nil},
		{"Queued/Enqueue", queued, Enqueue{ID: "b2", Spec: spec(1), Actor: "user", Now: at(1)}, func(t *testing.T, s State) { _ = s.(Queued) }, nil},
		{"Queued/ProcessLost", queued, ProcessLost{Now: at(1)}, func(t *testing.T, s State) { _ = s.(Queued) }, nil},
		{"Running/StepExited ok, more steps", running(t, 2), StepExited{Index: 0, Attempt: 1, Now: at(2)}, func(t *testing.T, s State) {
			r := s.(Running)
			if r.Current != 1 || !isSucceeded(r.Steps[0]) || r.Steps[1] != (StepRunning{1, at(2)}) {
				t.Fatalf("%#v", r)
			}
		}, []string{audit, "StartStep", pub}},
		{"Running/StepExited ok, last", last, StepExited{Index: 1, Attempt: 1, Now: at(3)}, func(t *testing.T, s State) {
			f := s.(Finished)
			if _, ok := f.Result.(ResultSucceeded); !ok || f.Artifacts != nil || f.CancelledAt != nil {
				t.Fatalf("%#v", f)
			}
		}, []string{audit, "CollectArtifacts", pub, wake}},
		{"Running/StepExited failed", running(t, 3), StepExited{Index: 0, Attempt: 1, ExitCode: 2, LogTruncated: true, Now: at(2)}, func(t *testing.T, s State) {
			f := s.(Finished)
			want := []StepState{StepDone{1, at(1), at(2), Failed{2}, true}, StepSkipped{}, StepSkipped{}}
			if f.Result != (ResultFailed{0}) || !reflect.DeepEqual(f.Steps, want) {
				t.Fatalf("%#v", f)
			}
		}, []string{audit, pub, wake}},
		{"Running/StepStartFailed", running(t, 2), StepStartFailed{Index: 0, Attempt: 1, Reason: StartNotFound, Now: at(2)}, func(t *testing.T, s State) {
			f := s.(Finished)
			if f.Result != (ResultFailed{0}) || f.Steps[0].(StepDone).Outcome != (StartFailed{StartNotFound}) {
				t.Fatalf("%#v", f)
			}
		}, []string{audit, pub, wake}},
		{"Running/StepStartFailed bad reason", running(t, 2), StepStartFailed{Index: 0, Attempt: 1, Reason: "x", Now: at(2)},
			func(t *testing.T, s State) { _ = s.(Running) }, nil},
		{"Running/StepExited stale index", running(t, 2), StepExited{Index: 1, Attempt: 1, Now: at(2)}, func(t *testing.T, s State) { _ = s.(Running) }, nil},
		{"Running/StepExited stale attempt", running(t, 2), StepExited{Index: 0, Attempt: 2, Now: at(2)}, func(t *testing.T, s State) { _ = s.(Running) }, nil},
		{"Running/StepExited negative index", running(t, 2), StepExited{Index: -1, Attempt: 1, Now: at(2)}, func(t *testing.T, s State) { _ = s.(Running) }, nil},
		{"Running/Cancel", running(t, 2), Cancel{Actor: "user", Now: at(2)}, func(t *testing.T, s State) {
			c := s.(Cancelling)
			if !c.CancelledAt.Equal(at(2)) {
				t.Fatalf("%#v", c)
			}
		}, []string{audit, "KillStep", pub}},
		{"Running/ProcessLost", running(t, 2), ProcessLost{Now: at(2)}, func(t *testing.T, s State) {
			f := s.(Finished)
			if f.Result != (ResultInterrupted{0}) || f.Steps[0].(StepDone).Outcome != (Interrupted{}) || f.CancelledAt != nil {
				t.Fatalf("%#v", f)
			}
		}, []string{audit, pub, wake}},
		{"Running/Admit", running(t, 2), Admit{Now: at(2)}, func(t *testing.T, s State) { _ = s.(Running) }, nil},
		{"Running/Enqueue", running(t, 2), Enqueue{ID: "b1", Spec: spec(1), Actor: "user", Now: at(2)}, func(t *testing.T, s State) { _ = s.(Running) }, nil},
		{"Running/ArtifactsCollected", running(t, 2), ArtifactsCollected{Now: at(2)}, func(t *testing.T, s State) { _ = s.(Running) }, nil},
		{"Cancelling/StepExited code 0", cancelling, StepExited{Index: 0, Attempt: 1, Now: at(4)}, func(t *testing.T, s State) {
			f := s.(Finished)
			if _, ok := f.Result.(ResultCancelled); !ok || f.CancelledAt == nil || !f.CancelledAt.Equal(at(3)) || f.Steps[0].(StepDone).Outcome != (Cancelled{}) {
				t.Fatalf("%#v", f)
			}
		}, []string{audit, pub, wake}},
		{"Cancelling/StepStartFailed", cancelling, StepStartFailed{Index: 0, Attempt: 1, Reason: StartOther, Now: at(4)}, func(t *testing.T, s State) {
			if _, ok := s.(Finished).Result.(ResultCancelled); !ok {
				t.Fatalf("%#v", s)
			}
		}, []string{audit, pub, wake}},
		{"Cancelling/ProcessLost", cancelling, ProcessLost{Now: at(4)}, func(t *testing.T, s State) {
			if _, ok := s.(Finished).Result.(ResultCancelled); !ok {
				t.Fatalf("%#v", s)
			}
		}, []string{audit, pub, wake}},
		{"Cancelling/StepExited stale", cancelling, StepExited{Index: 1, Attempt: 1, Now: at(4)}, func(t *testing.T, s State) { _ = s.(Cancelling) }, nil},
		{"Cancelling/Cancel", cancelling, Cancel{Actor: "user", Now: at(4)}, func(t *testing.T, s State) { _ = s.(Cancelling) }, nil},
		{"Finished/ArtifactsCollected", done, ArtifactsCollected{Artifacts: []Artifact{{RepoID: "r1", Path: "a.jar", Size: 3}}, Now: at(5)}, func(t *testing.T, s State) {
			if len(s.(Finished).Artifacts) != 1 {
				t.Fatalf("%#v", s)
			}
		}, []string{audit, pub}},
		{"Finished/ArtifactsCollected none found", done, ArtifactsCollected{Now: at(5)}, func(t *testing.T, s State) {
			if a := s.(Finished).Artifacts; a == nil || len(a) != 0 {
				t.Fatalf("artifacts = %#v, want empty non-nil", a)
			}
		}, []string{audit, pub}},
		{"Finished/Cancel", done, Cancel{Actor: "user", Now: at(5)}, func(t *testing.T, s State) { _ = s.(Finished) }, nil},
		{"Finished/Admit", done, Admit{Now: at(5)}, func(t *testing.T, s State) { _ = s.(Finished) }, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := c.from
			got, cmds := Step(c.from, c.in)
			c.check(t, got)
			if !reflect.DeepEqual(kinds(cmds), c.cmds) {
				t.Fatalf("commands %v, want %v", kinds(cmds), c.cmds)
			}
			if c.cmds == nil && !reflect.DeepEqual(got, before) {
				t.Fatal("no-op changed the state")
			}
		})
	}
}

func TestCancelWithInvalidActorIsNoop(t *testing.T) {
	queued, _ := apply(t, None{}, Enqueue{ID: "b1", Spec: spec(1), Actor: "user", Now: at(0)})
	for name, s := range map[string]State{"queued": queued, "running": running(t, 2)} {
		got, cmds := Step(s, Cancel{Actor: "", Now: at(5)})
		if !reflect.DeepEqual(got, s) || len(cmds) != 0 {
			t.Errorf("%s: Cancel with an invalid actor changed the state or emitted %v", name, cmds)
		}
	}
}

func TestProcessLostIsAuditedAsSystem(t *testing.T) {
	r := running(t, 2)
	cancelling, _ := apply(t, r, Cancel{Actor: "user", Now: at(3)})
	for name, s := range map[string]State{"running": r, "cancelling": cancelling} {
		_, cmds := Step(s, ProcessLost{Now: at(9)})
		found := false
		for _, c := range cmds {
			if a, ok := c.(WriteAudit); ok {
				found = true
				if a.Actor != "system" {
					t.Errorf("%s: ProcessLost audit actor %q, want system", name, a.Actor)
				}
			}
		}
		if !found {
			t.Errorf("%s: no audit entry", name)
		}
	}
}

func TestSecondArtifactsCollectedIsNoop(t *testing.T) {
	s, _ := apply(t, running(t, 1), StepExited{Index: 0, Attempt: 1, Now: at(2)}, ArtifactsCollected{Now: at(3)})
	got, cmds := Step(s, ArtifactsCollected{Artifacts: []Artifact{{RepoID: "r", Path: "x", Size: 1}}, Now: at(4)})
	if !reflect.DeepEqual(got, s) || len(cmds) != 0 {
		t.Fatal("second ArtifactsCollected changed the state")
	}
}

func TestArtifactsOnlyForSucceeded(t *testing.T) {
	s, _ := apply(t, running(t, 1), StepExited{Index: 0, Attempt: 1, ExitCode: 1, Now: at(2)})
	got, cmds := Step(s, ArtifactsCollected{Now: at(3)})
	if !reflect.DeepEqual(got, s) || len(cmds) != 0 {
		t.Fatal("failed build collected artifacts")
	}
}

func TestStartStepAndCollectData(t *testing.T) {
	sp := Spec{Target: InWorktree{WorktreeID: "w1"}, Steps: []StepSpec{
		{RepoID: "a", Name: "one", Argv: []string{"mvn", "verify"}},
		{RepoID: "b", Name: "two", Argv: []string{"npm", "test"}},
		{RepoID: "a", Name: "three", Argv: []string{"x"}},
	}}
	_, cmds := Run(None{}, []Input{
		Enqueue{ID: "b1", Spec: sp, Actor: "agent:x", Now: at(0)}, Admit{Now: at(1)},
		StepExited{Index: 0, Attempt: 1, Now: at(2)}, StepExited{Index: 1, Attempt: 1, Now: at(3)}, StepExited{Index: 2, Attempt: 1, Now: at(4)},
	})
	var starts []StartStep
	var collect CollectArtifacts
	for _, c := range cmds {
		switch x := c.(type) {
		case StartStep:
			starts = append(starts, x)
		case CollectArtifacts:
			collect = x
		}
	}
	if len(starts) != 3 || starts[1].RepoID != "b" || !reflect.DeepEqual(starts[1].Argv, []string{"npm", "test"}) || starts[1].Target != (InWorktree{WorktreeID: "w1"}) {
		t.Fatalf("starts = %#v", starts)
	}
	if len(collect.RepoIDs) != 2 || collect.RepoIDs[0] != "a" || collect.RepoIDs[1] != "b" {
		t.Fatalf("collect = %#v", collect)
	}
}

func TestStepDoesNotMutateInput(t *testing.T) {
	s := running(t, 3)
	before := reflect.ValueOf(s.(Running).Steps).Interface()
	snapshot := append([]StepState(nil), before.([]StepState)...)
	Step(s, StepExited{Index: 0, Attempt: 1, Now: at(2)})
	Step(s, StepExited{Index: 0, Attempt: 1, ExitCode: 1, Now: at(2)})
	if !reflect.DeepEqual(snapshot, s.(Running).Steps) {
		t.Fatal("Step mutated the previous state")
	}
}

func TestNext(t *testing.T) {
	q := []QueuedRef{{"b2", at(1)}, {"b3", at(0)}, {"b1", at(1)}}
	if id, ok := Next(q, 0, 1); !ok || id != "b3" {
		t.Fatalf("got %q %v", id, ok)
	}
	q[1].QueuedAt = at(1)
	if id, _ := Next(q, 0, 1); id != "b1" {
		t.Fatalf("tie not broken by ID: %q", id)
	}
	if _, ok := Next(q, 1, 1); ok {
		t.Fatal("admitted while running")
	}
	if _, ok := Next(nil, 0, 1); ok {
		t.Fatal("admitted from empty queue")
	}
	if id, ok := Next(q, 1, 2); !ok || id != "b1" {
		t.Fatalf("max 2: %q %v", id, ok)
	}
}

func TestOrderModules(t *testing.T) {
	c := func(a string) Coord { return Coord{Group: "g", Artifact: a, Version: "1"} }
	mods := []Module{
		{RepoID: "web", Coord: c("web"), Deps: []Coord{c("api"), {Group: "org.ext", Artifact: "lib"}}},
		{RepoID: "api", Coord: c("api"), Deps: []Coord{c("core")}},
		{RepoID: "core", Coord: c("core")},
		{RepoID: "x", Coord: c("x"), Deps: []Coord{c("y")}},
		{RepoID: "y", Coord: c("y"), Deps: []Coord{c("x")}},
		{RepoID: "solo", Coord: c("solo"), Deps: []Coord{c("solo")}},
	}
	order, cycles := OrderModules(mods)
	want := []string{"core", "api", "web", "x", "y", "solo"}
	var got []string
	for _, id := range order {
		got = append(got, string(id))
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if len(cycles) != 1 || len(cycles[0]) != 2 || cycles[0][0] != "x" || cycles[0][1] != "y" {
		t.Fatalf("cycles = %v", cycles)
	}
	if o, c := OrderModules(nil); o != nil || c != nil {
		t.Fatal("empty input")
	}
}
