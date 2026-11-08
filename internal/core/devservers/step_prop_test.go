package devservers

import (
	"fmt"
	"math/rand"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// D1: at most one live attempt; a StartRequested in a live state changes
// nothing (it only answers Reject).
func TestPropOneLiveAttempt(t *testing.T) {
	drive(t, func() checker {
		return func(prev Server, in Input, next Server, cmds []Command) error {
			if isLive(next) && attemptOf(next) < 1 {
				return fmt.Errorf("live state without an attempt")
			}
			if _, ok := in.(StartRequested); ok && (isLive(prev) || isOrphan(prev)) {
				if !reflect.DeepEqual(prev, next) || len(cmds) != 1 {
					return fmt.Errorf("start in a live state changed something")
				}
				if r, ok := cmds[0].(Reject); !ok || r.Reason != RejectAlreadyRunning {
					return fmt.Errorf("expected Reject{AlreadyRunning}")
				}
			}
			if isLive(prev) && isLive(next) && attemptOf(prev) != attemptOf(next) {
				return fmt.Errorf("the live attempt changed")
			}
			return nil
		}
	})
}

func isOrphan(s Server) bool { _, ok := s.(Orphaned); return ok }

// D2: StartProcess only on rest -> Starting, once per attempt.
func TestPropStartOnce(t *testing.T) {
	drive(t, func() checker {
		seen := map[int]bool{}
		return func(prev Server, in Input, next Server, cmds []Command) error {
			n := count[StartProcess](cmds)
			_, nextStarting := next.(Starting)
			if isRest(prev) && nextStarting {
				if n != 1 {
					return fmt.Errorf("rest -> Starting emitted %d StartProcess", n)
				}
			} else if n != 0 {
				return fmt.Errorf("StartProcess outside rest -> Starting")
			}
			for _, c := range cmds {
				if sp, ok := c.(StartProcess); ok {
					if seen[sp.Attempt] {
						return fmt.Errorf("attempt %d started twice", sp.Attempt)
					}
					seen[sp.Attempt] = true
				}
			}
			return nil
		}
	})
}

func hasAttemptInput(in Input) (int, bool) {
	switch v := in.(type) {
	case Started:
		return v.Attempt, true
	case StartFailed:
		return v.Attempt, true
	case PortsObserved:
		return v.Attempt, true
	case OutputAnnounced:
		return v.Attempt, true
	case Wake:
		return v.Attempt, true
	case Exited:
		return v.Attempt, true
	}
	return 0, false
}

// D3: a stale attempt changes nothing; the attempt never decreases.
func TestPropStaleIgnored(t *testing.T) {
	drive(t, func() checker {
		return func(prev Server, in Input, next Server, cmds []Command) error {
			if a, ok := hasAttemptInput(in); ok && a != attemptOf(prev) {
				if err := unchanged(prev, next, cmds); err != nil {
					return fmt.Errorf("stale attempt %d (state %d): %w", a, attemptOf(prev), err)
				}
			}
			if s, ok := in.(StartRequested); ok && isRest(prev) && s.Attempt <= attemptOf(prev) {
				if err := unchanged(prev, next, cmds); err != nil {
					return fmt.Errorf("old start attempt: %w", err)
				}
			}
			if attemptOf(next) < attemptOf(prev) {
				return fmt.Errorf("attempt decreased")
			}
			return nil
		}
	})
}

// D4: Stopping is entered together with KillProcess and leaves only to
// Stopped or Failed (or, at startup recovery, Orphaned).
func TestPropStoppingEnds(t *testing.T) {
	drive(t, func() checker {
		return func(prev Server, in Input, next Server, cmds []Command) error {
			if _, ok := next.(Stopping); ok {
				if _, was := prev.(Stopping); !was || !reflect.DeepEqual(prev, next) {
					if !has[KillProcess](cmds) {
						return fmt.Errorf("Stopping without KillProcess")
					}
				}
			}
			if _, was := prev.(Stopping); was {
				switch next.(type) {
				case Stopping, Stopped, Failed:
				case Orphaned:
					if _, ok := in.(Recovered); !ok {
						return fmt.Errorf("Stopping -> Orphaned without Recovered")
					}
				default:
					return fmt.Errorf("Stopping -> %T", next)
				}
			}
			return nil
		}
	})
}

// D5: no automatic restart: StartProcess only in the step of a StartRequested.
func TestPropNoAutoRestart(t *testing.T) {
	drive(t, func() checker {
		return func(prev Server, in Input, next Server, cmds []Command) error {
			if _, ok := in.(StartRequested); !ok && has[StartProcess](cmds) {
				return fmt.Errorf("StartProcess after %T", in)
			}
			return nil
		}
	})
}

// D6: after ShutdownRequested no server is Starting or Running and no step
// starts a process (the shell sends no StartRequested after it).
func TestPropShutdownDrains(t *testing.T) {
	drive(t, func() checker {
		closed := false
		return func(prev Server, in Input, next Server, cmds []Command) error {
			if _, ok := in.(ShutdownRequested); ok {
				closed = true
			}
			if closed {
				if has[StartProcess](cmds) {
					return fmt.Errorf("StartProcess after shutdown")
				}
				switch next.(type) {
				case Starting, Running:
					return fmt.Errorf("%T after shutdown", next)
				}
			}
			return nil
		}
	})
}

// D7: Running ports are sorted, unique, in range; warnings iff a port lies
// outside a non-empty block.
func TestPropPortsWellFormed(t *testing.T) {
	drive(t, func() checker {
		return func(prev Server, in Input, next Server, cmds []Command) error {
			rn, ok := next.(Running)
			if !ok {
				return nil
			}
			for _, ps := range [][]Port{rn.Ports, rn.Announced} {
				if !validPorts(ps) {
					return fmt.Errorf("ports %v not sorted, unique and in range", ps)
				}
			}
			outside := false
			for _, p := range rn.Ports {
				if !rn.Block.Empty() && !rn.Block.Contains(int(p)) {
					outside = true
				}
			}
			if outside != (len(rn.Warnings()) > 0) {
				return fmt.Errorf("warnings %v do not match ports %v in block %+v", rn.Warnings(), rn.Ports, rn.Block)
			}
			return nil
		}
	})
}

func TestOutsideBlockTable(t *testing.T) {
	b := core.PortBlock{Base: 20000, Size: 20}
	tests := []struct {
		name  string
		ports []Port
		block core.PortBlock
		want  []Port
	}{
		{"inside", []Port{20000, 20019}, b, nil},
		{"one past", []Port{20020}, b, []Port{20020}},
		{"one before", []Port{19999, 20000}, b, []Port{19999}},
		{"no block no warnings", []Port{3000}, core.PortBlock{}, nil},
		{"sorted and unique", []Port{3000, 80, 3000}, b, []Port{80, 3000}},
		{"empty ports", nil, b, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := OutsideBlock(tc.ports, tc.block); !slices.Equal(got, tc.want) {
				t.Errorf("OutsideBlock = %v, want %v", got, tc.want)
			}
		})
	}
}

// D8: every expansion lies inside the block; a template that cannot expand is
// rejected whole, never partly started.
func TestPropExpansionInsideBlock(t *testing.T) {
	for seed := int64(1); seed <= propRuns; seed++ {
		r := rand.New(rand.NewSource(seed))
		block := core.PortBlock{Base: 1 + r.Intn(60000), Size: r.Intn(21)}
		argv := []string{"prog"}
		var idx []int
		for i := 0; i < 1+r.Intn(4); i++ {
			if r.Intn(2) == 0 {
				argv = append(argv, "--p={port}")
				idx = append(idx, 0)
			} else {
				n := r.Intn(core.PortBlockSize)
				argv = append(argv, "--p={port:"+strconv.Itoa(n)+"}")
				idx = append(idx, n)
			}
		}
		tpl, err := ParseTemplate(argv, nil)
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		exp, err := Expand(tpl, block)
		fits := !block.Empty() && block.Validate() == nil
		for _, n := range idx {
			fits = fits && n < block.Size
		}
		if (err == nil) != fits {
			t.Fatalf("seed %d: block %+v argv %v: err=%v, fits=%v", seed, block, argv, err, fits)
		}
		if err != nil {
			if len(exp.Argv) != 0 || len(exp.Env) != 0 {
				t.Fatalf("seed %d: rejected expansion is not empty", seed)
			}
			continue
		}
		for i, n := range idx {
			got, convErr := strconv.Atoi(strings.TrimPrefix(exp.Argv[i+1], "--p="))
			if convErr != nil || !block.Contains(got) || got != block.Base+n {
				t.Fatalf("seed %d: arg %q outside block %+v", seed, exp.Argv[i+1], block)
			}
		}
	}
	// In the machine: a start is either Starting with a process, or unchanged with one Reject.
	drive(t, func() checker {
		return func(prev Server, in Input, next Server, cmds []Command) error {
			s, ok := in.(StartRequested)
			if !ok || !isRest(prev) || s.Attempt <= attemptOf(prev) || s.Attempt < 1 {
				return nil
			}
			if _, started := next.(Starting); started {
				for _, c := range cmds {
					if sp, ok := c.(StartProcess); ok {
						for k, v := range sp.Expanded.Env {
							if strings.HasPrefix(k, "YGG_PORT_") || k == "PORT" {
								n, err := strconv.Atoi(v)
								if err != nil || !s.Block.Contains(n) {
									return fmt.Errorf("%s=%s outside block", k, v)
								}
							}
						}
					}
				}
				return nil
			}
			if err := func() error {
				if len(cmds) != 1 {
					return fmt.Errorf("want exactly one Reject, got %d commands", len(cmds))
				}
				return nil
			}(); err != nil {
				return err
			}
			if r, ok := cmds[0].(Reject); !ok || r.Reason != RejectBadTemplate || r.Message == "" {
				return fmt.Errorf("want Reject{BadTemplate} with a message")
			}
			return unchangedState(prev, next)
		}
	})
}

func unchangedState(prev, next Server) error {
	if !reflect.DeepEqual(prev, next) {
		return fmt.Errorf("state changed")
	}
	return nil
}

// D9: Recovered leaves nothing live; Orphaned only comes from Alive; an
// orphan never starts.
func TestPropRecoveredNothingLive(t *testing.T) {
	drive(t, func() checker {
		return func(prev Server, in Input, next Server, cmds []Command) error {
			rec, ok := in.(Recovered)
			if !ok {
				return nil
			}
			if isLive(next) {
				return fmt.Errorf("live after Recovered")
			}
			if _, orphan := next.(Orphaned); orphan && !isOrphan(prev) {
				a, alive := rec.Liveness.(Alive)
				if !alive || a.Pid < 1 || !isLive(prev) {
					return fmt.Errorf("Orphaned without Alive from a live state")
				}
			}
			if isLive(prev) && !has[WriteAudit](cmds) {
				return fmt.Errorf("recovery without audit")
			}
			if !isLive(prev) && (!reflect.DeepEqual(prev, next) || len(cmds) != 0) {
				return fmt.Errorf("Recovered changed a server at rest")
			}
			return nil
		}
	})
}

func TestPropOrphanNeverStarts(t *testing.T) {
	drive(t, func() checker {
		return func(prev Server, in Input, next Server, cmds []Command) error {
			if isOrphan(prev) {
				if has[StartProcess](cmds) {
					return fmt.Errorf("orphan started")
				}
				switch next.(type) {
				case Orphaned, Stopped:
				default:
					return fmt.Errorf("Orphaned -> %T", next)
				}
			}
			if isOrphan(next) && !isOrphan(prev) {
				if _, ok := in.(Recovered); !ok {
					return fmt.Errorf("Orphaned from %T", in)
				}
			}
			return nil
		}
	})
}

// D10: every change of status has an Audit and a Publish.
func TestPropStatusChangeAudited(t *testing.T) {
	drive(t, func() checker {
		return func(prev Server, in Input, next Server, cmds []Command) error {
			if prev.Status() != next.Status() && (!has[WriteAudit](cmds) || !has[Publish](cmds)) {
				return fmt.Errorf("status %s -> %s without audit and publish", prev.Status(), next.Status())
			}
			for _, c := range cmds {
				if a, ok := c.(WriteAudit); ok && a.Action.Validate() != nil {
					return fmt.Errorf("unknown audit action %q", a.Action)
				}
			}
			return nil
		}
	})
}

// D11: Running and Stopping know the pid; StartProcess env holds only the
// template's names and the port variables, none token-like.
func TestPropEnvOnlyPorts(t *testing.T) {
	drive(t, func() checker {
		return func(prev Server, in Input, next Server, cmds []Command) error {
			switch st := next.(type) {
			case Running:
				if st.Pid < 1 {
					return fmt.Errorf("Running without pid")
				}
			case Stopping:
				if st.Pid < 1 {
					return fmt.Errorf("Stopping without pid")
				}
			}
			s, _ := in.(StartRequested)
			for _, c := range cmds {
				sp, ok := c.(StartProcess)
				if !ok {
					continue
				}
				for k := range sp.Expanded.Env {
					_, inTemplate := s.Template.Env[k]
					port := k == EnvPort || k == EnvPortBase || strings.HasPrefix(k, "YGG_PORT_")
					if !inTemplate && !port {
						return fmt.Errorf("unexpected env %s", k)
					}
					if secretName(k) {
						return fmt.Errorf("token-like env %s", k)
					}
				}
			}
			return nil
		}
	})
}

// D12: no command has a path field, and audit details hold no path.
func TestCommandsCarryNoPaths(t *testing.T) {
	for _, c := range []Command{StartProcess{}, KillProcess{}, PollPorts{}, ScheduleWake{}, StopOrphan{}, Publish{}, WriteAudit{}, Reject{}} {
		ty := reflect.TypeOf(c)
		for i := 0; i < ty.NumField(); i++ {
			n := strings.ToLower(ty.Field(i).Name)
			for _, bad := range []string{"dir", "path", "cwd", "folder", "file"} {
				if strings.Contains(n, bad) {
					t.Errorf("%s has path-like field %s", ty.Name(), ty.Field(i).Name)
				}
			}
		}
	}
	drive(t, func() checker {
		return func(prev Server, in Input, next Server, cmds []Command) error {
			for _, c := range cmds {
				if a, ok := c.(WriteAudit); ok {
					for k, v := range a.Detail {
						if strings.ContainsAny(k+v, `/\`) {
							return fmt.Errorf("audit detail %s=%s looks like a path", k, v)
						}
					}
				}
			}
			return nil
		}
	})
}

// Step does not change its input state or input.
func TestPropStepDoesNotMutateInput(t *testing.T) {
	for seed := int64(1); seed <= propRuns; seed++ {
		r := rand.New(rand.NewSource(seed))
		var cur Server = Stopped{}
		maxA := 0
		for i := 0; i < propSteps; i++ {
			in := gen(r, cur, maxA, false, epoch.Add(5*1e9))
			if s, ok := in.(StartRequested); ok && s.Attempt > maxA {
				maxA = s.Attempt
			}
			beforeS, beforeI := fmt.Sprintf("%#v", cur), fmt.Sprintf("%#v", in)
			next, _ := Step(cur, in)
			if fmt.Sprintf("%#v", cur) != beforeS || fmt.Sprintf("%#v", in) != beforeI {
				t.Fatalf("seed %d: Step changed its input (%T on %T)", seed, in, cur)
			}
			cur = next
		}
	}
}

// Every state is reachable and the table's main path works end to end.
func TestStepHappyPath(t *testing.T) {
	b := core.PortBlock{Base: 20000, Size: 20}
	tpl := Template{Argv: []string{"vite", "--port", "{port}"}}
	s, cmds := Step(Stopped{}, StartRequested{Attempt: 1, Now: epoch, Template: tpl, Block: b})
	if _, ok := s.(Starting); !ok || !has[StartProcess](cmds) {
		t.Fatalf("start: %#v %#v", s, cmds)
	}
	if sp := cmds[0].(StartProcess); sp.Expanded.Argv[2] != "20000" || sp.Expanded.Env["YGG_PORT_3"] != "20003" {
		t.Errorf("expansion: %#v", sp.Expanded)
	}
	s, _ = Step(s, Started{Attempt: 1, Pid: 42, Now: epoch})
	s, _ = Step(s, PortsObserved{Attempt: 1, Ports: []Port{20000, 3000, 20000}, Now: epoch})
	rn, ok := s.(Running)
	if !ok || !slices.Equal(rn.Ports, []Port{3000, 20000}) || !slices.Equal(rn.Warnings(), []Port{3000}) {
		t.Fatalf("running: %#v", s)
	}
	s, cmds = Step(s, StopRequested{Cause: CauseUserStop, Now: epoch})
	if st, ok := s.(Stopping); !ok || st.KillAttempt != KillFirst || !has[KillProcess](cmds) {
		t.Fatalf("stop: %#v", s)
	}
	s, _ = Step(s, Wake{Attempt: 1, Kind: WakeKillGrace, Now: epoch})
	s, _ = Step(s, Wake{Attempt: 1, Kind: WakeKillGrace, Now: epoch})
	if f, ok := s.(Failed); !ok || f.Kind != FailStopFailed {
		t.Fatalf("second grace: %#v", s)
	}
	s, _ = Step(s, StartRequested{Attempt: 2, Now: epoch, Template: tpl, Block: b})
	if _, ok := s.(Starting); !ok {
		t.Fatalf("restart after failure: %#v", s)
	}
}

func TestTimingConstants(t *testing.T) {
	if SettleAfter.Seconds() != 10 || PollStarting.Seconds() != 1 || PollRunning.Seconds() != 2 || KillGraceUser.Seconds() != 10 || KillGraceClose.Seconds() != 5 {
		t.Fatal("timing constants changed")
	}
	s, cmds := Step(Running{Attempt: 1, Pid: 2, Since: epoch}, ShutdownRequested{Now: epoch})
	_ = s
	for _, c := range cmds {
		if w, ok := c.(ScheduleWake); ok && w.At.Sub(epoch) != KillGraceClose {
			t.Errorf("shutdown grace = %v", w.At.Sub(epoch))
		}
	}
}

func TestStepStopBeforePid(t *testing.T) {
	st := Starting{Attempt: 3, Since: epoch}
	s, cmds := Step(st, StopRequested{Cause: CauseRestart, Now: epoch})
	if _, ok := s.(Stopped); !ok || !has[KillProcess](cmds) {
		t.Fatalf("got %#v %#v", s, cmds)
	}
	s, _ = Step(st, ShutdownRequested{Now: epoch})
	if sp, ok := s.(Stopped); !ok || sp.Last != LastInterrupted {
		t.Fatalf("shutdown before pid: %#v", s)
	}
}
