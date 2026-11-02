package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/changesets"
	"github.com/brendatamaar/yggdrasil/internal/changesets/changesetstest"
	"github.com/brendatamaar/yggdrasil/internal/core"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/proposals"
)

// stubProposals answers Confirm the way *proposals.Service does: a typed
// *Refusal for a wrong hash, ErrAlreadyDecided for a decided proposal, a
// wrapped db.ErrNotFound for an unknown one. (A run of the real service needs
// a git writer, resolver, lock and builds; its answers are pinned by the
// proposals package tests.)
type stubProposals struct {
	mu     sync.Mutex
	hash   map[core.ProposalID]string
	status map[core.ProposalID]core.ProposalStatus
	n      int
}

func (s *stubProposals) Confirm(_ context.Context, id core.ProposalID, hash, _ string) (proposals.View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.status[id]
	switch {
	case !ok:
		return proposals.View{}, fmt.Errorf("load proposal %s: %w", id, db.ErrNotFound)
	case st != core.ProposalPending:
		return proposals.View{Status: st}, proposals.ErrAlreadyDecided
	case s.hash[id] != hash:
		return proposals.View{Status: st}, &proposals.Refusal{Reason: coreprop.RefuseHashMismatch, Message: "What you confirmed is not what this proposal now says."}
	}
	s.status[id] = core.ProposalConfirmed
	return proposals.View{Status: core.ProposalConfirmed}, nil
}

func TestChangeSetProposalsAdapterPassesTheContract(t *testing.T) {
	changesetstest.ProposalsAPIContract(t, func(t *testing.T) changesetstest.APIHarness {
		s := &stubProposals{hash: map[core.ProposalID]string{}, status: map[core.ProposalID]core.ProposalStatus{}}
		return changesetstest.APIHarness{
			API: changeSetProposals{svc: s},
			NewPending: func(t *testing.T) (core.ProposalID, string) {
				s.mu.Lock()
				defer s.mu.Unlock()
				s.n++
				id := core.ProposalID(fmt.Sprintf("prp_%d", s.n))
				s.hash[id], s.status[id] = changesetstest.Hash(s.n), core.ProposalPending
				return id, s.hash[id]
			},
			Status: func(t *testing.T, id core.ProposalID) core.ProposalStatus {
				s.mu.Lock()
				defer s.mu.Unlock()
				return s.status[id]
			},
			Settle: func(t *testing.T, id core.ProposalID) core.ProposalStatus {
				s.mu.Lock()
				defer s.mu.Unlock()
				s.status[id] = core.ProposalExecuted
				return s.status[id]
			},
		}
	})
}

func TestChangeSetProposalsAdapterKeepsOtherErrorsTransient(t *testing.T) {
	boom := errors.New("database is locked")
	err := changeSetProposals{svc: failing{boom}}.Confirm(context.Background(), "prp_1", changesetstest.Hash(1), "u")
	var ref *changesets.Refused
	if errors.As(err, &ref) || !errors.Is(err, boom) {
		t.Fatalf("a transient error became a refusal: %v", err)
	}
	err = changeSetProposals{svc: failing{proposals.ErrNotStarted}}.Confirm(context.Background(), "prp_1", changesetstest.Hash(1), "u")
	if errors.As(err, &ref) {
		t.Fatal("a stopped service is not a refusal")
	}
}

type failing struct{ err error }

func (f failing) Confirm(context.Context, core.ProposalID, string, string) (proposals.View, error) {
	return proposals.View{}, f.err
}
