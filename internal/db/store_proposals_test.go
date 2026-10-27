package db

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/proposals"
)

var propNow = time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

func sampleActions(repo core.RepoID) []proposals.Action {
	head, h64 := strings.Repeat("a", 40), strings.Repeat("b", 64)
	wt := proposals.InWorktree{ID: "wt_1"}
	return []proposals.Action{
		proposals.Commit{Where: wt, Paths: []proposals.RelPath{"src/a.txt", "b.txt"}, Message: "feat: a", ExpectHead: head, ExpectDiffHash: h64},
		proposals.Push{Where: wt, Branch: "ygg/swift-fern", Remote: "origin", ExpectHead: head},
		proposals.MergeRequest{Where: wt, Branch: "ygg/swift-fern", Remote: "origin", TargetBranch: "main", Title: "Add a", Description: "one line", ExpectHead: head},
		proposals.AgentFile{Where: proposals.MainCheckout{Repo: repo}, Path: "CLAUDE.md", Content: "# rules\n", ExpectHash: h64},
		proposals.DeleteFiles{Where: wt, Paths: []proposals.RelPath{"old/x.txt"}},
		proposals.Install{Repo: repo, Step: "install"},
	}
}

type propFixture struct {
	s    *Store
	task core.Task
	repo core.Repo
	next int
}

func newPropFixture(t *testing.T, redact func(string) string) *propFixture {
	t.Helper()
	ctx := context.Background()
	s := NewStore(openTest(t), redact)
	p, r := seed(t, s)
	task, err := s.CreateTask(ctx, newTask(p, r, nil))
	if err != nil {
		t.Fatal(err)
	}
	return &propFixture{s: s, task: task, repo: r}
}

// create stores a pending proposal of a through the same path the service uses.
func (f *propFixture) create(t *testing.T, a proposals.Action) proposals.Proposal {
	t.Helper()
	ctx := context.Background()
	f.next++
	id := core.ProposalID("prp_" + string(rune('a'+f.next)))
	var p proposals.Proposal
	err := f.s.Commit(ctx, func(tx *Tx) error {
		cs, err := tx.EnsureChangeSet(ctx, f.task.ID, "cs_"+core.ChangeSetID(id), propNow)
		if err != nil {
			return err
		}
		in := proposals.Created{
			Proposal: proposals.Proposal{ID: id, ChangeSet: cs, Task: f.task.ID, Repo: f.repo.ID, Source: proposals.AgentSource{Task: f.task.ID}, Action: a, Summary: "because"},
			Facts:    proposals.Facts{RepoTracked: true, PathsOK: true, RemoteSupported: true, TaskChangeSet: cs},
			Now:      propNow,
		}
		var cmds []proposals.Command
		p, cmds = proposals.Step(proposals.Proposal{}, in)
		if p.State == nil {
			t.Fatalf("Step refused %T: %v", a, cmds)
		}
		return tx.CreateProposalRecord(ctx, p)
	})
	if err != nil {
		t.Fatalf("create %T: %v", a, err)
	}
	return p
}

func TestProposalRecordRoundTripsEveryActionKind(t *testing.T) {
	f := newPropFixture(t, nil)
	ctx := context.Background()
	for _, a := range sampleActions(f.repo.ID) {
		want := f.create(t, a)
		got, err := f.s.GetProposalRecord(ctx, want.ID)
		if err != nil {
			t.Fatalf("%T: %v", a, err)
		}
		want.Created = want.Created.UTC().Truncate(time.Millisecond)
		if !reflect.DeepEqual(got.Proposal, want) {
			t.Errorf("%T round trip:\n got %+v\nwant %+v", a, got.Proposal, want)
		}
	}
}

func TestProposalRecordRoundTripsEveryState(t *testing.T) {
	ctx := context.Background()
	at := propNow.Add(time.Minute)
	states := []proposals.State{
		proposals.Confirmed{Attempt: 1, At: at, By: "user"},
		proposals.Rejected{At: at},
		proposals.Executed{At: at, Result: proposals.Result{CommitID: strings.Repeat("c", 40), MRURL: "https://git.example.test/mr/1", MovedPaths: []proposals.RelPath{"_to_delete/x"}, BuildID: "bld_1", Note: "pushed"}},
		proposals.Failed{Attempt: 2, At: at, Kind: proposals.FailRemote, Message: "non-fast-forward", Retryable: false},
		proposals.Failed{Attempt: 1, At: at, Kind: proposals.FailInterrupted, Message: "stopped", Retryable: true},
		proposals.Pending{},
	}
	for _, st := range states {
		f := newPropFixture(t, nil)
		p := f.create(t, sampleActions(f.repo.ID)[0])
		p.State = st
		if err := f.s.Commit(ctx, func(tx *Tx) error { _, err := tx.SaveProposalRecord(ctx, p, 0); return err }); err != nil {
			t.Fatalf("save %T: %v", st, err)
		}
		got, err := f.s.GetProposalRecord(ctx, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Proposal.State, st) {
			t.Errorf("state round trip:\n got %#v\nwant %#v", got.Proposal.State, st)
		}
		if got.Version != 1 {
			t.Errorf("version after one save = %d, want 1", got.Version)
		}
	}
}

func TestProposalSaveRefusesAStaleVersion(t *testing.T) {
	ctx := context.Background()
	f := newPropFixture(t, nil)
	p := f.create(t, sampleActions(f.repo.ID)[1])
	p.State = proposals.Confirmed{Attempt: 1, At: propNow, By: "user"}
	if err := f.s.Commit(ctx, func(tx *Tx) error { _, err := tx.SaveProposalRecord(ctx, p, 0); return err }); err != nil {
		t.Fatal(err)
	}
	p.State = proposals.Rejected{At: propNow}
	err := f.s.Commit(ctx, func(tx *Tx) error { _, err := tx.SaveProposalRecord(ctx, p, 0); return err })
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("second save at version 0 = %v, want ErrConflict", err)
	}
	got, _ := f.s.GetProposalRecord(ctx, p.ID)
	if _, ok := got.Proposal.State.(proposals.Confirmed); !ok {
		t.Fatalf("a refused save changed the state to %T", got.Proposal.State)
	}
	if err := f.s.Commit(ctx, func(tx *Tx) error {
		_, err := tx.SaveProposalRecord(ctx, proposals.Proposal{ID: "prp_nope", State: proposals.Pending{}}, 0)
		return err
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("save of a missing proposal = %v, want ErrNotFound", err)
	}
}

func TestOneChangeSetPerTask(t *testing.T) {
	ctx := context.Background()
	f := newPropFixture(t, nil)
	a, b := f.create(t, sampleActions(f.repo.ID)[0]), f.create(t, sampleActions(f.repo.ID)[1])
	if a.ChangeSet == "" || a.ChangeSet != b.ChangeSet {
		t.Fatalf("change sets %q and %q: want one shared by the task", a.ChangeSet, b.ChangeSet)
	}
	if sets, err := f.s.ListChangeSets(ctx, f.task.ID); err != nil || len(sets) != 1 {
		t.Fatalf("change sets of the task = %v, %v; want exactly one", sets, err)
	}
	if id, err := f.s.GetTaskChangeSetID(ctx, f.task.ID); err != nil || id != a.ChangeSet {
		t.Fatalf("GetTaskChangeSetID = %q, %v", id, err)
	}
	if n, err := f.s.GetPendingProposalCount(ctx, f.task.ID); err != nil || n != 2 {
		t.Fatalf("pending count = %d, %v; want 2", n, err)
	}
}

func TestProposalLoadRefusesBadPayloadAndHash(t *testing.T) {
	ctx := context.Background()
	for name, sqlText := range map[string]string{
		"unknown field":  `UPDATE proposals SET payload = json_set(payload, '$.surprise', 1) WHERE id = ?`,
		"unknown kind":   `UPDATE proposals SET payload = '{"worktree":"wt_1","branch":"b","remote":"origin","expect_head":"` + strings.Repeat("a", 40) + `"}' WHERE id = ?`,
		"changed action": `UPDATE proposals SET payload = json_set(payload, '$.message', 'different') WHERE id = ?`,
		"hash not of it": `UPDATE proposals SET action_hash = '` + strings.Repeat("0", 64) + `' WHERE id = ?`,
		"path escapes":   `UPDATE proposals SET payload = json_set(payload, '$.paths', json('["../x"]')), action_hash = '' WHERE id = ?`,
		"unknown status": `UPDATE proposals SET status = 'executed', result = '{"evil":1}' WHERE id = ?`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newPropFixture(t, nil)
			p := f.create(t, sampleActions(f.repo.ID)[0])
			if _, err := f.s.db.write.ExecContext(ctx, sqlText, string(p.ID)); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.GetProposalRecord(ctx, p.ID); err == nil {
				t.Fatal("a proposal with a tampered row loaded")
			}
		})
	}
}

func TestProposalFailureTextIsRedactedBeforeItIsStored(t *testing.T) {
	ctx := context.Background()
	redact := func(s string) string { return strings.ReplaceAll(s, "glpat-SECRET", "[redacted]") }
	f := newPropFixture(t, redact)
	p := f.create(t, sampleActions(f.repo.ID)[1])
	p.State = proposals.Failed{Attempt: 1, At: propNow, Kind: proposals.FailExec, Message: "remote said glpat-SECRET denied", Retryable: true}
	if err := f.s.Commit(ctx, func(tx *Tx) error { _, err := tx.SaveProposalRecord(ctx, p, 0); return err }); err != nil {
		t.Fatal(err)
	}
	got, _ := f.s.GetProposalRecord(ctx, p.ID)
	if m := got.Proposal.State.(proposals.Failed).Message; strings.Contains(m, "SECRET") {
		t.Fatalf("stored message holds the token: %q", m)
	}
}

func TestCreateProposalRecordRefusesWhatStepDidNotMake(t *testing.T) {
	ctx := context.Background()
	f := newPropFixture(t, nil)
	p := f.create(t, sampleActions(f.repo.ID)[0])
	bad := p
	bad.ID = "prp_zz"
	bad.ActionHash = strings.Repeat("0", 64)
	if err := f.s.Commit(ctx, func(tx *Tx) error { return tx.CreateProposalRecord(ctx, bad) }); err == nil {
		t.Error("a proposal with a hash that is not its action's was stored")
	}
	bad.ActionHash = p.ActionHash
	bad.State = proposals.Confirmed{Attempt: 1, At: propNow}
	if err := f.s.Commit(ctx, func(tx *Tx) error { return tx.CreateProposalRecord(ctx, bad) }); err == nil {
		t.Error("a proposal created already confirmed was stored")
	}
}
