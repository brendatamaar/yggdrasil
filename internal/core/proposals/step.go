package proposals

// Step is (proposal, input) -> (proposal, commands): the lifecycle table of
// docs/design/proposals.md. Created applies to a proposal with a nil State.
// An input that is not handled in the current state, including Confirm or
// Reject on a decided proposal and any input with a stale Attempt, changes
// nothing. No input edits the Action, its hash or the header.
func Step(p Proposal, in Input) (Proposal, []Command) {
	if p.State == nil {
		if c, ok := in.(Created); ok {
			return stepCreated(c)
		}
		return p, nil
	}
	switch st := p.State.(type) {
	case Pending:
		switch i := in.(type) {
		case Confirm:
			return stepConfirm(p, i)
		case Reject:
			p.State = Rejected{At: i.Now}
			return p, changed(p, "proposal.reject", "")
		}
	case Confirmed:
		switch i := in.(type) {
		case ExecDone:
			if i.Attempt != st.Attempt {
				return p, nil
			}
			p.State = Executed{At: i.Now, Result: i.Result}
			return p, changed(p, "proposal.execute", "")
		case ExecFailed:
			if i.Attempt != st.Attempt {
				return p, nil
			}
			kind := i.Kind
			if kind.Validate() != nil {
				kind = FailExec
			}
			p.State = Failed{Attempt: st.Attempt, At: i.Now, Kind: kind, Message: i.Message, Retryable: i.Retryable}
			return p, changed(p, "proposal.fail", string(kind))
		case Recovered:
			return p, []Command{Reconcile{ID: p.ID, Attempt: st.Attempt, Action: p.Action}}
		case Reconciled:
			return stepReconciled(p, st, i)
		}
	case Failed:
		if i, ok := in.(Retry); ok && st.Retryable {
			return stepRetry(p, st, i)
		}
	}
	return p, nil
}

// changed is the Audit and Publish that accompany every status change (P7).
func changed(p Proposal, action, outcome string) []Command {
	return []Command{
		Audit{Entry: AuditEntry{Action: action, Proposal: p.ID, Kind: p.Action.Kind(), Outcome: outcome}},
		Publish{Status: p.State.Status()},
	}
}

func stepCreated(c Created) (Proposal, []Command) {
	p := c.Proposal
	refuse := func(r RefuseReason) (Proposal, []Command) {
		return Proposal{}, []Command{Refuse{Reason: r}}
	}
	if p.Action == nil || supported(p.Action.Kind()) != nil {
		return refuse(RefuseUnsupported)
	}
	f := c.Facts
	switch {
	case !f.RepoTracked:
		return refuse(RefuseRepoUntracked)
	case !f.PathsOK || !f.RemoteSupported:
		return refuse(RefuseBadFacts)
	case f.PendingForTask >= MaxPendingPerTask:
		return refuse(RefuseTooMany)
	case p.ID.Validate() != nil || p.ChangeSet.Validate() != nil || p.Task.Validate() != nil ||
		p.Repo.Validate() != nil || p.Source == nil:
		return refuse(RefuseBadFacts)
	}
	p.ActionHash = Hash(p.Action) // never trust a hash from outside
	p.Created = c.Now
	p.State = Pending{}
	return p, append([]Command{Persist{Proposal: p}}, changed(p, "proposal.create", "")...)
}

func stepConfirm(p Proposal, c Confirm) (Proposal, []Command) {
	if p.Action == nil || p.ActionHash == "" || c.Hash != p.ActionHash || c.Hash != Hash(p.Action) {
		return p, []Command{Refuse{Reason: RefuseHashMismatch}}
	}
	if !c.RepoTracked {
		return p, []Command{Refuse{Reason: RefuseRepoUntracked}}
	}
	p.State = Confirmed{Attempt: 1, At: c.Now, By: c.By}
	return p, append([]Command{Execute{ID: p.ID, Attempt: 1, Action: p.Action}}, changed(p, "proposal.confirm", "")...)
}

func stepRetry(p Proposal, st Failed, r Retry) (Proposal, []Command) {
	if !r.RepoTracked {
		return p, []Command{Refuse{Reason: RefuseRepoUntracked}}
	}
	n := st.Attempt + 1
	p.State = Confirmed{Attempt: n, At: r.Now, By: r.By}
	return p, append([]Command{Execute{ID: p.ID, Attempt: n, Action: p.Action}}, changed(p, "proposal.retry", "")...)
}

func stepReconciled(p Proposal, st Confirmed, r Reconciled) (Proposal, []Command) {
	if r.Attempt != st.Attempt {
		return p, nil
	}
	switch r.Outcome {
	case ReconcileDone:
		p.State = Executed{At: r.Now, Result: r.Result}
		return p, changed(p, "proposal.execute", "")
	case ReconcileNotDone, ReconcileUnknown:
		p.State = Failed{
			Attempt: st.Attempt, At: r.Now, Kind: FailInterrupted, Retryable: true,
			Message: "Yggdrasil stopped while this was running, and it could not be confirmed that it finished. Check the repo, then retry if needed.",
		}
		return p, changed(p, "proposal.fail", string(FailInterrupted))
	}
	return p, nil
}
