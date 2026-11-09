package worktreeenv

import (
	"math/rand"
	"reflect"
	"slices"
	"strings"
	"testing"
)

var hashes = []string{"h1", "h2", "h3"}

func randHash(r *rand.Rand) string { return hashes[r.Intn(len(hashes))] }

func randComposeInput(r *rand.Rand) ComposeInput {
	att := r.Intn(4)
	switch r.Intn(13) {
	case 0:
		c := ""
		if r.Intn(2) == 0 {
			c = randHash(r)
		}
		return Enabled{Hash: randHash(r), Confirmed: c}
	case 1:
		return Disabled{}
	case 2, 3:
		return Confirmed{Hash: randHash(r)}
	case 4:
		return FileChanged{Hash: randHash(r)}
	case 5, 6, 7:
		h := randHash(r)
		if r.Intn(10) == 0 {
			h = ""
		}
		return UpRequested{Hash: h, Attempt: att, Now: epoch}
	case 8:
		return StopRequested{Attempt: att, Now: epoch}
	case 9:
		return DownRequested{Attempt: att, Now: epoch}
	case 10, 11:
		f := FailKind("")
		if r.Intn(3) == 0 {
			f = []FailKind{FailDockerMissing, FailDaemonDown, FailComposeError}[r.Intn(3)]
		}
		return OpFinished{Attempt: att, Failure: f, Summary: "s", Now: epoch}
	}
	return RecoveredPs{Result: []PsResult{PsRunning, PsNotRunning, PsUnavailable}[r.Intn(3)], Now: epoch}
}

func kind(s Service) string { return reflect.TypeOf(s).Name() }

func runs(cmds []ComposeCommand) []RunCompose {
	var out []RunCompose
	for _, c := range cmds {
		if rc, ok := c.(RunCompose); ok {
			out = append(out, rc)
		}
	}
	return out
}

func has[T ComposeCommand](cmds []ComposeCommand) bool {
	for _, c := range cmds {
		if _, ok := c.(T); ok {
			return true
		}
	}
	return false
}

func confirmedOf(s Service) string {
	switch v := s.(type) {
	case Down:
		return v.Confirmed
	case ComposeStarting:
		return v.Confirmed
	case Up:
		return v.Confirmed
	case ComposeStopping:
		return v.Confirmed
	case ComposeFailed:
		return v.Confirmed
	}
	return ""
}

// walk runs random inputs from Off and calls check after every step.
func walkCompose(t *testing.T, check func(prev Service, in ComposeInput, next Service, cmds []ComposeCommand)) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		var s Service = Off{}
		for range 80 {
			in := randComposeInput(r)
			before := in
			next, cmds := Compose(s, in)
			if !reflect.DeepEqual(before, in) {
				t.Fatal("step changed its input")
			}
			check(s, in, next, cmds)
			s = next
		}
	})
}

func TestPropUpNeedsConfirmedHash(t *testing.T) {
	// hashes the user confirmed (or the shell loaded from storage), as seen by the test
	confirmedSet := map[string]bool{}
	var seed string
	_ = seed
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		clear(confirmedSet)
		var s Service = Off{}
		for range 80 {
			in := randComposeInput(r)
			if e, ok := in.(Enabled); ok && e.Confirmed == e.Hash {
				confirmedSet[e.Hash] = true
			}
			next, cmds := Compose(s, in)
			for _, c := range cmds {
				if a, ok := c.(ComposeAudit); ok && a.Action == ActionComposeConfirmed {
					confirmedSet[in.(Confirmed).Hash] = true
				}
			}
			for _, rc := range runs(cmds) {
				if rc.Op != OpUp {
					continue
				}
				up, ok := in.(UpRequested)
				if !ok {
					t.Fatalf("Up emitted for %T", in)
				}
				switch s.(type) {
				case Down, ComposeFailed:
				default:
					t.Fatalf("Up emitted from %s", kind(s))
				}
				c := confirmedOf(s)
				if c == "" || c != up.Hash || !confirmedSet[c] {
					t.Fatalf("Up ran with confirmed %q, file %q, set %v", c, up.Hash, confirmedSet)
				}
			}
			s = next
		}
	})
}

func TestOpsAreClosedSet(t *testing.T) {
	walkCompose(t, func(_ Service, _ ComposeInput, _ Service, cmds []ComposeCommand) {
		for _, rc := range runs(cmds) {
			if !slices.Contains([]ComposeOp{OpUp, OpStop, OpDown}, rc.Op) {
				t.Fatalf("op %q is not one of up, stop, down", rc.Op)
			}
		}
	})
	if reflect.TypeOf(RunCompose{}).NumField() != 2 {
		t.Fatal("RunCompose must hold only Op and Attempt: no field for extra arguments")
	}
	if err := ComposeOp("down -v").Validate(); err == nil {
		t.Fatal("an unknown op must be rejected")
	}
	if _, err := Argv("down -v", "ygg-a", "c.yml", false); err == nil {
		t.Fatal("Argv must refuse an unknown op")
	}
}

func TestArgvTable(t *testing.T) {
	cases := []struct {
		op     ComposeOp
		file   RelPath
		hasEnv bool
		want   string
	}{
		{OpUp, "deploy/compose.yml", false, "docker compose -p ygg-feat-1 --file - --project-directory deploy up -d"},
		{OpUp, "deploy/compose.yml", true, "docker compose -p ygg-feat-1 --file - --project-directory deploy --env-file deploy/.env up -d"},
		{OpStop, "deploy/compose.yml", true, "docker compose -p ygg-feat-1 --file - --project-directory deploy --env-file deploy/.env stop"},
		{OpDown, "deploy/compose.yml", false, "docker compose -p ygg-feat-1 --file - --project-directory deploy down"},
		{OpPs, "compose.yml", false, "docker compose -p ygg-feat-1 --file - ps --format json"},
		{OpUp, "compose.yml", true, "docker compose -p ygg-feat-1 --file - --env-file .env up -d"},
		{OpDown, "a/b/c.yaml", true, "docker compose -p ygg-feat-1 --file - --project-directory a/b --env-file a/b/.env down"},
	}
	for _, c := range cases {
		got, err := Argv(c.op, "ygg-feat-1", c.file, c.hasEnv)
		if err != nil || strings.Join(got, " ") != c.want {
			t.Errorf("%s: %v %v", c.op, got, err)
		}
		for _, a := range got {
			if slices.Contains([]string{"-v", "--volumes", "--rmi", "--remove-orphans", "-f"}, a) {
				t.Errorf("%s: forbidden flag %s", c.op, a)
			}
		}
	}
}

func TestPropOneOperation(t *testing.T) {
	walkCompose(t, func(prev Service, _ ComposeInput, next Service, cmds []ComposeCommand) {
		n := len(runs(cmds))
		if n > 1 {
			t.Fatalf("%d operations at once", n)
		}
		if n == 1 {
			switch prev.(type) {
			case ComposeStarting, ComposeStopping:
				t.Fatalf("operation started while %s", kind(prev))
			}
			switch next.(type) {
			case ComposeStarting, ComposeStopping:
			default:
				t.Fatalf("operation started but state is %s", kind(next))
			}
		}
	})
}

func TestPropOffRunsNothing(t *testing.T) {
	walkCompose(t, func(prev Service, _ ComposeInput, _ Service, cmds []ComposeCommand) {
		if _, off := prev.(Off); off && len(runs(cmds)) > 0 {
			t.Fatal("Off ran an operation")
		}
	})
}

func TestPropChangedFileNeedsConfirm(t *testing.T) {
	walkCompose(t, func(prev Service, in ComposeInput, next Service, cmds []ComposeCommand) {
		// a file that differs from the confirmed one never reaches Up
		if up, ok := in.(UpRequested); ok {
			if c := confirmedOf(prev); c != up.Hash && len(runs(cmds)) > 0 {
				t.Fatal("Up ran for a file that was not confirmed")
			}
		}
		// Down always sees the confirmed file: a different one moves to NeedsConfirm
		if fc, ok := in.(FileChanged); ok {
			if d, ok := prev.(Down); ok && fc.Hash != d.Confirmed {
				if nc, ok := next.(NeedsConfirm); !ok || nc.FileHash != fc.Hash {
					t.Fatalf("changed file left Down as %s", kind(next))
				}
			}
		}
		// NeedsConfirm leaves only through a matching Confirmed (or Disabled, or an observed running project)
		if prevNC, ok := prev.(NeedsConfirm); ok {
			if _, still := next.(NeedsConfirm); !still {
				switch v := in.(type) {
				case Confirmed:
					if v.Hash != prevNC.FileHash {
						t.Fatal("confirmed a different hash")
					}
				case Disabled, RecoveredPs:
				default:
					t.Fatalf("left NeedsConfirm on %T", in)
				}
			}
		}
	})
}

func TestPropStatusChangeAudited(t *testing.T) {
	walkCompose(t, func(prev Service, in ComposeInput, next Service, cmds []ComposeCommand) {
		if kind(prev) != kind(next) {
			if !has[ComposeAudit](cmds) || !has[ComposePublish](cmds) {
				t.Fatalf("%s -> %s on %T without audit and publish", kind(prev), kind(next), in)
			}
		}
	})
}

func TestShutdownRunsNothing(t *testing.T) {
	// There is no shutdown input: the closed input set cannot ask for an
	// operation at exit. The nearest input, Disabled, runs nothing in any state.
	states := []Service{Off{}, NeedsConfirm{FileHash: "h1"}, Down{Confirmed: "h1"}, ComposeStarting{Confirmed: "h1", Attempt: 1},
		Up{Confirmed: "h1"}, ComposeStopping{Confirmed: "h1", Op: OpStop, Attempt: 1}, ComposeFailed{Confirmed: "h1"}}
	for _, s := range states {
		if _, cmds := Compose(s, Disabled{}); len(runs(cmds)) != 0 {
			t.Errorf("%s: Disabled ran an operation", kind(s))
		}
	}
	for _, in := range []ComposeInput{Enabled{}, Disabled{}, Confirmed{}, FileChanged{}, UpRequested{}, StopRequested{}, DownRequested{}, OpFinished{}, RecoveredPs{}} {
		n := reflect.TypeOf(in).Name()
		if strings.Contains(strings.ToLower(n), "shutdown") {
			t.Errorf("input %s must not exist", n)
		}
	}
}

func TestPropOnlyAttemptFinishes(t *testing.T) {
	walkCompose(t, func(prev Service, in ComposeInput, next Service, cmds []ComposeCommand) {
		of, ok := in.(OpFinished)
		if !ok {
			return
		}
		switch p := prev.(type) {
		case ComposeStarting:
			if of.Attempt != p.Attempt && (!reflect.DeepEqual(prev, next) || len(cmds) != 0) {
				t.Fatal("a stale result changed the state")
			}
		case ComposeStopping:
			if of.Attempt != p.Attempt && (!reflect.DeepEqual(prev, next) || len(cmds) != 0) {
				t.Fatal("a stale result changed the state")
			}
		default:
			if !reflect.DeepEqual(prev, next) || len(cmds) != 0 {
				t.Fatalf("OpFinished in %s changed something", kind(prev))
			}
		}
	})
}

func TestComposeTransitions(t *testing.T) {
	var s Service = Off{}
	step := func(in ComposeInput) []ComposeCommand {
		var c []ComposeCommand
		s, c = Compose(s, in)
		return c
	}
	step(Enabled{Hash: "h1"})
	if s != (NeedsConfirm{FileHash: "h1"}) {
		t.Fatal(s)
	}
	if c := step(UpRequested{Hash: "h1", Attempt: 1}); len(runs(c)) != 0 || !has[ComposeReject](c) {
		t.Fatal("up before confirm")
	}
	step(Confirmed{Hash: "h1"})
	if c := step(UpRequested{Hash: "h1", Attempt: 1, Now: epoch}); len(runs(c)) != 1 || runs(c)[0].Op != OpUp {
		t.Fatal("up after confirm", c)
	}
	step(OpFinished{Attempt: 1, Now: epoch})
	if _, ok := s.(Up); !ok {
		t.Fatal(s)
	}
	if c := step(StopRequested{Attempt: 2}); runs(c)[0].Op != OpStop {
		t.Fatal("stop")
	}
	step(OpFinished{Attempt: 2, Failure: FailComposeError, Summary: "x", Now: epoch})
	if _, ok := s.(ComposeFailed); !ok {
		t.Fatal(s)
	}
	step(FileChanged{Hash: "h2"})
	if s != (NeedsConfirm{FileHash: "h2"}) {
		t.Fatal(s)
	}
	step(Disabled{})
	if s != (Off{}) {
		t.Fatal(s)
	}
	// startup recovery of a running project
	step(Enabled{Hash: "h1", Confirmed: "h1"})
	step(RecoveredPs{Result: PsRunning, Now: epoch})
	if _, ok := s.(Up); !ok {
		t.Fatal(s)
	}
}
