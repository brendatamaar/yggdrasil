package notify

import (
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

func TestOfTaskStatus(t *testing.T) {
	want := map[core.TaskStatus]Status{
		core.TaskNeedsYou: NeedsYou, core.TaskReview: ReadyForReview, core.TaskDone: Done,
		core.TaskFailed: Failed, core.TaskCancelled: Cancelled,
	}
	for _, s := range []core.TaskStatus{core.TaskDraft, core.TaskQueued, core.TaskRunning, core.TaskNeedsYou, core.TaskReview, core.TaskDone, core.TaskFailed, core.TaskCancelled} {
		got, ok := OfTaskStatus(s)
		w, notifiable := want[s]
		if ok != notifiable || got != w {
			t.Errorf("OfTaskStatus(%s) = %q, %v", s, got, ok)
		}
		if ok {
			if err := got.Validate(); err != nil {
				t.Errorf("%s: %v", s, err)
			}
		}
	}
}

func notice() Notice {
	return Notice{ID: "ntc_1", Task: "tsk_1", TaskTitle: "Fix the build", Status: Done, Source: Source{Kind: SourceTask},
		SourceRevision: 3, CreatedAt: time.Unix(1, 0)}
}

func TestNoticeValidate(t *testing.T) {
	if err := notice().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, f := range map[string]func(*Notice){
		"no id": func(n *Notice) { n.ID = "" }, "no title": func(n *Notice) { n.TaskTitle = "" },
		"long title": func(n *Notice) { n.TaskTitle = strings.Repeat("a", MaxTitle+1) },
		"status":     func(n *Notice) { n.Status = "wow" }, "revision": func(n *Notice) { n.SourceRevision = 0 },
		"run on task source":  func(n *Notice) { n.Source.RunID = "r1" },
		"verification no run": func(n *Notice) { n.Source = Source{Kind: SourceVerification} },
		"unknown source":      func(n *Notice) { n.Source = Source{Kind: "email"} },
	} {
		n := notice()
		f(&n)
		if n.Validate() == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// A notice has no free-text body: the only strings it holds are the task title,
// the closed status and metadata, which is the privacy rule of docs/AGENTS.md 5.9.
func TestStatusesAreAClosedSet(t *testing.T) {
	if len(Statuses()) != 7 {
		t.Errorf("%d statuses, want the seven of the design", len(Statuses()))
	}
}

func TestCursorGap(t *testing.T) {
	for _, c := range []struct {
		after, oldest int64
		gap           bool
	}{
		{0, 50, false}, {10, 0, false}, {49, 50, false}, {50, 50, false}, {48, 50, true}, {1, 200, true}, {199, 200, false},
	} {
		if got := CursorGap(c.after, c.oldest); got != c.gap {
			t.Errorf("CursorGap(%d, %d) = %v, want %v", c.after, c.oldest, got, c.gap)
		}
	}
}

func TestStepIsInert(t *testing.T) {
	for _, in := range []Input{TaskTransition{Revision: 1}, VerificationTransition{Revision: 1}, Recovered{}, Stop{}} {
		got, cmds := Step(Empty{}, in)
		if _, ok := got.(Empty); !ok || len(cmds) != 0 {
			t.Errorf("Step(%T) = %T, %v", in, got, cmds)
		}
	}
}
