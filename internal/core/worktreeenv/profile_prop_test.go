package worktreeenv

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

var entryPool = func() []RelPath {
	var out []RelPath
	for i := range 8 {
		out = append(out, RelPath(fmt.Sprintf("conf/f%d.env", i)))
	}
	return out
}()

func randSource(r *rand.Rand) Source {
	switch r.Intn(7) {
	case 0:
		return SourceMissing{}
	case 1:
		return SourceDirectory{}
	case 2:
		return SourceLink{}
	case 3:
		return SourceUnreadable{}
	case 4:
		return SourceFile{Size: int64(MaxFileBytes) + 1}
	case 5:
		return SourceFile{Size: int64(r.Intn(MaxFileBytes))}
	}
	return SourceFile{Size: int64(MaxFileBytes - r.Intn(1000))}
}

func randRequest(r *rand.Rand) CopyRequested {
	var entries []RelPath
	for range r.Intn(10) {
		entries = append(entries, entryPool[r.Intn(len(entryPool))])
	}
	if r.Intn(10) == 0 { // over the entry cap
		for i := range MaxEntries + 3 {
			entries = append(entries, RelPath(fmt.Sprintf("extra/e%d", i)))
		}
	}
	trig := []Trigger{TriggerUserStarted, TriggerUserConfirmed, TriggerAgentTask, "bogus"}[r.Intn(4)]
	return CopyRequested{Worktree: "wt_1", Trigger: trig, Entries: entries, AgentCopy: r.Intn(3) == 0, Now: epoch}
}

type jobRun struct {
	req      CopyRequested
	observed map[RelPath]Observation
	cmds     []ProfileCommand // everything emitted
	final    Job
}

// drive runs one job with random (and sometimes stray) answers and calls
// check after every step.
func drive(t *testing.T, r *rand.Rand, check func(step int, before, after Job, in ProfileInput, cmds []ProfileCommand, run *jobRun)) *jobRun {
	t.Helper()
	run := &jobRun{req: randRequest(r), observed: map[RelPath]Observation{}}
	var j Job = JobIdle{}
	var queue []ProfileInput
	queue = append(queue, run.req)
	for step := 0; step < 400 && (len(queue) > 0 || step < 3); step++ {
		var in ProfileInput
		switch {
		case len(queue) > 0 && r.Intn(4) != 0:
			i := r.Intn(len(queue))
			in = queue[i]
			queue = append(queue[:i:i], queue[i+1:]...)
		default: // stray input
			e := entryPool[r.Intn(len(entryPool))]
			switch r.Intn(4) {
			case 0:
				in = Observed{Entry: e, Source: randSource(r), Dest: DestAbsent}
			case 1:
				in = FileCopied{Entry: e}
			case 2:
				in = FileCopyFailed{Entry: e, Reason: "x"}
			default:
				in = run.req
			}
		}
		before := deepCopyJob(j)
		after, cmds := Profile(j, in)
		if !reflect.DeepEqual(j, before) {
			t.Fatalf("Profile changed its input job on %#v", in)
		}
		for _, c := range cmds {
			switch v := c.(type) {
			case Observe:
				queue = append(queue, Observed{Entry: v.Entry, Source: randSource(r), Dest: []Dest{DestAbsent, DestAbsent, DestExists}[r.Intn(3)]})
			case CopyFile:
				if r.Intn(5) == 0 {
					queue = append(queue, FileCopyFailed{Entry: v.Entry, Reason: "locked"})
				} else {
					queue = append(queue, FileCopied{Entry: v.Entry})
				}
			}
		}
		if o, ok := in.(Observed); ok {
			if _, dup := run.observed[o.Entry]; !dup {
				if ob, isObs := j.(Observing); isObs && contains(ob.Entries, o.Entry) {
					if _, seen := ob.Seen[o.Entry]; !seen {
						run.observed[o.Entry] = Observation{Source: o.Source, Dest: o.Dest}
					}
				}
			}
		}
		run.cmds = append(run.cmds, cmds...)
		check(step, before, after, in, cmds, run)
		j = after
	}
	run.final = j
	return run
}

func deepCopyJob(j Job) Job {
	switch v := j.(type) {
	case Observing:
		v.Entries = append([]RelPath(nil), v.Entries...)
		seen := map[RelPath]Observation{}
		for k, o := range v.Seen {
			seen[k] = o
		}
		v.Seen = seen
		v.Extra = copyResults(v.Extra)
		return v
	case Copying:
		v.Pending = append([]RelPath(nil), v.Pending...)
		v.Results = copyResults(v.Results)
		return v
	case JobDone:
		v.Results = copyResults(v.Results)
		return v
	}
	return j
}

func copyResults(m map[RelPath]Result) map[RelPath]Result {
	if m == nil {
		return nil
	}
	out := map[RelPath]Result{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

func noCheck(int, Job, Job, ProfileInput, []ProfileCommand, *jobRun) {}

func TestPropCopyOnlyObservedFiles(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		drive(t, r, func(_ int, _, _ Job, _ ProfileInput, cmds []ProfileCommand, run *jobRun) {
			for _, c := range cmds {
				cf, ok := c.(CopyFile)
				if !ok {
					continue
				}
				o, seen := run.observed[cf.Entry]
				if !seen {
					t.Fatalf("CopyFile for unobserved %s", cf.Entry)
				}
				if _, isFile := o.Source.(SourceFile); !isFile || o.Dest != DestAbsent {
					t.Fatalf("CopyFile for %s observed as %#v", cf.Entry, o)
				}
			}
		})
	})
}

func TestPropAgentTasksOffCopiesNothing(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		run := drive(t, r, noCheck)
		agent := run.req.Trigger != TriggerUserStarted && run.req.Trigger != TriggerUserConfirmed
		if !agent || run.req.AgentCopy {
			return
		}
		for _, c := range run.cmds {
			switch c.(type) {
			case Observe, CopyFile:
				t.Fatalf("agent task with toggle off emitted %#v", c)
			}
		}
	})
}

func TestPropEveryEntryResolved(t *testing.T) {
	done := 0
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		run := drive(t, r, noCheck)
		d, ok := run.final.(JobDone)
		if !ok {
			return
		}
		done++
		want := map[RelPath]bool{}
		for _, e := range run.req.Entries {
			want[e] = true
		}
		if len(d.Results) != len(want) {
			t.Fatalf("%d results for %d distinct entries", len(d.Results), len(want))
		}
		for e := range want {
			if d.Results[e] == nil {
				t.Fatalf("no result for %s", e)
			}
		}
	})
	if done == 0 {
		t.Fatal("no job ever finished")
	}
}

func TestPropBudgetRespected(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		run := drive(t, r, noCheck)
		var total int64
		for _, c := range run.cmds {
			if cf, ok := c.(CopyFile); ok {
				sz := run.observed[cf.Entry].Source.(SourceFile).Size
				if sz > MaxFileBytes {
					t.Fatalf("%s over the file cap", cf.Entry)
				}
				total += sz
			}
		}
		if total > MaxTotalByte {
			t.Fatalf("planned %d bytes", total)
		}
		var observes int
		for _, c := range run.cmds {
			if _, ok := c.(Observe); ok {
				observes++
			}
		}
		if observes > MaxEntries {
			t.Fatalf("%d entries observed", observes)
		}
	})
}

func TestPropStaleJobIgnored(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		run := drive(t, r, noCheck)
		d, ok := run.final.(JobDone)
		if !ok {
			return
		}
		for _, in := range []ProfileInput{
			Observed{Entry: entryPool[0], Source: SourceFile{Size: 1}, Dest: DestAbsent},
			FileCopied{Entry: entryPool[0]}, FileCopyFailed{Entry: entryPool[0]},
		} {
			after, cmds := Profile(d, in)
			if len(cmds) != 0 || !reflect.DeepEqual(after, d) {
				t.Fatalf("a finished job reacted to %#v", in)
			}
		}
	})
}

func TestAuditHasNoContents(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		run := drive(t, r, noCheck)
		for _, c := range run.cmds {
			a, ok := c.(ProfileAudit)
			if !ok {
				continue
			}
			if a.Copied != len(a.Names) {
				t.Fatalf("names %v do not match count %d", a.Names, a.Copied)
			}
			// the audit value serialises to counts and relative names only
			raw, _ := json.Marshal(a)
			var m map[string]any
			_ = json.Unmarshal(raw, &m)
			for k := range m {
				switch k {
				case "Action", "Worktree", "Trigger", "Copied", "Skipped", "Failed", "Names":
				default:
					t.Fatalf("unexpected audit field %s", k)
				}
			}
			for _, n := range a.Names {
				if n.Validate() != nil && !strings.HasPrefix(string(n), "extra/") && !strings.HasPrefix(string(n), "conf/") {
					t.Fatalf("name %q is not a relative name", n)
				}
			}
		}
	})
}

func TestProfileHappyPath(t *testing.T) {
	req := CopyRequested{Worktree: "wt_1", Trigger: TriggerUserStarted, Entries: []RelPath{".env", "a/b.txt", ".env"}}
	j, cmds := Profile(JobIdle{}, req)
	if len(cmds) != 2 {
		t.Fatalf("want one Observe per distinct entry, got %#v", cmds)
	}
	j, cmds = Profile(j, Observed{Entry: ".env", Source: SourceFile{Size: 10}, Dest: DestAbsent})
	if len(cmds) != 0 {
		t.Fatal("copy started before every entry was observed")
	}
	j, cmds = Profile(j, Observed{Entry: "a/b.txt", Source: SourceLink{}, Dest: DestAbsent})
	if !reflect.DeepEqual(cmds, []ProfileCommand{CopyFile{Entry: ".env"}}) {
		t.Fatalf("got %#v", cmds)
	}
	j, cmds = Profile(j, FileCopied{Entry: ".env"})
	d := j.(JobDone)
	if d.Results["a/b.txt"] != (Skipped{Reason: SkipNotRegularFile}) || d.Results[".env"] != (Copied{}) {
		t.Fatalf("results %#v", d.Results)
	}
	a := cmds[0].(ProfileAudit)
	if a.Action != ActionProfileCopied || a.Copied != 1 || a.Skipped != 1 || !reflect.DeepEqual(a.Names, []RelPath{".env"}) {
		t.Fatalf("audit %#v", a)
	}
	if _, ok := cmds[1].(ProfilePublish); !ok {
		t.Fatal("no publish")
	}
}

func TestProfileAgentOffAndOn(t *testing.T) {
	req := CopyRequested{Worktree: "wt_1", Trigger: TriggerAgentTask, Entries: []RelPath{".env"}}
	j, cmds := Profile(JobIdle{}, req)
	if _, ok := j.(JobDone); !ok || cmds[0].(ProfileAudit).Action != ActionProfileSkipped {
		t.Fatalf("agent copy should be skipped: %#v %#v", j, cmds)
	}
	req.AgentCopy = true
	if _, cmds = Profile(JobIdle{}, req); !reflect.DeepEqual(cmds, []ProfileCommand{Observe{Entry: ".env"}}) {
		t.Fatalf("toggle on should observe: %#v", cmds)
	}
}

func TestProfileSkipReasons(t *testing.T) {
	cases := []struct {
		name string
		o    Observation
		want SkipReason
	}{
		{"missing", Observation{SourceMissing{}, DestAbsent}, SkipSourceMissing},
		{"dir", Observation{SourceDirectory{}, DestAbsent}, SkipNotRegularFile},
		{"link", Observation{SourceLink{}, DestAbsent}, SkipNotRegularFile},
		{"unreadable", Observation{SourceUnreadable{}, DestAbsent}, SkipSourceUnreadble},
		{"exists", Observation{SourceFile{Size: 3}, DestExists}, SkipDestExists},
		{"big", Observation{SourceFile{Size: MaxFileBytes + 1}, DestAbsent}, SkipTooLarge},
	}
	for _, c := range cases {
		j, _ := Profile(JobIdle{}, CopyRequested{Worktree: "w", Trigger: TriggerUserStarted, Entries: []RelPath{"f"}})
		j, _ = Profile(j, Observed{Entry: "f", Source: c.o.Source, Dest: c.o.Dest})
		if got := j.(JobDone).Results["f"]; got != (Skipped{Reason: c.want}) {
			t.Errorf("%s: %#v", c.name, got)
		}
	}
}

func TestProfileBudget(t *testing.T) {
	var entries []RelPath
	for i := range 6 {
		entries = append(entries, RelPath(fmt.Sprintf("f%d", i)))
	}
	j, _ := Profile(JobIdle{}, CopyRequested{Worktree: "w", Trigger: TriggerUserStarted, Entries: entries})
	var cmds []ProfileCommand
	for _, e := range entries {
		j, cmds = Profile(j, Observed{Entry: e, Source: SourceFile{Size: MaxFileBytes}, Dest: DestAbsent})
	}
	if len(cmds) != 5 {
		t.Fatalf("5 MiB budget should allow 5 files of 1 MiB, got %d", len(cmds))
	}
	if r := j.(Copying).Results["f5"]; r != (Skipped{Reason: SkipBudgetExceeded}) {
		t.Fatalf("sixth file: %#v", r)
	}
}

func TestProfileTooManyEntries(t *testing.T) {
	var entries []RelPath
	for i := range MaxEntries + 2 {
		entries = append(entries, RelPath(fmt.Sprintf("f%d", i)))
	}
	j, cmds := Profile(JobIdle{}, CopyRequested{Worktree: "w", Trigger: TriggerUserStarted, Entries: entries})
	if len(cmds) != MaxEntries {
		t.Fatalf("observed %d", len(cmds))
	}
	if j.(Observing).Extra[entries[MaxEntries]] != (Skipped{Reason: SkipTooManyEntries}) {
		t.Fatal("entry beyond the cap not skipped")
	}
}
