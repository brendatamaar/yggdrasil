package landing

import (
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// StepRemove is (state, input) -> (state, commands): the removal table of
// docs/design/landing.md. An input it does not handle changes nothing; terminal
// states never change. Every terminal transition emits exactly one WriteAudit.
// The command set is closed (R6): the only delete is DeleteStaged, which
// carries a StagePath, never the worktree's own path.
func StepRemove(s RemoveState, in RemoveInput) (RemoveState, []RemoveCommand) {
	switch st := s.(type) {
	case RemoveIdle:
		if v, ok := in.(RemoveStart); ok {
			return removeStart(v)
		}
	case RemoveChecking:
		if v, ok := in.(RemoveObserved); ok {
			return removeChecked(st, v)
		}
	case Moving:
		switch v := in.(type) {
		case Moved:
			req, staged := st.Req, st.Staged
			return Deleting{Req: req, Staged: staged, OtherPrunable: st.OtherPrunable},
				[]RemoveCommand{MarkRemoving{Worktree: req.Worktree, Staged: staged}, DeleteStaged{Staged: staged}}
		case MoveFailed:
			return Locked{Class: okClass(v.Class), Holders: cleanHolders(v.Holders)}, removeAudit("locked")
		}
	case Deleting:
		switch v := in.(type) {
		case Deleted:
			if st.OtherPrunable {
				return Removed{Note: noteGitCleanupLater}, []RemoveCommand{MarkRemoved{Worktree: st.Req.Worktree}, removeAuditCmd("removed_unpruned")}
			}
			return Pruning{Req: st.Req}, []RemoveCommand{PruneWorktrees{Repo: st.Req.Repo}}
		case DeleteFailed:
			return Stuck{Staged: st.Staged, Class: okClass(v.Class), Holders: cleanHolders(v.Holders), Remaining: max(v.Remaining, 0)}, removeAudit("stuck")
		}
	case Pruning:
		switch in.(type) {
		case Pruned:
			return Removed{}, []RemoveCommand{MarkRemoved{Worktree: st.Req.Worktree}, removeAuditCmd("removed")}
		case PruneFailed:
			return Removed{Note: noteGitCleanupLater}, []RemoveCommand{MarkRemoved{Worktree: st.Req.Worktree}, removeAuditCmd("removed_unpruned")}
		}
	}
	return s, nil
}

func removeAuditCmd(outcome string) WriteAudit {
	return WriteAudit{Action: "landing.remove", Outcome: outcome}
}

func removeAudit(outcome string) []RemoveCommand {
	return []RemoveCommand{removeAuditCmd(outcome)}
}

func removeRefuse(r RemoveRefuse) (RemoveState, []RemoveCommand) {
	return RemoveRefused{Reason: r}, removeAudit("refused_" + string(r))
}

func removeStart(v RemoveStart) (RemoveState, []RemoveCommand) {
	planned, err := StagingPath(v.Req.Root, v.Req.Name, v.StageID)
	if v.Req.Validate() != nil || err != nil {
		// The boundary validates; a bad request here is a bug, never a delete.
		return RemoveFailed{Reason: "The removal request was not valid. Nothing was changed.", Retryable: false}, removeAudit("failed")
	}
	return RemoveChecking{Req: v.Req, Planned: planned}, []RemoveCommand{ObserveWorktree{Req: v.Req}}
}

// stagedBelongs reports whether an observed staged folder is one this
// worktree's earlier run could have made: same root, a leaf of the form
// <sanitised name>-<id>, nothing else. The shell's path check is a second
// line; this keeps DeleteStaged away from any other folder (R3).
func stagedBelongs(req RemoveReq, p StagePath) bool {
	if p.Root != req.Root {
		return false
	}
	probe, err := StagingPath(req.Root, req.Name, "x")
	if err != nil {
		return false
	}
	prefix := strings.TrimSuffix(probe.Leaf, "x")
	id, ok := strings.CutPrefix(p.Leaf, prefix)
	if !ok || id == "" || len(id) > 64 {
		return false
	}
	return strings.Trim(id, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") == ""
}

// removeChecked applies the guards in the order of the table.
func removeChecked(st RemoveChecking, o RemoveObserved) (RemoveState, []RemoveCommand) {
	req := st.Req
	switch {
	case !o.UnderRoot:
		return removeRefuse(RemoveOutsideRoot)
	case o.IsMain:
		return removeRefuse(RemoveMainCheckout)
	case (o.RowStatus == core.WorktreeRemoved || o.RowStatus == core.WorktreeRemoving) && !o.PathExists && !o.StagedExists:
		// Nothing left on disk. A row still at removing means a crash after the
		// delete: finish the bookkeeping.
		if o.RowStatus == core.WorktreeRemoving {
			return Removed{Note: noteAlreadyRemoved}, []RemoveCommand{MarkRemoved{Worktree: req.Worktree}, removeAuditCmd("already_removed")}
		}
		return Removed{Note: noteAlreadyRemoved}, removeAudit("already_removed")
	case o.StagedExists:
		if !stagedBelongs(req, o.Staged) {
			return RemoveFailed{Reason: "A folder from an earlier removal could not be matched to this worktree, so nothing was deleted. Check the .removing folder in the worktree root.", Retryable: false},
				removeAudit("failed")
		}
		var cmds []RemoveCommand
		if o.RowStatus == core.WorktreeActive || o.RowStatus == core.WorktreeMerged {
			cmds = append(cmds, MarkRemoving{Worktree: req.Worktree, Staged: o.Staged})
		}
		cmds = append(cmds, DeleteStaged{Staged: o.Staged})
		return Deleting{Req: req, Staged: o.Staged, OtherPrunable: o.OtherPrunable}, cmds
	case !o.StatusReadable:
		return removeRefuse(RemoveUnreadable)
	case o.TaskLive:
		return removeRefuse(RemoveTaskRunning)
	case o.BuildLive:
		return removeRefuse(RemoveInUse)
	case !o.Clean:
		return removeRefuse(RemoveDirty)
	}
	return Moving{Req: req, Staged: st.Planned, OtherPrunable: o.OtherPrunable}, []RemoveCommand{MoveToStaging{Req: req, To: st.Planned}}
}
