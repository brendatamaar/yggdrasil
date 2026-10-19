package builds

import (
	"errors"
	"fmt"
	"testing"
)

// Oracle for the transition table's StepExited / StepStartFailed rows: the
// exit code alone decides success, and the other invariants (shape, audit)
// would still hold if an exit code of 1 were read as success.
func TestPropExitCodeDecidesOutcome(t *testing.T) {
	var zero, nonzero, startFailed, cancelling int // reach counters
	defer func() {
		if !t.Failed() && (zero == 0 || nonzero == 0 || startFailed == 0 || cancelling == 0) {
			t.Errorf("generator reach: exit 0 %d, exit != 0 %d, start failed %d, exits while cancelling %d", zero, nonzero, startFailed, cancelling)
		}
	}()
	drive(t, stateless(func(prev State, in Input, next State, cmds []Command) error {
		idx, att, live := current(prev)
		switch x := in.(type) {
		case StepExited:
			if !live || x.Index != idx || x.Attempt != att {
				return nil
			}
			if _, isCancelling := prev.(Cancelling); isCancelling {
				cancelling++
				f, ok := next.(Finished)
				if !ok {
					return fmt.Errorf("cancelling build did not finish on %#v", x)
				}
				if _, ok := f.Result.(ResultCancelled); !ok {
					return fmt.Errorf("cancelling build ended %#v whatever the exit code (%d)", f.Result, x.ExitCode)
				}
				return nil
			}
			run := prev.(Running)
			if x.ExitCode != 0 {
				nonzero++
				f, ok := next.(Finished)
				if !ok {
					return fmt.Errorf("exit code %d did not end the build: %T", x.ExitCode, next)
				}
				if res, ok := f.Result.(ResultFailed); !ok || res.Step != idx {
					return fmt.Errorf("exit code %d gave result %#v, want ResultFailed{%d}", x.ExitCode, f.Result, idx)
				}
				if d, ok := f.Steps[idx].(StepDone); !ok || d.Outcome != (Failed{ExitCode: x.ExitCode}) {
					return fmt.Errorf("step outcome %#v, want Failed{%d}", f.Steps[idx], x.ExitCode)
				}
				if count[StartStep](cmds) != 0 || count[CollectArtifacts](cmds) != 0 {
					return errors.New("a failed step started a step or collected artifacts")
				}
				return nil
			}
			zero++
			last := idx+1 == len(run.Steps)
			if d, ok := stepAt(next, idx).(StepDone); !ok || d.Outcome != (Succeeded{}) {
				return fmt.Errorf("exit 0 left step %d as %#v", idx, stepAt(next, idx))
			}
			if last {
				f, ok := next.(Finished)
				if !ok {
					return fmt.Errorf("last step succeeded but the build is %T", next)
				}
				if _, ok := f.Result.(ResultSucceeded); !ok || count[CollectArtifacts](cmds) != 1 {
					return fmt.Errorf("last step succeeded: result %#v, %d CollectArtifacts", f.Result, count[CollectArtifacts](cmds))
				}
				return nil
			}
			n, ok := next.(Running)
			if !ok || n.Current != idx+1 || count[StartStep](cmds) != 1 {
				return fmt.Errorf("step %d succeeded: next %#v with %d StartStep", idx, next, count[StartStep](cmds))
			}
			for _, c := range cmds {
				if s, ok := c.(StartStep); ok && (s.Index != idx+1 || s.Attempt != 1) {
					return fmt.Errorf("started %+v after step %d", s, idx)
				}
			}
		case StepStartFailed:
			if !live || x.Index != idx || x.Attempt != att || !reasonOK(x.Reason) {
				return nil
			}
			if _, isRunning := prev.(Running); !isRunning {
				return nil
			}
			startFailed++
			f, ok := next.(Finished)
			if !ok {
				return fmt.Errorf("start failure did not end the build: %T", next)
			}
			if d, ok := f.Steps[idx].(StepDone); !ok || d.Outcome != (StartFailed{Reason: x.Reason}) {
				return fmt.Errorf("step outcome %#v, want StartFailed{%v}", f.Steps[idx], x.Reason)
			}
			if res, ok := f.Result.(ResultFailed); !ok || res.Step != idx {
				return fmt.Errorf("start failure gave result %#v", f.Result)
			}
		}
		return nil
	}))
}

func stepAt(s State, i int) StepState {
	switch st := s.(type) {
	case Running:
		return st.Steps[i]
	case Finished:
		return st.Steps[i]
	}
	return nil
}

func reasonOK(r StartFailure) bool { return r.Validate() == nil }
