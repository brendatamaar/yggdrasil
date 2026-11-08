package devservers

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

const (
	propRuns  = 300
	propSteps = 80
)

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func attemptOf(s Server) int {
	switch st := s.(type) {
	case Stopped:
		return st.Attempt
	case Starting:
		return st.Attempt
	case Running:
		return st.Attempt
	case Stopping:
		return st.Attempt
	case Failed:
		return st.Attempt
	case Orphaned:
		return st.Attempt
	}
	return 0
}

func isLive(s Server) bool {
	switch s.(type) {
	case Starting, Running, Stopping:
		return true
	}
	return false
}

func isRest(s Server) bool {
	switch s.(type) {
	case Stopped, Failed:
		return true
	}
	return false
}

var blocks = []core.PortBlock{{}, {Base: 20000, Size: 20}, {Base: 20000, Size: 20}, {Base: 100, Size: 5}, {Base: 65530, Size: 20}}

var templates = []Template{
	{Argv: []string{"npm", "run", "dev"}},
	{Argv: []string{"vite", "--port", "{port}"}},
	{Argv: []string{"vite", "--port", "{port:1}", "--x={port:19}"}, Env: map[string]string{"A": "b"}},
	{Argv: []string{"vite", "--port", "{port:25}"}},
	{Argv: []string{"{port}"}},
	{Argv: []string{"x", "{nope}"}},
}

func randPorts(r *rand.Rand) []Port {
	pool := []Port{-1, 0, 1, 80, 3000, 20000, 20001, 20019, 20020, 65535, 65536, 70000}
	n := r.Intn(4)
	out := make([]Port, n)
	for i := range out {
		out[i] = pool[r.Intn(len(pool))]
	}
	return out
}

// gen makes a random input for the state. maxAttempt is the highest attempt
// the run has used; closed says Yggdrasil is shutting down (the shell then
// sends no StartRequested).
func gen(r *rand.Rand, cur Server, maxAttempt int, closed bool, now time.Time) Input {
	if r.Intn(10) < 6 {
		if in := wellSequenced(r, cur, maxAttempt, closed, now); in != nil {
			return in
		}
	}
	att := attemptOf(cur)
	if r.Intn(4) == 0 {
		att += r.Intn(3) - 1 // stale or future
	}
	switch r.Intn(13) {
	case 0, 1:
		if closed {
			return OrphanStopped{Now: now}
		}
		a := maxAttempt + 1
		if r.Intn(5) == 0 {
			a = r.Intn(maxAttempt + 1)
		}
		return StartRequested{Attempt: a, Now: now, Template: templates[r.Intn(len(templates))], Block: blocks[r.Intn(len(blocks))]}
	case 2:
		return Started{Attempt: att, Pid: r.Intn(4), Now: now}
	case 3:
		return StartFailed{Attempt: att, Reason: "no", Now: now}
	case 4, 5:
		return PortsObserved{Attempt: att, Ports: portsInts(randPorts(r)), Now: now}
	case 6:
		return OutputAnnounced{Attempt: att, Ports: randPorts(r)}
	case 7, 8:
		kinds := []WakeKind{WakeSettle, WakeKillGrace, WakePoll}
		return Wake{Attempt: att, Kind: kinds[r.Intn(3)], Now: now}
	case 9:
		return Exited{Attempt: att, Code: []int{0, 0, 1, 137}[r.Intn(4)], Now: now}
	case 10:
		c := []StopCause{CauseUserStop, CauseRestart, CauseAppShutdown}
		return StopRequested{Cause: c[r.Intn(3)], Now: now}
	case 11:
		switch r.Intn(12) {
		case 0:
			return ShutdownRequested{Now: now}
		case 1:
			return Recovered{Liveness: Dead{}, Now: now}
		case 2:
			return Recovered{Liveness: Alive{Pid: r.Intn(3)}, Now: now}
		}
		return StopRequested{Cause: CauseUserStop, Now: now}
	}
	if r.Intn(3) == 0 {
		return OrphanStopFailed{Now: now}
	}
	return OrphanStopped{Now: now}
}

func portsInts(ps []Port) []Port { return ps }

type checker func(prev Server, in Input, next Server, cmds []Command) error

// drive runs random sequences and applies the checker after every step. The
// shell-like driver stops sending StartRequested after ShutdownRequested.
func drive(t *testing.T, mk func() checker) {
	t.Helper()
	for seed := int64(1); seed <= propRuns; seed++ {
		r := rand.New(rand.NewSource(seed))
		check := mk()
		var cur Server = Stopped{Last: LastNever}
		maxAttempt, closed := 0, false
		now := epoch
		for i := 0; i < propSteps; i++ {
			now = now.Add(time.Second)
			in := gen(r, cur, maxAttempt, closed, now)
			if s, ok := in.(StartRequested); ok && s.Attempt > maxAttempt {
				maxAttempt = s.Attempt
			}
			if _, ok := in.(ShutdownRequested); ok {
				closed = true
			}
			next, cmds := Step(cur, in)
			if err := check(cur, in, next, cmds); err != nil {
				t.Fatalf("seed %d step %d: %v\n  prev %#v\n  in   %#v\n  next %#v\n  cmds %#v", seed, i, err, cur, in, next, cmds)
			}
			cur = next
		}
	}
}

func has[T Command](cmds []Command) bool {
	for _, c := range cmds {
		if _, ok := c.(T); ok {
			return true
		}
	}
	return false
}

func count[T Command](cmds []Command) int {
	n := 0
	for _, c := range cmds {
		if _, ok := c.(T); ok {
			n++
		}
	}
	return n
}

func unchanged(prev, next Server, cmds []Command) error {
	if !reflect.DeepEqual(prev, next) || len(cmds) != 0 {
		return fmt.Errorf("expected no change and no commands")
	}
	return nil
}

// wellSequenced is the input a healthy run would send next, so the random
// runs reach Running, Stopping and Orphaned often.
func wellSequenced(r *rand.Rand, cur Server, maxAttempt int, closed bool, now time.Time) Input {
	att := attemptOf(cur)
	switch cur.(type) {
	case Stopped, Failed:
		if closed {
			return nil
		}
		return StartRequested{Attempt: maxAttempt + 1, Now: now, Template: templates[r.Intn(3)], Block: blocks[1+r.Intn(2)]}
	case Starting:
		switch r.Intn(4) {
		case 0:
			return Started{Attempt: att, Pid: 1 + r.Intn(100), Now: now}
		case 1:
			return PortsObserved{Attempt: att, Ports: []Port{20000 + Port(r.Intn(25))}, Now: now}
		case 2:
			return Wake{Attempt: att, Kind: WakeSettle, Now: now}
		}
		return Exited{Attempt: att, Code: r.Intn(2), Now: now}
	case Running:
		switch r.Intn(5) {
		case 0:
			return PortsObserved{Attempt: att, Ports: randPorts(r), Now: now}
		case 1:
			return Wake{Attempt: att, Kind: WakePoll, Now: now}
		case 2:
			return OutputAnnounced{Attempt: att, Ports: randPorts(r)}
		case 3:
			return Exited{Attempt: att, Code: r.Intn(2), Now: now}
		}
		return StopRequested{Cause: []StopCause{CauseUserStop, CauseRestart}[r.Intn(2)], Now: now}
	case Stopping:
		if r.Intn(2) == 0 {
			return Exited{Attempt: att, Code: r.Intn(2), Now: now}
		}
		return Wake{Attempt: att, Kind: WakeKillGrace, Now: now}
	case Orphaned:
		switch r.Intn(3) {
		case 0:
			return StopRequested{Cause: CauseUserStop, Now: now}
		case 1:
			return OrphanStopFailed{Now: now}
		}
		return OrphanStopped{Now: now}
	}
	return nil
}
