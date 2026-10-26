package proposals

import (
	"reflect"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

func base() Proposal {
	return Proposal{
		ID: "p1", ChangeSet: "cs1", Task: "t1", Repo: "r1", Source: AgentSource{Task: "t1"},
		Action: Push{Where: InWorktree{ID: "w1"}, Branch: "ygg/x", Remote: "origin", ExpectHead: "abcdef0"},
	}
}

func okFacts() Facts { return Facts{RepoTracked: true, PathsOK: true, RemoteSupported: true} }

func created(t *testing.T) Proposal {
	t.Helper()
	p, _ := Step(Proposal{}, Created{Proposal: base(), Facts: okFacts(), Now: epoch})
	if _, ok := p.State.(Pending); !ok {
		t.Fatalf("not pending: %+v", p.State)
	}
	return p
}

func inState(p Proposal, s State) Proposal { p.State = s; return p }

func refusal(t *testing.T, cmds []Command, want RefuseReason) {
	t.Helper()
	if len(cmds) != 1 {
		t.Fatalf("want a single Refuse(%s), got %v", want, cmds)
	}
	if r, ok := cmds[0].(Refuse); !ok || r.Reason != want {
		t.Fatalf("want Refuse(%s), got %v", want, cmds[0])
	}
}

func TestCreatedTable(t *testing.T) {
	tests := []struct {
		name   string
		mod    func(*Created)
		refuse RefuseReason
	}{
		{"ok", func(*Created) {}, ""},
		{"just under the cap", func(c *Created) { c.Facts.PendingForTask = MaxPendingPerTask - 1 }, ""},
		{"at the cap", func(c *Created) { c.Facts.PendingForTask = MaxPendingPerTask }, RefuseTooMany},
		{"repo untracked", func(c *Created) { c.Facts.RepoTracked = false }, RefuseRepoUntracked},
		{"paths not ok", func(c *Created) { c.Facts.PathsOK = false }, RefuseBadFacts},
		{"remote unsupported", func(c *Created) { c.Facts.RemoteSupported = false }, RefuseBadFacts},
		{"publish kind", func(c *Created) { c.Proposal.Action = fakeAction{core.ProposalPublish} }, RefuseUnsupported},
		{"no action", func(c *Created) { c.Proposal.Action = nil }, RefuseUnsupported},
		{"task change set matches", func(c *Created) { c.Facts.TaskChangeSet = c.Proposal.ChangeSet }, ""},
		{"task change set differs", func(c *Created) { c.Facts.TaskChangeSet = "cs-other" }, RefuseBadFacts},
		{"no change set", func(c *Created) { c.Proposal.ChangeSet = "" }, RefuseBadFacts},
		{"no source", func(c *Created) { c.Proposal.Source = nil }, RefuseBadFacts},
		{"bad id", func(c *Created) { c.Proposal.ID = "a b" }, RefuseBadFacts},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := Created{Proposal: base(), Facts: okFacts(), Now: epoch}
			tc.mod(&c)
			got, cmds := Step(Proposal{}, c)
			if tc.refuse != "" {
				if got.State != nil {
					t.Fatalf("refused but state is %+v", got.State)
				}
				refusal(t, cmds, tc.refuse)
				return
			}
			if _, ok := got.State.(Pending); !ok {
				t.Fatalf("state = %+v", got.State)
			}
			if got.ActionHash != Hash(got.Action) || !got.Created.Equal(epoch) {
				t.Errorf("hash %q or created time wrong", got.ActionHash)
			}
			if len(cmds) != 3 || !has[Persist](cmds) || !has[Audit](cmds) || !has[Publish](cmds) {
				t.Errorf("commands = %v", cmds)
			}
			if a := cmds[1].(Audit); a.Entry.Action != "proposal.create" {
				t.Errorf("audit = %+v", a)
			}
		})
	}
}

// fakeAction is a kind the core refuses.
type fakeAction struct{ k core.ProposalKind }

func (f fakeAction) Kind() core.ProposalKind { return f.k }
func (fakeAction) isAction()                 {}
func (fakeAction) Validate() error           { return nil }

func TestCreatedIgnoresOutsideHash(t *testing.T) {
	b := base()
	b.ActionHash = "forged"
	got, _ := Step(Proposal{}, Created{Proposal: b, Facts: okFacts(), Now: epoch})
	if got.ActionHash != Hash(b.Action) {
		t.Fatalf("hash %q kept", got.ActionHash)
	}
}

func TestInputsBeforeCreationDoNothing(t *testing.T) {
	for _, in := range []Input{Confirm{Hash: "x", RepoTracked: true}, Reject{}, ExecDone{Attempt: 1}, Retry{RepoTracked: true}, Recovered{}} {
		got, cmds := Step(Proposal{ID: "p"}, in)
		if got.State != nil || cmds != nil {
			t.Errorf("%T acted on a missing proposal", in)
		}
	}
}

func TestConfirmTable(t *testing.T) {
	p := created(t)
	now := epoch.Add(time.Minute)
	t.Run("matching hash", func(t *testing.T) {
		got, cmds := Step(p, Confirm{Hash: p.ActionHash, By: "me", RepoTracked: true, Now: now})
		want := Confirmed{Attempt: 1, At: now, By: "me"}
		if !reflect.DeepEqual(got.State, want) {
			t.Fatalf("state = %+v", got.State)
		}
		if len(cmds) != 3 {
			t.Fatalf("commands = %v", cmds)
		}
		ex, ok := cmds[0].(Execute)
		if !ok || ex.ID != "p1" || ex.Attempt != 1 || !reflect.DeepEqual(ex.Action, p.Action) {
			t.Errorf("execute = %+v", cmds[0])
		}
		if a := cmds[1].(Audit); a.Entry.Action != "proposal.confirm" {
			t.Errorf("audit = %+v", a)
		}
		if pub := cmds[2].(Publish); pub.Status != core.ProposalConfirmed {
			t.Errorf("publish = %+v", pub)
		}
	})
	t.Run("wrong hash", func(t *testing.T) {
		got, cmds := Step(p, Confirm{Hash: "nope", RepoTracked: true})
		if !reflect.DeepEqual(got.State, p.State) {
			t.Fatal("state changed")
		}
		refusal(t, cmds, RefuseHashMismatch)
	})
	t.Run("repo untracked", func(t *testing.T) {
		got, cmds := Step(p, Confirm{Hash: p.ActionHash, RepoTracked: false})
		if !reflect.DeepEqual(got.State, p.State) {
			t.Fatal("state changed")
		}
		refusal(t, cmds, RefuseRepoUntracked)
	})
	t.Run("hash mismatch wins over untracked", func(t *testing.T) {
		_, cmds := Step(p, Confirm{Hash: "nope", RepoTracked: false})
		refusal(t, cmds, RefuseHashMismatch)
	})
	t.Run("stored hash corrupted", func(t *testing.T) {
		bad := p
		bad.ActionHash = "forged"
		_, cmds := Step(bad, Confirm{Hash: "forged", RepoTracked: true})
		refusal(t, cmds, RefuseHashMismatch)
	})
	t.Run("empty hash", func(t *testing.T) {
		bad := p
		bad.Action, bad.ActionHash = nil, ""
		got, cmds := Step(bad, Confirm{Hash: "", RepoTracked: true})
		if !reflect.DeepEqual(got.State, bad.State) {
			t.Fatal("state changed")
		}
		refusal(t, cmds, RefuseHashMismatch)
	})
}

func TestRejectTable(t *testing.T) {
	p := created(t)
	got, cmds := Step(p, Reject{By: "me", Now: epoch})
	if !reflect.DeepEqual(got.State, (Rejected{At: epoch})) || len(cmds) != 2 || has[Execute](cmds) {
		t.Fatalf("state %+v cmds %v", got.State, cmds)
	}
	if a := cmds[0].(Audit); a.Entry.Action != "proposal.reject" {
		t.Errorf("audit = %+v", a)
	}
	// Reject on every other state does nothing.
	for _, s := range []State{Confirmed{Attempt: 1}, Rejected{}, Executed{}, Failed{Attempt: 1, Retryable: true}} {
		q := inState(p, s)
		got, cmds := Step(q, Reject{})
		if !reflect.DeepEqual(got.State, s) || cmds != nil {
			t.Errorf("Reject on %T acted", s)
		}
	}
}

func TestExecResults(t *testing.T) {
	p := inState(created(t), Confirmed{Attempt: 2})
	res := Result{CommitID: "abc"}
	t.Run("done", func(t *testing.T) {
		got, cmds := Step(p, ExecDone{Attempt: 2, Result: res, Now: epoch})
		if !reflect.DeepEqual(got.State, (Executed{At: epoch, Result: res})) || len(cmds) != 2 {
			t.Fatalf("state %+v cmds %v", got.State, cmds)
		}
		if a := cmds[0].(Audit); a.Entry.Action != "proposal.execute" {
			t.Errorf("audit = %+v", a)
		}
	})
	t.Run("failed", func(t *testing.T) {
		got, cmds := Step(p, ExecFailed{Attempt: 2, Kind: FailRemote, Message: "no", Retryable: true, Now: epoch})
		want := Failed{Attempt: 2, At: epoch, Kind: FailRemote, Message: "no", Retryable: true}
		if !reflect.DeepEqual(got.State, want) || len(cmds) != 2 {
			t.Fatalf("state %+v cmds %v", got.State, cmds)
		}
		if a := cmds[0].(Audit); a.Entry.Action != "proposal.fail" || a.Entry.Outcome != string(FailRemote) {
			t.Errorf("audit = %+v", a)
		}
	})
	t.Run("unknown kind becomes exec_error", func(t *testing.T) {
		got, _ := Step(p, ExecFailed{Attempt: 2, Kind: "weird"})
		if f, ok := got.State.(Failed); !ok || f.Kind != FailExec {
			t.Fatalf("state %+v", got.State)
		}
	})
	t.Run("stale attempts", func(t *testing.T) {
		for _, in := range []Input{ExecDone{Attempt: 1}, ExecDone{Attempt: 3}, ExecFailed{Attempt: 1, Kind: FailExec}, Reconciled{Attempt: 1, Outcome: ReconcileDone}} {
			got, cmds := Step(p, in)
			if !reflect.DeepEqual(got.State, p.State) || cmds != nil {
				t.Errorf("%T with a stale attempt acted", in)
			}
		}
	})
	t.Run("not confirmed", func(t *testing.T) {
		for _, s := range []State{Pending{}, Rejected{}, Executed{}, Failed{Attempt: 2}} {
			got, cmds := Step(inState(p, s), ExecDone{Attempt: 2})
			if !reflect.DeepEqual(got.State, s) || cmds != nil {
				t.Errorf("ExecDone acted on %T", s)
			}
		}
	})
}

func TestRetryTable(t *testing.T) {
	p := inState(created(t), Failed{Attempt: 2, Kind: FailExec, Retryable: true})
	t.Run("retryable", func(t *testing.T) {
		got, cmds := Step(p, Retry{By: "me", RepoTracked: true, Now: epoch})
		if !reflect.DeepEqual(got.State, (Confirmed{Attempt: 3, At: epoch, By: "me"})) || len(cmds) != 3 {
			t.Fatalf("state %+v cmds %v", got.State, cmds)
		}
		if ex := cmds[0].(Execute); ex.Attempt != 3 {
			t.Errorf("execute = %+v", ex)
		}
		if a := cmds[1].(Audit); a.Entry.Action != "proposal.retry" {
			t.Errorf("audit = %+v", a)
		}
	})
	t.Run("untracked", func(t *testing.T) {
		got, cmds := Step(p, Retry{RepoTracked: false})
		if !reflect.DeepEqual(got.State, p.State) {
			t.Fatal("state changed")
		}
		refusal(t, cmds, RefuseRepoUntracked)
	})
	t.Run("not retryable", func(t *testing.T) {
		q := inState(p, Failed{Attempt: 2, Kind: FailStale})
		got, cmds := Step(q, Retry{RepoTracked: true})
		if !reflect.DeepEqual(got.State, q.State) || cmds != nil {
			t.Fatal("retried a non-retryable failure")
		}
	})
	t.Run("other states", func(t *testing.T) {
		for _, s := range []State{Pending{}, Confirmed{Attempt: 1}, Rejected{}, Executed{}} {
			got, cmds := Step(inState(p, s), Retry{RepoTracked: true})
			if !reflect.DeepEqual(got.State, s) || cmds != nil {
				t.Errorf("Retry acted on %T", s)
			}
		}
	})
}

func TestRecoveryTable(t *testing.T) {
	p := inState(created(t), Confirmed{Attempt: 4})
	got, cmds := Step(p, Recovered{Now: epoch})
	if !reflect.DeepEqual(got.State, p.State) || len(cmds) != 1 {
		t.Fatalf("state %+v cmds %v", got.State, cmds)
	}
	if rc, ok := cmds[0].(Reconcile); !ok || rc.Attempt != 4 || rc.ID != "p1" || !reflect.DeepEqual(rc.Action, p.Action) {
		t.Errorf("reconcile = %+v", cmds[0])
	}
	res := Result{CommitID: "c"}
	got, cmds = Step(p, Reconciled{Attempt: 4, Outcome: ReconcileDone, Result: res, Now: epoch})
	if !reflect.DeepEqual(got.State, (Executed{At: epoch, Result: res})) || len(cmds) != 2 {
		t.Fatalf("done: state %+v cmds %v", got.State, cmds)
	}
	for _, o := range []ReconcileOutcome{ReconcileNotDone, ReconcileUnknown} {
		got, cmds = Step(p, Reconciled{Attempt: 4, Outcome: o, Now: epoch})
		f, ok := got.State.(Failed)
		if !ok || f.Kind != FailInterrupted || !f.Retryable || f.Attempt != 4 || f.Message == "" || len(cmds) != 2 || has[Execute](cmds) {
			t.Errorf("%s: state %+v cmds %v", o, got.State, cmds)
		}
	}
	got, cmds = Step(p, Reconciled{Attempt: 4, Outcome: "bogus"})
	if !reflect.DeepEqual(got.State, p.State) || cmds != nil {
		t.Error("unknown outcome acted")
	}
	// Recovered on a non-confirmed proposal does nothing.
	for _, s := range []State{Pending{}, Rejected{}, Executed{}, Failed{Attempt: 1, Retryable: true}} {
		got, cmds := Step(inState(p, s), Recovered{})
		if !reflect.DeepEqual(got.State, s) || cmds != nil {
			t.Errorf("Recovered acted on %T", s)
		}
	}
}

func TestCreatedOnExistingProposalDoesNothing(t *testing.T) {
	p := created(t)
	got, cmds := Step(p, Created{Proposal: base(), Facts: okFacts(), Now: epoch})
	if !reflect.DeepEqual(got, p) || cmds != nil {
		t.Fatal("Created re-created an existing proposal")
	}
}
