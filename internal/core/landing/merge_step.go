package landing

import "slices"

// StepMerge is (state, input) -> (state, commands): the merge table of
// docs/design/landing.md. An input it does not handle in the current state
// changes nothing and emits nothing; terminal states never change. Every
// terminal transition emits exactly one WriteAudit. No transition can emit a
// push, reset, branch or delete command: the command set is closed (L3).
func StepMerge(s MergeState, in MergeInput) (MergeState, []MergeCommand) {
	switch st := s.(type) {
	case MergeIdle:
		if v, ok := in.(MergeStart); ok {
			return mergeStart(v)
		}
	case MergeChecking:
		if v, ok := in.(Observed); ok {
			return mergeChecked(st, v)
		}
	case MergePredicting:
		if v, ok := in.(Predicted); ok {
			return mergePredicted(st, v)
		}
	case MergeMerging:
		switch v := in.(type) {
		case MergeDone:
			return Merged{Commit: v.Commit}, []MergeCommand{MarkWorktreeMerged{Worktree: st.Req.Worktree}, mergeAudit("merged")}
		case MergeConflicted:
			r := st.Req
			return MergeAborting{Req: r, Cause: AfterConflict{Files: slices.Clone(v.Files)}}, []MergeCommand{Abort{Req: r}}
		case MergeErrored:
			reason := v.Reason
			if reason != FailIdentityMissing {
				reason = FailExec
			}
			r := st.Req
			return MergeSettling{Req: r, Cause: AfterFailure{Reason: reason}}, []MergeCommand{Observe{Req: r}}
		}
	case MergeAborting:
		switch in.(type) {
		case AbortDone:
			r := st.Req
			return MergeSettling{Req: r, Cause: st.Cause}, []MergeCommand{Observe{Req: r}}
		case AbortErrored:
			return NeedsHuman{Why: "The merge could not be undone. Run `git merge --abort` in " + capRunes(plainText(st.Req.Path), maxPathRunes) + " and check the folder before trying again."},
				[]MergeCommand{mergeAudit("needs_human")}
		}
	case MergeSettling:
		if v, ok := in.(Observed); ok {
			return mergeSettled(st, v)
		}
	}
	return s, nil
}

func mergeAudit(outcome string) WriteAudit {
	return WriteAudit{Action: "landing.merge", Outcome: outcome}
}

func mergeStart(v MergeStart) (MergeState, []MergeCommand) {
	if v.Req.Validate() != nil || v.Mode.Validate() != nil {
		// The boundary validates; a bad request here is a bug, never a merge.
		return MergeFailed{Reason: FailExec, Retryable: false}, []MergeCommand{mergeAudit("failed_exec")}
	}
	return MergeChecking{Req: v.Req, Mode: v.Mode}, []MergeCommand{Observe{Req: v.Req}}
}

func refuse(r RefuseReason) (MergeState, []MergeCommand) {
	return MergeRefused{Reason: r}, []MergeCommand{mergeAudit("refused_" + string(r))}
}

// mergeChecked applies the guards in the order of the table.
func mergeChecked(st MergeChecking, o Observed) (MergeState, []MergeCommand) {
	req := st.Req
	switch {
	case !o.Tracked:
		return refuse(RefuseUntrackedRepo)
	case o.WorktreeBranch != req.Branch:
		return refuse(RefuseWorktreeBranch)
	case o.AlreadyMerged:
		return AlreadyMerged{Head: o.Head}, []MergeCommand{MarkWorktreeMerged{Worktree: req.Worktree}, mergeAudit("already_merged")}
	case st.Mode == ModeRecovering && o.MergeInProgress && o.MergeHead != "" &&
		o.MergeHead == o.BranchHead && o.MergeHead == req.ExpectBranchHead:
		// Our own interrupted merge: the only merge in progress we ever abort.
		return MergeAborting{Req: req, Cause: AfterFailure{Reason: FailInterrupted}}, []MergeCommand{Abort{Req: req}}
	case o.MergeInProgress:
		return refuse(RefuseMergeInProgress)
	case o.DetachedHead || o.CurrentBranch != req.TargetBranch:
		return refuse(RefuseWrongBranch)
	case !o.Clean:
		return refuse(RefuseMainDirty)
	case o.BranchHead == "":
		return refuse(RefuseBranchMissing)
	case o.BranchHead != req.ExpectBranchHead:
		return refuse(RefuseStale)
	case !o.WorktreeClean:
		return refuse(RefuseWorktreeDirty)
	}
	return MergePredicting{Req: req}, []MergeCommand{Predict{Req: req}}
}

func mergePredicted(st MergePredicting, p Predicted) (MergeState, []MergeCommand) {
	switch r := p.Result.(type) {
	case PredictClean, PredictUnsupported:
		mr := st.Req
		return MergeMerging{Req: mr}, []MergeCommand{RunMerge{Req: mr, Message: MergeMessage(mr.Branch, mr.Title)}}
	case PredictConflicts:
		return Conflicted{Files: slices.Clone(r.Files)}, []MergeCommand{mergeAudit("conflicted")}
	case PredictError:
		return MergeFailed{Reason: FailExec, Retryable: true}, []MergeCommand{mergeAudit("failed_exec")}
	}
	return st, nil
}

// mergeSettled decides after a merge attempt only from a clean observation
// (L8); a dirty tree or a merge still in progress needs the user.
func mergeSettled(st MergeSettling, o Observed) (MergeState, []MergeCommand) {
	if !o.Clean || o.MergeInProgress {
		return NeedsHuman{Why: "The folder " + capRunes(plainText(st.Req.Path), maxPathRunes) + " is not clean after the merge attempt. Check it with git status, run `git merge --abort` if a merge is in progress, and try again."},
			[]MergeCommand{mergeAudit("needs_human")}
	}
	if c, ok := st.Cause.(AfterConflict); ok {
		return Conflicted{Files: slices.Clone(c.Files)}, []MergeCommand{mergeAudit("conflicted")}
	}
	reason := FailExec
	if f, ok := st.Cause.(AfterFailure); ok && f.Reason.Validate() == nil {
		reason = f.Reason
	}
	return MergeFailed{Reason: reason, Retryable: true}, []MergeCommand{mergeAudit("failed_" + string(reason))}
}
