package repos

import (
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/git"
)

// MaxRefreshIDs is the most repo IDs one refresh request may name.
const MaxRefreshIDs = 1000

// SnapshotFrom is the boundary between git's types and the core Snapshot.
// commit is nil before the first commit. It rejects values git cannot
// produce: negative counts, or a detached HEAD that also has a branch name.
func SnapshotFrom(st git.Status, commit *git.Commit) (corerepos.Snapshot, error) {
	for name, n := range map[string]int{
		"ahead": st.Ahead, "behind": st.Behind, "staged": st.Staged,
		"unstaged": st.Unstaged, "untracked": st.Untracked, "conflicted": st.Conflicted,
	} {
		if n < 0 {
			return corerepos.Snapshot{}, fmt.Errorf("git reported a negative %s count (%d)", name, n)
		}
	}
	if st.Detached && st.Branch != "" {
		return corerepos.Snapshot{}, fmt.Errorf("git reported a detached HEAD on branch %q", st.Branch)
	}
	s := corerepos.Snapshot{
		Branch: st.Branch, Detached: st.Detached, Upstream: st.Upstream, HasUpstream: st.HasUpstream,
		Ahead: st.Ahead, Behind: st.Behind, Staged: st.Staged, Unstaged: st.Unstaged,
		Untracked: st.Untracked, Conflicted: st.Conflicted,
	}
	if commit != nil {
		s.LastCommit = &corerepos.CommitRef{OID: commit.Hash, Subject: commit.Subject, Author: commit.Author, At: commit.Date}
	}
	return s, nil
}

// ParseRefresh turns the body of POST /api/repos/refresh into a core input.
// An empty reason means "user". Empty ids mean all repos. Repeated IDs are
// kept once, in order. now comes from the shell's clock.
func ParseRefresh(ids []string, reason string, now time.Time) (corerepos.RefreshRequested, error) {
	if reason == "" {
		reason = string(corerepos.ReasonUser)
	}
	r := corerepos.RefreshReason(reason)
	if err := r.Validate(); err != nil {
		return corerepos.RefreshRequested{}, apperr.New(apperr.Invalid,
			"The refresh reason is not one of user, focus, git_action, build, tracking or tick.", err)
	}
	if len(ids) > MaxRefreshIDs {
		return corerepos.RefreshRequested{}, apperr.New(apperr.Invalid,
			fmt.Sprintf("You can refresh at most %d repos at once. Send fewer IDs, or none to refresh them all.", MaxRefreshIDs), nil)
	}
	out := corerepos.RefreshRequested{Reason: r, Now: now}
	seen := make(map[core.RepoID]struct{}, len(ids))
	for _, raw := range ids {
		id := core.RepoID(raw)
		if err := id.Validate(); err != nil {
			return corerepos.RefreshRequested{}, apperr.New(apperr.Invalid, "One of the repo IDs is not valid.", err)
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out.IDs = append(out.IDs, id)
	}
	return out, nil
}

// ParseFilter turns the ?filter= value into a Filter. An empty value means
// "all".
func ParseFilter(s string) (corerepos.Filter, error) {
	if s == "" {
		return corerepos.FilterAll, nil
	}
	f := corerepos.Filter(s)
	if err := f.Validate(); err != nil {
		return "", apperr.New(apperr.Invalid,
			"The filter is not one of all, uncommitted, behind, ahead, failed_pipeline or problems.", err)
	}
	return f, nil
}
