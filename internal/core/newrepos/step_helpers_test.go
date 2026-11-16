package newrepos

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func testIDs() IDs {
	return IDs{Operation: "op_1", Repo: "rep_1", SetupTask: "tsk_1", Proposal: "pro_1"}
}

func reserveIn() Reserve {
	return Reserve{Spec: validInit(), IDs: testIDs(), ProjectTracked: true, LeafFree: true, HasAuthor: true, Now: t0}
}

func destURL(p PublishSpec) string {
	return "https://" + p.HostInstance + "/" + p.Owner + "/" + p.Name + ".git"
}

func fileSum(s string) string { return fileHash(s) }

// initReceiptFor returns the valid receipt of an init stage for spec.
func initReceiptFor(spec InitSpec, s InitStage) Receipt {
	switch s {
	case StageCreateDirectory:
		return DirReceipt{Identity: "vol:1/dir"}
	case StageInitGit:
		return GitReceipt{GitDirIdentity: "vol:1/git"}
	case StageWriteREADME:
		return FileReceipt{Name: "README.md", ContentHash: fileSum(spec.README)}
	case StageWriteIgnore:
		return FileReceipt{Name: ".gitignore", ContentHash: fileSum(spec.Ignore)}
	case StageFirstCommit:
		return CommitReceipt{Commit: strings.Repeat("a", 40), Tree: strings.Repeat("c", 40), MessageHash: fileSum(spec.CommitMessage), Author: "Ada <ada@acme.dev>"}
	default:
		return TrackedReceipt{}
	}
}

func remoteReceiptFor(p PublishSpec) RemoteReceipt {
	return RemoteReceipt{RemoteID: "rid-" + p.Marker, URL: destURL(p)}
}

func publishReceiptFor(p PublishSpec, s PublishStage) Receipt {
	switch s {
	case StageCreateRemote:
		return remoteReceiptFor(p)
	case StageAddOrigin:
		return OriginReceipt{URL: destURL(p)}
	default:
		return PushReceipt{Head: p.ExpectedHead}
	}
}

// reserved returns an operation in Reserved.
func reserved(t *testing.T) Operation {
	t.Helper()
	o, cmds := Step(Operation{}, reserveIn())
	if _, ok := o.State.(Reserved); !ok || len(cmds) == 0 {
		t.Fatalf("reserve: %+v %v", o, cmds)
	}
	return o
}

func awaiting(t *testing.T) Operation {
	t.Helper()
	o := reserved(t)
	o, _ = Step(o, ProposalCreated{ID: "pro_1", Hash: validInit().Hash()})
	if _, ok := o.State.(AwaitingConfirm); !ok {
		t.Fatalf("awaiting: %+v", o)
	}
	return o
}

// initializingAt drives to Initializing at stage s with attempt 1.
func initializingAt(t *testing.T, s InitStage) Operation {
	t.Helper()
	o := awaiting(t)
	o, _ = Step(o, Confirmed{Hash: validInit().Hash(), Attempt: 1, Now: t0})
	for {
		st, ok := o.State.(Initializing)
		if !ok {
			t.Fatalf("not initializing: %+v", o.State)
		}
		if st.Stage == s {
			return o
		}
		o, _ = Step(o, StageSucceeded{Attempt: 1, Stage: st.Stage, Receipt: initReceiptFor(st.Spec, st.Stage), Now: t0})
	}
}

func readyOp(t *testing.T) Operation {
	t.Helper()
	o := initializingAt(t, StageFinalizeTracking)
	o, _ = Step(o, StageSucceeded{Attempt: 1, Stage: StageFinalizeTracking, Receipt: TrackedReceipt{}, Now: t0})
	if _, ok := o.State.(Ready); !ok {
		t.Fatalf("ready: %+v", o.State)
	}
	return o
}

func publishConfirmIn(attempt int) PublishConfirmed {
	p := validPublish()
	return PublishConfirmed{Spec: p, Hash: p.Hash(), Attempt: attempt, OriginAbsent: true, HeadMatches: true, Now: t0}
}

func publishingAt(t *testing.T, s PublishStage) Operation {
	t.Helper()
	o, _ := Step(readyOp(t), publishConfirmIn(1))
	for {
		st, ok := o.State.(Publishing)
		if !ok {
			t.Fatalf("not publishing: %+v", o.State)
		}
		if st.Stage == s {
			return o
		}
		o, _ = Step(o, StageSucceeded{Attempt: 1, Stage: st.Stage, Receipt: publishReceiptFor(st.Spec, st.Stage), Now: t0})
	}
}

func same(a, b Operation) bool { return reflect.DeepEqual(a, b) }

func kinds(cmds []Command) []string {
	out := make([]string, len(cmds))
	for i, c := range cmds {
		out[i] = reflect.TypeOf(c).Name()
	}
	return out
}

func wantKinds(t *testing.T, cmds []Command, want ...string) {
	t.Helper()
	if got := kinds(cmds); len(got) != len(want) || (len(got) > 0 && !reflect.DeepEqual(got, want)) {
		t.Fatalf("commands = %v, want %v", got, want)
	}
}

func unchanged(t *testing.T, before Operation, after Operation, cmds []Command) {
	t.Helper()
	if !same(before, after) || len(cmds) != 0 {
		t.Fatalf("expected no change; state %+v commands %v", after.State, kinds(cmds))
	}
}
