package changesets_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/changesets"
	"github.com/brendatamaar/yggdrasil/internal/changesets/changesetstest"
	"github.com/brendatamaar/yggdrasil/internal/core"
	cs "github.com/brendatamaar/yggdrasil/internal/core/changesets"
)

type rig struct {
	t     *testing.T
	w     *changesetstest.World
	store *changesetstest.Store
	api   *changesetstest.API
	msg   *changesetstest.Messenger
	hub   *changesetstest.Hub
	deps  *changesetstest.Deps
	diffs *changesetstest.Diffs
	svc   *changesets.Service
	now   atomic.Int64 // unix seconds
	ids   atomic.Int64
}

const (
	task = core.TaskID("tsk_1")
	set  = core.ChangeSetID("cs_1")
	rep1 = core.RepoID("rep_1")
	rep2 = core.RepoID("rep_2")
)

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, w: changesetstest.NewWorld(), msg: &changesetstest.Messenger{}, hub: &changesetstest.Hub{}, deps: &changesetstest.Deps{}, diffs: &changesetstest.Diffs{}}
	r.now.Store(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC).Unix())
	r.w.AddTask(task, rep1, rep2)
	r.w.AddSet(set, task)
	r.store = changesetstest.NewStore(r.w)
	r.api = changesetstest.NewAPI(r.w)
	r.svc = r.newService()
	return r
}

// newService builds a service on the same world: a "restart".
func (r *rig) newService() *changesets.Service {
	svc, err := changesets.New(changesets.Deps{
		Store: r.store, Proposals: r.api, Messenger: r.msg, Deps: r.deps, Diffs: r.diffs, Hub: r.hub,
		Clock: func() time.Time { return time.Unix(r.now.Load(), 0).UTC() },
		IDs:   func(p string) string { return fmt.Sprintf("%s_%d", p, r.ids.Add(1)) },
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		r.t.Fatal(err)
	}
	return svc
}

func (r *rig) advance(d time.Duration) { r.now.Add(int64(d / time.Second)) }

func (r *rig) add(repo core.RepoID, kind core.ProposalKind) (core.ProposalID, string) {
	return r.w.AddProposal(set, repo, kind)
}

func (r *rig) reconcile() {
	r.t.Helper()
	if err := r.svc.Reconcile(context.Background()); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) view() changesets.View {
	r.t.Helper()
	v, err := r.svc.Get(context.Background(), set)
	if err != nil {
		r.t.Fatal(err)
	}
	return v
}

func (r *rig) confirmAll(seen map[core.ProposalID]string, ack ...string) (changesets.View, error) {
	m := map[string]string{}
	for id, h := range seen {
		m[string(id)] = h
	}
	return r.svc.ConfirmRun(context.Background(), set, changesets.RawConfirm{Scope: "all", Seen: m, Ack: ack}, "user")
}

func userMessage(err error) string {
	var u interface{ UserMessage() string }
	if errors.As(err, &u) {
		return u.UserMessage()
	}
	return ""
}

func openOf(st cs.State) cs.Open {
	switch v := st.(type) {
	case cs.Open:
		return v
	case cs.Confirming:
		return v.Open
	case cs.Shipped:
		return v.Open
	case cs.Closed:
		return v.Open
	}
	return cs.Open{}
}

func (r *rig) confirmedIDs() []core.ProposalID {
	var out []core.ProposalID
	for _, c := range r.api.Calls() {
		if c.Err == nil {
			out = append(out, c.ID)
		}
	}
	return out
}
