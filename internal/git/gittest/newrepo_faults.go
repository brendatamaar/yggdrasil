package gittest

import (
	"errors"
	"sync"
)

// FaultMode is where a scripted fault strikes relative to the effect.
type FaultMode int

const (
	// FailBefore: the call fails and the effect did not happen.
	FailBefore FaultMode = iota + 1
	// LostReply: the effect happened, but the caller gets an error instead of
	// the result (a timeout, a dropped connection, a crash after the write).
	LostReply
)

// ErrInjected is the default error of a scripted fault.
var ErrInjected = errors.New("injected failure")

type fault struct {
	nth  int
	mode FaultMode
	err  error
}

// Faults scripts failures by operation name and call number. It is safe for
// concurrent use; the zero value injects nothing.
type Faults struct {
	mu    sync.Mutex
	plan  map[string][]fault
	calls map[string]int
}

// Inject makes the nth call (1-based) of op fail in mode with err (ErrInjected
// when nil). Several faults for one op are allowed.
func (f *Faults) Inject(op string, nth int, mode FaultMode, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.plan == nil {
		f.plan = map[string][]fault{}
	}
	if err == nil {
		err = ErrInjected
	}
	f.plan[op] = append(f.plan[op], fault{nth: nth, mode: mode, err: err})
}

// Calls returns how many times op was called.
func (f *Faults) Calls(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[op]
}

// next counts a call of op and returns the fault scripted for it, if any.
func (f *Faults) next(op string) (FaultMode, error, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[op]++
	for _, x := range f.plan[op] {
		if x.nth == f.calls[op] {
			return x.mode, x.err, true
		}
	}
	return 0, nil, false
}
