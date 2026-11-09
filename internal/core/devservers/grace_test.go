package devservers

import (
	"reflect"
	"testing"
	"time"
)

// A KillGrace wake from an attempt that a second Stop replaced must not end
// the escalated stop early: it is ignored until the current GraceUntil.
func TestStaleKillGraceWakeIsIgnored(t *testing.T) {
	var s Server = Stopping{Attempt: 1, Cause: CauseUserStop, KillAttempt: KillFirst, Since: epoch, Pid: 7, GraceUntil: epoch.Add(KillGraceUser)}
	// The user presses Stop again 8 s in: the grace restarts from there.
	s, _ = Step(s, StopRequested{Cause: CauseUserStop, Now: epoch.Add(8 * time.Second)})
	st := s.(Stopping)
	if want := epoch.Add(8*time.Second + KillGraceUser); !st.GraceUntil.Equal(want) {
		t.Fatalf("GraceUntil = %v, want %v", st.GraceUntil, want)
	}
	// The first attempt's wake fires at 10 s: stale, nothing changes.
	next, cmds := Step(s, Wake{Attempt: 1, Kind: WakeKillGrace, Now: epoch.Add(KillGraceUser)})
	if !reflect.DeepEqual(next, s) || len(cmds) != 0 {
		t.Fatalf("stale wake changed state: %#v %v", next, cmds)
	}
	// The current attempt's wake ends it.
	next, _ = Step(s, Wake{Attempt: 1, Kind: WakeKillGrace, Now: epoch.Add(8*time.Second + KillGraceUser)})
	if f, ok := next.(Failed); !ok || f.Kind != FailStopFailed {
		t.Fatalf("current wake: %#v", next)
	}
}

// The first wake moves a first kill to the second and sets a new deadline.
func TestKillGraceWakeSetsNextDeadline(t *testing.T) {
	var s Server = Stopping{Attempt: 1, Cause: CauseUserStop, KillAttempt: KillFirst, Since: epoch, Pid: 7, GraceUntil: epoch.Add(KillGraceUser)}
	at := epoch.Add(KillGraceUser)
	s, _ = Step(s, Wake{Attempt: 1, Kind: WakeKillGrace, Now: at})
	st := s.(Stopping)
	if st.KillAttempt != KillSecond || !st.GraceUntil.Equal(at.Add(KillGraceUser)) {
		t.Fatalf("after first wake: %+v", st)
	}
	if next, _ := Step(s, Wake{Attempt: 1, Kind: WakeKillGrace, Now: at}); !reflect.DeepEqual(next, s) {
		t.Fatalf("a repeat of the same wake must be ignored: %#v", next)
	}
}
