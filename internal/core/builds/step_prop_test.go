package builds

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func stepsOf(s State) []StepState {
	switch st := s.(type) {
	case Running:
		return st.Steps
	case Cancelling:
		return st.Steps
	case Finished:
		return st.Steps
	}
	return nil
}

func isSucceeded(ss StepState) bool {
	d, ok := ss.(StepDone)
	if !ok {
		return false
	}
	_, ok = d.Outcome.(Succeeded)
	return ok
}

func count[T Command](cmds []Command) (n int) {
	for _, c := range cmds {
		if _, ok := c.(T); ok {
			n++
		}
	}
	return n
}

func stateless(f checker) func() checker { return func() checker { return f } }

// I1
func TestPropRunningShape(t *testing.T) {
	drive(t, stateless(func(_ State, _ Input, next State, _ []Command) error {
		var run Running
		switch st := next.(type) {
		case Running:
			run = st
		case Cancelling:
			run = st.Running
		default:
			return nil
		}
		if len(run.Steps) != len(run.Spec.Steps) || run.Current < 0 || run.Current >= len(run.Steps) {
			return errors.New("steps and current do not fit the spec")
		}
		for i, ss := range run.Steps {
			switch {
			case i < run.Current && !isSucceeded(ss):
				return fmt.Errorf("step %d before current is %#v", i, ss)
			case i == run.Current:
				if _, ok := ss.(StepRunning); !ok {
					return fmt.Errorf("current step is %#v", ss)
				}
			case i > run.Current:
				if _, ok := ss.(StepPending); !ok {
					return fmt.Errorf("step %d after current is %#v", i, ss)
				}
			}
		}
		return nil
	}))
}

// I2
func TestPropFinishedHasNoLiveStep(t *testing.T) {
	drive(t, stateless(func(_ State, _ Input, next State, _ []Command) error {
		switch next.(type) {
		case Finished:
			for i, ss := range stepsOf(next) {
				switch ss.(type) {
				case StepPending, StepRunning:
					return fmt.Errorf("step %d is live in Finished", i)
				}
			}
		case Queued:
			if stepsOf(next) != nil {
				return errors.New("Queued has step state")
			}
		}
		return nil
	}))
}

// I3
func TestPropStepsStartInOrderOnce(t *testing.T) {
	drive(t, func() checker {
		want := 0
		return func(_ State, _ Input, _ State, cmds []Command) error {
			for _, c := range cmds {
				if ss, ok := c.(StartStep); ok {
					if ss.Index != want || ss.Attempt != 1 {
						return fmt.Errorf("StartStep(%d,%d), want index %d attempt 1", ss.Index, ss.Attempt, want)
					}
					want++
				}
			}
			return nil
		}
	})
}

// I4
func TestPropStopsAtFirstFailure(t *testing.T) {
	drive(t, stateless(func(_ State, _ Input, next State, _ []Command) error {
		steps := stepsOf(next)
		for i, ss := range steps {
			if _, ok := ss.(StepDone); !ok || isSucceeded(ss) {
				continue
			}
			if _, fin := next.(Finished); !fin {
				return fmt.Errorf("step %d failed but state is %T", i, next)
			}
			for j := i + 1; j < len(steps); j++ {
				if _, ok := steps[j].(StepSkipped); !ok {
					return fmt.Errorf("step %d after failed step %d is %#v", j, i, steps[j])
				}
			}
		}
		return nil
	}))
}

// I5
func TestPropStaleInputIsNoop(t *testing.T) {
	drive(t, stateless(func(prev State, in Input, next State, cmds []Command) error {
		var idx, att int
		switch x := in.(type) {
		case StepExited:
			idx, att = x.Index, x.Attempt
		case StepStartFailed:
			idx, att = x.Index, x.Attempt
		default:
			return nil
		}
		ci, ca, live := current(prev)
		if live && ci == idx && ca == att {
			return nil
		}
		if !reflect.DeepEqual(prev, next) || len(cmds) != 0 {
			return errors.New("stale input changed the state or emitted commands")
		}
		return nil
	}))
}

// I6
func TestPropNoStartAfterCancel(t *testing.T) {
	drive(t, func() checker {
		cancelled := false
		return func(prev State, in Input, next State, cmds []Command) error {
			if cancelled && count[StartStep](cmds) > 0 {
				return errors.New("StartStep after Cancel")
			}
			if _, ok := in.(Cancel); ok && !reflect.DeepEqual(prev, next) {
				cancelled = true
			}
			if f, ok := next.(Finished); ok && cancelled {
				if _, ok := f.Result.(ResultCancelled); !ok {
					return fmt.Errorf("cancelled build ended %#v", f.Result)
				}
			}
			return nil
		}
	})
}

// I7
func TestPropFinishedIsTerminal(t *testing.T) {
	drive(t, stateless(func(prev State, _ Input, next State, cmds []Command) error {
		pf, ok := prev.(Finished)
		if !ok {
			return nil
		}
		nf, ok := next.(Finished)
		if !ok {
			return fmt.Errorf("Finished moved to %T", next)
		}
		if !reflect.DeepEqual(nf.Result, pf.Result) || !reflect.DeepEqual(nf.Steps, pf.Steps) {
			return errors.New("Finished result or steps changed")
		}
		if count[StartStep](cmds)+count[KillStep](cmds) > 0 {
			return errors.New("Finished emitted StartStep or KillStep")
		}
		return nil
	}))
}

// I8
func TestPropStatusDerived(t *testing.T) {
	drive(t, stateless(func(_ State, _ Input, next State, _ []Command) error {
		all := len(stepsOf(next)) > 0
		for _, ss := range stepsOf(next) {
			all = all && isSucceeded(ss)
		}
		_, fin := next.(Finished)
		succeeded := Status(next) == "succeeded"
		if succeeded != (all && fin) {
			return fmt.Errorf("Status %q but all steps succeeded = %v", Status(next), all)
		}
		return nil
	}))
}

// I9
func TestPropAuditPerChange(t *testing.T) {
	drive(t, stateless(func(prev State, _ Input, next State, cmds []Command) error {
		if reflect.DeepEqual(prev, next) {
			if len(cmds) != 0 {
				return errors.New("unchanged state emitted commands")
			}
			return nil
		}
		if n := count[WriteAudit](cmds); n != 1 {
			return fmt.Errorf("%d WriteAudit commands for a change", n)
		}
		return nil
	}))
}

func stateTimes(s State) []time.Time {
	var ts []time.Time
	add := func(t time.Time) { ts = append(ts, t) }
	addSteps := func(ss []StepState) {
		for _, x := range ss {
			switch y := x.(type) {
			case StepRunning:
				add(y.StartedAt)
			case StepDone:
				add(y.StartedAt)
				add(y.FinishedAt)
			}
		}
	}
	switch st := s.(type) {
	case Queued:
		add(st.QueuedAt)
	case Running:
		add(st.QueuedAt)
		add(st.StartedAt)
		addSteps(st.Steps)
	case Cancelling:
		add(st.QueuedAt)
		add(st.StartedAt)
		add(st.CancelledAt)
		addSteps(st.Steps)
	case Finished:
		add(st.QueuedAt)
		add(st.FinishedAt)
		if st.StartedAt != nil {
			add(*st.StartedAt)
		}
		if st.CancelledAt != nil {
			add(*st.CancelledAt)
		}
		addSteps(st.Steps)
	}
	return ts
}

func inputTime(in Input) time.Time {
	switch x := in.(type) {
	case Enqueue:
		return x.Now
	case Admit:
		return x.Now
	case StepExited:
		return x.Now
	case StepStartFailed:
		return x.Now
	case Cancel:
		return x.Now
	case ProcessLost:
		return x.Now
	case ArtifactsCollected:
		return x.Now
	}
	return time.Time{}
}

// I10
func TestPropTimesFromInputs(t *testing.T) {
	drive(t, func() checker {
		seen := map[time.Time]bool{}
		return func(_ State, in Input, next State, _ []Command) error {
			seen[inputTime(in)] = true
			for _, ts := range stateTimes(next) {
				if !seen[ts] {
					return fmt.Errorf("time %v in state is not from any input", ts)
				}
			}
			for i, ss := range stepsOf(next) {
				if d, ok := ss.(StepDone); ok && d.StartedAt.After(d.FinishedAt) {
					return fmt.Errorf("step %d started after it finished", i)
				}
			}
			return nil
		}
	})
}

// I11
func TestPropCancelledAtMatchesResult(t *testing.T) {
	drive(t, stateless(func(prev State, _ Input, next State, _ []Command) error {
		f, ok := next.(Finished)
		if !ok {
			return nil
		}
		_, cancelled := f.Result.(ResultCancelled)
		if (f.CancelledAt != nil) != cancelled {
			return fmt.Errorf("CancelledAt set = %v but result %#v", f.CancelledAt != nil, f.Result)
		}
		if c, ok := prev.(Cancelling); ok && (f.CancelledAt == nil || !f.CancelledAt.Equal(c.CancelledAt)) {
			return errors.New("Cancelling to Finished lost CancelledAt")
		}
		return nil
	}))
}
