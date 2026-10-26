package proposals

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

const propRuns = 300

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// forSeeds runs f with a deterministic generator per seed and names the seed
// of a failing run.
func forSeeds(t *testing.T, f func(t *testing.T, r *rand.Rand)) {
	t.Helper()
	for seed := int64(1); seed <= propRuns; seed++ {
		ok := t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) { f(t, rand.New(rand.NewSource(seed))) })
		if !ok {
			return
		}
	}
}

// StepRecord is one step of a run.
type StepRecord struct {
	Prev Proposal
	In   Input
	Next Proposal
	Cmds []Command
}

// Run feeds inputs to Step one after the other and records every step.
func Run(initial Proposal, inputs []Input) []StepRecord {
	out := make([]StepRecord, 0, len(inputs))
	cur := initial
	for _, in := range inputs {
		next, cmds := Step(cur, in)
		out = append(out, StepRecord{Prev: cur, In: in, Next: next, Cmds: cmds})
		cur = next
	}
	return out
}

func pick[T any](r *rand.Rand, xs ...T) T { return xs[r.Intn(len(xs))] }

func hexStr(r *rand.Rand, n int) string {
	const digits = "0123456789abcdef"
	b := make([]byte, n)
	for i := range b {
		b[i] = digits[r.Intn(16)]
	}
	return string(b)
}

func genTarget(r *rand.Rand) Target {
	if r.Intn(2) == 0 {
		return InWorktree{ID: core.WorktreeID(fmt.Sprintf("w%d", r.Intn(4)))}
	}
	return MainCheckout{Repo: core.RepoID(fmt.Sprintf("r%d", r.Intn(4)))}
}

func genPaths(r *rand.Rand) []RelPath {
	n := 1 + r.Intn(5)
	out := make([]RelPath, n)
	for i := range out {
		out[i] = RelPath(fmt.Sprintf("%s/f%d_%d.txt", pick(r, "src", "docs", "a b", "pkg/x"), i, r.Intn(100)))
	}
	return out
}

func genBranch(r *rand.Rand) string {
	return pick(r, "main", "ygg/swift-fern", "feature/x-"+hexStr(r, 3), "fix_"+hexStr(r, 2))
}

// genAction returns a valid action of a random kind.
func genAction(r *rand.Rand) Action {
	switch r.Intn(6) {
	case 0:
		return Commit{Where: genTarget(r), Paths: genPaths(r), Message: "msg " + hexStr(r, 4), ExpectHead: hexStr(r, 40), ExpectDiffHash: hexStr(r, 64)}
	case 1:
		return Push{Where: genTarget(r), Branch: genBranch(r), Remote: pick(r, "origin", "up.stream"), ExpectHead: hexStr(r, 40)}
	case 2:
		return MergeRequest{Where: genTarget(r), Branch: genBranch(r), Remote: "origin", TargetBranch: "main", Title: "Title " + hexStr(r, 3), Description: pick(r, "", "one line "+hexStr(r, 3)), ExpectHead: hexStr(r, 40)}
	case 3:
		return AgentFile{Where: genTarget(r), Path: RelPath(pick(r, ".claude/settings.json", "CLAUDE.md", ".mcp.json", "AGENTS.md", ".opencode/a/b.json")), Content: "content " + hexStr(r, 5), ExpectHash: pick(r, "", hexStr(r, 64))}
	case 4:
		return DeleteFiles{Where: genTarget(r), Paths: genPaths(r)}
	}
	return Install{Repo: core.RepoID(fmt.Sprintf("r%d", r.Intn(4))), Step: pick(r, "install", "npm-ci")}
}

func genFacts(r *rand.Rand) Facts {
	return Facts{RepoTracked: true, PathsOK: true, RemoteSupported: true, PendingForTask: r.Intn(10)}
}

// genAnyFacts varies every fact, including bad ones and counts at or over the
// cap. goodFacts reports whether Created may succeed with them.
func genAnyFacts(r *rand.Rand, h Proposal) Facts {
	f := Facts{
		RepoTracked: r.Intn(5) != 0, PathsOK: r.Intn(5) != 0, RemoteSupported: r.Intn(5) != 0,
		PendingForTask: r.Intn(MaxPendingPerTask + 3),
	}
	switch r.Intn(4) {
	case 0:
		f.TaskChangeSet = h.ChangeSet
	case 1:
		f.TaskChangeSet = "cs-other"
	}
	return f
}

func goodFacts(f Facts, h Proposal) bool {
	return f.RepoTracked && f.PathsOK && f.RemoteSupported && f.PendingForTask < MaxPendingPerTask &&
		(f.TaskChangeSet == "" || f.TaskChangeSet == h.ChangeSet)
}

func header(r *rand.Rand, id string) Proposal {
	task := core.TaskID(fmt.Sprintf("t%d", r.Intn(3)))
	return Proposal{
		ID: core.ProposalID(id), ChangeSet: core.ChangeSetID("cs-" + string(task)), Task: task,
		Repo: core.RepoID(fmt.Sprintf("r%d", r.Intn(4))), Source: AgentSource{Task: task},
		Action: genAction(r), Summary: "summary",
	}
}

// genPending creates a pending proposal through Step.
func genPending(t *testing.T, r *rand.Rand, id string) Proposal {
	t.Helper()
	p, cmds := Step(Proposal{}, Created{Proposal: header(r, id), Facts: genFacts(r), Now: epoch})
	if _, ok := p.State.(Pending); !ok {
		t.Fatalf("Created gave %T, commands %v", p.State, cmds)
	}
	return p
}

func curAttempt(s State) (int, bool) {
	switch st := s.(type) {
	case Confirmed:
		return st.Attempt, true
	case Failed:
		return st.Attempt, true
	}
	return 0, false
}

// genInput mixes legal, stale, wrong-hash and unrelated inputs, biased by the
// state.
func genInput(r *rand.Rand, p Proposal, now time.Time) Input {
	att, _ := curAttempt(p.State)
	if r.Intn(4) == 0 {
		att += r.Intn(3) - 1 // stale or future attempt
	}
	tracked := r.Intn(5) != 0
	switch r.Intn(11) {
	case 0, 1, 2:
		h := p.ActionHash
		if r.Intn(3) == 0 {
			h = pick(r, "", hexStr(r, 64), strings.ToUpper(p.ActionHash), p.ActionHash+"0")
		}
		return Confirm{Hash: h, By: "user", RepoTracked: tracked, Now: now}
	case 3:
		return Reject{By: "user", Now: now}
	case 4, 5:
		return ExecDone{Attempt: att, Result: Result{CommitID: hexStr(r, 40)}, Now: now}
	case 6, 7:
		return ExecFailed{Attempt: att, Kind: pick(r, FailStale, FailExec, FailRemote, FailInterrupted, "bogus"), Message: "boom", Retryable: r.Intn(2) == 0, Now: now}
	case 8, 9:
		return Retry{By: "user", RepoTracked: tracked, Now: now}
	case 10:
		if r.Intn(2) == 0 {
			return Recovered{Now: now}
		}
		return Reconciled{Attempt: att, Outcome: pick(r, ReconcileDone, ReconcileNotDone, ReconcileUnknown, "bogus"), Now: now}
	}
	return Created{Proposal: header(r, "other"), Facts: genFacts(r), Now: now}
}

func genRun(r *rand.Rand, p Proposal, n int) []Input {
	ins := make([]Input, 0, n)
	cur := p
	now := epoch
	for i := 0; i < n; i++ {
		now = now.Add(time.Second)
		in := genInput(r, cur, now)
		ins = append(ins, in)
		cur, _ = Step(cur, in) // so later inputs are biased by the real state
	}
	return ins
}

func executes(cmds []Command) []Execute {
	var out []Execute
	for _, c := range cmds {
		if e, ok := c.(Execute); ok {
			out = append(out, e)
		}
	}
	return out
}

func has[T Command](cmds []Command) bool {
	for _, c := range cmds {
		if _, ok := c.(T); ok {
			return true
		}
	}
	return false
}
