package devservers

import (
	"sync"
	"time"
)

// Timer is a pending AfterFunc call.
type Timer interface {
	// Stop prevents the call and reports whether it did (false: it already
	// ran or is running).
	Stop() bool
}

// Clock is the time capability of the shell: the current time and one-shot
// callbacks. devserverstest has a fake whose time only moves when told.
type Clock interface {
	Now() time.Time
	// AfterFunc calls f once, after d, on a goroutine the clock owns. It never
	// calls f before it returns.
	AfterFunc(d time.Duration, f func()) Timer
}

// RealClock is the Clock of the real time.
type RealClock struct{}

// Now implements Clock.
func (RealClock) Now() time.Time { return time.Now() }

// AfterFunc implements Clock.
func (RealClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }

// Wakes schedules the wakes of the core (ScheduleWake) and the shell's own
// short timers. A key is idempotent: scheduling a key again replaces the
// pending call. Every call runs once, never after Close, and Close waits for
// calls already running.
type Wakes struct {
	clock Clock

	mu     sync.Mutex
	closed bool
	timers map[string]*wakeTimer
	wg     sync.WaitGroup
}

type wakeTimer struct{ t Timer }

// NewWakes returns a scheduler on clock.
func NewWakes(clock Clock) *Wakes {
	return &Wakes{clock: clock, timers: map[string]*wakeTimer{}}
}

// At calls fire(now) once at time at (immediately if at is past), unless key
// is scheduled again or cancelled first. After Close it does nothing.
func (w *Wakes) At(key string, at time.Time, fire func(now time.Time)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	w.stopLocked(key)
	d := max(at.Sub(w.clock.Now()), 0)
	entry := &wakeTimer{}
	w.wg.Add(1)
	entry.t = w.clock.AfterFunc(d, func() {
		defer w.wg.Done()
		w.mu.Lock()
		if w.closed || w.timers[key] != entry {
			w.mu.Unlock()
			return
		}
		delete(w.timers, key)
		w.mu.Unlock()
		fire(w.clock.Now())
	})
	w.timers[key] = entry
}

// Cancel drops a pending call.
func (w *Wakes) Cancel(key string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stopLocked(key)
}

func (w *Wakes) stopLocked(key string) {
	if old, ok := w.timers[key]; ok {
		delete(w.timers, key)
		if old.t.Stop() {
			w.wg.Done()
		}
	}
}

// Close drops every pending call and waits for the running ones.
func (w *Wakes) Close() {
	w.mu.Lock()
	w.closed = true
	for key := range w.timers {
		w.stopLocked(key)
	}
	w.mu.Unlock()
	w.wg.Wait()
}
