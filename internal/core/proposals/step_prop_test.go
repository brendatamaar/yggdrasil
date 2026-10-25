package proposals

import (
	"math/rand"
	"reflect"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

func runSeed(t *testing.T, r *rand.Rand) (Proposal, []StepRecord) {
	p := genPending(t, r, "p1")
	return p, Run(p, genRun(r, p, 40))
}

// P1: Execute comes only from Confirm with the matching hash, or from Retry.
func TestPropExecuteOnlyAfterConfirm(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		_, recs := runSeed(t, r)
		for i, s := range recs {
			ex := executes(s.Cmds)
			if len(ex) == 0 {
				continue
			}
			switch in := s.In.(type) {
			case Confirm:
				if _, ok := s.Prev.State.(Pending); !ok || in.Hash != s.Prev.ActionHash || in.Hash != Hash(s.Prev.Action) || !in.RepoTracked {
					t.Fatalf("step %d: Execute from Confirm %+v in %T", i, in, s.Prev.State)
				}
			case Retry:
				if f, ok := s.Prev.State.(Failed); !ok || !f.Retryable {
					t.Fatalf("step %d: Execute from Retry in %T", i, s.Prev.State)
				}
			default:
				t.Fatalf("step %d: Execute from %T", i, s.In)
			}
		}
	})
}

// P2: Execute is emitted once per (ID, Attempt), and attempts only grow.
func TestPropExecuteOncePerAttempt(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		_, recs := runSeed(t, r)
		type key struct {
			id core.ProposalID
			a  int
		}
		seen := map[key]bool{}
		last := 0
		for i, s := range recs {
			ex := executes(s.Cmds)
			if len(ex) > 1 {
				t.Fatalf("step %d: %d Execute commands", i, len(ex))
			}
			for _, e := range ex {
				k := key{e.ID, e.Attempt}
				if seen[k] || e.Attempt <= last {
					t.Fatalf("step %d: Execute %v repeated or out of order (last %d)", i, k, last)
				}
				seen[k] = true
				last = e.Attempt
				if c, ok := s.Next.State.(Confirmed); !ok || c.Attempt != e.Attempt {
					t.Fatalf("step %d: Execute attempt %d but state %+v", i, e.Attempt, s.Next.State)
				}
			}
		}
	})
}

// P3: the action, its hash and the header never change.
func TestPropActionImmutable(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		p0, recs := runSeed(t, r)
		for i, s := range recs {
			n := s.Next
			if !reflect.DeepEqual(n.Action, p0.Action) || n.ActionHash != p0.ActionHash || n.ActionHash != Hash(n.Action) ||
				n.ID != p0.ID || n.ChangeSet != p0.ChangeSet || n.Task != p0.Task || n.Repo != p0.Repo ||
				n.Source != p0.Source || n.Summary != p0.Summary || !n.Created.Equal(p0.Created) {
				t.Fatalf("step %d: header or action changed by %T", i, s.In)
			}
		}
	})
}

// P4: Rejected and Executed are terminal; Failed leaves only through a
// retryable Retry.
func TestPropTerminalStates(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		_, recs := runSeed(t, r)
		for i, s := range recs {
			switch prev := s.Prev.State.(type) {
			case Rejected, Executed:
				if !reflect.DeepEqual(s.Next.State, s.Prev.State) || len(s.Cmds) != 0 {
					t.Fatalf("step %d: terminal %T moved on %T", i, prev, s.In)
				}
			case Failed:
				if reflect.DeepEqual(s.Next.State, s.Prev.State) {
					continue
				}
				_, isRetry := s.In.(Retry)
				c, isConf := s.Next.State.(Confirmed)
				if !isRetry || !prev.Retryable || !isConf || c.Attempt != prev.Attempt+1 {
					t.Fatalf("step %d: Failed left by %T to %+v", i, s.In, s.Next.State)
				}
			}
		}
	})
}

// P5: a Confirm with a different hash changes nothing and executes nothing.
func TestPropWrongHashDoesNothing(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		p := genPending(t, r, "p1")
		for i := 0; i < 20; i++ {
			h := pick(r, "", hexStr(r, 64), p.ActionHash+"0", p.ActionHash[1:])
			next, cmds := Step(p, Confirm{Hash: h, By: "user", RepoTracked: true, Now: epoch})
			if !reflect.DeepEqual(next, p) || len(executes(cmds)) != 0 || len(cmds) != 1 {
				t.Fatalf("wrong hash %q changed state or emitted %v", h, cmds)
			}
			if rf, ok := cmds[0].(Refuse); !ok || rf.Reason != RefuseHashMismatch {
				t.Fatalf("want hash mismatch refusal, got %v", cmds)
			}
		}
	})
}

// P6: Recovered emits Reconcile for each Confirmed and never Execute.
func TestPropRecoveryNeverExecutes(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		_, recs := runSeed(t, r)
		for i, s := range recs {
			if _, ok := s.In.(Recovered); !ok {
				continue
			}
			if len(executes(s.Cmds)) != 0 || !reflect.DeepEqual(s.Next, s.Prev) {
				t.Fatalf("step %d: Recovered executed or changed state", i)
			}
			c, conf := s.Prev.State.(Confirmed)
			if !conf {
				if len(s.Cmds) != 0 {
					t.Fatalf("step %d: Recovered on %T emitted %v", i, s.Prev.State, s.Cmds)
				}
				continue
			}
			if len(s.Cmds) != 1 {
				t.Fatalf("step %d: want one Reconcile, got %v", i, s.Cmds)
			}
			if rc, ok := s.Cmds[0].(Reconcile); !ok || rc.Attempt != c.Attempt || rc.ID != s.Prev.ID {
				t.Fatalf("step %d: bad Reconcile %v", i, s.Cmds[0])
			}
		}
	})
}

// P7: a status change comes with Audit and Publish, and nothing else does.
func TestPropStatusChangeAudited(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		_, recs := runSeed(t, r)
		for i, s := range recs {
			changed := !reflect.DeepEqual(s.Next.State, s.Prev.State)
			audits, pubs := 0, 0
			for _, c := range s.Cmds {
				switch v := c.(type) {
				case Audit:
					audits++
					if v.Entry.Proposal != s.Prev.ID || v.Entry.Kind != s.Prev.Action.Kind() || v.Entry.Action == "" {
						t.Fatalf("step %d: bad audit entry %+v", i, v.Entry)
					}
				case Publish:
					pubs++
					if v.Status != s.Next.State.Status() {
						t.Fatalf("step %d: published %s, state is %s", i, v.Status, s.Next.State.Status())
					}
				}
			}
			if changed && (audits != 1 || pubs != 1) {
				t.Fatalf("step %d: %T changed state with %d audit, %d publish", i, s.In, audits, pubs)
			}
			if !changed && (audits != 0 || pubs != 0 || len(executes(s.Cmds)) != 0) {
				t.Fatalf("step %d: %T changed nothing but emitted %v", i, s.In, s.Cmds)
			}
		}
	})
}

// P11: all proposals of a task share one change set. The shell's
// EnsureChangeSet is modelled by a map; Step must keep what it was given, for
// any interleaving of proposals of several tasks.
func TestPropOneChangeSetPerTask(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		ensure := map[core.TaskID]core.ChangeSetID{}
		var ps []Proposal
		for i := 0; i < 6; i++ {
			h := header(r, string(rune('a'+i)))
			if cs, ok := ensure[h.Task]; ok {
				h.ChangeSet = cs
			} else {
				ensure[h.Task] = h.ChangeSet
			}
			p, _ := Step(Proposal{}, Created{Proposal: h, Facts: genFacts(r), Now: epoch})
			ps = append(ps, p)
		}
		for i := 0; i < 60; i++ {
			k := r.Intn(len(ps))
			ps[k], _ = Step(ps[k], genInput(r, ps[k], epoch))
			for _, p := range ps {
				if p.ChangeSet != ensure[p.Task] {
					t.Fatalf("proposal %s of %s has change set %s, want %s", p.ID, p.Task, p.ChangeSet, ensure[p.Task])
				}
			}
		}
	})
}

// P12: a proposal never becomes Confirmed while its repo is untracked.
func TestPropUntrackedRepoNeverConfirmed(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		_, recs := runSeed(t, r)
		for i, s := range recs {
			_, wasConf := s.Prev.State.(Confirmed)
			_, isConf := s.Next.State.(Confirmed)
			if wasConf || !isConf {
				continue
			}
			switch in := s.In.(type) {
			case Confirm:
				if !in.RepoTracked {
					t.Fatalf("step %d: confirmed with untracked repo", i)
				}
			case Retry:
				if !in.RepoTracked {
					t.Fatalf("step %d: retried with untracked repo", i)
				}
			default:
				t.Fatalf("step %d: became Confirmed by %T", i, s.In)
			}
		}
	})
}

// Stale attempts and inputs that do not apply change nothing.
func TestPropStaleAttemptsIgnored(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		_, recs := runSeed(t, r)
		for i, s := range recs {
			c, ok := s.Prev.State.(Confirmed)
			if !ok {
				continue
			}
			var att int
			switch in := s.In.(type) {
			case ExecDone:
				att = in.Attempt
			case ExecFailed:
				att = in.Attempt
			case Reconciled:
				att = in.Attempt
			default:
				continue
			}
			if att != c.Attempt && (!reflect.DeepEqual(s.Next.State, s.Prev.State) || len(s.Cmds) != 0) {
				t.Fatalf("step %d: stale attempt %d (current %d) was applied", i, att, c.Attempt)
			}
		}
	})
}
