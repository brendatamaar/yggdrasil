package changesets

import (
	"maps"
	"slices"
	"strings"
	"unicode"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// maxReasonRunes caps a reason shown to the user.
const maxReasonRunes = 200

// open returns the open part shared by every state.
func open(s State) Open {
	switch v := s.(type) {
	case Open:
		return v
	case Confirming:
		return v.Open
	case Shipped:
		return v.Open
	case Closed:
		return v.Open
	}
	return Open{}
}

func changed(set core.ChangeSetID, action, outcome string) []Command {
	return []Command{WriteAudit{Action: action, Outcome: outcome, Set: set}, Publish{Set: set}}
}

func isActive(st core.ProposalStatus) bool {
	return st == core.ProposalPending || st == core.ProposalConfirmed
}

// classify derives the state of a set that is not running: Shipped when every
// member is terminal, one executed and none failed; Closed when every member
// was rejected; Open otherwise.
func classify(o Open) State {
	var executed, rejected int
	for _, m := range o.Members {
		switch m.Status {
		case core.ProposalExecuted:
			executed++
		case core.ProposalRejected:
			rejected++
		default:
			return o
		}
	}
	switch {
	case len(o.Members) == 0:
		return o
	case executed > 0:
		return Shipped{o}
	case rejected == len(o.Members):
		return Closed{o}
	}
	return o
}

func find(ms []Member, id core.ProposalID) (Member, bool) {
	for _, m := range ms {
		if m.ID == id {
			return m, true
		}
	}
	return Member{}, false
}

// cleanReason makes a reason from the shell fit for the user: one line, capped.
func cleanReason(s string) string {
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || isLineBreak(r) {
			return ' '
		}
		return r
	}, s))
	if s == "" {
		return "the proposal was refused"
	}
	if rs := []rune(s); len(rs) > maxReasonRunes {
		s = string(rs[:maxReasonRunes])
	}
	return s
}

func openEq(a, b Open) bool {
	return a.Set.ID == b.Set.ID && a.Set.Task == b.Set.Task &&
		slices.Equal(a.Set.Repos, b.Set.Repos) && slices.Equal(a.Set.Order, b.Set.Order) &&
		slices.Equal(a.Members, b.Members) && maps.Equal(a.Links, b.Links) && a.LastRun == b.LastRun
}

func stateEq(a, b State) bool {
	switch x := a.(type) {
	case Open:
		y, ok := b.(Open)
		return ok && openEq(x, y)
	case Shipped:
		y, ok := b.(Shipped)
		return ok && openEq(x.Open, y.Open)
	case Closed:
		y, ok := b.(Closed)
		return ok && openEq(x.Open, y.Open)
	case Confirming:
		y, ok := b.(Confirming)
		return ok && openEq(x.Open, y.Open) && x.Scope == y.Scope && x.Current == y.Current && slices.Equal(x.Queue, y.Queue)
	}
	return false
}

// StepSet is (set, input) -> (set, commands): the change-set table of
// docs/design/changesets.md. An input it does not handle in the current state
// changes nothing and returns no commands. Every change emits one WriteAudit
// and one Publish (plus at most one ConfirmProposal).
func StepSet(s State, in Input) (State, []Command) {
	if s == nil || s.Validate() != nil {
		return s, nil
	}
	switch v := in.(type) {
	case MembersChanged:
		return onMembers(s, v)
	case SetOrder:
		return onSetOrder(s, v)
	case ConfirmRun:
		return onConfirmRun(s, v)
	case StopRun:
		if c, ok := s.(Confirming); ok {
			return halt(c, c.Current, "the run was stopped", "changeset.stop_run")
		}
	case ConfirmRefused:
		if c, ok := s.(Confirming); ok && v.ID == c.Current {
			return halt(c, v.ID, cleanReason(v.Reason), "changeset.confirm_refused")
		}
	case Close:
		if o, ok := s.(Open); ok && !slices.ContainsFunc(o.Members, func(m Member) bool { return isActive(m.Status) }) {
			return Closed{o}, changed(o.Set.ID, "changeset.close", "ok")
		}
	}
	return s, nil
}

// halt ends a run at a member. The member being executed finishes by itself.
func halt(c Confirming, at core.ProposalID, reason, action string) (State, []Command) {
	o := c.Open
	o.LastRun = RunHalted{At: at, Reason: reason}
	return o, changed(o.Set.ID, action, "halted")
}

func onSetOrder(s State, in SetOrder) (State, []Command) {
	o, ok := s.(Open)
	if !ok || !IsPermutation(in.Order, o.Set.Repos) || slices.Equal(in.Order, o.Set.Order) {
		return s, nil
	}
	o.Set.Order = slices.Clone(in.Order)
	return o, changed(o.Set.ID, "changeset.order", "ok")
}

// cleanMembers accepts the member list only when every member is valid, in a
// repo of the set, and no ID repeats.
func cleanMembers(set Header, ms []Member) ([]Member, bool) {
	if len(ms) > MaxMembers {
		return nil, false
	}
	seen := make(map[core.ProposalID]bool, len(ms))
	for _, m := range ms {
		if m.Validate() != nil || !slices.Contains(set.Repos, m.Repo) || seen[m.ID] {
			return nil, false
		}
		seen[m.ID] = true
	}
	return slices.Clone(ms), true
}

func cleanLinks(set Header, links map[core.RepoID]string) map[core.RepoID]string {
	var out map[core.RepoID]string
	for r, u := range links {
		if slices.Contains(set.Repos, r) {
			if out == nil {
				out = map[core.RepoID]string{}
			}
			out[r] = u
		}
	}
	return out
}

func onMembers(s State, in MembersChanged) (State, []Command) {
	o := open(s)
	ms, ok := cleanMembers(o.Set, in.Members)
	if !ok {
		return s, nil
	}
	next := o
	next.Members = ms
	next.Links = cleanLinks(o.Set, in.Links)

	var ns State
	var cmds []Command
	action := ""
	switch v := s.(type) {
	case Confirming:
		ns, cmds, action = progress(v, next)
	case Closed:
		if slices.ContainsFunc(ms, func(m Member) bool { return isActive(m.Status) }) {
			ns = next
		} else {
			ns = Closed{next}
		}
	default:
		ns = classify(next)
	}
	if stateEq(s, ns) {
		return s, nil
	}
	if action == "" {
		action = "changeset.members"
		if ns.Status() != s.Status() {
			action = "changeset." + string(ns.Status())
		} else if _, was := s.(Shipped); was {
			action = "changeset.reopen"
		}
	}
	return ns, append(changed(o.Set.ID, action, "ok"), cmds...)
}

// progress moves a running confirm run on after the members changed.
func progress(c Confirming, next Open) (State, []Command, string) {
	stop := func(at core.ProposalID, reason string) (State, []Command, string) {
		next.LastRun = RunHalted{At: at, Reason: reason}
		return classify(next), nil, "changeset.run_halted"
	}
	cur, found := find(next.Members, c.Current)
	switch {
	case !found:
		return stop(c.Current, "a proposal of the run is no longer part of this change set")
	case cur.Status == core.ProposalFailed:
		return stop(c.Current, "a proposal failed")
	case cur.Status == core.ProposalRejected:
		return stop(c.Current, "a proposal was rejected")
	case cur.Status != core.ProposalExecuted:
		c.Open = next
		return c, nil, ""
	}
	if len(c.Queue) == 0 {
		next.LastRun = RunCompleted{}
		return classify(next), nil, "changeset.run_completed"
	}
	head := c.Queue[0]
	if m, ok := find(next.Members, head.ID); !ok || m.Status != core.ProposalPending || m.Hash != head.Hash {
		return stop(head.ID, "a proposal changed since you reviewed it")
	}
	c.Open = next
	c.Queue = slices.Clone(c.Queue[1:])
	c.Current = head.ID
	return c, []Command{ConfirmProposal(head)}, "changeset.run_next"
}

func onConfirmRun(s State, in ConfirmRun) (State, []Command) {
	o, ok := s.(Open)
	if !ok || in.Scope == nil || in.Scope.Validate() != nil {
		return s, nil
	}
	if sr, ok := in.Scope.(ScopeRepo); ok && !slices.Contains(o.Set.Repos, sr.Repo) {
		return s, nil
	}
	plan := Plan(o, in.Scope)
	if len(plan) == 0 || len(in.Seen) != len(plan) {
		return s, nil
	}
	for _, p := range plan {
		if h, ok := in.Seen[p.ID]; !ok || h != p.Hash {
			return s, nil
		}
	}
	var raised []string
	for _, w := range Preflight(o, in.Scope, in.Deps) {
		if !slices.Contains(in.Ack, w.Code) {
			return s, nil
		}
		raised = append(raised, string(w.Code))
	}
	outcome := "ok"
	if len(raised) > 0 {
		slices.Sort(raised)
		outcome = "acknowledged: " + strings.Join(slices.Compact(raised), ",")
	}
	o.LastRun = RunIdle{}
	run := Confirming{Open: o, Scope: in.Scope, Current: plan[0].ID}
	if len(plan) > 1 {
		run.Queue = slices.Clone(plan[1:])
	}
	return run, append(changed(o.Set.ID, "changeset.confirm_run", outcome), ConfirmProposal{ID: plan[0].ID, Hash: plan[0].Hash})
}
