package notify

import (
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

var t0 = time.Unix(1_700_000_000, 0).UTC()

func taskIn(task string, st core.TaskStatus, rev int64, id string) TaskTransition {
	return TaskTransition{Task: core.TaskID(task), Title: "Fix the build", Status: st, Revision: rev, Now: t0, ID: core.NoticeID(id)}
}

func verIn(task, run string, res VerifyResult, rev int64, id string) VerificationTransition {
	return VerificationTransition{Task: core.TaskID(task), Title: "Fix the build", RunID: run, Result: res, Revision: rev, Now: t0, ID: core.NoticeID(id)}
}

func mustCurrent(t *testing.T, s State) Current {
	t.Helper()
	c, ok := s.(Current)
	if !ok {
		t.Fatalf("state is %T, want Current", s)
	}
	return c
}

func TestEmptyRecoveredBecomesCurrent(t *testing.T) {
	k := Key{Task: "tsk_1", Source: Source{Kind: SourceTask}}
	n := notice()
	n.Cursor = 7
	bad := Key{Task: "", Source: Source{Kind: SourceTask}}
	s, cmds := Step(Empty{}, Recovered{Recent: []Notice{n, {}}, HighWater: map[Key]int64{k: 2, bad: 9}})
	c := mustCurrent(t, s)
	if len(cmds) != 0 {
		t.Fatalf("commands %v", cmds)
	}
	if len(c.Recent) != 1 || c.HighWater[k] != 3 || len(c.HighWater) != 1 {
		t.Errorf("recovered = %+v (the notice raises the mark to its revision, invalid entries are dropped)", c)
	}
}

func TestTransitionTable(t *testing.T) {
	cases := []struct {
		name    string
		in      Input
		notice  Status
		persist bool // a high-water-only write
	}{
		{"needs you", taskIn("tsk_1", core.TaskNeedsYou, 1, "ntc_1"), NeedsYou, false},
		{"review", taskIn("tsk_1", core.TaskReview, 1, "ntc_1"), ReadyForReview, false},
		{"done", taskIn("tsk_1", core.TaskDone, 1, "ntc_1"), Done, false},
		{"failed", taskIn("tsk_1", core.TaskFailed, 1, "ntc_1"), Failed, false},
		{"cancelled", taskIn("tsk_1", core.TaskCancelled, 1, "ntc_1"), Cancelled, false},
		{"running", taskIn("tsk_1", core.TaskRunning, 1, ""), "", true},
		{"queued", taskIn("tsk_1", core.TaskQueued, 1, ""), "", true},
		{"verify pass", verIn("tsk_1", "run_1", VerifyPassed, 1, "ntc_1"), VerificationPassed, false},
		{"verify fail", verIn("tsk_1", "run_1", VerifyFailed, 1, "ntc_1"), VerificationFailed, false},
		{"verify running", verIn("tsk_1", "run_1", VerifyInProgress, 1, ""), "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, cmds := Step(Empty{}, c.in)
			cur := mustCurrent(t, s)
			if c.persist {
				if len(cmds) != 1 || len(cur.Recent) != 0 {
					t.Fatalf("cmds %v recent %d", cmds, len(cur.Recent))
				}
				if _, ok := cmds[0].(PersistHighWater); !ok {
					t.Fatalf("cmd %T", cmds[0])
				}
			} else {
				if len(cmds) != 2 {
					t.Fatalf("cmds %v", cmds)
				}
				p, ok := cmds[0].(PersistNoticeAndAudit)
				if !ok || p.Notice.Status != c.notice || p.HighWater != 1 || p.Notice.Validate() != nil {
					t.Fatalf("persist %+v", cmds[0])
				}
				if pub, ok := cmds[1].(PublishNotice); !ok || pub.ID != "ntc_1" {
					t.Fatalf("publish %+v", cmds[1])
				}
				if len(cur.Recent) != 1 {
					t.Errorf("recent %d", len(cur.Recent))
				}
			}
			if len(cur.HighWater) != 1 {
				t.Errorf("high water %v", cur.HighWater)
			}
		})
	}
}

func TestStaleDuplicateAndInvalidAreInert(t *testing.T) {
	s, _ := Step(Empty{}, taskIn("tsk_1", core.TaskDone, 5, "ntc_1"))
	for name, in := range map[string]Input{
		"replay":        taskIn("tsk_1", core.TaskDone, 5, "ntc_2"),
		"older":         taskIn("tsk_1", core.TaskFailed, 4, "ntc_2"),
		"no revision":   taskIn("tsk_1", core.TaskDone, 0, "ntc_2"),
		"bad status":    taskIn("tsk_1", "wow", 9, "ntc_2"),
		"no task":       taskIn("", core.TaskDone, 9, "ntc_2"),
		"no id":         taskIn("tsk_1", core.TaskDone, 9, ""),
		"no title":      TaskTransition{Task: "tsk_1", Status: core.TaskDone, Revision: 9, Now: t0, ID: "ntc_2"},
		"blank title":   TaskTransition{Task: "tsk_1", Title: "  ", Status: core.TaskDone, Revision: 9, Now: t0, ID: "ntc_2"},
		"no time":       TaskTransition{Task: "tsk_1", Title: "x", Status: core.TaskDone, Revision: 9, ID: "ntc_2"},
		"verify no run": verIn("tsk_1", "", VerifyPassed, 1, "ntc_2"),
		"verify result": verIn("tsk_1", "r", "meh", 1, "ntc_2"),
		"recovered":     Recovered{},
	} {
		got, cmds := Step(s, in)
		if len(cmds) != 0 || !reflect.DeepEqual(got, s) {
			t.Errorf("%s: %v, %v", name, got, cmds)
		}
	}
}

func TestVerificationRunsAreSeparateSources(t *testing.T) {
	s, _ := Step(Empty{}, verIn("tsk_1", "run_1", VerifyFailed, 4, "ntc_1"))
	// A late older revision of the same run is ignored; a second run is its own sequence.
	if _, cmds := Step(s, verIn("tsk_1", "run_1", VerifyPassed, 3, "ntc_2")); len(cmds) != 0 {
		t.Error("an older verification overwrote a newer one")
	}
	if _, cmds := Step(s, verIn("tsk_1", "run_2", VerifyPassed, 1, "ntc_2")); len(cmds) != 2 {
		t.Error("a new run was suppressed")
	}
	// The task source and a verification source of the same task do not share a mark.
	if _, cmds := Step(s, taskIn("tsk_1", core.TaskDone, 1, "ntc_2")); len(cmds) != 2 {
		t.Error("a task transition was suppressed by a verification mark")
	}
}

func TestClosedAndStop(t *testing.T) {
	for _, from := range []State{Empty{}, Current{}} {
		if s, cmds := Step(from, Stop{}); !reflect.DeepEqual(s, Closed{}) || len(cmds) != 0 {
			t.Errorf("Stop from %T = %v", from, s)
		}
	}
	for _, in := range []Input{taskIn("tsk_1", core.TaskDone, 1, "ntc_1"), verIn("tsk_1", "r", VerifyFailed, 1, "ntc_1"), Recovered{}, Stop{}} {
		if s, cmds := Step(Closed{}, in); !reflect.DeepEqual(s, Closed{}) || len(cmds) != 0 {
			t.Errorf("Closed + %T = %v, %v", in, s, cmds)
		}
	}
}

func TestStepDoesNotMutateItsState(t *testing.T) {
	s, _ := Step(Empty{}, taskIn("tsk_1", core.TaskDone, 1, "ntc_1"))
	before := mustCurrent(t, s)
	hw := map[Key]int64{}
	for k, v := range before.HighWater {
		hw[k] = v
	}
	Step(s, taskIn("tsk_2", core.TaskDone, 1, "ntc_2"))
	if !reflect.DeepEqual(hw, before.HighWater) || len(before.Recent) != 1 {
		t.Error("Step changed the state it was given")
	}
}

func TestLongTitleIsClipped(t *testing.T) {
	in := taskIn("tsk_1", core.TaskDone, 1, "ntc_1")
	in.Title = strings.Repeat("é", MaxTitle) // 2 bytes each
	_, cmds := Step(Empty{}, in)
	n := cmds[0].(PersistNoticeAndAudit).Notice
	if len(n.TaskTitle) > MaxTitle || n.Validate() != nil {
		t.Errorf("title %d bytes", len(n.TaskTitle))
	}
}

// ---- properties ----

func randInput(r *rand.Rand, i int) Input {
	tasks := []string{"tsk_1", "tsk_2", "tsk_3"}
	sts := []core.TaskStatus{core.TaskDraft, core.TaskQueued, core.TaskRunning, core.TaskNeedsYou, core.TaskReview, core.TaskDone, core.TaskFailed, core.TaskCancelled}
	rev := int64(r.Intn(8))
	id := fmt.Sprintf("ntc_%d", i)
	switch r.Intn(10) {
	case 0:
		return Stop{}
	case 1:
		return Recovered{}
	case 2, 3, 4:
		return verIn(tasks[r.Intn(3)], []string{"r1", "r2", ""}[r.Intn(3)], []VerifyResult{VerifyInProgress, VerifyPassed, VerifyFailed, "x"}[r.Intn(4)], rev, id)
	default:
		return taskIn(tasks[r.Intn(3)], sts[r.Intn(len(sts))], rev, id)
	}
}

// Invariants of Step over random input sequences, checked after every step:
// the high-water mark of a key never moves back; a notice or mark write is made
// only for a revision above the mark; a notice is followed by its publish and
// is never made twice for one (key, revision); Closed never moves; history is
// bounded.
func TestPropStepInvariants(t *testing.T) {
	for seed := int64(0); seed < 300; seed++ {
		r := rand.New(rand.NewSource(seed))
		var s State = Empty{}
		if r.Intn(2) == 0 {
			s, _ = Step(s, Recovered{})
		}
		produced := map[Key]map[int64]bool{}
		for i := 0; i < 120; i++ {
			in := randInput(r, i)
			prev := s
			var prevHW map[Key]int64
			if c, ok := prev.(Current); ok {
				prevHW = c.HighWater
			}
			s2, cmds := Step(s, in)
			if _, closed := prev.(Closed); closed && (len(cmds) != 0 || !reflect.DeepEqual(s2, prev)) {
				t.Fatalf("seed %d: Closed moved", seed)
			}
			if c2, ok := s2.(Current); ok {
				for k, v := range prevHW {
					if c2.HighWater[k] < v {
						t.Fatalf("seed %d step %d: high water of %v went back", seed, i, k)
					}
				}
				if len(c2.Recent) > MaxRecent {
					t.Fatalf("recent unbounded")
				}
			}
			for ci, c := range cmds {
				switch cc := c.(type) {
				case PersistNoticeAndAudit:
					k := Key{Task: cc.Notice.Task, Source: cc.Notice.Source}
					if cc.HighWater <= prevHW[k] || cc.HighWater != cc.Notice.SourceRevision {
						t.Fatalf("seed %d: notice at %d with mark %d", seed, cc.HighWater, prevHW[k])
					}
					if produced[k] == nil {
						produced[k] = map[int64]bool{}
					}
					if produced[k][cc.HighWater] {
						t.Fatalf("seed %d: duplicate notice for %v rev %d", seed, k, cc.HighWater)
					}
					produced[k][cc.HighWater] = true
					if ci+1 >= len(cmds) || cmds[ci+1] != (PublishNotice{ID: cc.Notice.ID}) {
						t.Fatalf("a notice is not followed by its publish")
					}
				case PersistHighWater:
					if cc.Revision <= prevHW[cc.Key] {
						t.Fatalf("seed %d: high-water write not above mark", seed)
					}
				case PublishNotice:
					// checked with its PersistNoticeAndAudit above
				}
			}
			if len(cmds) == 0 {
				switch in.(type) {
				case Stop, Recovered:
				case TaskTransition, VerificationTransition:
					if reflect.TypeOf(s2) == reflect.TypeOf(prev) && !reflect.DeepEqual(s2, prev) {
						t.Fatalf("seed %d: state changed with no command on %T", seed, in)
					}
				}
			}
			s = s2
		}
	}
}

func TestPropHistoryBounded(t *testing.T) {
	var s State = Empty{}
	for i := 1; i <= MaxRecent*3; i++ {
		s, _ = Step(s, taskIn("tsk_1", core.TaskDone, int64(i), fmt.Sprintf("ntc_%d", i)))
		c := mustCurrent(t, s)
		if len(c.Recent) > MaxRecent {
			t.Fatalf("recent %d after %d", len(c.Recent), i)
		}
		if c.Recent[len(c.Recent)-1].SourceRevision != int64(i) {
			t.Fatal("the newest notice is not last")
		}
	}
	c := mustCurrent(t, s)
	if len(c.Recent) != MaxRecent || c.Recent[0].SourceRevision != int64(MaxRecent*2+1) {
		t.Errorf("kept %d, oldest rev %d", len(c.Recent), c.Recent[0].SourceRevision)
	}
	// Trimming history never re-enables an old notice.
	if _, cmds := Step(s, taskIn("tsk_1", core.TaskDone, 1, "ntc_x")); len(cmds) != 0 {
		t.Error("a trimmed notice was produced again")
	}
	// A Recovered state with more history than the bound is cut to the bound.
	var many []Notice
	for i := 1; i <= MaxRecent+50; i++ {
		n := notice()
		n.ID = core.NoticeID(fmt.Sprintf("ntc_%d", i))
		n.SourceRevision, n.Cursor = int64(i), int64(i)
		many = append(many, n)
	}
	rs, _ := Step(Empty{}, Recovered{Recent: many})
	if rc := mustCurrent(t, rs); len(rc.Recent) != MaxRecent || rc.Recent[0].Cursor != 51 {
		t.Errorf("recovered %d notices", len(rc.Recent))
	}
}

// A notice holds the title (clipped) and a closed-set status; for random
// inputs it is valid and the displayable text carries nothing else.
func TestPropNoticeNeverContainsContent(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	secrets := []string{"ghp_abcdefghijklmnopqrstuvwxyz0123456789", `C:\Users\me\secret.txt`, "/home/me/.ssh/id_rsa", "diff --git a/x b/x"}
	for i := 0; i < 500; i++ {
		title := "Task " + fmt.Sprint(r.Intn(100))
		poison := secrets[r.Intn(len(secrets))]
		id := core.NoticeID(fmt.Sprintf("ntc_%d", i))
		var in Input
		if r.Intn(2) == 0 {
			in = VerificationTransition{Task: "tsk_1", Title: title, RunID: "run_" + fmt.Sprint(i), Result: VerifyFailed, Revision: int64(i + 1), Now: t0, ID: id}
		} else {
			in = TaskTransition{Task: "tsk_1", Title: title, Status: core.TaskDone, Revision: int64(i + 1), Now: t0, ID: id}
		}
		_, cmds := Step(Empty{}, in)
		p := cmds[0].(PersistNoticeAndAudit).Notice
		if p.Validate() != nil || p.TaskTitle != title || p.Status.Validate() != nil {
			t.Fatalf("notice %+v", p)
		}
		if strings.Contains(p.TaskTitle+string(p.Status), poison) {
			t.Fatal("poison in the displayable text")
		}
	}
	// The Notice type has only the allowed fields: pin the set so a free-text field cannot be added unseen.
	var names []string
	typ := reflect.TypeOf(Notice{})
	for i := 0; i < typ.NumField(); i++ {
		names = append(names, typ.Field(i).Name)
	}
	if got := strings.Join(names, ","); got != "ID,Task,TaskTitle,Status,Source,SourceRevision,CreatedAt,Cursor" {
		t.Errorf("Notice fields changed: %s", got)
	}
}

// Replays and out-of-order revisions of one source: a revision at or below the
// running high-water mark produces nothing, each revision above it is handled once.
func TestPropRevisionDedup(t *testing.T) {
	for seed := int64(0); seed < 200; seed++ {
		r := rand.New(rand.NewSource(seed))
		var s State = Empty{}
		high := int64(0)
		for i := 0; i < 80; i++ {
			rev := int64(1 + r.Intn(12))
			st := []core.TaskStatus{core.TaskDone, core.TaskFailed, core.TaskRunning}[r.Intn(3)]
			var cmds []Command
			s, cmds = Step(s, taskIn("tsk_1", st, rev, fmt.Sprintf("ntc_%d", i)))
			if rev <= high {
				if len(cmds) != 0 {
					t.Fatalf("seed %d: revision %d <= %d produced %v", seed, rev, high, cmds)
				}
				continue
			}
			if len(cmds) == 0 {
				t.Fatalf("seed %d: newer revision %d ignored", seed, rev)
			}
			high = rev
		}
	}
}

// Recovering from a store and then replaying everything that was stored
// produces nothing (no duplicates after a restart), with or without history.
func TestPropRecoveredNoDuplicates(t *testing.T) {
	for seed := int64(0); seed < 100; seed++ {
		r := rand.New(rand.NewSource(seed))
		var s State = Empty{}
		var all []Input
		for i := 0; i < 60; i++ {
			in := randInput(r, i)
			switch in.(type) {
			case Stop, Recovered:
				continue
			case TaskTransition, VerificationTransition:
			}
			all = append(all, in)
			s, _ = Step(s, in)
		}
		c, ok := s.(Current)
		if !ok {
			continue
		}
		s2, _ := Step(Empty{}, Recovered{Recent: c.Recent, HighWater: c.HighWater})
		s3, _ := Step(Empty{}, Recovered{HighWater: c.HighWater})
		for _, in := range all {
			if _, cmds := Step(s2, in); len(cmds) != 0 {
				t.Fatalf("seed %d: replay after recovery produced %v", seed, cmds)
			}
			if _, cmds := Step(s3, in); len(cmds) != 0 {
				t.Fatalf("seed %d: replay after marks-only recovery produced %v", seed, cmds)
			}
		}
	}
}

func FuzzStep(f *testing.F) {
	f.Add("tsk_1", "t", "running", int64(1), "run", "x")
	f.Fuzz(func(t *testing.T, task, title, status string, rev int64, run, result string) {
		s, _ := Step(Empty{}, TaskTransition{Task: core.TaskID(task), Title: title, Status: core.TaskStatus(status), Revision: rev, Now: t0, ID: "ntc_1"})
		_, cmds := Step(s, VerificationTransition{Task: core.TaskID(task), Title: title, RunID: run, Result: VerifyResult(result), Revision: rev, Now: t0, ID: "ntc_2"})
		for _, c := range cmds {
			if p, ok := c.(PersistNoticeAndAudit); ok && p.Notice.Validate() != nil {
				t.Fatalf("invalid notice produced: %+v", p.Notice)
			}
		}
	})
}
